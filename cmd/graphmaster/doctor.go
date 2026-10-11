package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/config"
	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/install"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/version"
)

// errDoctorProblems makes `graphmaster doctor` exit non-zero.
var errDoctorProblems = errors.New("doctor found problems")

func newDoctorCmd() *cobra.Command { return doctorCmdWith(defaultSetup) }

func doctorCmdWith(get func() setup) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report installation and index health",
		Long: "Check, for every supported agent, that graphmaster's MCP server, hooks and\n" +
			"instructions are installed and point to an existing binary, plus the user\n" +
			"config and the index of the current repository. Exits non-zero when\n" +
			"something is broken.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.Context(), cmd.OutOrStdout(), get())
		},
	}
}

func runDoctor(ctx context.Context, out io.Writer, s setup) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r := &doctorReport{}
	top := r.section("graphmaster " + version.String())
	bin := doctorBinary(top, s)
	doctorConfig(top, s)

	results, err := doctorAgents(r, s, bin)
	if err != nil {
		return err
	}
	installed := false
	for _, res := range results {
		installed = installed || res.installed
	}
	if !installed {
		top.add(lvlFail, "not installed for any agent (run graphmaster install)")
	}
	doctorRepo(ctx, r.section("repository"), s.dir)

	r.write(out)
	if n := r.count(lvlFail); n > 0 {
		return fmt.Errorf("%w (%d)", errDoctorProblems, n)
	}
	return nil
}

// doctorBinary reports the graphmaster path install would write and
// returns it (or the best available stand-in) for building plans.
func doctorBinary(sec *section, s setup) string {
	bin, err := s.binary()
	if err == nil {
		sec.add(lvlOK, "binary %s (%s)", bin.Path, bin.Reason)
		return bin.Path
	}
	sec.add(lvlWarn, "cannot resolve a stable binary path: %v", err)
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return filepath.Join(string(filepath.Separator), install.BinaryName)
}

func doctorConfig(sec *section, s setup) {
	path, err := userConfigPath(s.env)
	if err != nil {
		sec.add(lvlWarn, "config: %v", err)
		return
	}
	cfg, err := config.LoadWith(config.Options{UserPath: path, LookupEnv: s.env.LookupEnv})
	if err != nil {
		sec.add(lvlFail, "config: %v", err)
		return
	}
	if fileExists(path) {
		sec.add(lvlOK, "config %s (mode %s)", displayPath(s.env, path), cfg.Mode)
	} else {
		sec.add(lvlInfo, "no user config at %s; defaults apply (mode %s)", displayPath(s.env, path), cfg.Mode)
	}
	for _, w := range cfg.Warnings {
		sec.add(lvlWarn, "config: %s", w)
	}
}

// doctorRepo reports the index of the repository enclosing dir.
func doctorRepo(ctx context.Context, sec *section, dir string) {
	loc, err := index.Locate(dir, false)
	if errors.Is(err, index.ErrNotGitRepo) {
		sec.add(lvlInfo, "not inside a git repository; index check skipped")
		return
	}
	if err != nil {
		sec.add(lvlWarn, "cannot locate repository: %v", err)
		return
	}
	root := loc.Root.WorkTree
	if _, err := os.Stat(filepath.Join(loc.Dir, store.GraphDBName)); errors.Is(err, fs.ErrNotExist) {
		sec.add(lvlInfo, "%s: not indexed yet; the next agent session or `graphmaster index` builds it", root)
		return
	}
	ix, err := index.Open(ctx, dir, index.Options{})
	if err != nil {
		sec.add(lvlFail, "%s: cannot open the index: %v (run graphmaster index --force)", root, err)
		return
	}
	defer ix.Close()
	st, err := ix.Status(ctx)
	if err != nil {
		sec.add(lvlFail, "%s: cannot read the index: %v", root, err)
		return
	}
	lvl := lvlOK
	if st.State != store.StateReady {
		lvl = lvlInfo
	}
	f := sec.add(lvl, "%s", root)
	for _, line := range strings.Split(strings.TrimSpace(st.Format()), "\n") {
		if !strings.HasPrefix(line, "repository:") {
			f.detail = append(f.detail, line)
		}
	}
}

// level is the severity of one doctor finding.
type level int

const (
	lvlOK level = iota
	lvlInfo
	lvlWarn
	lvlFail
)

func (l level) String() string {
	return [...]string{"ok", "info", "WARN", "FAIL"}[l]
}

type finding struct {
	level  level
	text   string
	detail []string
}

type section struct {
	title    string
	findings []*finding
}

func (s *section) add(l level, format string, args ...any) *finding {
	f := &finding{level: l, text: fmt.Sprintf(format, args...)}
	s.findings = append(s.findings, f)
	return f
}

type doctorReport struct{ sections []*section }

func (r *doctorReport) section(title string) *section {
	s := &section{title: title}
	r.sections = append(r.sections, s)
	return s
}

func (r *doctorReport) count(l level) int {
	n := 0
	for _, s := range r.sections {
		for _, f := range s.findings {
			if f.level == l {
				n++
			}
		}
	}
	return n
}

func (r *doctorReport) write(w io.Writer) {
	for i, s := range r.sections {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, s.title)
		for _, f := range s.findings {
			fmt.Fprintf(w, "  %-5s %s\n", f.level, f.text)
			for _, d := range f.detail {
				fmt.Fprintf(w, "        %s\n", d)
			}
		}
	}
	fails, warns := r.count(lvlFail), r.count(lvlWarn)
	fmt.Fprintf(w, "\n%d problem(s), %d warning(s)\n", fails, warns)
}
