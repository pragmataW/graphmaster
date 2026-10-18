package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
	"github.com/pragmataW/graphmaster/internal/instructions"

	// Agent adapters register themselves in init().
	_ "github.com/pragmataW/graphmaster/internal/adapters/claude"
	_ "github.com/pragmataW/graphmaster/internal/adapters/codex"
	_ "github.com/pragmataW/graphmaster/internal/adapters/cursor"
	_ "github.com/pragmataW/graphmaster/internal/adapters/opencode"
)

// setup is what install, uninstall and doctor depend on; tests point it at
// a temporary HOME and a fake binary.
type setup struct {
	env    install.Env
	binary func() (install.BinaryPath, error)
	agents []adapters.Agent
	// dir is the directory whose repository doctor inspects.
	dir string
}

func defaultSetup() setup {
	return setup{env: install.Env{}, binary: install.ResolveBinary, agents: adapters.All(), dir: "."}
}

func newInstallCmd() *cobra.Command   { return installCmdWith(defaultSetup) }
func newUninstallCmd() *cobra.Command { return uninstallCmdWith(defaultSetup) }

func installCmdWith(get func() setup) *cobra.Command {
	var names []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Register graphmaster with supported AI agents",
		Long: "Register the graphmaster MCP server, hooks and instructions in the user-level\n" +
			"configuration of every detected agent (claude, codex, cursor, opencode), or of\n" +
			"the agents named with --agents. Every edit is recorded and backed up so\n" +
			"`graphmaster uninstall` can revert it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInstall(cmd.OutOrStdout(), get(), names, dryRun)
		},
	}
	cmd.Flags().StringSliceVar(&names, "agents", nil, "comma-separated agents (default: every detected agent)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the changes as diffs without writing anything")
	return cmd
}

func uninstallCmdWith(get func() setup) *cobra.Command {
	var names []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove graphmaster from supported AI agents",
		Long: "Revert what `graphmaster install` changed. Files untouched since then are\n" +
			"restored byte for byte from their backup; otherwise only graphmaster's own\n" +
			"entries are removed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd.OutOrStdout(), get(), names, dryRun)
		},
	}
	cmd.Flags().StringSliceVar(&names, "agents", nil, "comma-separated agents (default: every installed agent)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the changes as diffs without writing anything")
	return cmd
}

func runInstall(out io.Writer, s setup, names []string, dryRun bool) error {
	bin, err := s.binary()
	if err != nil {
		return err
	}
	agents, err := s.pick(names)
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		return fmt.Errorf("no supported agent detected (looked for %s); pass --agents to install anyway", agentList(s.agents))
	}
	opts := adapters.InstallOptions{Binary: bin.Path, Instructions: instructions.Text()}
	plans := make([]install.Plan, 0, len(agents))
	for _, a := range agents {
		p, err := a.InstallPlan(s.env, opts)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name(), err)
		}
		plans = append(plans, p)
	}
	rep, err := install.NewEngine(s.env).Install(install.Options{DryRun: dryRun}, plans...)
	if err != nil {
		if rep != nil {
			writeReport(out, s.env, rep, agents, dryRun)
		}
		return err
	}

	fmt.Fprintf(out, "graphmaster binary: %s (%s)\n", bin.Path, bin.Reason)
	writeReport(out, s.env, rep, agents, dryRun)
	for _, a := range agents {
		for _, n := range a.Notes(s.env) {
			fmt.Fprintf(out, "note (%s): %s\n", a.Name(), n)
		}
	}

	path, created, err := ensureUserConfig(s.env, dryRun)
	switch {
	case err != nil:
		return fmt.Errorf("write default config: %w", err)
	case created && dryRun:
		fmt.Fprintf(out, "config: would create %s\n", displayPath(s.env, path))
	case created:
		fmt.Fprintf(out, "config: created %s (mode = %q)\n", displayPath(s.env, path), "balanced")
	default:
		fmt.Fprintf(out, "config: %s (kept)\n", displayPath(s.env, path))
	}
	return nil
}

func runUninstall(out io.Writer, s setup, names []string, dryRun bool) error {
	var agents []adapters.Agent
	if len(names) > 0 {
		var err error
		if agents, err = s.pick(names); err != nil {
			return err
		}
	}
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = string(a.Name())
	}
	rep, err := install.NewEngine(s.env).Uninstall(install.Options{DryRun: dryRun}, ids...)
	if rep != nil {
		if len(rep.Files) == 0 {
			fmt.Fprintln(out, "nothing to uninstall")
		} else {
			writeReport(out, s.env, rep, nil, dryRun)
		}
	}
	if err != nil {
		return err
	}
	if path, err := userConfigPath(s.env); err == nil && fileExists(path) && len(names) == 0 && !dryRun {
		fmt.Fprintf(out, "config kept: %s (delete it yourself if you no longer need it)\n", displayPath(s.env, path))
	}
	return nil
}

// pick resolves --agents, or returns every detected adapter when names is
// empty.
func (s setup) pick(names []string) ([]adapters.Agent, error) {
	if len(names) == 0 {
		var out []adapters.Agent
		for _, a := range s.agents {
			if a.Detect(s.env) {
				out = append(out, a)
			}
		}
		return out, nil
	}
	var out []adapters.Agent
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			continue
		}
		id, err := hookapi.ParseAgent(n)
		if err != nil {
			return nil, fmt.Errorf("%w (supported: %s)", err, agentList(s.agents))
		}
		a := s.agent(id)
		if a == nil {
			return nil, fmt.Errorf("agent %q has no adapter in this build", id)
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("--agents names no agent")
	}
	return out, nil
}

func (s setup) agent(id hookapi.Agent) adapters.Agent {
	for _, a := range s.agents {
		if a.Name() == id {
			return a
		}
	}
	return nil
}

func agentList(agents []adapters.Agent) string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = string(a.Name())
	}
	return strings.Join(names, ", ")
}

// writeReport prints the engine report grouped by agent (in agents order,
// or in report order when agents is nil), with diffs in dry-run mode.
func writeReport(out io.Writer, env install.Env, rep *install.Report, agents []adapters.Agent, dryRun bool) {
	if dryRun {
		fmt.Fprintln(out, "dry run: no files written")
	}
	var groups []string
	if agents != nil {
		for _, a := range agents {
			groups = append(groups, string(a.Name()))
		}
	} else {
		for _, f := range rep.Files {
			for _, a := range f.Agents {
				if !slices.Contains(groups, a) {
					groups = append(groups, a)
				}
			}
		}
	}
	for _, g := range groups {
		fmt.Fprintf(out, "%s:\n", g)
		for _, f := range rep.Files {
			if !slices.Contains(f.Agents, g) {
				continue
			}
			fmt.Fprintf(out, "  %-14s %s", f.Action, displayPath(env, f.Path))
			if f.Note != "" {
				fmt.Fprintf(out, " (%s)", f.Note)
			}
			fmt.Fprintln(out)
			if dryRun && f.Diff != "" {
				for _, line := range strings.SplitAfter(strings.TrimSuffix(f.Diff, "\n"), "\n") {
					fmt.Fprintf(out, "    %s", strings.TrimSuffix(line, "\n"))
					fmt.Fprintln(out)
				}
			}
		}
	}
}

// displayPath shortens paths under HOME to ~/... .
func displayPath(env install.Env, p string) string {
	home, err := env.Home()
	if err != nil || home == "" {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return p
	}
	return "~/" + filepath.ToSlash(rel)
}
