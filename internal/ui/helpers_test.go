package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

const testToken = "0123456789abcdef0123456789abcdef"

// countingFreshener records how often the server freshened the index.
type countingFreshener struct{ n atomic.Int64 }

func (f *countingFreshener) Freshen(context.Context, time.Duration) (*index.Result, error) {
	f.n.Add(1)
	return &index.Result{}, nil
}

// sharedIndex indexes a repository once per test binary (read-only use).
type sharedIndex struct {
	once sync.Once
	dir  string
	ix   *index.Indexer
	err  error
}

var (
	fixture  sharedIndex
	hubRepo  sharedIndex
	allIndex = []*sharedIndex{&fixture, &hubRepo}
)

func (s *sharedIndex) get(t testing.TB, build func(root string) error) *index.Indexer {
	t.Helper()
	s.once.Do(func() {
		if s.dir, s.err = os.MkdirTemp("", "gm-ui-"); s.err != nil {
			return
		}
		root := filepath.Join(s.dir, "repo")
		if s.err = build(root); s.err != nil {
			return
		}
		s.ix, s.err = index.Open(context.Background(), root,
			index.Options{Resolver: &resolve.Resolver{}, Logf: func(string, ...any) {}})
		if s.err != nil {
			return
		}
		_, s.err = s.ix.Freshen(context.Background(), time.Minute)
	})
	if s.err != nil {
		t.Fatalf("index: %v", s.err)
	}
	return s.ix
}

func TestMain(m *testing.M) {
	code := m.Run()
	for _, s := range allIndex {
		if s.ix != nil {
			_ = s.ix.Close()
		}
		if s.dir != "" {
			_ = os.RemoveAll(s.dir)
		}
	}
	os.Exit(code)
}

// fixtureIndex returns the shared index of the materialized fixture.
func fixtureIndex(t testing.TB) *index.Indexer {
	t.Helper()
	return fixture.get(t, testutil.MaterializeTo)
}

// fixtureServer returns a server over the materialized fixture repository.
func fixtureServer(t testing.TB) (*Server, *countingFreshener) {
	t.Helper()
	ix := fixtureIndex(t)
	fr := &countingFreshener{}
	return New(ix, Options{Freshener: fr, Token: testToken}), fr
}

// hubCallers is the number of functions calling Hub in the hub repository.
const hubCallers = 1700

// hubServer returns a server over a Go repository where one function has
// more callers than any response may hold.
func hubServer(t testing.TB) *Server {
	t.Helper()
	ix := hubRepo.get(t, func(root string) error {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
		var b strings.Builder
		b.WriteString("package hub\n\nfunc Hub() {}\n\n")
		for i := range hubCallers {
			fmt.Fprintf(&b, "func Caller%04d() { Hub() }\n", i)
		}
		files := map[string]string{
			"go.mod":       "module example.com/hub\n\ngo 1.22\n",
			"hub/hub.go":   b.String(),
			"hub/other.go": "package hub\n\nfunc Other() { Hub(); Caller0001() }\n",
		}
		for p, c := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
				return err
			}
		}
		testutil.Git(t, root, "init", "-q")
		testutil.Git(t, root, "add", "-A")
		testutil.Git(t, root, "commit", "-q", "-m", "hub")
		return nil
	})
	return New(ix, Options{Freshener: &countingFreshener{}, Token: testToken})
}

// get performs an authorized request against the server's handler.
func get(t testing.TB, s *Server, path string, params url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("token", testToken)
	req := httptest.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	req.Host = "127.0.0.1:4242"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// getJSON performs an authorized request, requires status, and decodes the body.
func getJSON[T any](t testing.TB, s *Server, path string, params url.Values, status int) T {
	t.Helper()
	rec := get(t, s, path, params)
	if rec.Code != status {
		t.Fatalf("GET %s %v: status %d, want %d: %s", path, params, rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("GET %s: content type %q", path, ct)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("GET %s: decode: %v\n%s", path, err, rec.Body.String())
	}
	return v
}

// findSymbol returns the id of the first search hit with the given qname.
func findSymbol(t testing.TB, s *Server, name, qname string) int64 {
	t.Helper()
	res := getJSON[SearchResponse](t, s, "/api/search", url.Values{"q": {name}, "limit": {"100"}}, http.StatusOK)
	for _, sym := range res.Symbols {
		if sym.QName == qname || strings.HasSuffix(sym.QName, "."+qname) {
			return sym.ID
		}
	}
	t.Fatalf("search %q: no %s in %+v", name, qname, res.Symbols)
	return 0
}

// nodesByLabel indexes the node elements of a graph by label.
func nodesByLabel(els []Element) map[string]Element {
	out := map[string]Element{}
	for _, e := range els {
		if e.Group == "nodes" {
			out[e.Data.Label] = e
		}
	}
	return out
}

// checkGraph verifies the invariants every graph response must keep.
func checkGraph(t testing.TB, g GraphResponse, max int) {
	t.Helper()
	if len(g.Elements) > max {
		t.Fatalf("%d elements, cap %d", len(g.Elements), max)
	}
	ids := map[string]bool{}
	for _, e := range g.Elements {
		if ids[e.Data.ID] {
			t.Fatalf("duplicate element id %s", e.Data.ID)
		}
		switch e.Group {
		case "nodes":
			if e.Data.Parent != "" && !ids[e.Data.Parent] {
				t.Fatalf("node %s precedes or lacks its parent %s", e.Data.ID, e.Data.Parent)
			}
		case "edges":
			if !ids[e.Data.Source] || !ids[e.Data.Target] {
				t.Fatalf("edge %s has a dangling endpoint", e.Data.ID)
			}
		default:
			t.Fatalf("element %s has group %q", e.Data.ID, e.Group)
		}
		ids[e.Data.ID] = true
	}
	if g.Banner == "" {
		t.Fatal("missing banner")
	}
}
