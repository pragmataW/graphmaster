package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/pragmataW/graphmaster/internal/query"
)

// Response and request bounds.
const (
	// maxElements caps the cytoscape elements (nodes + edges) of one
	// response (PLAN: 1500).
	maxElements = 1500
	// DefaultFreshenBudget is the lock wait before an API request answers
	// from the current graph; the UI favours latency over completeness.
	DefaultFreshenBudget = 2 * time.Second
)

// Options configure a Server.
type Options struct {
	// Freshener runs before every API request. Nil uses the backend when
	// it implements query.Freshener.
	Freshener query.Freshener
	// FreshenBudget bounds the wait for the write lock (default 2s).
	FreshenBudget time.Duration
	// Token is the URL secret; empty generates a random one.
	Token string
	// MaxElements overrides the per-response element cap (tests only;
	// default and maximum 1500).
	MaxElements int
}

// Server is the UI's HTTP handler. It is safe for concurrent use.
type Server struct {
	b      query.Backend
	fr     query.Freshener
	budget time.Duration
	token  string
	max    int
	h      http.Handler
}

// New returns a Server over the index b (normally an *index.Indexer).
func New(b query.Backend, opts Options) *Server {
	s := &Server{b: b, fr: opts.Freshener, budget: opts.FreshenBudget, token: opts.Token, max: opts.MaxElements}
	if s.fr == nil {
		if f, ok := b.(query.Freshener); ok {
			s.fr = f
		}
	}
	if s.budget <= 0 {
		s.budget = DefaultFreshenBudget
	}
	if s.token == "" {
		s.token = newToken()
	}
	if s.max <= 0 || s.max > maxElements {
		s.max = maxElements
	}
	s.h = s.guard(s.routes())
	return s
}

// newToken returns 128 random bits as 32 hex digits.
func newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("ui: random token: %v", err)) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b[:])
}

// Token returns the secret every request must carry.
func (s *Server) Token() string { return s.token }

// Handler returns the guarded handler (token, Host and method checks).
func (s *Server) Handler() http.Handler { return s.h }

// URL is the address to open in a browser for a listener at addr.
func (s *Server) URL(addr net.Addr) string {
	port := ""
	if ta, ok := addr.(*net.TCPAddr); ok {
		port = strconv.Itoa(ta.Port)
	} else if _, p, err := net.SplitHostPort(addr.String()); err == nil {
		port = p
	}
	return "http://127.0.0.1:" + port + "/?token=" + s.token
}

// Listen binds the loopback interface; port 0 picks a free port.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// Serve answers requests on ln until ctx is cancelled, then shuts down
// gracefully. It returns nil after a cancellation.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
