package hookrun

import (
	"context"
	"crypto/sha1" // a cache key, not a security boundary
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

// DedupeTTL is how long a cached decision can be replayed (PLAN: 10 min).
const DedupeTTL = 10 * time.Minute

// Defaults for waiting on a concurrent invocation that owns a key.
const (
	defaultDedupeWait = 2 * time.Second
	defaultDedupePoll = 20 * time.Millisecond
)

// pendingDecision marks a claimed key whose decision is being computed.
const pendingDecision = ""

// claimResult tells the caller what to do with a dedupe key.
type claimResult int

const (
	// claimSkip: compute the decision and do not cache it (no key, or a
	// concurrent owner never finished).
	claimSkip claimResult = iota
	// claimOwner: this invocation owns the key; compute, then store.
	claimOwner
	// claimReplay: replay the returned cached decision.
	claimReplay
)

func (c claimResult) String() string {
	switch c {
	case claimOwner:
		return "owner"
	case claimReplay:
		return "replay"
	}
	return "skip"
}

// dedupe answers one tool call once when an agent fires two hooks for it
// (Cursor running both its native hooks and the Claude Code hooks). The
// first invocation claims the key and stores its decision in hook_dedupe;
// the duplicate replays that decision without consulting the steering
// engine, so neither the deny budget nor the retry rule sees it twice. A
// replay consumes the entry: a later identical call is steered again.
type dedupe struct {
	db   *sql.DB
	now  func() time.Time // default time.Now
	ttl  time.Duration    // default DedupeTTL
	wait time.Duration    // wait for a concurrent owner (default 2s)
	poll time.Duration    // poll interval while waiting (default 20ms)
}

func (d *dedupe) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func orDefault(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// dedupeKey is sha1(sid|call id|event|normalized input), or "" when the
// event has no session or no per-call id (tool_use_id, else Cursor's
// generation_id). Without a call id a deliberate retry would be
// indistinguishable from a duplicate, so such events are never deduped.
func dedupeKey(ev hookapi.HookEvent) string {
	id := ev.ToolUseID
	if id == "" {
		id = generationID(ev.Raw)
	}
	if ev.SessionID == "" || id == "" {
		return ""
	}
	sum := sha1.Sum([]byte(strings.Join([]string{ev.SessionID, id, string(ev.Event), normalizedInput(ev)}, "|")))
	return hex.EncodeToString(sum[:])
}

// normalizedInput is the agent-independent identity of a tool input.
func normalizedInput(ev hookapi.HookEvent) string {
	paths := make([]string, len(ev.Paths))
	for i, p := range ev.Paths {
		paths[i] = strings.TrimSpace(p)
	}
	return strings.Join([]string{
		string(ev.Tool), squashSpace(ev.Pattern), strings.TrimSpace(ev.Glob),
		strings.Join(paths, "\x1f"), squashSpace(ev.Command), ev.AgentID,
	}, "\x1e")
}

func squashSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func generationID(raw json.RawMessage) string {
	var p struct {
		GenerationID string `json:"generation_id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		return ""
	}
	return p.GenerationID
}

// claim purges expired entries and claims key. When another invocation
// owns it, claim replays its decision, waiting up to d.wait for an
// in-flight one.
func (d *dedupe) claim(ctx context.Context, key string) (hookapi.Decision, claimResult, error) {
	if key == "" {
		return hookapi.Decision{}, claimSkip, nil
	}
	now := d.clock()
	ttl := orDefault(d.ttl, DedupeTTL)
	if _, err := d.db.ExecContext(ctx, `DELETE FROM hook_dedupe WHERE ts < ?`, now.Add(-ttl).UnixNano()); err != nil {
		return hookapi.Decision{}, claimSkip, err
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO hook_dedupe(k, decision, ts) VALUES(?, ?, ?)`, key, pendingDecision, now.UnixNano())
	if err != nil {
		return hookapi.Decision{}, claimSkip, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return hookapi.Decision{}, claimOwner, nil
	}

	deadline := time.Now().Add(orDefault(d.wait, defaultDedupeWait))
	poll := orDefault(d.poll, defaultDedupePoll)
	for {
		var stored string
		err := d.db.QueryRowContext(ctx, `SELECT decision FROM hook_dedupe WHERE k = ?`, key).Scan(&stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return hookapi.Decision{}, claimSkip, nil // consumed or released meanwhile
		case err != nil:
			return hookapi.Decision{}, claimSkip, err
		case stored != pendingDecision:
			return d.consume(ctx, key, stored)
		}
		if !time.Now().Before(deadline) {
			return hookapi.Decision{}, claimSkip, nil
		}
		select {
		case <-ctx.Done():
			return hookapi.Decision{}, claimSkip, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// consume deletes a stored decision and replays it; when a concurrent
// duplicate consumed it first, the caller computes on its own.
func (d *dedupe) consume(ctx context.Context, key, stored string) (hookapi.Decision, claimResult, error) {
	res, err := d.db.ExecContext(ctx, `DELETE FROM hook_dedupe WHERE k = ? AND decision = ?`, key, stored)
	if err != nil {
		return hookapi.Decision{}, claimSkip, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return hookapi.Decision{}, claimSkip, nil
	}
	var dec hookapi.Decision
	if err := json.Unmarshal([]byte(stored), &dec); err != nil {
		return hookapi.Decision{}, claimSkip, err
	}
	return dec, claimReplay, nil
}

// store records the decision for a key this invocation owns.
func (d *dedupe) store(ctx context.Context, key string, dec hookapi.Decision) error {
	if key == "" {
		return nil
	}
	b, err := json.Marshal(dec)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `INSERT OR REPLACE INTO hook_dedupe(k, decision, ts) VALUES(?, ?, ?)`,
		key, string(b), d.clock().UnixNano())
	return err
}

// release drops an unfinished claim so a duplicate does not wait for it.
func (d *dedupe) release(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := d.db.ExecContext(ctx, `DELETE FROM hook_dedupe WHERE k = ? AND decision = ?`, key, pendingDecision)
	return err
}
