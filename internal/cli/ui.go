package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/ui"
)

// uiStartBudget is how long `graphmaster ui` waits for the write lock
// while bringing the index up to date before serving.
const uiStartBudget = 30 * time.Second

// openBrowser opens url in the default browser; tests replace it.
var openBrowser = defaultOpenBrowser

// uiConfig are the inputs of runUI.
type uiConfig struct {
	dir    string
	port   int
	noOpen bool
	out    io.Writer
	errw   io.Writer
	// open launches the browser; nil uses openBrowser.
	open func(url string) error
}

func newUICmd() *cobra.Command {
	var port int
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Serve the graph explorer on localhost",
		Long: "Serve an interactive graph explorer for the current repository on 127.0.0.1.\n" +
			"The printed URL carries a random token that every request must present.\n" +
			"Stop with Ctrl-C.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(contextOf(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runUI(ctx, uiConfig{
				dir: ".", port: port, noOpen: noOpen,
				out: cmd.OutOrStdout(), errw: cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "port to listen on (0 picks a free port)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser")
	return cmd
}

func contextOf(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// runUI indexes the repository, serves the UI until ctx is cancelled and
// opens the browser unless cfg.noOpen.
func runUI(ctx context.Context, cfg uiConfig) error {
	if cfg.port < 0 || cfg.port > 65535 {
		return fmt.Errorf("ui: invalid port %d", cfg.port)
	}
	opts := index.Options{}
	if cliResolver != nil {
		opts.Resolver = cliResolver()
	}
	ix, err := index.Open(ctx, cfg.dir, opts)
	if err != nil {
		return err
	}
	defer ix.Close()

	// Bind before indexing so a busy port fails fast.
	ln, err := ui.Listen(cfg.port)
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	defer ln.Close()

	if res, err := ix.Freshen(ctx, uiStartBudget); err != nil {
		fmt.Fprintf(cfg.errw, "graphmaster ui: freshen failed: %v (serving the current graph)\n", err)
	} else if !res.Clean() {
		fmt.Fprintf(cfg.errw, "graphmaster ui: index %s\n", res.Banner())
	}

	srv := ui.New(ix, ui.Options{})
	url := srv.URL(ln.Addr())
	fmt.Fprintf(cfg.out, "graphmaster ui: %s\n", url)
	if !cfg.noOpen {
		open := cfg.open
		if open == nil {
			open = openBrowser
		}
		if err := open(url); err != nil {
			fmt.Fprintf(cfg.errw, "graphmaster ui: could not open a browser (%v); open the URL above\n", err)
		}
	}
	return srv.Serve(ctx, ln)
}

// defaultOpenBrowser starts the platform's URL opener without waiting.
func defaultOpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
