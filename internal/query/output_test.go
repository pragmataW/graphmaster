package query

import (
	"strings"
	"testing"
)

func TestBudgetBytes(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, DefaultMaxTokens * 4},
		{-5, DefaultMaxTokens * 4},
		{100, 100 * 4},
		{MaxTokensCap + 1, MaxTokensCap * 4},
		{1, minMaxTokens * 4},
	}
	for _, c := range cases {
		if got := budgetBytes(c.in); got != c.want {
			t.Errorf("budgetBytes(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestOutputStopsAtBudget(t *testing.T) {
	o := newOutput(minMaxTokens)
	o.header("graphmaster test · fresh")
	items := make([]string, 200)
	for i := range items {
		items[i] = strings.Repeat("x", 30)
	}
	o.list(len(items), func(i int) string { return items[i] }, 0)
	s := o.String()
	if len(s) > minMaxTokens*4 {
		t.Fatalf("output is %d bytes, budget %d", len(s), minMaxTokens*4)
	}
	if !strings.Contains(s, "… +") || !strings.Contains(s, "more (") {
		t.Fatalf("missing truncation footer:\n%s", s)
	}
	if !strings.HasPrefix(s, "graphmaster test · fresh\n") {
		t.Fatalf("missing banner:\n%s", s)
	}
}

func TestOutputFooterCountsRemaining(t *testing.T) {
	o := newOutput(DefaultMaxTokens)
	o.header("b")
	o.list(3, func(i int) string { return "row" }, 7)
	want := "b\nrow\nrow\nrow\n… +7 more (narrow path_prefix / raise limit)\n"
	if got := o.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestOutputLongLineIsClipped(t *testing.T) {
	o := newOutput(minMaxTokens)
	o.header("b")
	if !o.line(strings.Repeat("y", 10_000)) {
		t.Fatal("first oversized line should be clipped, not dropped")
	}
	if len(o.String()) > minMaxTokens*4 {
		t.Fatalf("clipped output %d bytes exceeds budget", len(o.String()))
	}
}
