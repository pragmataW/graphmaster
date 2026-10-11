package hookrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/steer"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Request is one `graphmaster hook <event> --agent <name>` invocation.
type Request struct {
	// Event is the CLI event argument (canonical kebab-case, e.g.
	// "pre-tool-use"); empty or unknown lets the adapter use the payload.
	Event string
	// Agent is the --agent value.
	Agent string
	// Stdin is the raw hook payload.
	Stdin []byte
	// Env is the process environment (os.Environ form); nil means the real
	// environment.
	Env []string
	// WorkDir is the process working directory, used when the payload
	// carries no cwd.
	WorkDir string
}

// Options configure a Dispatcher. Every field is optional.
type Options struct {
	// Adapter looks up an agent adapter (default adapters.Get).
	Adapter func(hookapi.Agent) (adapters.Agent, bool)
	// Open opens a located repository's index (default index.Open with
	// the real resolver).
	Open func(ctx context.Context, loc *index.Location, cfg *config.Config) (steer.Index, error)
	// Spawn starts a detached background build in root (default
	// SpawnIndex).
	Spawn func(root string) error
	// Timeout is the per-event deadline (default EventTimeout).
	Timeout func(hookapi.Event, *config.Config) time.Duration
	// LookupBudget bounds the steering graph lookup (steer default 25ms).
	LookupBudget time.Duration
	// DedupeWait bounds the wait for a concurrent duplicate (default 2s).
	DedupeWait time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// Dispatcher runs hook invocations. It never fails: every internal error,
// panic or timeout produces the adapter's no-opinion output (fail-open)
// and a line in <root>/.graphmaster/log.
type Dispatcher struct {
	opts Options
}

// New returns a Dispatcher.
func New(opts Options) *Dispatcher {
	if opts.Adapter == nil {
		opts.Adapter = adapters.Get
	}
	if opts.Open == nil {
		opts.Open = openIndex
	}
	if opts.Spawn == nil {
		opts.Spawn = SpawnIndex
	}
	if opts.Timeout == nil {
		opts.Timeout = EventTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Dispatcher{opts: opts}
}

func openIndex(ctx context.Context, loc *index.Location, cfg *config.Config) (steer.Index, error) {
	return index.Open(ctx, loc.Root.WorkTree, index.Options{
		Config: cfg, Resolver: &resolve.Resolver{}, AllowNoGit: loc.NoGit,
	})
}

// failOpen is the decision of a hook that could not decide: no opinion,
// so the agent's own default applies.
func failOpen() hookapi.Decision { return hookapi.Noop() }

// Run handles one invocation and returns what to write back to the agent.
func (d *Dispatcher) Run(ctx context.Context, req Request) (out adapters.HookOutput) {
	inv := &invocation{d: d, req: req, log: newLogBuffer(d.opts.Now), indexes: map[string]steer.Index{}}
	defer inv.finish()
	defer func() {
		if r := recover(); r != nil {
			inv.logf("panic: %v\n%s", r, debug.Stack())
			out = adapters.HookOutput{}
		}
	}()

	agent, err := hookapi.ParseAgent(req.Agent)
	if err != nil {
		inv.logf("hook %s: %v", req.Event, err)
		return adapters.HookOutput{}
	}
	inv.record(agent)
	agent, silent := resolveAgent(agent, req.Stdin, inv.home())
	if silent {
		return adapters.HookOutput{} // Cursor's native graphmaster hook answers this call
	}
	ad, ok := d.opts.Adapter(agent)
	if !ok {
		inv.logf("hook %s: no adapter for agent %q", req.Event, agent)
		return adapters.HookOutput{}
	}
	event, err := hookapi.ParseEvent(req.Event)
	if err != nil && req.Event != "" {
		inv.logf("hook: %v (using the payload's event)", err)
	}

	ev, err := parseEvent(ad, event, req.Stdin, inv.environ())
	if err != nil {
		inv.locate(req.WorkDir)
		inv.logf("%s %s: parse payload: %v", agent, req.Event, err)
		return inv.render(ad, hookapi.HookEvent{Agent: agent, Event: event}, failOpen())
	}
	inv.prefix = fmt.Sprintf("%s %s", ev.Agent, ev.Event)
	inv.locate(firstNonEmpty(ev.CWD, req.WorkDir))
	cfg := inv.config()

	ctx, cancel := context.WithTimeout(ctx, d.opts.Timeout(ev.Event, cfg))
	defer cancel()
	return inv.render(ad, ev, inv.decide(ctx, ev, cfg))
}

// parseEvent calls the adapter, turning a panic into an error.
func parseEvent(ad adapters.Agent, event hookapi.Event, payload []byte, env []string) (ev hookapi.HookEvent, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("adapter panic: %v", r)
		}
	}()
	return ad.ParseEvent(event, payload, env)
}

// invocation is the state of one Run: the located repository, opened
// indexes and buffered log lines.
type invocation struct {
	d      *Dispatcher
	req    Request
	log    *logBuffer
	prefix string // "<agent> <event>" for log lines

	loc *index.Location // nil outside a repository
	cfg *config.Config

	openMu    sync.Mutex // serializes index opens
	mu        sync.Mutex // guards indexes and spawned
	indexes   map[string]steer.Index
	spawned   map[string]bool
	abandoned atomic.Bool // a timed-out route may still use the indexes
}

func (inv *invocation) logf(format string, args ...any) {
	if inv.prefix != "" {
		format = inv.prefix + ": " + format
	}
	inv.log.Logf(format, args...)
}

// decide routes ev under ctx's deadline. A panic or the deadline yields
// fail-open; a timed-out route keeps running in the background until the
// process exits, so its indexes are left open.
func (inv *invocation) decide(ctx context.Context, ev hookapi.HookEvent, cfg *config.Config) hookapi.Decision {
	ch := make(chan hookapi.Decision, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				inv.logf("panic: %v\n%s", r, debug.Stack())
				ch <- failOpen()
			}
		}()
		ch <- inv.route(ctx, ev, cfg)
	}()
	select {
	case dec := <-ch:
		return dec
	case <-ctx.Done():
		inv.abandoned.Store(true)
		inv.logf("gave up: %v; failing open", context.Cause(ctx))
		return failOpen()
	}
}

// render calls the adapter, turning a panic into empty output.
func (inv *invocation) render(ad adapters.Agent, ev hookapi.HookEvent, dec hookapi.Decision) (out adapters.HookOutput) {
	defer func() {
		if r := recover(); r != nil {
			inv.logf("render panic: %v", r)
			out = adapters.HookOutput{}
		}
	}()
	return ad.RenderDecision(ev, dec)
}

// finish closes the indexes and writes buffered log lines to the
// repository's .graphmaster/log. Outside a repository they are dropped.
func (inv *invocation) finish() {
	if !inv.abandoned.Load() {
		inv.mu.Lock()
		indexes := inv.indexes
		inv.indexes = map[string]steer.Index{}
		inv.mu.Unlock()
		for root, ix := range indexes {
			if c, ok := ix.(interface{ Close() error }); ok {
				if err := c.Close(); err != nil {
					inv.logf("close index %s: %v", root, err)
				}
			}
		}
	}
	if inv.log.empty() {
		return
	}
	loc := inv.loc
	if loc == nil {
		if l, err := index.Locate(inv.req.WorkDir, false); err == nil {
			loc = l
		}
	}
	if loc == nil || loc.Prepare() != nil {
		return
	}
	_ = inv.log.flush(filepath.Join(loc.Dir, LogFileName))
}

// locate finds the repository enclosing dir (symlinks resolved); outside a
// repository loc stays nil.
func (inv *invocation) locate(dir string) {
	if dir == "" {
		return
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	loc, err := index.Locate(dir, false)
	if err != nil {
		if !errors.Is(err, index.ErrNotGitRepo) {
			inv.logf("locate %s: %v", dir, err)
		}
		return
	}
	inv.loc = loc
}

// config loads the configuration for the located repository with the
// request's environment; errors fall back to the defaults.
func (inv *invocation) config() *config.Config {
	if inv.cfg != nil {
		return inv.cfg
	}
	root := ""
	if inv.loc != nil {
		root = inv.loc.Root.WorkTree
	}
	cfg, err := config.LoadWith(config.Options{Root: root, LookupEnv: inv.lookupEnv})
	if err != nil {
		inv.logf("config: %v (using defaults)", err)
		cfg = config.Default()
	}
	inv.cfg = cfg
	return cfg
}

func (inv *invocation) environ() []string {
	if inv.req.Env == nil {
		return os.Environ()
	}
	return inv.req.Env
}

// lookupEnv finds key in the request environment (last entry wins).
func (inv *invocation) lookupEnv(key string) (string, bool) {
	if inv.req.Env == nil {
		return os.LookupEnv(key)
	}
	val, found := "", false
	for _, kv := range inv.req.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val, found = v, true
		}
	}
	return val, found
}

func (inv *invocation) getenv(key string) string {
	v, _ := inv.lookupEnv(key)
	return v
}

func (inv *invocation) home() string {
	if h := inv.getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// record dumps the payload when GRAPHMASTER_RECORD is set.
func (inv *invocation) record(agent hookapi.Agent) {
	root := ""
	if l, err := index.Locate(inv.req.WorkDir, false); err == nil {
		root = l.Root.WorkTree
	}
	rec := RecorderFromEnv(inv.getenv, root)
	if rec == nil {
		return
	}
	rec.Environ = inv.environ()
	if _, err := rec.Record(string(agent), inv.req.Event, inv.req.Stdin, inv.req.WorkDir); err != nil {
		inv.logf("record: %v", err)
	}
}

// index opens (once per invocation) the index of the located repository.
func (inv *invocation) index(ctx context.Context) (steer.Index, error) {
	if inv.loc == nil {
		return nil, index.ErrNotGitRepo
	}
	root := inv.loc.Root.WorkTree
	inv.openMu.Lock()
	defer inv.openMu.Unlock()
	inv.mu.Lock()
	ix, ok := inv.indexes[root]
	inv.mu.Unlock()
	if ok {
		return ix, nil
	}
	ix, err := inv.d.opts.Open(ctx, inv.loc, inv.config())
	if err != nil {
		return nil, err
	}
	if ix == nil {
		return nil, errors.New("opener returned no index")
	}
	inv.mu.Lock()
	inv.indexes[root] = ix
	inv.mu.Unlock()
	return ix, nil
}

// graphExists reports whether the located repository has a graph.db.
// Checking first keeps an open from creating an empty one.
func (inv *invocation) graphExists() bool {
	if inv.loc == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(inv.loc.Dir, store.GraphDBName))
	return err == nil
}

// indexReady reports whether the located repository has a graph that was
// built (or is being built), as opposed to none or an empty one.
func (inv *invocation) indexReady(ctx context.Context) bool {
	if !inv.graphExists() {
		return false
	}
	ix, err := inv.index(ctx)
	if err != nil {
		inv.logf("open index: %v", err)
		return false
	}
	state, _, err := ix.Store().Meta(ctx, store.MetaState)
	if err != nil {
		inv.logf("read index state: %v", err)
		return false
	}
	return state == store.StateReady || state == store.StateBuilding
}

// startBuild spawns a background `graphmaster index --quiet` for root,
// at most once per invocation and only when no writer is running there.
func (inv *invocation) startBuild(root string) {
	inv.mu.Lock()
	if inv.spawned == nil {
		inv.spawned = map[string]bool{}
	}
	done := inv.spawned[root]
	inv.spawned[root] = true
	inv.mu.Unlock()
	if done {
		return
	}
	loc, err := index.Locate(root, false)
	if err != nil {
		inv.logf("background build %s: %v", root, err)
		return
	}
	if writerBusy(loc) {
		return
	}
	if err := inv.d.opts.Spawn(loc.Root.WorkTree); err != nil {
		inv.logf("background build: %v", err)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
