// Command graphmaster builds and serves a code graph for AI coding agents.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run executes the CLI with args and returns the process exit code.
func run(args []string, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "graphmaster: %v\n", err)
		return 1
	}
	return 0
}

// newRootCmd assembles the command tree. Each subcommand lives in its own file.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "graphmaster",
		Short:         "Code graph and search steering for AI coding agents",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("graphmaster {{.Version}}\n")
	root.AddCommand(
		newIndexCmd(),
		newStatusCmd(),
		newQueryCmd(),
		newMCPCmd(),
		newHookCmd(),
		newUICmd(),
		newInstallCmd(),
		newUninstallCmd(),
		newDoctorCmd(),
	)
	return root
}
