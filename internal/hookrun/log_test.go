package hookrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }

func TestLogBufferFlushAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LogFileName)
	l := newLogBuffer(fixedNow)
	if err := l.flush(path); err != nil || fileExists(path) {
		t.Fatalf("empty flush must not create the file: %v", err)
	}
	l.Logf("claude pre-tool-use: %s", "boom")
	l.Logf("second")
	if err := l.flush(path); err != nil {
		t.Fatal(err)
	}
	l.Logf("third")
	if err := l.flush(path); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	want := "2026-10-11T12:00:00Z claude pre-tool-use: boom\n2026-10-11T12:00:00Z second\n2026-10-11T12:00:00Z third\n"
	if got != want {
		t.Fatalf("log =\n%s\nwant\n%s", got, want)
	}
}

func TestLogBufferRotatesAtLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LogFileName)
	old := strings.Repeat("x", maxLogSize-10) + "\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newLogBuffer(fixedNow)
	l.Logf("this line does not fit")
	if err := l.flush(path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != old {
		t.Fatalf("rotated file has %d bytes, want the old log", len(got))
	}
	if got := readFile(t, path); !strings.HasSuffix(got, "this line does not fit\n") || len(got) > 100 {
		t.Fatalf("fresh log = %q", got)
	}
}

func TestLogBufferClipsLongLines(t *testing.T) {
	l := newLogBuffer(fixedNow)
	l.Logf("%s", strings.Repeat("y", 10*maxLogLine)+"\nsecond line")
	path := filepath.Join(t.TempDir(), LogFileName)
	if err := l.flush(path); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if len(got) > maxLogLine+64 || strings.Count(got, "\n") != 1 {
		t.Fatalf("line not clipped to one line: %d bytes, %d newlines", len(got), strings.Count(got, "\n"))
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
