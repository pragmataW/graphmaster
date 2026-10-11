package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/resolve"
)

// cliResolver supplies the name resolver used by CLI-driven indexing.
// Tests may replace it; nil leaves names queued in pending_names.
var cliResolver = newResolver

// newResolver returns the production resolver (internal/resolve).
func newResolver() index.Resolver { return &resolve.Resolver{} }

// indexLockBudget is how long `graphmaster index` waits for another writer.
const indexLockBudget = 10 * time.Minute

func newIndexCmd() *cobra.Command {
	var quiet, force bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build or refresh the code graph for the current repository",
		Long: "Build or incrementally refresh .graphmaster/graph.db for the enclosing git repository.\n" +
			"--force rebuilds the graph from scratch (cache.db is kept) and also allows\n" +
			"indexing a directory that is not a git repository.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runIndex(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), ".", quiet, force)
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "print nothing except errors")
	cmd.Flags().BoolVar(&force, "force", false, "rebuild from scratch; allow a non-git directory")
	return cmd
}

// cpuProfileEnv names a file to write a CPU profile of `graphmaster index`
// to (a debugging aid; unset in normal use).
const cpuProfileEnv = "GRAPHMASTER_CPUPROFILE"

func runIndex(ctx context.Context, out, errw io.Writer, dir string, quiet, force bool) error {
	if p := os.Getenv(cpuProfileEnv); p != "" {
		f, err := os.Create(p)
		if err != nil {
			return fmt.Errorf("%s: %w", cpuProfileEnv, err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return fmt.Errorf("%s: %w", cpuProfileEnv, err)
		}
		defer pprof.StopCPUProfile()
	}
	opts := index.Options{AllowNoGit: force}
	if cliResolver != nil {
		opts.Resolver = cliResolver()
	}
	var progress *progressLine
	if !quiet {
		progress = &progressLine{w: errw}
		opts.Progress = progress.update
	}
	ix, err := index.Open(ctx, dir, opts)
	if err != nil {
		return err
	}
	defer ix.Close()

	run := ix.Freshen
	if force {
		run = ix.Rebuild
	}
	res, err := run(ctx, indexLockBudget)
	if progress != nil {
		progress.clear()
	}
	if err != nil {
		return err
	}
	if res.Stale {
		return fmt.Errorf("another graphmaster process holds the write lock; %d files pending", res.Pending)
	}
	if quiet {
		return nil
	}
	root := ix.Root().WorkTree
	if res.Clean() {
		fmt.Fprintf(out, "%s: up to date (%s)\n", root, res.Duration.Round(time.Millisecond))
		return nil
	}
	verb := "updated"
	if res.FullBuild {
		verb = "indexed"
	}
	fmt.Fprintf(out, "%s %s: +%d ~%d -%d files (parsed %d, cache hits %d, parse errors %d) in %s\n",
		verb, root, res.Added, res.Modified, res.Deleted, res.Parsed, res.CacheHits, res.ParseErrors,
		res.Duration.Round(time.Millisecond))
	if res.Rebuilt != "" {
		fmt.Fprintf(out, "rebuilt: %s\n", res.Rebuilt)
	}
	return nil
}

// progressLine renders "indexing 37% (1234/3456)" on one terminal line,
// at most every 100ms.
type progressLine struct {
	w     io.Writer
	mu    sync.Mutex
	last  time.Time
	shown bool
}

func (p *progressLine) update(done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if total < 200 || (done < total && time.Since(p.last) < 100*time.Millisecond) {
		return
	}
	p.last = time.Now()
	p.shown = true
	fmt.Fprintf(p.w, "\rindexing %3d%% (%d/%d)", done*100/total, done, total)
}

func (p *progressLine) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shown {
		fmt.Fprint(p.w, "\r\033[K")
	}
}
