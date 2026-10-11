package instructions

import (
	"regexp"
	"strings"
	"testing"
)

// knownTools mirrors the MCP tools graphmaster serves (internal/mcp).
var knownTools = map[string]bool{
	"find_symbol": true, "search": true, "repo_map": true, "file_outline": true,
	"read_symbol": true, "callers": true, "callees": true, "impact": true,
	"imports": true, "inheritance": true, "tests_for": true, "traverse": true,
	"status": true,
}

func TestTextFitsBudget(t *testing.T) {
	txt := Text()
	if n := len(txt); n == 0 || n > MaxBytes {
		t.Fatalf("instructions are %d bytes, want 1..%d", n, MaxBytes)
	}
	if !strings.HasSuffix(txt, "\n") || strings.HasSuffix(txt, "\n\n") {
		t.Fatalf("instructions must end with exactly one newline")
	}
}

func TestTextHasNoMarkerLines(t *testing.T) {
	if strings.Contains(Text(), "graphmaster:begin") || strings.Contains(Text(), "graphmaster:end") {
		t.Fatal("instructions must not contain graphmaster marker lines")
	}
}

func TestTextMentionsCoreTools(t *testing.T) {
	txt := Text()
	for _, tool := range []string{"find_symbol", "search", "callers", "impact", "repo_map", "status"} {
		if !strings.Contains(txt, "`"+tool+"`") {
			t.Errorf("instructions do not mention %s", tool)
		}
	}
	// Every backticked snake_case identifier that looks like a tool must exist.
	for _, m := range regexp.MustCompile("`([a-z]+_[a-z_]+)`").FindAllStringSubmatch(txt, -1) {
		if !knownTools[m[1]] {
			t.Errorf("instructions mention unknown tool %q", m[1])
		}
	}
}

func TestTextExplainsRetry(t *testing.T) {
	if !strings.Contains(strings.ToLower(Text()), "again") {
		t.Fatal("instructions must tell the model a repeated search is allowed")
	}
}
