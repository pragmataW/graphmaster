package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/store"
)

// apiFunc answers one API request. banner is the freshness line of the
// index; the returned value is encoded as JSON.
type apiFunc func(ctx context.Context, d *data, r *http.Request, banner string) (any, error)

// routes maps the API endpoints and the static frontend.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	for path, fn := range map[string]apiFunc{
		"/api/search":   s.search,
		"/api/node":     s.node,
		"/api/ego":      s.ego,
		"/api/file":     s.file,
		"/api/packages": s.packages,
		"/api/impact":   s.impact,
	} {
		mux.Handle(path, s.api(fn))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "unknown endpoint"})
	})
	mux.HandleFunc("/", s.static)
	return mux
}

// api adapts fn: freshen the index, run fn on the current graph and encode
// its answer or error.
func (s *Server) api(fn apiFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		banner := s.freshen(ctx)
		st := s.b.Store()
		d := &data{st: st, db: st.DB(), root: s.b.Root(), max: s.max}
		v, err := fn(ctx, d, r, banner)
		if err != nil {
			status := http.StatusInternalServerError
			var he *httpError
			switch {
			case errors.As(err, &he):
				status = he.status
			case errors.Is(err, store.ErrNotFound):
				status = http.StatusNotFound
			}
			writeJSON(w, status, ErrorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
}

// freshen brings the index up to date (fail-open) and returns its banner.
func (s *Server) freshen(ctx context.Context) string {
	if s.fr == nil {
		return "fresh"
	}
	res, err := s.fr.Freshen(ctx, s.budget)
	switch {
	case err != nil:
		return "⚠ freshen failed: " + err.Error()
	case res == nil:
		return "fresh"
	}
	return res.Banner()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

// httpError carries a client-facing status code.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &httpError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error {
	return &httpError{status: http.StatusNotFound, msg: fmt.Sprintf(format, args...)}
}

// intParam parses an optional integer parameter, clamped to [1, hi];
// missing or empty yields def.
func intParam(r *http.Request, name string, def, hi int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, badRequest("%s must be an integer", name)
	}
	return min(max(v, 1), hi), nil
}

// idParam parses the required node id parameter.
func idParam(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("id"))
	if raw == "" {
		return 0, badRequest("id is required")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("id must be a positive integer")
	}
	return id, nil
}

// normPath turns a user-supplied repo path into the stored form.
func normPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return strings.TrimLeft(p, "/")
}
