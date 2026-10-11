package query

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
)

// maxReadLines caps max_lines.
const maxReadLines = 2000

// ReadSymbol prints a definition's header row followed by its numbered
// source lines (plus optional context), read from the working tree.
func (e *Engine) ReadSymbol(ctx context.Context, in ReadSymbolInput) (string, error) {
	return e.run(ctx, "read_symbol", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		s, err := q.symbolArg(ctx, o, in.Symbol)
		if s == nil || err != nil {
			return err
		}
		o.line(s.row())
		data, err := os.ReadFile(e.b.Root().Abs(s.Path))
		if err != nil {
			o.linef("cannot read source: %v", err)
			return nil
		}
		lines := bytes.Split(data, []byte("\n"))
		if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
			lines = lines[:n-1]
		}
		ctxLines := max(in.ContextLines, 0)
		first := max(s.Start-ctxLines, 1)
		last := min(s.End+ctxLines, len(lines))
		if first > last {
			o.linef("lines %d-%d are beyond the end of the file (%d lines); the index may be stale", s.Start, s.End, len(lines))
			return nil
		}
		maxLines := orDefault(in.MaxLines, 200, maxReadLines)
		end := min(last, first+maxLines-1)
		width := len(strconv.Itoa(end))
		shown := o.list(end-first+1, func(i int) string {
			n := first + i
			src := bytes.TrimRight(lines[n-1], "\r")
			if len(bytes.TrimSpace(src)) == 0 {
				return fmt.Sprintf("%*d", width, n)
			}
			return fmt.Sprintf("%*d  %s", width, n, src)
		}, 0)
		if rest := last - end; rest > 0 && shown == end-first+1 {
			o.linef("… +%d more lines (raise max_lines)", rest)
		}
		return nil
	})
}
