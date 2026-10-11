package replay

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/hookrun"
)

// TestRealPayloadsParse checks that every payload captured from a real agent
// session still parses with its adapter, so agent upgrades that change the
// wire format show up as a failing test instead of a silent fail-open.
func TestRealPayloadsParse(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(HooksDir(), "*", "real-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no real payload fixtures found")
	}
	vars := map[string]string{"REPO": t.TempDir(), "HOME": t.TempDir()}
	for _, f := range files {
		rel, _ := filepath.Rel(HooksDir(), f)
		t.Run(filepath.ToSlash(rel), func(t *testing.T) {
			rec, err := Load(filepath.ToSlash(rel))
			if err != nil {
				t.Fatal(err)
			}
			a, ok := adapters.Get(hookapi.Agent(rec.Agent))
			if !ok {
				t.Fatalf("no adapter for %q", rec.Agent)
			}
			ev, err := hookapi.ParseEvent(rec.Event)
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := Stdin(rec, withIDs(vars, rec))
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.ParseEvent(ev, stdin, nil)
			if err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}
			if got.Event != ev {
				t.Errorf("event = %q, want %q", got.Event, ev)
			}
			if ev.IsToolEvent() && got.Tool == "" {
				t.Errorf("tool event parsed without a tool")
			}
		})
	}
}

// withIDs fills every id-style placeholder a fixture uses with a stable value.
func withIDs(base map[string]string, rec *hookrun.Recording) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for _, p := range Placeholders(string(rec.Stdin) + rec.StdinText) {
		if _, ok := out[p]; !ok {
			out[p] = "id-" + strings.ToLower(p)
		}
	}
	return out
}
