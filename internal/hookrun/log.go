package hookrun

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// LogFileName is the hook error log inside <root>/.graphmaster.
const LogFileName = "log"

// maxLogSize rotates the log to log.1 once a write would exceed it.
const maxLogSize = 1 << 20

// maxLogLine caps one log entry.
const maxLogLine = 2000

// logBuffer collects log lines during one hook invocation. The repository
// (and so the log file) is only known once the payload is parsed, so lines
// are buffered and written by flush at the end.
type logBuffer struct {
	now func() time.Time

	mu    sync.Mutex
	lines []string
}

func newLogBuffer(now func() time.Time) *logBuffer {
	if now == nil {
		now = time.Now
	}
	return &logBuffer{now: now}
}

// Logf records one line: RFC 3339 UTC timestamp, then the message folded
// onto a single line.
func (l *logBuffer) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	msg = strings.Join(strings.Fields(strings.ReplaceAll(msg, "\n", " ")), " ")
	if len(msg) > maxLogLine {
		msg = msg[:maxLogLine] + "…"
	}
	line := l.now().UTC().Format(time.RFC3339) + " " + msg + "\n"
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

// empty reports whether nothing was logged.
func (l *logBuffer) empty() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines) == 0
}

// flush appends the buffered lines to path, rotating it to path.1 when the
// result would exceed maxLogSize, and clears the buffer.
func (l *logBuffer) flush(path string) error {
	l.mu.Lock()
	data := strings.Join(l.lines, "")
	l.lines = nil
	l.mu.Unlock()
	if data == "" {
		return nil
	}
	if fi, err := os.Stat(path); err == nil && fi.Size()+int64(len(data)) > maxLogSize {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
