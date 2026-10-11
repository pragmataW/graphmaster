package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/store"
)

// DefaultFreshenBudget is how long a tool call waits for the write lock
// before answering from the current graph (PLAN: MCP 20s).
const DefaultFreshenBudget = 20 * time.Second

// ErrInvalidArgument marks errors caused by tool arguments rather than the
// index; callers can show them to the agent as a usage error.
var ErrInvalidArgument = errors.New("invalid argument")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, fmt.Sprintf(format, args...))
}

// Freshener brings the index up to date before a tool answers.
// *index.Indexer implements it; tests inject a fixed result.
type Freshener interface {
	Freshen(ctx context.Context, budget time.Duration) (*index.Result, error)
}

// Backend gives the tools access to the index. *index.Indexer implements
// it.
type Backend interface {
	// Store returns the current graph.db (it may change after Freshen).
	Store() *store.Store
	// Root locates the working tree (for reading sources and git state).
	Root() *repo.Root
	// Status summarizes the index for the status tool.
	Status(ctx context.Context) (*index.Status, error)
}

// Options configure an Engine.
type Options struct {
	// Freshener runs before every tool. Nil uses the backend when it
	// implements Freshener, otherwise tools answer without freshening.
	Freshener Freshener
	// FreshenBudget bounds the wait for the write lock (default: the
	// backend's timeouts.mcp when it exposes its config, else 20s).
	FreshenBudget time.Duration
	// NoGit reports that the backend indexes a directory outside git, so
	// impact changed=true has nothing to compare against.
	NoGit bool
}

// Engine runs the query tools against one repository. Apart from a cache
// of the files table (invalidated by files_gen) it holds no state; it is
// safe for concurrent use.
type Engine struct {
	b      Backend
	fr     Freshener
	budget time.Duration
	noGit  bool
	files  fileCache
}

// New returns an Engine over b.
func New(b Backend, opts Options) *Engine {
	e := &Engine{b: b, fr: opts.Freshener, budget: opts.FreshenBudget, noGit: opts.NoGit}
	if e.fr == nil {
		if f, ok := b.(Freshener); ok {
			e.fr = f
		}
	}
	if e.budget <= 0 {
		e.budget = configBudget(b)
	}
	if ix, ok := b.(*index.Indexer); ok && ix.Location() != nil && ix.Location().NoGit {
		e.noGit = true
	}
	return e
}

// configBudget is the MCP lock budget of the backend's configuration
// (timeouts.mcp, e.g. from .graphmaster/config.toml or
// GRAPHMASTER_TIMEOUT_MCP), or DefaultFreshenBudget.
func configBudget(b Backend) time.Duration {
	if c, ok := b.(interface{ Config() *config.Config }); ok {
		if cfg := c.Config(); cfg != nil && cfg.Timeouts.MCP.D() > 0 {
			return cfg.Timeouts.MCP.D()
		}
	}
	return DefaultFreshenBudget
}

func (e *Engine) querier() *querier {
	st := e.b.Store()
	return &querier{st: st, db: st.DB(), files: &e.files}
}

// banner freshens the index and returns the first output line. A failed
// freshen is reported in the banner; the tool still answers from the graph
// as it is (fail-open).
func (e *Engine) banner(ctx context.Context, tool string) string {
	state := "fresh"
	if e.fr != nil {
		res, err := e.fr.Freshen(ctx, e.budget)
		switch {
		case err != nil:
			state = "⚠ freshen failed: " + err.Error()
		case res != nil:
			state = res.Banner()
		}
	}
	return "graphmaster " + tool + " · " + state
}

// run is the common entry point: freshen, write the banner, then let body
// fill the budgeted output.
func (e *Engine) run(ctx context.Context, tool string, maxTokens int, body func(ctx context.Context, q *querier, o *output) error) (string, error) {
	o := newOutput(maxTokens)
	o.header(e.banner(ctx, tool))
	q := e.querier()
	if err := body(ctx, q, o); err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	return o.String(), nil
}
