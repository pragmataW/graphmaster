package query

import (
	"context"
	"strings"
)

// Status reports file states, languages, database size, last scan and the
// steering mode.
func (e *Engine) Status(ctx context.Context, in StatusInput) (string, error) {
	return e.run(ctx, "status", in.MaxTokens, func(ctx context.Context, _ *querier, o *output) error {
		st, err := e.b.Status(ctx)
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimRight(st.Format(), "\n"), "\n")
		o.list(len(lines), func(i int) string { return lines[i] }, 0)
		return nil
	})
}
