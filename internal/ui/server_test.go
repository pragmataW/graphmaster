package ui

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewGeneratesRandomToken(t *testing.T) {
	ix := fixtureIndex(t)
	a, b := New(ix, Options{}), New(ix, Options{})
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a.Token()) {
		t.Fatalf("token %q is not 32 hex digits", a.Token())
	}
	if a.Token() == b.Token() {
		t.Fatal("two servers share a token")
	}
}

func TestTokenRequired(t *testing.T) {
	s, fr := fixtureServer(t)
	for _, target := range []string{
		"/", "/app.js", "/style.css", "/cytoscape.min.js",
		"/api/search?q=NewMemory", "/api/packages",
		"/api/search?q=NewMemory&token=", "/api/search?q=NewMemory&token=wrong",
		"/api/search?q=NewMemory&token=" + strings.ToUpper(testToken),
		"/api/search?q=NewMemory&token=" + testToken[:31],
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = "127.0.0.1:4242"
		req.Header.Set("X-Graphmaster-Token", testToken) // only the URL counts
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", target, rec.Code)
		}
	}
	if n := fr.n.Load(); n != 0 {
		t.Fatalf("rejected requests freshened the index %d times", n)
	}
}

func TestHostHeaderChecked(t *testing.T) {
	s, _ := fixtureServer(t)
	for host, ok := range map[string]bool{
		"127.0.0.1:4242":          true,
		"127.0.0.1":               true,
		"localhost:4242":          true,
		"LOCALHOST:4242":          true,
		"localhost":               true,
		"evil.com":                false,
		"evil.com:4242":           false,
		"127.0.0.1.nip.io":        false,
		"localhost.evil.com:4242": false,
		"[::1]:4242":              false,
		"0.0.0.0:4242":            false,
		"":                        false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/packages?token="+testToken, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if got := rec.Code == http.StatusOK; got != ok {
			t.Errorf("Host %q: status %d, want ok=%v", host, rec.Code, ok)
		}
		if !ok && rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, rec.Code)
		}
	}
}

func TestOnlyGetAllowed(t *testing.T) {
	s, _ := fixtureServer(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/api/packages?token="+testToken, strings.NewReader("{}"))
		req.Host = "127.0.0.1:4242"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", m, rec.Code)
		}
	}
}

func TestStaticAssets(t *testing.T) {
	s, fr := fixtureServer(t)
	for path, want := range map[string]struct{ ctype, body string }{
		"/":                 {"text/html", "token=" + testToken},
		"/index.html":       {"text/html", `<div id="graph"`},
		"/app.js":           {"text/javascript", "/api/ego"},
		"/style.css":        {"text/css", "prefers-color-scheme: dark"},
		"/cytoscape.min.js": {"text/javascript", "Cytoscape Consortium"},
	} {
		rec := get(t, s, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, want.ctype) {
			t.Errorf("%s: content type %q, want %s", path, ct, want.ctype)
		}
		if !strings.Contains(rec.Body.String(), want.body) {
			t.Errorf("%s: body lacks %q", path, want.body)
		}
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy"} {
			if rec.Header().Get(h) == "" {
				t.Errorf("%s: missing %s header", path, h)
			}
		}
	}
	if rec := get(t, s, "/nope.js", nil); rec.Code != http.StatusNotFound {
		t.Errorf("/nope.js: status %d, want 404", rec.Code)
	}
	if strings.Contains(get(t, s, "/", nil).Body.String(), tokenPlaceholder) {
		t.Error("index.html still contains the token placeholder")
	}
	if n := fr.n.Load(); n != 0 {
		t.Errorf("static assets freshened the index %d times", n)
	}
}

func TestEveryAPIRequestFreshens(t *testing.T) {
	s, fr := fixtureServer(t)
	before := fr.n.Load()
	get(t, s, "/api/packages", nil)
	get(t, s, "/api/search", url.Values{"q": {"NewMemory"}})
	if got := fr.n.Load() - before; got != 2 {
		t.Fatalf("freshened %d times for 2 API requests", got)
	}
}

func TestUnknownAPIEndpoint(t *testing.T) {
	s, _ := fixtureServer(t)
	rec := get(t, s, "/api/everything", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestServeLifecycle(t *testing.T) {
	s, _ := fixtureServer(t)
	ln, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); host != "127.0.0.1" {
		t.Fatalf("listening on %s, want 127.0.0.1", ln.Addr())
	}
	u := s.URL(ln.Addr())
	if !strings.HasPrefix(u, "http://127.0.0.1:") || !strings.HasSuffix(u, "/?token="+testToken) {
		t.Fatalf("URL %q", u)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "graphmaster") {
		t.Fatalf("GET %s: %d %q", u, resp.StatusCode, body)
	}
	bad, err := http.Get(strings.Replace(u, "token=", "token=x", 1))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d", bad.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

// TestRealServerRejectsForeignHost drives a real listener, where the Host
// header comes from the request line (DNS rebinding scenario).
func TestRealServerRejectsForeignHost(t *testing.T) {
	s, _ := fixtureServer(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/packages?token="+testToken, nil)
	req.Host = "attacker.example:80"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	ok, err := http.Get(srv.URL + "/api/packages?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", ok.StatusCode)
	}
}
