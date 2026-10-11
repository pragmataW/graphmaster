package steer

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/query"
	"github.com/pragmataW/graphmaster/internal/store"
)

// sessionKey identifies the steering state of one agent in one session.
// The main agent has an empty AgentID; every subagent has its own state.
type sessionKey struct {
	SID     string
	AgentID string
}

// sessionStore persists steering state in graph.db (tables sessions and
// session_patterns). Times are Unix milliseconds.
type sessionStore struct {
	db  *sql.DB
	now func() time.Time
}

// lookup reports whether pnorm was already denied for key, and how many
// denies key has used.
func (s sessionStore) lookup(ctx context.Context, k sessionKey, pnorm string) (seen bool, denies int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM session_patterns WHERE sid = ?1 AND agent_id = ?2 AND pnorm = ?3),
		coalesce((SELECT denies FROM sessions WHERE sid = ?1 AND agent_id = ?2), 0)`,
		k.SID, k.AgentID, pnorm).Scan(&seen, &denies)
	return seen, denies, err
}

// recordDeny counts a deny for key and remembers pnorm, atomically.
func (s sessionStore) recordDeny(ctx context.Context, k sessionKey, agent hookapi.Agent, pnorm string, tool hookapi.Tool) error {
	ts := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(sid, agent_id, agent, started, last, denies, allows)
		VALUES(?, ?, ?, ?, ?, 1, 0)
		ON CONFLICT(sid, agent_id) DO UPDATE SET denies = denies + 1, last = excluded.last`,
		k.SID, k.AgentID, string(agent), ts, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_patterns(sid, agent_id, pnorm, tool, denies, first)
		VALUES(?, ?, ?, ?, 1, ?)
		ON CONFLICT(sid, agent_id, pnorm) DO UPDATE SET denies = denies + 1`,
		k.SID, k.AgentID, pnorm, string(tool), ts); err != nil {
		return err
	}
	return tx.Commit()
}

// recordAllow counts a steering allow (retry or exhausted budget).
func (s sessionStore) recordAllow(ctx context.Context, k sessionKey, agent hookapi.Agent) error {
	ts := s.now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(sid, agent_id, agent, started, last, denies, allows)
		VALUES(?, ?, ?, ?, ?, 0, 1)
		ON CONFLICT(sid, agent_id) DO UPDATE SET allows = allows + 1, last = excluded.last`,
		k.SID, k.AgentID, string(agent), ts, ts)
	return err
}

// touch registers a session (start events) without changing its counters.
func (s sessionStore) touch(ctx context.Context, k sessionKey, agent hookapi.Agent) error {
	ts := s.now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(sid, agent_id, agent, started, last)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(sid, agent_id) DO UPDATE SET last = excluded.last`,
		k.SID, k.AgentID, string(agent), ts, ts)
	return err
}

// MaxStartContextBytes caps SessionStart / SubagentStart context
// (600 tokens at ~4 bytes per token).
const MaxStartContextBytes = 600 * 4

// startRepoMapLines caps the top-level repo_map in start context.
const startRepoMapLines = 16

// SessionStartContext is Engine.SessionStartContext with a one-shot engine.
func SessionStartContext(ctx context.Context, ev hookapi.HookEvent, cfg config.Config) string {
	e := New(Options{})
	defer func() { _ = e.Close() }()
	return e.SessionStartContext(ctx, ev, cfg)
}

// SubagentStartContext is Engine.SubagentStartContext with a one-shot engine.
func SubagentStartContext(ctx context.Context, ev hookapi.HookEvent, cfg config.Config) string {
	e := New(Options{})
	defer func() { _ = e.Close() }()
	return e.SubagentStartContext(ctx, ev, cfg)
}

// SessionStartContext returns the context injected at session start: index
// statistics, the MCP tool cheat-sheet, the steering rules of the mode and
// the top-level repo_map, within MaxStartContextBytes. A missing index is
// built first (the session start budget allows it). Outside a repository
// it returns "".
func (e *Engine) SessionStartContext(ctx context.Context, ev hookapi.HookEvent, cfg config.Config) string {
	return e.startContext(ctx, ev, cfg, false)
}

// SubagentStartContext is the subagent variant (Claude's Explore / Plan
// agents do not read CLAUDE.md). It never builds in the foreground: a
// missing index is handed to StartBuild and the rules are still returned.
func (e *Engine) SubagentStartContext(ctx context.Context, ev hookapi.HookEvent, cfg config.Config) string {
	return e.startContext(ctx, ev, cfg, true)
}

func (e *Engine) startContext(ctx context.Context, ev hookapi.HookEvent, cfg config.Config, subagent bool) (out string) {
	defer func() {
		if r := recover(); r != nil {
			e.opts.Logf("steer: start context panic: %v", r)
			out = ""
		}
	}()
	mode := effectiveMode(cfg.Mode)
	g, ok := e.graph(ctx, ev, &cfg, !subagent)
	if !ok {
		if !subagent || !e.inRepo(ev) {
			return ""
		}
		// No index yet: graph() handed the build to StartBuild.
		return composeStart(subagent, mode, "index: building in the background; graph tools answer once it is ready", nil, softBudget(cfg))
	}
	ss := sessionStore{db: g.ix.Store().DB(), now: e.opts.Now}
	if err := ss.touch(ctx, sessionKey{SID: ev.SessionID, AgentID: ev.AgentID}, ev.Agent); err != nil {
		e.opts.Logf("steer: register session: %v", err)
	}
	stats := e.statsLine(ctx, g)
	var repoMap []string
	if rm, err := g.q.RepoMap(ctx, query.RepoMapInput{Depth: 1, MaxTokens: 600}); err == nil {
		repoMap = firstN(bodyLines(rm), startRepoMapLines)
	} else {
		e.opts.Logf("steer: repo_map: %v", err)
	}
	return composeStart(subagent, mode, stats, repoMap, softBudget(cfg))
}

// inRepo reports whether ev's directory is inside a repository.
func (e *Engine) inRepo(ev hookapi.HookEvent) bool {
	if ev.CWD == "" {
		return false
	}
	_, err := index.Locate(resolvePath(ev.CWD), false)
	return err == nil
}

// statsLine summarizes the index in one line.
func (e *Engine) statsLine(ctx context.Context, g *graph) string {
	st, err := g.ix.Status(ctx)
	if err != nil {
		e.opts.Logf("steer: status: %v", err)
		return "index: available"
	}
	var indexed int64
	langs := make([]model.Lang, 0, len(st.Langs))
	for l, n := range st.Langs {
		indexed += n
		langs = append(langs, l)
	}
	sort.Slice(langs, func(i, j int) bool {
		a, b := st.Langs[langs[i]], st.Langs[langs[j]]
		return a > b || (a == b && langs[i] < langs[j])
	})
	parts := make([]string, 0, len(langs))
	for _, l := range langs {
		parts = append(parts, fmt.Sprintf("%s %d", l, st.Langs[l]))
	}
	line := fmt.Sprintf("index: %d files", indexed)
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}
	line += fmt.Sprintf(", %d symbols, %d edges", st.Nodes, st.Edges)
	state := "fresh"
	switch {
	case st.State == store.StateBuilding:
		state = fmt.Sprintf("building %d%%", st.Progress)
	case g.res != nil:
		state = g.res.Banner()
	}
	return line + " · " + state
}

// cheatSheet lists the MCP tools, most useful first.
const cheatSheet = `MCP tools (server "graphmaster"):
  find_symbol <name>      definitions by name (exact > prefix > fuzzy)
  search <words>          symbol and file names
  read_symbol <symbol>    source of one definition
  file_outline <path>     definitions of a file
  repo_map [path_prefix]  directory tree with top symbols
  callers / callees <sym> call tree; impact <sym|path|changed> blast radius
  imports, inheritance, tests_for, traverse, status`

func modeRules(mode config.Mode, budget int) string {
	switch mode {
	case config.ModeStrict:
		return "Steering mode strict: grep/glob/rg/find over code are always blocked; use the tools above. " +
			"Docs, configs, a single named file and Read are never blocked. Ask the user to set mode = \"balanced\" if text search is required."
	case config.ModeOff:
		return "Steering mode off: searches are not steered, but the graph tools are faster and smaller than grep for code."
	}
	return fmt.Sprintf("Steering mode balanced: the first grep/glob for a code symbol is blocked once with graph results; "+
		"run the same search again to get text matches. Text, docs and config searches and Read are never blocked. "+
		"After %d redirects per agent nothing is blocked.", budget)
}

// composeStart assembles the start context and trims the repo map to fit.
func composeStart(subagent bool, mode config.Mode, stats string, repoMap []string, budget int) string {
	var head strings.Builder
	head.WriteString("graphmaster: a code graph of this repository is available; prefer it over grep/find/ls for code navigation.\n")
	if subagent {
		head.WriteString("These rules apply to you as a subagent too (with your own redirect budget).\n")
	}
	head.WriteString(stats)
	head.WriteByte('\n')
	head.WriteString(cheatSheet)
	head.WriteByte('\n')
	head.WriteString(modeRules(mode, budget))
	out := head.String()
	if len(repoMap) == 0 {
		return clipLine(out, MaxStartContextBytes)
	}
	var b strings.Builder
	b.WriteString(out)
	b.WriteString("\nrepo map:")
	for _, l := range repoMap {
		l = clipLine(l, 160)
		if b.Len()+1+len(l) > MaxStartContextBytes {
			break
		}
		b.WriteByte('\n')
		b.WriteString(l)
	}
	return clipLine(b.String(), MaxStartContextBytes)
}
