package hookrun

import (
	"context"
	"path/filepath"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/steer"
)

// backgroundStartContext is the session-start context while the first
// build runs in the background (Cursor, OpenCode).
const backgroundStartContext = "graphmaster: the code graph of this repository is being built in the background. " +
	"Once it is ready, prefer the graphmaster MCP tools (find_symbol, search, read_symbol, callers, " +
	"file_outline, repo_map) over grep/find/ls for code navigation."

// route handles one parsed event (PLAN "Steering algoritması" and
// "Post-edit").
func (inv *invocation) route(ctx context.Context, ev hookapi.HookEvent, cfg *config.Config) hookapi.Decision {
	switch ev.Event {
	case hookapi.EventSessionStart:
		return inv.sessionStart(ctx, ev, cfg)
	case hookapi.EventSubagentStart:
		return hookapi.Noop().WithContext(inv.engine(false).SubagentStartContext(ctx, ev, *cfg))
	case hookapi.EventPreToolUse:
		return inv.preToolUse(ctx, ev, cfg)
	case hookapi.EventPostToolUse:
		inv.postToolUse(ctx, ev, cfg)
	case hookapi.EventPreCompact:
		return inv.preCompact(ctx, ev, cfg)
	}
	return hookapi.Noop()
}

// engine returns a steering engine over this invocation's indexes. With
// readOnly the engine never freshens: it answers from the graph as it is.
func (inv *invocation) engine(readOnly bool) *steer.Engine {
	return steer.New(steer.Options{
		Open: func(ctx context.Context, _ *index.Location, _ *config.Config) (steer.Index, error) {
			ix, err := inv.index(ctx)
			if err != nil {
				return nil, err
			}
			if readOnly {
				return noFreshen{ix}, nil
			}
			return borrowed{ix}, nil
		},
		StartBuild:   inv.startBuild,
		LookupBudget: inv.d.opts.LookupBudget,
		Now:          inv.d.opts.Now,
		Logf:         inv.logf,
	})
}

// borrowed hides Close: the invocation, not the engine, owns the index.
type borrowed struct{ steer.Index }

// noFreshen answers from the current graph; a background process refreshes.
type noFreshen struct{ steer.Index }

func (noFreshen) Freshen(context.Context, time.Duration) (*index.Result, error) {
	return &index.Result{}, nil
}

// sessionStart: Claude and Codex wait for the first build (590s budget);
// Cursor and OpenCode cannot block, so a detached `graphmaster index
// --quiet` builds or refreshes while the context is returned at once.
func (inv *invocation) sessionStart(ctx context.Context, ev hookapi.HookEvent, cfg *config.Config) hookapi.Decision {
	switch ev.Agent {
	case hookapi.AgentClaude, hookapi.AgentCodex:
		return hookapi.Noop().WithContext(inv.engine(false).SessionStartContext(ctx, ev, *cfg))
	}
	if inv.loc == nil {
		return hookapi.Noop()
	}
	inv.startBuild(inv.loc.Root.WorkTree)
	if !inv.indexReady(ctx) {
		return hookapi.Noop().WithContext(backgroundStartContext)
	}
	return hookapi.Noop().WithContext(inv.engine(true).SessionStartContext(ctx, ev, *cfg))
}

// preToolUse steers the call (the engine freshens with the PreToolUse
// budget first). A duplicate invocation of the same call replays the
// first decision instead.
func (inv *invocation) preToolUse(ctx context.Context, ev hookapi.HookEvent, cfg *config.Config) hookapi.Decision {
	eng := inv.engine(false)
	dd, key := inv.dedupeFor(ctx, ev)
	if dd == nil {
		return eng.Decide(ctx, ev, *cfg)
	}
	dec, claim, err := dd.claim(ctx, key)
	if err != nil {
		inv.logf("dedupe: %v", err)
	}
	switch claim {
	case claimReplay:
		return dec
	case claimSkip:
		return eng.Decide(ctx, ev, *cfg)
	}
	dec = eng.Decide(ctx, ev, *cfg)
	// A decision past the deadline was never delivered: do not replay it.
	bg := context.WithoutCancel(ctx)
	if ctx.Err() != nil {
		err = dd.release(bg, key)
	} else {
		err = dd.store(bg, key, dec)
	}
	if err != nil {
		inv.logf("dedupe: %v", err)
	}
	return dec
}

// dedupeFor returns the dedupe table of the event's repository and the
// event's key, or nil when the event cannot be deduplicated.
func (inv *invocation) dedupeFor(ctx context.Context, ev hookapi.HookEvent) (*dedupe, string) {
	key := dedupeKey(ev)
	if key == "" || !inv.graphExists() {
		return nil, ""
	}
	ix, err := inv.index(ctx)
	if err != nil {
		inv.logf("open index: %v", err)
		return nil, ""
	}
	return &dedupe{db: ix.Store().DB(), now: inv.d.opts.Now, wait: inv.d.opts.DedupeWait}, key
}

// postToolUse keeps the graph fresh after the agent changed files: edits
// record dirty hints for their paths and reindex; a shell command may have
// changed anything, so it runs a full freshen. Both use the PostToolUse
// lock budget.
func (inv *invocation) postToolUse(ctx context.Context, ev hookapi.HookEvent, cfg *config.Config) {
	if !ev.Tool.IsMutation() && ev.Tool != hookapi.ToolShell {
		return
	}
	if !inv.indexReady(ctx) {
		return // no graph yet: session start (or a background build) creates it
	}
	ix, err := inv.index(ctx)
	if err != nil {
		inv.logf("open index: %v", err)
		return
	}
	if ev.Tool.IsMutation() {
		hints := inv.hintPaths(ev)
		if err := ix.Store().AddDirtyHints(ctx, hints, inv.d.opts.Now().UnixNano()); err != nil {
			inv.logf("dirty hints: %v", err)
		}
	}
	res, err := ix.Freshen(ctx, cfg.Timeouts.ForEvent(hookapi.EventPostToolUse))
	if err != nil {
		inv.logf("freshen: %v", err)
		return
	}
	if res.Stale {
		inv.logf("freshen: %s (writer busy)", res.Banner())
	}
}

// hintPaths maps the event's target paths to repo-relative paths; paths
// outside the repository are dropped.
func (inv *invocation) hintPaths(ev hookapi.HookEvent) []string {
	cwd := firstNonEmpty(ev.CWD, inv.req.WorkDir)
	var out []string
	for _, p := range ev.Paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if rel, ok := relInRepo(inv.loc.Root, filepath.Clean(p)); ok && rel != "" {
			out = append(out, rel)
		}
	}
	return out
}

// relInRepo maps abs into root, resolving symlinks in the path (or, for a
// deleted file, in its directory) when the literal path is outside.
func relInRepo(root *repo.Root, abs string) (string, bool) {
	if rel, ok := root.Rel(abs); ok {
		return rel, true
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return root.Rel(r)
	}
	if d, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return root.Rel(filepath.Join(d, filepath.Base(abs)))
	}
	return "", false
}

// preCompact re-injects the start context so the steering rules survive
// compaction (the agent's adapter decides whether it can carry context).
func (inv *invocation) preCompact(ctx context.Context, ev hookapi.HookEvent, cfg *config.Config) hookapi.Decision {
	if inv.loc == nil {
		return hookapi.Noop()
	}
	if !inv.indexReady(ctx) {
		inv.startBuild(inv.loc.Root.WorkTree)
		return hookapi.Noop().WithContext(backgroundStartContext)
	}
	return hookapi.Noop().WithContext(inv.engine(false).SessionStartContext(ctx, ev, *cfg))
}
