package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/store"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show index freshness and statistics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(cmd.Context(), cmd.OutOrStdout(), ".")
		},
	}
}

// runStatus prints file counts by status, languages, DB size, last scan and
// steering mode. It reads the existing index and never builds one.
func runStatus(ctx context.Context, out io.Writer, dir string) error {
	loc, err := index.Locate(dir, false)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(loc.Dir, store.GraphDBName)); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(out, "repository: %s\nstate: not indexed (run `graphmaster index`)\n", loc.Root.WorkTree)
		return nil
	}
	ix, err := index.Open(ctx, dir, index.Options{})
	if err != nil {
		return err
	}
	defer ix.Close()
	s, err := ix.Status(ctx)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, s.Format())
	return err
}
