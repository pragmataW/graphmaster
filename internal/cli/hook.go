package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	// Agent adapters register themselves for the hook dispatcher.
	_ "github.com/pragmataW/graphmaster/internal/adapters/claude"
	_ "github.com/pragmataW/graphmaster/internal/adapters/codex"
	_ "github.com/pragmataW/graphmaster/internal/adapters/cursor"
	_ "github.com/pragmataW/graphmaster/internal/adapters/opencode"
	"github.com/pragmataW/graphmaster/internal/hookrun"
)

// maxHookPayload bounds the stdin read of one hook.
const maxHookPayload = 32 << 20

// Test seams.
var (
	hookWorkDir = os.Getwd
	hookExit    = os.Exit
)

func newHookCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "hook <event> --agent <name>",
		Short: "Handle an agent hook event",
		Long: "Handle one agent hook: read the payload from stdin and answer in the agent's format.\n" +
			"Installed by `graphmaster install`; it always exits 0 and fails open on any error.",
		Args: cobra.ArbitraryArgs,
		// Hooks written by a newer graphmaster may pass flags this binary
		// does not know; they must not turn into a failing hook.
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			event := ""
			if len(args) > 0 {
				event = args[0]
			}
			wd, _ := hookWorkDir()
			out := hookrun.New(hookrun.Options{}).Run(cmd.Context(), hookrun.Request{
				Event:   event,
				Agent:   agent,
				Stdin:   readPayload(cmd.InOrStdin()),
				Env:     os.Environ(),
				WorkDir: wd,
			})
			_, _ = cmd.OutOrStdout().Write(out.Stdout)
			_, _ = cmd.ErrOrStderr().Write(out.Stderr)
			if out.ExitCode != 0 {
				hookExit(out.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent that fired the hook: claude, codex, cursor or opencode")
	return cmd
}

// readPayload reads stdin up to maxHookPayload. An interactive terminal
// (someone running the command by hand) yields an empty payload instead of
// blocking.
func readPayload(r io.Reader) []byte {
	if f, ok := r.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(r, maxHookPayload))
	return b
}
