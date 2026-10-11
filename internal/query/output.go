package query

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Token budget (PLAN "MCP tool'ları"): output size is bounded by
// max_tokens, estimated as bytes/4.
const (
	DefaultMaxTokens = 1500
	MaxTokensCap     = 6000
	minMaxTokens     = 64
	bytesPerToken    = 4

	// footerReserve keeps room for the truncation footer.
	footerReserve = 72
	// maxLineBytes clips pathological lines (minified source, huge names).
	maxLineBytes = 480
)

// Truncation hints shown in footers.
const (
	hintLimit  = "narrow path_prefix / raise limit"
	hintTokens = "narrow path_prefix / raise max_tokens"
)

// budgetBytes converts a max_tokens argument into a byte budget, applying
// the default, the cap and a small floor so the banner always fits.
func budgetBytes(maxTokens int) int {
	switch {
	case maxTokens <= 0:
		maxTokens = DefaultMaxTokens
	case maxTokens > MaxTokensCap:
		maxTokens = MaxTokensCap
	case maxTokens < minMaxTokens:
		maxTokens = minMaxTokens
	}
	return maxTokens * bytesPerToken
}

// output accumulates a tool's text answer within its byte budget. Once a
// line does not fit, the output is full and further lines are dropped; the
// caller reports what was dropped with more.
type output struct {
	b     strings.Builder
	limit int
	body  int // lines written after the header
	full  bool
}

func newOutput(maxTokens int) *output {
	return &output{limit: budgetBytes(maxTokens)}
}

// header writes the banner line; it is never dropped.
func (o *output) header(s string) {
	o.b.WriteString(clip(s, o.limit-footerReserve-1))
	o.b.WriteByte('\n')
}

// line appends s and reports whether it fit. The first body line is clipped
// rather than dropped so that a tiny budget still yields an answer.
func (o *output) line(s string) bool {
	if o.full {
		return false
	}
	s = clip(s, maxLineBytes)
	room := o.limit - footerReserve - o.b.Len() - 1
	if len(s) > room {
		if o.body > 0 || room < 8 {
			o.full = true
			return false
		}
		s = clip(s, room)
	}
	o.b.WriteString(s)
	o.b.WriteByte('\n')
	o.body++
	return true
}

// linef is line with formatting.
func (o *output) linef(format string, args ...any) bool {
	return o.line(fmt.Sprintf(format, args...))
}

// more writes the truncation footer for n omitted items. The hint depends
// on whether the budget or a result limit cut the list.
func (o *output) more(n int) {
	if n <= 0 {
		return
	}
	hint := hintLimit
	if o.full {
		hint = hintTokens
	}
	fmt.Fprintf(&o.b, "… +%d more (%s)\n", n, hint)
}

// list renders n items until the budget is exhausted and writes a footer
// covering the items not shown plus extra items cut earlier (e.g. by a
// limit). It returns the number of items shown.
func (o *output) list(n int, render func(i int) string, extra int) int {
	shown := 0
	for ; shown < n; shown++ {
		if !o.line(render(shown)) {
			break
		}
	}
	o.more(n - shown + extra)
	return shown
}

func (o *output) String() string { return o.b.String() }

// clip shortens s to at most n bytes on a rune boundary, marking the cut
// with an ellipsis.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}
