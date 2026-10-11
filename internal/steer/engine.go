package steer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/query"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Steering limits (PLAN "Steering algoritması").
const (
	// DefaultSoftBudget is the balanced-mode deny budget per (session,
	// agent) used when config.SoftBudget is not positive.
	DefaultSoftBudget = 8
	// DefaultLookupBudget bounds the graph lookup of one decision.
	DefaultLookupBudget = 25 * time.Millisecond
	// LookupLimit is the number of graph results quoted in a reason.
	LookupLimit = 12
	// MaxReasonLen caps a deny reason in bytes.
	MaxReasonLen = 1200
	// RepoMapMaxLines caps the repo_map quoted for an exploration.
	RepoMapMaxLines = 40
	// ReadOutlineMinLines is the size above which a whole-file read of a
	// code file gets an outline.
	ReadOutlineMinLines = 400
	// ReadOutlineMaxLines caps that outline.
	ReadOutlineMaxLines = 25
)

// defaultHookBudget is the freshen lock wait when the config has none.
const defaultHookBudget = 1500 * time.Millisecond

// Index is the code graph the engine consults. *index.Indexer implements it.
type Index interface {
	query.Backend
	Freshen(ctx context.Context, budget time.Duration) (*index.Result, error)
}

// Options configure an Engine. Every field is optional.
type Options struct {
	// Open opens the index of a located repository. The default is
	// index.Open with the real resolver. Indexes implementing io.Closer are
	// closed by Engine.Close.
	Open func(ctx context.Context, loc *index.Location, cfg *config.Config) (Index, error)
	// StartBuild is the hook point for building a missing index in the
	// background (e.g. spawning a detached `graphmaster index --quiet`).
	// Decisions never build an index themselves. Nil does nothing.
	StartBuild func(root string)
	// LookupBudget bounds the graph lookup (default 25ms).
	LookupBudget time.Duration
	// Now is the session clock (default time.Now).
	Now func() time.Time
	// Logf receives internal errors; steering always fails open.
	Logf func(format string, args ...any)
}

// Engine makes steering decisions for hook events. It caches opened
// indexes per repository root; it is safe for concurrent use.
type Engine struct {
	opts Options

	mu      sync.Mutex
	indexes map[string]Index
}

// New returns an Engine.
func New(opts Options) *Engine {
	if opts.Open == nil {
		opts.Open = openIndex
	}
	if opts.LookupBudget <= 0 {
		opts.LookupBudget = DefaultLookupBudget
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Engine{opts: opts, indexes: map[string]Index{}}
}

func openIndex(ctx context.Context, loc *index.Location, cfg *config.Config) (Index, error) {
	return index.Open(ctx, loc.Root.WorkTree, index.Options{
		Config: cfg, Resolver: &resolve.Resolver{}, AllowNoGit: loc.NoGit,
	})
}

// Close closes the indexes the engine opened.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var errs []error
	for root, ix := range e.indexes {
		if c, ok := ix.(io.Closer); ok {
			errs = append(errs, c.Close())
		}
		delete(e.indexes, root)
	}
	return errors.Join(errs...)
}

// Decide is Engine.Decide with a one-shot engine (one hook process).
func Decide(ctx context.Context, ev hookapi.HookEvent, cfg config.Config) hookapi.Decision {
	e := New(Options{})
	defer func() { _ = e.Close() }()
	return e.Decide(ctx, ev, cfg)
}

// Decide steers one PreToolUse event (grep, glob, read or shell). Any
// other event or tool, and every internal error, yields allow.
func (e *Engine) Decide(ctx context.Context, ev hookapi.HookEvent, cfg config.Config) (d hookapi.Decision) {
	defer func() {
		if r := recover(); r != nil {
			e.opts.Logf("steer: panic: %v", r)
			d = hookapi.Allow()
		}
	}()
	if ev.Event != hookapi.EventPreToolUse {
		return hookapi.Allow()
	}
	mode := effectiveMode(cfg.Mode)
	if mode == config.ModeOff {
		return hookapi.Allow()
	}
	switch ev.Tool {
	case hookapi.ToolGrep, hookapi.ToolGlob, hookapi.ToolRead, hookapi.ToolShell:
	default:
		return hookapi.Allow()
	}
	g, ok := e.graph(ctx, ev, &cfg, false)
	if !ok {
		return hookapi.Allow()
	}
	dc := &decider{e: e, g: g, ev: ev, mode: mode, budget: softBudget(cfg)}
	switch ev.Tool {
	case hookapi.ToolRead:
		return dc.read(ctx)
	case hookapi.ToolGrep:
		return dc.run(ctx, dc.grepRequest(ctx))
	case hookapi.ToolGlob:
		return dc.run(ctx, dc.globRequest(ctx))
	default:
		return dc.run(ctx, dc.shellRequest(ctx))
	}
}

func effectiveMode(m config.Mode) config.Mode {
	if p, err := config.ParseMode(string(m)); err == nil {
		return p
	}
	return config.ModeBalanced
}

func softBudget(cfg config.Config) int {
	if cfg.SoftBudget > 0 {
		return cfg.SoftBudget
	}
	return DefaultSoftBudget
}

// graph is an opened, freshened index seen from one event.
type graph struct {
	ix    Index
	root  string // symlink-resolved work tree
	cwd   string // symlink-resolved absolute event directory
	exts  ExtSet
	q     *query.Engine
	res   *index.Result
	state string
}

// fixedResult makes the query engine reuse the freshen result of the
// decision instead of freshening before every lookup.
type fixedResult struct{ res *index.Result }

func (f fixedResult) Freshen(context.Context, time.Duration) (*index.Result, error) {
	if f.res == nil {
		return &index.Result{}, nil
	}
	r := *f.res
	return &r, nil
}

// graph locates the repository of ev and returns its freshened index.
// Without an index it calls StartBuild and returns false, unless build is
// set (SessionStart), in which case freshening builds it in the foreground.
func (e *Engine) graph(ctx context.Context, ev hookapi.HookEvent, cfg *config.Config, build bool) (*graph, bool) {
	cwd := ev.CWD
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, false
		}
		cwd = wd
	}
	cwd = resolvePath(cwd)
	loc, err := index.Locate(cwd, false)
	if err != nil {
		return nil, false // not a git repository (or unreadable): never steer
	}
	_, statErr := os.Stat(filepath.Join(loc.Dir, store.GraphDBName))
	missing := statErr != nil
	if missing && !build {
		e.startBuild(loc.Root.WorkTree)
		return nil, false
	}
	ix, err := e.open(ctx, loc, cfg)
	if err != nil {
		e.opts.Logf("steer: open index: %v", err)
		return nil, false
	}
	g := &graph{ix: ix, root: loc.Root.WorkTree, cwd: cwd, exts: NewExtSet(cfg.EnabledLangs())}
	g.state, _, _ = ix.Store().Meta(ctx, store.MetaState)
	if g.state == "" && !build {
		// Created but never scanned: a foreground build would stall the
		// agent, so build in the background and do not steer yet.
		e.startBuild(loc.Root.WorkTree)
		return nil, false
	}
	if g.state != store.StateBuilding || build {
		budget := cfg.Timeouts.ForEvent(ev.Event)
		if budget <= 0 {
			budget = defaultHookBudget
		}
		res, err := ix.Freshen(ctx, budget)
		if err != nil {
			e.opts.Logf("steer: freshen: %v", err) // answer from the graph as it is
		}
		g.res = res
		g.state, _, _ = ix.Store().Meta(ctx, store.MetaState)
	} else {
		g.res = &index.Result{Building: true}
	}
	g.q = query.New(ix, query.Options{Freshener: fixedResult{g.res}})
	return g, true
}

func (e *Engine) startBuild(root string) {
	if e.opts.StartBuild != nil {
		e.opts.StartBuild(root)
	}
}

func (e *Engine) open(ctx context.Context, loc *index.Location, cfg *config.Config) (Index, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ix, ok := e.indexes[loc.Root.WorkTree]; ok {
		return ix, nil
	}
	ix, err := e.opts.Open(ctx, loc, cfg)
	if err != nil {
		return nil, err
	}
	if ix == nil {
		return nil, errors.New("opener returned no index")
	}
	e.indexes[loc.Root.WorkTree] = ix
	return ix, nil
}

// resolvePath makes p absolute and resolves symlinks when it exists.
func resolvePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// rel maps a target (relative to the event directory unless absolute) to a
// repo-relative slash path; ok is false outside the work tree.
func (g *graph) rel(target string) (string, bool) {
	p := target
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(g.cwd, p)
	}
	p = filepath.Clean(p)
	if r, ok := relUnder(g.root, p); ok {
		return r, true
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return relUnder(g.root, r)
	}
	return "", false
}

func relUnder(root, abs string) (string, bool) {
	r, err := filepath.Rel(root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	if r == "." {
		return "", true
	}
	return filepath.ToSlash(r), true
}

// staticDir cuts a path before its first segment holding glob syntax.
func staticDir(p string) string {
	segs := strings.Split(filepath.ToSlash(p), "/")
	for i, s := range segs {
		if strings.ContainsAny(s, globMeta+"{") {
			out := strings.Join(segs[:i], "/")
			if out == "" && strings.HasPrefix(p, "/") {
				return "/"
			}
			if out == "" {
				return "."
			}
			return out
		}
	}
	return p
}

// hasIndexed reports whether rel is an indexed file or a directory with at
// least one indexed file below it ("" = the whole repository).
func (g *graph) hasIndexed(ctx context.Context, rel string) (bool, error) {
	db := g.ix.Store().DB()
	ok := fmt.Sprintf("status IN (%d, %d)", model.StatusOK, model.StatusParseErr)
	var n int
	var err error
	if rel == "" {
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM files WHERE `+ok+`)`).Scan(&n)
	} else {
		// '0' sorts right after '/', bounding the "rel/" prefix range.
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM files
			WHERE (path = ? OR (path >= ? AND path < ?)) AND `+ok+`)`,
			rel, rel+"/", rel+"0").Scan(&n)
	}
	return n == 1, err
}

// scope resolves search targets. inScope is true when at least one target
// may hold indexed code; prefix is the repo-relative directory to scope
// lookups by when there is exactly one such target.
func (g *graph) scope(ctx context.Context, paths, filters []string) (inScope bool, prefix string, err error) {
	if len(filters) > 0 {
		allNonCode := true
		for _, f := range filters {
			if ClassifyGlob(f, g.exts).Kind != GlobNonCode {
				allNonCode = false
				break
			}
		}
		if allNonCode {
			return false, "", nil
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	var hits []string
	for _, p := range paths {
		switch ClassifyTarget(p, g.exts) {
		case TargetNonCode:
			continue
		case TargetCode:
			if !strings.ContainsAny(p, globMeta+"{") {
				continue // one named file: a targeted read, never steered
			}
		}
		rel, ok := g.rel(staticDir(p))
		if !ok {
			continue
		}
		has, err := g.hasIndexed(ctx, rel)
		if err != nil {
			return false, "", err
		}
		if has {
			hits = append(hits, rel)
		}
	}
	if len(hits) == 1 {
		prefix = hits[0]
	}
	return len(hits) > 0, prefix, nil
}

// reqKind is what a steered search is after.
type reqKind int

const (
	reqSymbol  reqKind = iota // symbol-like content search
	reqText                   // other code-scoped content search
	reqExplore                // broad file-name scan
)

// request is a code-scoped search that steering applies to.
type request struct {
	kind   reqKind
	class  PatternClass // reqSymbol, reqText
	norm   string       // pnorm for retry detection
	prefix string       // repo-relative lookup scope
	label  string       // what the agent ran, for the reason
}

// decider carries one decision.
type decider struct {
	e      *Engine
	g      *graph
	ev     hookapi.HookEvent
	mode   config.Mode
	budget int
}

func (dc *decider) fail(what string, err error) hookapi.Decision {
	dc.e.opts.Logf("steer: %s: %v", what, err)
	return hookapi.Allow()
}

func (dc *decider) grepRequest(ctx context.Context) *request {
	var filters []string
	if dc.ev.Glob != "" {
		filters = []string{dc.ev.Glob}
	}
	in, prefix, err := dc.g.scope(ctx, dc.ev.Paths, filters)
	if err != nil {
		dc.e.opts.Logf("steer: scope: %v", err)
		return nil
	}
	if !in {
		return nil
	}
	return contentRequest(ClassifyPattern(dc.ev.Pattern), prefix, "grep "+quote(dc.ev.Pattern))
}

func contentRequest(c PatternClass, prefix, label string) *request {
	r := &request{class: c, norm: c.Norm, prefix: prefix, label: label, kind: reqText}
	if c.SymbolLike {
		r.kind = reqSymbol
	}
	return r
}

func (dc *decider) globRequest(ctx context.Context) *request {
	gc := ClassifyGlob(dc.ev.Glob, dc.g.exts)
	if gc.Allow() {
		return nil
	}
	paths := dc.ev.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}
	// "src/**" scans src: fold the glob's static directory into each root.
	scoped := make([]string, 0, len(paths))
	dir := staticDir(dc.ev.Glob)
	for _, p := range paths {
		switch {
		case filepath.IsAbs(dir):
			scoped = append(scoped, dir)
		case dir == "." || dir == "":
			scoped = append(scoped, p)
		default:
			scoped = append(scoped, filepath.Join(p, dir))
		}
	}
	in, prefix, err := dc.g.scope(ctx, scoped, nil)
	if err != nil {
		dc.e.opts.Logf("steer: scope: %v", err)
		return nil
	}
	if !in {
		return nil
	}
	return &request{kind: reqExplore, norm: "glob:" + baseName(dc.ev.Glob), prefix: prefix, label: "glob " + quote(dc.ev.Glob)}
}

// shellRequest picks the steered search of a shell command line: a
// symbol-like content search first, then any other code-scoped content
// search, then a broad file-name scan.
func (dc *decider) shellRequest(ctx context.Context) *request {
	an, err := AnalyzeShell(dc.ev.Command)
	if err != nil || !an.Steerable() {
		return nil
	}
	var text, explore *request
	for _, sc := range an.Searches {
		if sc.ReadsStdin {
			continue
		}
		label := sc.Tool + " " + quote(strings.Join(sc.Patterns, " "))
		if !sc.NameSearch {
			if (!sc.Recursive && sc.Paths != nil) || len(sc.Patterns) == 0 {
				continue // one named file, or an unknown (dynamic / -f) pattern
			}
			in, prefix, err := dc.g.scope(ctx, sc.Paths, sc.Globs)
			if err != nil {
				dc.e.opts.Logf("steer: scope: %v", err)
				return nil
			}
			if !in {
				continue
			}
			r := contentRequest(ClassifyPatterns(sc.Patterns, sc.Fixed), prefix, label)
			if r.kind == reqSymbol {
				return r
			}
			if text == nil {
				text = r
			}
			continue
		}
		if explore != nil || !sc.Recursive || !dc.broadNameSearch(sc) {
			continue
		}
		in, prefix, err := dc.g.scope(ctx, sc.Paths, nil)
		if err != nil {
			dc.e.opts.Logf("steer: scope: %v", err)
			return nil
		}
		if in {
			explore = &request{kind: reqExplore, norm: "glob:" + nameNorm(sc), prefix: prefix, label: strings.TrimSpace(sc.Tool + " " + strings.Join(sc.Globs, " "))}
		}
	}
	if text != nil {
		return text
	}
	return explore
}

// broadNameSearch reports whether a file-name search scans code broadly:
// no name filter at all, or a wildcard filter that may match code files.
// A literal file name or a non-code filter is a targeted lookup.
func (dc *decider) broadNameSearch(sc SearchCall) bool {
	if len(sc.Globs) == 0 && len(sc.Patterns) == 0 {
		return true
	}
	for _, gl := range sc.Globs {
		if ClassifyGlob(gl, dc.g.exts).Kind == GlobExplore {
			return true
		}
	}
	for _, p := range sc.Patterns {
		if !ClassifyPattern(p).SymbolLike {
			return true // a regex over names, e.g. find -regex '.*\.go'
		}
	}
	return false
}

func nameNorm(sc SearchCall) string {
	parts := make([]string, 0, len(sc.Globs)+len(sc.Patterns))
	for _, g := range sc.Globs {
		parts = append(parts, baseName(g))
	}
	parts = append(parts, sc.Patterns...)
	if len(parts) == 0 {
		return "*"
	}
	return strings.Join(parts, " ")
}

// run applies the mode rules (PLAN steps 6-8) to a request; nil allows.
func (dc *decider) run(ctx context.Context, r *request) hookapi.Decision {
	if r == nil {
		return hookapi.Allow()
	}
	ss := sessionStore{db: dc.g.ix.Store().DB(), now: dc.e.opts.Now}
	key := sessionKey{SID: dc.ev.SessionID, AgentID: dc.ev.AgentID}

	if dc.mode == config.ModeStrict {
		results := dc.results(ctx, r)
		if err := ss.recordDeny(ctx, key, dc.ev.Agent, r.norm, dc.ev.Tool); err != nil {
			dc.e.opts.Logf("steer: record deny: %v", err)
		}
		return hookapi.Deny(strictReason(r, results))
	}

	// balanced
	if r.kind == reqText {
		return hookapi.Allow()
	}
	seen, denies, err := ss.lookup(ctx, key, r.norm)
	if err != nil {
		return dc.fail("session", err)
	}
	if seen {
		dc.noteAllow(ctx, ss, key)
		return hookapi.Allow()
	}
	results := dc.results(ctx, r)
	if len(results) == 0 {
		return hookapi.Allow()
	}
	if denies >= dc.budget {
		dc.noteAllow(ctx, ss, key)
		return hookapi.Allow().WithContext(budgetReminder(dc.budget))
	}
	if err := ss.recordDeny(ctx, key, dc.ev.Agent, r.norm, dc.ev.Tool); err != nil {
		return dc.fail("record deny", err) // without the record a retry would be denied again
	}
	return hookapi.Deny(balancedReason(r, results))
}

func (dc *decider) noteAllow(ctx context.Context, ss sessionStore, key sessionKey) {
	if err := ss.recordAllow(ctx, key, dc.ev.Agent); err != nil {
		dc.e.opts.Logf("steer: record allow: %v", err)
	}
}

// results looks the request up in the graph within the lookup budget.
// Lookup errors (including the timeout) count as "no results".
func (dc *decider) results(ctx context.Context, r *request) []string {
	ctx, cancel := context.WithTimeout(ctx, dc.e.opts.LookupBudget)
	defer cancel()
	q := dc.g.q
	if r.kind == reqExplore {
		depth := 2
		if r.prefix == "" {
			depth = 1 // the whole repository: top-level directories only
		}
		out, err := q.RepoMap(ctx, query.RepoMapInput{PathPrefix: r.prefix, Depth: depth, MaxTokens: query.MaxTokensCap})
		if err != nil {
			dc.e.opts.Logf("steer: repo_map: %v", err)
			return nil
		}
		return firstN(bodyLines(out), RepoMapMaxLines)
	}

	var lines []string
	seen := map[string]bool{}
	add := func(out string) {
		for _, l := range bodyLines(out) {
			if !seen[l] && len(lines) < LookupLimit {
				seen[l] = true
				lines = append(lines, l)
			}
		}
	}
	queries := r.class.Queries
	if len(queries) > 4 {
		queries = queries[:4]
	}
	for _, qs := range queries {
		out, err := q.FindSymbol(ctx, query.FindSymbolInput{Query: qs, PathPrefix: r.prefix, Limit: LookupLimit, MaxTokens: 600})
		if err != nil {
			dc.e.opts.Logf("steer: find_symbol: %v", err)
			break
		}
		add(out)
	}
	if len(lines) == 0 {
		if term := longestIdent(r.class.Idents); term != "" {
			out, err := q.Search(ctx, query.SearchInput{Query: term, PathPrefix: r.prefix, Limit: LookupLimit, MaxTokens: 600})
			if err != nil {
				dc.e.opts.Logf("steer: search: %v", err)
			} else {
				add(out)
			}
		}
	}
	return lines
}

// bodyLines drops the banner, "nothing found" lines and truncation
// footers from a query tool answer.
func bodyLines(out string) []string {
	all := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(all) > 0 {
		all = all[1:]
	}
	var keep []string
	for _, l := range all {
		switch {
		case strings.TrimSpace(l) == "",
			strings.HasPrefix(l, "… +"),
			strings.HasPrefix(l, "no definitions match"),
			strings.HasPrefix(l, "nothing matches"),
			strings.HasPrefix(l, "no indexed files"):
			continue
		}
		keep = append(keep, l)
	}
	return keep
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func longestIdent(ids []string) string {
	best := ""
	for _, id := range ids {
		if len(id) > len(best) {
			best = id
		}
	}
	if len(best) < 3 {
		return ""
	}
	return best
}

func quote(s string) string {
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return "`" + s + "`"
}

// Reason texts. Each tells the agent what the graph knows, which MCP tool
// to use next and how to get past the block.
func balancedReason(r *request, results []string) string {
	if r.kind == reqExplore {
		return buildReason(
			fmt.Sprintf("graphmaster: %s scans the code tree; the code graph already has the layout (repo_map):", r.label),
			results,
			"Next: use the graphmaster MCP tools repo_map (path_prefix) and file_outline <path>; search finds files by name.\n"+
				"Need the raw file list? Run the same search again; it will be allowed.")
	}
	return buildReason(
		fmt.Sprintf("graphmaster: %s looks up code symbols; the code graph has them:", r.label),
		results,
		"Next: use the graphmaster MCP tools read_symbol <name> for source, callers/callees for usage, find_symbol for more matches.\n"+
			"Need a literal text match? Run the same search again; it will be allowed.")
}

func strictReason(r *request, results []string) string {
	head := fmt.Sprintf("graphmaster (strict mode): %s is blocked; code search goes through the code graph.", r.label)
	if len(results) > 0 {
		head += " Graph results:"
	}
	tools := "Use the graphmaster MCP tools find_symbol, search, read_symbol, callers and impact."
	if r.kind == reqExplore {
		tools = "Use the graphmaster MCP tools repo_map, file_outline and search."
	}
	return buildReason(head, results,
		tools+"\nTo allow text searches again, set mode = \"balanced\" in .graphmaster/config.toml (or GRAPHMASTER_MODE=balanced).")
}

func budgetReminder(budget int) string {
	return fmt.Sprintf("graphmaster: the steering budget of this session is used up (%d redirects), so searches are no longer blocked. "+
		"For code navigation prefer the graphmaster MCP tools find_symbol, search, read_symbol and callers.", budget)
}

// maxReasonLine clips one quoted result line.
const maxReasonLine = 200

// buildReason joins head, as many result lines as fit and foot within
// MaxReasonLen, noting how many results were left out.
func buildReason(head string, results []string, foot string) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteByte('\n')
	room := MaxReasonLen - b.Len() - len(foot) - len("… +999 more\n")
	shown := 0
	for _, l := range results {
		l = "  " + clipLine(l, maxReasonLine)
		if len(l)+1 > room {
			break
		}
		b.WriteString(l)
		b.WriteByte('\n')
		room -= len(l) + 1
		shown++
	}
	if n := len(results) - shown; n > 0 {
		fmt.Fprintf(&b, "… +%d more\n", n)
	}
	b.WriteString(foot)
	return clipLine(b.String(), MaxReasonLen)
}

// clipLine shortens s to at most n bytes on a rune boundary.
func clipLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + ell
}

// read handles PLAN step 5: reads are never denied; Claude and Codex get
// an outline when they read a large code file whole.
func (dc *decider) read(ctx context.Context) hookapi.Decision {
	if dc.ev.Agent != hookapi.AgentClaude && dc.ev.Agent != hookapi.AgentCodex {
		return hookapi.Allow()
	}
	if len(dc.ev.Paths) != 1 || !IsCodePath(dc.ev.Paths[0], dc.g.exts) || readHasRange(dc.ev.Raw) {
		return hookapi.Allow()
	}
	rel, ok := dc.g.rel(dc.ev.Paths[0])
	if !ok || rel == "" {
		return hookapi.Allow()
	}
	f, err := dc.g.ix.Store().FileByPath(ctx, rel)
	if err != nil || !f.Status.Indexed() || f.LOC <= ReadOutlineMinLines {
		return hookapi.Allow()
	}
	ctx, cancel := context.WithTimeout(ctx, dc.e.opts.LookupBudget)
	defer cancel()
	out, err := dc.g.q.FileOutline(ctx, query.FileOutlineInput{Path: rel, MaxTokens: query.MaxTokensCap})
	if err != nil {
		return dc.fail("file_outline", err)
	}
	lines := bodyLines(out)
	if len(lines) <= 1 { // header only: nothing to outline
		return hookapi.Allow()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "graphmaster: %s has %d lines. Outline (read one definition with the read_symbol MCP tool, or Read with offset/limit):\n", rel, f.LOC)
	for _, l := range firstN(lines[1:], ReadOutlineMaxLines) {
		b.WriteString(clipLine(l, maxReasonLine))
		b.WriteByte('\n')
	}
	if n := len(lines) - 1 - ReadOutlineMaxLines; n > 0 {
		fmt.Fprintf(&b, "… +%d more (file_outline)\n", n)
	}
	return hookapi.Allow().WithContext(strings.TrimRight(b.String(), "\n"))
}

// rangeKeys are tool-input members that limit a read to part of a file.
var rangeKeys = []string{"offset", "limit", "start_line", "end_line", "line_start", "line_end", "view_range", "lines"}

// readHasRange reports whether a raw read payload asks for part of a file:
// a range member in the payload itself or in its tool_input / args / input
// object.
func readHasRange(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return false
	}
	objs := []map[string]json.RawMessage{top}
	for _, k := range []string{"tool_input", "args", "input"} {
		var m map[string]json.RawMessage
		if v, ok := top[k]; ok && json.Unmarshal(v, &m) == nil {
			objs = append(objs, m)
		}
	}
	for _, m := range objs {
		for _, k := range rangeKeys {
			if v, ok := m[k]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return true
			}
		}
	}
	return false
}
