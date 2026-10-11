package e2e

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startUI runs `graphmaster ui --no-open` in root and returns the printed
// URL. The process is interrupted at cleanup.
func startUI(t *testing.T, root string) *url.URL {
	t.Helper()
	cmd := exec.Command(binary(t), "ui", "--no-open")
	cmd.Dir = root
	cmd.Env = childEnv(t.TempDir())
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ui exit after interrupt: %v", err)
			}
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Error("ui did not stop after interrupt")
		}
	})

	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "graphmaster ui: "); ok {
				lines <- rest
				break
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
		done <- cmd.Wait()
	}()
	select {
	case raw := <-lines:
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("ui url %q: %v", raw, err)
		}
		return u
	case err := <-done:
		t.Fatalf("ui exited before serving: %v", err)
	case <-time.After(2 * time.Minute):
		t.Fatal("ui did not print its URL")
	}
	return nil
}

func get(t *testing.T, u string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(u) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func TestUIServesSearch(t *testing.T) {
	binary(t)
	root := fixture(t)
	base := startUI(t, root)
	token := base.Query().Get("token")
	if token == "" {
		t.Fatalf("printed URL %s has no token", base)
	}
	api := *base
	api.Path = "/api/search"

	q := url.Values{"q": {"PlaceOrder"}, "token": {token}}
	api.RawQuery = q.Encode()
	code, body := get(t, api.String())
	if code != http.StatusOK {
		t.Fatalf("search with token: %d %s", code, body)
	}
	var res struct {
		Symbols []struct {
			Name string `json:"name"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("search body %s: %v", body, err)
	}
	found := false
	for _, s := range res.Symbols {
		found = found || s.Name == "PlaceOrder"
	}
	if !found {
		t.Fatalf("search returned no PlaceOrder results: %s", body)
	}

	for label, tok := range map[string]string{"missing": "", "wrong": strings.Repeat("0", len(token))} {
		q := url.Values{"q": {"PlaceOrder"}}
		if tok != "" {
			q.Set("token", tok)
		}
		api.RawQuery = q.Encode()
		if code, _ := get(t, api.String()); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("%s token: status %d, want 401/403", label, code)
		}
	}

	// A request carrying the token but a foreign Host (DNS rebinding) is
	// rejected.
	api.RawQuery = url.Values{"q": {"PlaceOrder"}, "token": {token}}.Encode()
	req, err := http.NewRequest(http.MethodGet, api.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: status %d, want 403", resp.StatusCode)
	}
}
