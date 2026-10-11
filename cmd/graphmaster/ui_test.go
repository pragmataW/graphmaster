package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/testutil"
)

// uiLineWriter collects output and reports the first printed UI URL.
type uiLineWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	urls chan string
}

var uiURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?token=[0-9a-f]{32}`)

func (w *uiLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if u := uiURLPattern.FindString(w.buf.String()); u != "" && w.urls != nil {
		w.urls <- u
		w.urls = nil
	}
	return len(p), nil
}

// startUI runs runUI in the background and returns its URL.
func startUI(t *testing.T, cfg uiConfig) (string, func() error) {
	t.Helper()
	urls := make(chan string, 1)
	cfg.out = &uiLineWriter{urls: urls}
	cfg.errw = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runUI(ctx, cfg) }()
	stop := func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(15 * time.Second):
			t.Fatal("runUI did not stop")
			return nil
		}
	}
	select {
	case u := <-urls:
		return u, stop
	case err := <-done:
		cancel()
		t.Fatalf("runUI exited early: %v", err)
	case <-time.After(2 * time.Minute):
		cancel()
		t.Fatal("runUI printed no URL")
	}
	return "", nil
}

func uiGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestUICommandServesWithoutBrowser(t *testing.T) {
	dir := testutil.Materialize(t)
	opened := false
	u, stop := startUI(t, uiConfig{dir: dir, noOpen: true, open: func(string) error { opened = true; return nil }})

	code, body := uiGet(t, u)
	if code != http.StatusOK || !strings.Contains(body, "graphmaster") {
		t.Fatalf("GET %s: %d", u, code)
	}
	base := strings.SplitN(u, "/?", 2)[0]
	token := u[strings.Index(u, "token=")+len("token="):]
	code, body = uiGet(t, base+"/api/search?q=NewMemory&token="+token)
	if code != http.StatusOK || !strings.Contains(body, `"name":"NewMemory"`) {
		t.Fatalf("search through the CLI server: %d %s", code, body)
	}
	if code, _ := uiGet(t, base+"/api/search?q=NewMemory"); code != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", code)
	}
	if err := stop(); err != nil {
		t.Fatalf("runUI: %v", err)
	}
	if opened {
		t.Fatal("--no-open still opened a browser")
	}
}

func TestUICommandOpensBrowser(t *testing.T) {
	dir := testutil.Materialize(t)
	got := make(chan string, 1)
	u, stop := startUI(t, uiConfig{dir: dir, open: func(url string) error { got <- url; return nil }})
	defer stop()
	select {
	case o := <-got:
		if o != u {
			t.Fatalf("opened %q, printed %q", o, u)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("browser not opened")
	}
}

func TestUICommandFlags(t *testing.T) {
	cmd := newUICmd()
	for _, name := range []string{"port", "no-open"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	if err := runUI(context.Background(), uiConfig{dir: t.TempDir(), port: 70000, out: io.Discard, errw: io.Discard}); err == nil {
		t.Fatal("invalid port accepted")
	}
}
