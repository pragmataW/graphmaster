package cli

import (
	"context"
	"io"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/index"
	gmmcp "github.com/pragmataW/graphmaster/internal/mcp"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the code graph over MCP (stdio)",
		Long: "Serve the code-graph tools over the Model Context Protocol on stdin/stdout.\n" +
			"The repository enclosing the working directory answers by default; tools\n" +
			"given an absolute path in another repository answer from that one. The\n" +
			"index is freshened before every tool call.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCP(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), ".")
		},
	}
}

// runMCP serves one MCP session over in/out (stdio in production) until the
// client disconnects.
func runMCP(ctx context.Context, in io.Reader, out io.Writer, dir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	opts := gmmcp.Options{Dir: dir, NewResolver: cliResolver}
	if cliResolver == nil {
		// Same as `graphmaster index`: no resolver leaves names queued.
		opts.NewResolver = func() index.Resolver { return nil }
	}
	srv, err := gmmcp.New(opts)
	if err != nil {
		return err
	}
	defer srv.Close()
	return srv.Run(ctx, &gomcp.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}})
}

// nopWriteCloser leaves the underlying writer (stdout) open on Close.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
