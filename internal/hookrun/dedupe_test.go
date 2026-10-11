package hookrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), ".graphmaster"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func grepEvent(sid, id, pattern string) hookapi.HookEvent {
	return hookapi.HookEvent{
		Agent: hookapi.AgentClaude, Event: hookapi.EventPreToolUse, SessionID: sid, ToolUseID: id,
		Tool: hookapi.ToolGrep, Pattern: pattern, CWD: "/r",
	}
}

func TestDedupeKey(t *testing.T) {
	base := grepEvent("s1", "t1", "Foo")
	k := dedupeKey(base)
	if len(k) != 40 {
		t.Fatalf("key %q is not a sha1 hex digest", k)
	}
	// The agent and payload spelling do not matter: Cursor's claude-shaped
	// and native invocations of one tool call share the key.
	other := base
	other.Agent = hookapi.AgentCursor
	other.Raw = json.RawMessage(`{"x":1}`)
	other.CWD = "/elsewhere"
	if dedupeKey(other) != k {
		t.Fatal("agent, raw payload and cwd must not change the key")
	}
	// Insignificant whitespace in the input is normalized.
	ws := base
	ws.Pattern = "  Foo "
	if dedupeKey(ws) != k {
		t.Fatal("whitespace must be normalized")
	}
	for name, ev := range map[string]hookapi.HookEvent{
		"session": grepEvent("s2", "t1", "Foo"),
		"call id": grepEvent("s1", "t2", "Foo"),
		"pattern": grepEvent("s1", "t1", "Bar"),
		"event": func() hookapi.HookEvent {
			e := base
			e.Event = hookapi.EventPostToolUse
			return e
		}(),
	} {
		if dedupeKey(ev) == k {
			t.Errorf("%s change must change the key", name)
		}
	}
}

func TestDedupeKeyUsesGenerationID(t *testing.T) {
	ev := grepEvent("s1", "", "Foo")
	if dedupeKey(ev) != "" {
		t.Fatal("no call id: no dedupe (a retry must reach the steering engine)")
	}
	ev.Raw = json.RawMessage(`{"conversation_id":"s1","generation_id":"g1"}`)
	k := dedupeKey(ev)
	if k == "" {
		t.Fatal("generation_id must key the event")
	}
	ev.Raw = json.RawMessage(`{"generation_id":"g2"}`)
	if dedupeKey(ev) == k {
		t.Fatal("generation id must be part of the key")
	}
	if dedupeKey(grepEvent("", "t1", "Foo")) != "" {
		t.Fatal("no session: no dedupe")
	}
}

func TestDedupeClaimStoreReplayOnce(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	d := &dedupe{db: openTestDB(t), now: clk.Now}
	key := dedupeKey(grepEvent("s", "t", "Foo"))

	_, c, err := d.claim(ctx, key)
	if err != nil || c != claimOwner {
		t.Fatalf("first claim = %v, %v; want owner", c, err)
	}
	want := hookapi.Deny("use find_symbol").WithContext("ctx")
	if err := d.store(ctx, key, want); err != nil {
		t.Fatal(err)
	}
	got, c, err := d.claim(ctx, key)
	if err != nil || c != claimReplay {
		t.Fatalf("second claim = %v, %v; want replay", c, err)
	}
	if got.Action != want.Action || got.Reason != want.Reason || got.AdditionalContext != want.AdditionalContext {
		t.Fatalf("replayed %+v, want %+v", got, want)
	}
	// A replay consumes the entry: a third identical call is a new owner.
	if _, c, _ := d.claim(ctx, key); c != claimOwner {
		t.Fatalf("third claim = %v, want owner", c)
	}
}

func TestDedupeExpiry(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	d := &dedupe{db: openTestDB(t), now: clk.Now}
	key := dedupeKey(grepEvent("s", "t", "Foo"))
	if _, c, _ := d.claim(ctx, key); c != claimOwner {
		t.Fatal("want owner")
	}
	if err := d.store(ctx, key, hookapi.Allow()); err != nil {
		t.Fatal(err)
	}
	clk.Add(DedupeTTL + time.Second)
	if _, c, _ := d.claim(ctx, key); c != claimOwner {
		t.Fatalf("expired entry replayed: %v", c)
	}
}

func TestDedupeWaitsForConcurrentOwner(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	key := dedupeKey(grepEvent("s", "t", "Foo"))
	owner := &dedupe{db: db}
	if _, c, _ := owner.claim(ctx, key); c != claimOwner {
		t.Fatal("want owner")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = owner.store(ctx, key, hookapi.Deny("r"))
	}()
	waiter := &dedupe{db: db, wait: 5 * time.Second, poll: 5 * time.Millisecond}
	got, c, err := waiter.claim(ctx, key)
	if err != nil || c != claimReplay || got.Action != hookapi.ActionDeny {
		t.Fatalf("waiter = %+v, %v, %v", got, c, err)
	}
}

func TestDedupeGivesUpOnStuckOwner(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	key := dedupeKey(grepEvent("s", "t", "Foo"))
	if _, c, _ := (&dedupe{db: db}).claim(ctx, key); c != claimOwner {
		t.Fatal("want owner")
	}
	waiter := &dedupe{db: db, wait: 30 * time.Millisecond, poll: 5 * time.Millisecond}
	if _, c, err := waiter.claim(ctx, key); err != nil || c != claimSkip {
		t.Fatalf("stuck owner: %v, %v; want skip", c, err)
	}
	// The owner failing releases its claim.
	owner := &dedupe{db: db}
	if err := owner.release(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, c, _ := owner.claim(ctx, key); c != claimOwner {
		t.Fatal("released key must be claimable")
	}
}

func TestDedupeEmptyKeyIsNoop(t *testing.T) {
	d := &dedupe{db: openTestDB(t)}
	if _, c, err := d.claim(context.Background(), ""); err != nil || c != claimSkip {
		t.Fatalf("empty key = %v, %v", c, err)
	}
	if err := d.store(context.Background(), "", hookapi.Allow()); err != nil {
		t.Fatal(err)
	}
}
