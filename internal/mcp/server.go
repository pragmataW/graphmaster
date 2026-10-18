package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/query"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/version"
)

// ServerName is the MCP implementation name agents register.
const ServerName = "graphmaster"

// Options configure a Server.
type Options struct {
	// Dir is the start directory; its repository answers calls that do not
	// name a path elsewhere. Default: the process working directory.
	Dir string
	// NewResolver creates the name resolver for each opened index.
	// Default: internal/resolve.
	NewResolver func() index.Resolver
	// FreshenBudget bounds the wait for the write lock before a tool
	// answers from the current graph (default query.DefaultFreshenBudget).
	FreshenBudget time.Duration
	// Logf receives indexer diagnostics; default discards. It must not
	// write to stdout, which carries the protocol.
	Logf func(format string, args ...any)
}

// Server is graphmaster's MCP server. It keeps one index per repository it
// has answered for; Close releases them.
type Server struct {
	opts Options
	dir  string
	srv  *gomcp.Server

	mu      sync.Mutex
	defRoot string // start repository root, once located
	engines map[string]*repoEngine
	closed  bool
	args    map[string]toolArgs // tool name -> arguments, for aliasMiddleware
}

// repoEngine is an open index and the query engine over it.
type repoEngine struct {
	ix *index.Indexer
	e  *query.Engine
}

// New builds a server with every query tool registered. It does not open
// any index yet: the first tool call does, so a server started outside a
// repository still lists its tools and explains the problem per call.
func New(opts Options) (*Server, error) {
	dir := opts.Dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("mcp: working directory: %w", err)
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	if opts.NewResolver == nil {
		opts.NewResolver = func() index.Resolver { return &resolve.Resolver{} }
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	s := &Server{opts: opts, dir: dir, engines: map[string]*repoEngine{}, args: map[string]toolArgs{}}
	s.srv = gomcp.NewServer(
		&gomcp.Implementation{Name: ServerName, Title: "graphmaster code graph", Version: version.String()},
		&gomcp.ServerOptions{
			Instructions: instructions,
			// Static tool list; no logging notifications.
			Capabilities: &gomcp.ServerCapabilities{Tools: &gomcp.ToolCapabilities{}},
		},
	)
	if err := s.register(); err != nil {
		return nil, err
	}
	s.srv.AddReceivingMiddleware(aliasMiddleware(s.args))
	return s, nil
}

// MCP returns the underlying SDK server (tests connect it in-process).
func (s *Server) MCP() *gomcp.Server { return s.srv }

// Run serves one session over t until the client disconnects or ctx ends.
func (s *Server) Run(ctx context.Context, t gomcp.Transport) error {
	err := s.srv.Run(ctx, t)
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil //nolint:nilerr // cancellation by the caller is a clean shutdown
	}
	return err
}

// Close closes every open index. Calls after Close fail.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	var errs []error
	for root, re := range s.engines {
		if err := re.ix.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close index %s: %w", root, err))
		}
		delete(s.engines, root)
	}
	return errors.Join(errs...)
}

// engine returns the query engine for the repository rooted at root,
// opening its index on first use. Failed opens are not cached, so a later
// call can succeed (e.g. after `graphmaster index --force`).
func (s *Server) engine(ctx context.Context, root string) (*query.Engine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("server is shut down")
	}
	if re, ok := s.engines[root]; ok {
		return re.e, nil
	}
	ix, err := index.Open(ctx, root, index.Options{Resolver: s.opts.NewResolver(), Logf: s.opts.Logf})
	if err != nil {
		return nil, err
	}
	re := &repoEngine{ix: ix, e: query.New(ix, query.Options{FreshenBudget: s.opts.FreshenBudget})}
	s.engines[root] = re
	return re.e, nil
}

// openRoots reports how many repositories have an open index.
func (s *Server) openRoots() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.engines)
}
