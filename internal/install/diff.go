package install

import (
	"fmt"
	"strings"
)

const (
	diffContext = 3
	// maxLCSCells caps the quadratic LCS table; beyond it the changed middle
	// is shown as a whole-block replacement.
	maxLCSCells = 4 << 20
)

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
}

// unifiedDiff renders a unified diff of a -> b labelled with path. Equal
// inputs yield "".
func unifiedDiff(path string, a, b []byte) string {
	if string(a) == string(b) {
		return ""
	}
	al, bl := splitLines(string(a)), splitLines(string(b))
	ops := diffLines(al, bl)

	var out strings.Builder
	fmt.Fprintf(&out, "--- a%s\n+++ b%s\n", path, path)
	// Group ops into hunks with diffContext lines of context.
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(i-diffContext, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(end+diffContext, len(ops))
				break
			}
			end = run
		}
		writeHunk(&out, ops, start, end)
		i = end
	}
	return out.String()
}

func writeHunk(out *strings.Builder, ops []diffOp, start, end int) {
	// Line numbers are 1-based positions in a and b.
	aLine, bLine := 1, 1
	for _, op := range ops[:start] {
		if op.kind != '+' {
			aLine++
		}
		if op.kind != '-' {
			bLine++
		}
	}
	var aN, bN int
	for _, op := range ops[start:end] {
		if op.kind != '+' {
			aN++
		}
		if op.kind != '-' {
			bN++
		}
	}
	if aN == 0 {
		aLine--
	}
	if bN == 0 {
		bLine--
	}
	fmt.Fprintf(out, "@@ -%d,%d +%d,%d @@\n", aLine, aN, bLine, bN)
	for _, op := range ops[start:end] {
		out.WriteByte(op.kind)
		out.WriteString(op.text)
		if !strings.HasSuffix(op.text, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines returns an edit script, trimming the common prefix and suffix
// before running LCS on the (usually small) changed middle.
func diffLines(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var ops []diffOp
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{' ', l})
	}
	ops = append(ops, lcsDiff(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

func lcsDiff(a, b []string) []diffOp {
	var ops []diffOp
	if len(a)*len(b) > maxLCSCells {
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}
	// dp[i][j] = LCS length of a[i:], b[j:].
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}
