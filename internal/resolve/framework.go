// Package resolve turns unresolved reference edges (dst NULL) into graph
// edges, following PLAN "Name resolution":
//
//	T1 import-qualified 100  qualifier/alias/named import -> unique def in target
//	T2 lexical           90  same file (inner scope first), then same module
//	T3 receiver-typed    80  x.M() with x's type known locally + ancestor walk
//	T4 unique global     50  single candidate in the language
//	T5 ambiguous       30/n  2..5 candidates, one edge each; more -> dst NULL
//
// The first matching tier wins. Names with more than Options.CommonNameLimit
// definitions or references skip T4/T5.
//
// Work is driven by the pending_names queue: for every pending name, all
// edges carrying that name are re-resolved and their rows REPLACED (a T5
// fan-out collapses back to one row when a candidate disappears), so an
// incremental run converges to the same graph as a full build. Processed
// names leave the queue in the same transaction as their edge updates, which
// makes the pass crash-safe.
//
// Language specifics plug in through LangResolver (resolve/<lang>.go,
// registered from init()); module identity and import targets come from the
// parse layer's model.LanguageSpec.
//
// Every run that changed something ends with the Go implements pass
// (goimpl.go), which derives the implicit interface satisfaction of Go
// types from the resolved graph.
package resolve

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Confidence values of the tiers.
const (
	ConfImport   uint8 = 100 // T1
	ConfLexical  uint8 = 90  // T2
	ConfReceiver uint8 = 80  // T3
	ConfUnique   uint8 = 50  // T4
	ConfAmbig    uint8 = 30  // T5, divided by the number of candidates
)

// MaxAmbiguous is the largest candidate set that still produces T5 edges.
const MaxAmbiguous = 5

// Defaults for Options.
const (
	DefaultBatchNames        = 256
	DefaultBatchFiles        = 128
	DefaultCommonNameLimit   = 2000
	DefaultMaxDependentFiles = 512
	defaultCacheLimit        = 200_000
	maxDepth                 = 32 // guard for parent chains and inference
)

// Tier identifies which rule resolved a reference.
type Tier uint8

// Tiers.
const (
	TierNone Tier = iota // unresolved
	Tier1Import
	Tier2Lexical
	Tier3Receiver
	Tier4Unique
	Tier5Ambiguous
	tierCount
)

var tierNames = [...]string{"none", "T1", "T2", "T3", "T4", "T5"}

func (t Tier) String() string {
	if t < tierCount {
		return tierNames[t]
	}
	return fmt.Sprintf("tier(%d)", uint8(t))
}

// Options configure a resolution run. The zero value is valid.
type Options struct {
	// BatchNames is how many pending names are resolved per transaction
	// (default 256).
	BatchNames int
	// CommonNameLimit: names with more definitions or references than this
	// skip T4/T5 (default 2000).
	CommonNameLimit int
	// Source reads a repo-relative file for receiver typing (T3). Default:
	// RepoContext.ReadFile. Content whose length differs from files.size is
	// treated as stale and ignored.
	Source func(path string) ([]byte, error)
	// NoDependents disables re-resolving receiver-typed references in files
	// that use a pending name (see ResolvePending). Full builds disable it
	// automatically since every name is pending anyway.
	NoDependents bool
	// MaxDependentFiles bounds the files re-checked as dependents per batch
	// (default 512); above it dependents are skipped for that batch.
	MaxDependentFiles int
	// BatchFiles is how many files ResolveAll resolves per transaction
	// (default 128).
	BatchFiles int
}

func (o Options) batchFiles() int {
	if o.BatchFiles > 0 {
		return o.BatchFiles
	}
	return DefaultBatchFiles
}

func (o Options) batchNames() int {
	if o.BatchNames > 0 {
		return o.BatchNames
	}
	return DefaultBatchNames
}

func (o Options) commonLimit() int {
	if o.CommonNameLimit > 0 {
		return o.CommonNameLimit
	}
	return DefaultCommonNameLimit
}

func (o Options) maxDependentFiles() int {
	if o.MaxDependentFiles > 0 {
		return o.MaxDependentFiles
	}
	return DefaultMaxDependentFiles
}

func (o Options) cacheLimit() int { return defaultCacheLimit }

// Stats summarizes a run.
type Stats struct {
	Names      int // pending names drained
	Files      int // queued files whose own references were re-resolved
	Refs       int // references (edge groups) resolved
	Dependents int // of Refs, re-resolved as dependents of a pending name
	// DependentsSkipped counts batches whose dependents exceeded
	// Options.MaxDependentFiles and were not re-resolved.
	DependentsSkipped int
	Batches           int // committed transactions
	Unresolved        int // references left with dst NULL
	Edges             int // edge rows written (updates + inserts + deletes)
	ByTier            [tierCount]int
	// Implements counts derived Go implements rows written (see goimpl.go);
	// ImplementsSkipped the interfaces left unlinked by the bounds there.
	Implements        int
	ImplementsSkipped int
}

// ResolvePending drains the per-file queue (see PendingFilePrefix), the
// removed-names and dependents queues (PendingRemovedPrefix,
// PendingDependentsPrefix), then pending_names, in batched transactions. For every queued file its own edges are
// re-resolved; for every pending name all edges carrying it are
// re-resolved, replacing their rows.
// Unless Options.NoDependents is set, receiver-shaped references in files
// that mention a pending name are re-resolved too, since their receiver
// typing (T3) may depend on a definition of that name (see dependentGroups
// for the bounds).
//
// The caller must hold the store's write lock and must not have an open
// Writer. If a bulk build was left unfinished (meta bulk=1), FinishBulk runs
// first. On error or cancellation the current batch is rolled back and the
// remaining names stay queued.
func ResolvePending(ctx context.Context, st *store.Store, rc model.RepoContext, opts Options) (Stats, error) {
	return run(ctx, st, rc, opts, false)
}

// ResolveAll re-resolves every reference of the graph and empties the
// pending_names queue; use it after a full (bulk) build. It walks the
// edges file by file (Options.BatchFiles per transaction), so every source
// file is read and parsed about once, instead of once per name batch. The
// queue is filled first and cleared only when the walk is complete: an
// interrupted run leaves every name queued for ResolvePending.
func ResolveAll(ctx context.Context, st *store.Store, rc model.RepoContext, opts Options) (Stats, error) {
	if _, err := st.DB().ExecContext(ctx, `INSERT OR IGNORE INTO pending_names(name_id)
		SELECT DISTINCT name_id FROM edges WHERE name_id IS NOT NULL`); err != nil {
		return Stats{}, fmt.Errorf("resolve: enqueue all: %w", err)
	}
	return run(ctx, st, rc, opts, true)
}

// Resolver adapts the package functions to the indexer's Resolver
// interface (ResolvePending(ctx, st, rc) error).
type Resolver struct {
	Options Options
	// Last holds the stats of the most recent run.
	Last Stats
}

// ResolvePending implements the indexer's Resolver interface.
func (r *Resolver) ResolvePending(ctx context.Context, st *store.Store, rc model.RepoContext) error {
	stats, err := ResolvePending(ctx, st, rc, r.Options)
	r.Last = stats
	return err
}

// ResolveAll runs a full resolution with r's options.
func (r *Resolver) ResolveAll(ctx context.Context, st *store.Store, rc model.RepoContext) error {
	stats, err := ResolveAll(ctx, st, rc, r.Options)
	r.Last = stats
	return err
}

func run(ctx context.Context, st *store.Store, rc model.RepoContext, opts Options, full bool) (stats Stats, err error) {
	// Resolution relies on the secondary indexes (nodes by name, edges by
	// src). A bulk build interrupted before FinishBulk lacks them.
	if bulk, ok, err := st.Meta(ctx, store.MetaBulk); err != nil {
		return stats, fmt.Errorf("resolve: %w", err)
	} else if ok && bulk == "1" {
		if err := st.FinishBulk(ctx); err != nil {
			return stats, fmt.Errorf("resolve: finish bulk: %w", err)
		}
	}
	w, err := st.NewWriter(ctx, store.WriterOptions{})
	if err != nil {
		return stats, fmt.Errorf("resolve: %w", err)
	}
	defer func() {
		if err != nil {
			_ = w.Abort()
		}
		if cerr := w.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("resolve: %w", cerr)
		}
	}()
	s := newSession(ctx, w, rc, opts)
	defer s.close()
	tr := newPhaseTracer()
	if full {
		// The flag makes a later run redo the Go implements pass if this
		// one stops after the walk (see finishImpl).
		if err := w.SetMeta(metaImplDirty, "1"); err != nil {
			return stats, fmt.Errorf("resolve: %w", err)
		}
		if err := runByFile(s, w, &stats); err != nil {
			return stats, err
		}
		tr.phase("full", &stats)
		err := finishImpl(s, w, &stats)
		tr.phase("go implements", &stats)
		return stats, err
	}
	// The Go implements pass runs after the queues are drained; the flag
	// commits first so that an interrupted run still runs it next time.
	impl, err := implQueued(s)
	if err != nil {
		return stats, fmt.Errorf("resolve: %w", err)
	}
	if impl {
		if err := w.SetMeta(metaImplDirty, "1"); err != nil {
			return stats, fmt.Errorf("resolve: %w", err)
		}
		if err := w.Flush(); err != nil {
			return stats, fmt.Errorf("resolve: commit: %w", err)
		}
	}
	if err := drainFileQueue(s, w, &stats); err != nil {
		return stats, err
	}
	tr.phase("file queue", &stats)
	for _, prefix := range []string{PendingRemovedPrefix, PendingDependentsPrefix} {
		if err := drainNameQueue(s, w, prefix, &stats); err != nil {
			return stats, err
		}
		tr.phase(prefix, &stats)
	}

	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		ids, err := pendingBatch(s, opts.batchNames())
		if err != nil {
			return stats, fmt.Errorf("resolve: read pending: %w", err)
		}
		if len(ids) == 0 {
			tr.phase("pending_names", &stats)
			if !impl {
				return stats, nil
			}
			err := finishImpl(s, w, &stats)
			tr.phase("go implements", &stats)
			return stats, err
		}
		if err := resolveBatch(s, w, ids, full || opts.NoDependents, &stats); err != nil {
			return stats, err
		}
		if err := w.ClearPending(ids...); err != nil {
			return stats, fmt.Errorf("resolve: clear pending: %w", err)
		}
		if err := w.Flush(); err != nil {
			return stats, fmt.Errorf("resolve: commit: %w", err)
		}
		stats.Names += len(ids)
		stats.Batches++
		s.endBatch()
	}
}

// runByFile resolves every edge of the graph, s.opts.batchFiles() files per
// committed batch, then clears the pending queue. Groups never span files,
// so a file's edges form complete groups.
func runByFile(s *Session, w *store.Writer, stats *Stats) error {
	var files []int64
	if err := s.rows(`SELECT DISTINCT file_id FROM edges WHERE name_id IS NOT NULL ORDER BY file_id`, func(r *sql.Rows) error {
		var id int64
		if err := r.Scan(&id); err != nil {
			return err
		}
		files = append(files, id)
		return nil
	}); err != nil {
		return fmt.Errorf("resolve: list files: %w", err)
	}
	for len(files) > 0 {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		chunk := files[:min(len(files), s.opts.batchFiles())]
		files = files[len(chunk):]
		gs := newGroupSet()
		for _, f := range chunk {
			if err := s.rows(`SELECT `+edgeCols+` FROM edges e WHERE e.file_id = ? AND e.name_id IS NOT NULL ORDER BY e.id`,
				scanEdgeInto(gs), f); err != nil {
				return fmt.Errorf("resolve: load edges: %w", err)
			}
		}
		gs.finish()
		if err := resolveGroups(s, w, gs, stats); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("resolve: commit: %w", err)
		}
		stats.Batches++
		s.endBatch()
	}
	res, err := w.Exec(`DELETE FROM pending_names`)
	if err != nil {
		return fmt.Errorf("resolve: clear pending: %w", err)
	}
	if err := clearQueues(w); err != nil {
		return fmt.Errorf("resolve: clear queues: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		stats.Names = int(n)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("resolve: commit: %w", err)
	}
	return nil
}

// finishImpl runs the Go implements pass (goimpl.go) and clears its flag
// in one committed transaction.
func finishImpl(s *Session, w *store.Writer, stats *Stats) error {
	if err := goImplPass(s, w, stats); err != nil {
		return fmt.Errorf("resolve: go implements: %w", err)
	}
	if _, err := w.Exec(`DELETE FROM meta WHERE key = ?`, metaImplDirty); err != nil {
		return fmt.Errorf("resolve: %w", err)
	}
	stats.Batches++
	if err := w.Flush(); err != nil {
		return fmt.Errorf("resolve: commit: %w", err)
	}
	return nil
}

func pendingBatch(s *Session, n int) ([]int64, error) {
	var ids []int64
	err := s.rows(`SELECT name_id FROM pending_names ORDER BY name_id LIMIT ?`, func(r *sql.Rows) error {
		var id int64
		if err := r.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}, n)
	return ids, err
}

// resolveBatch re-resolves every reference named in ids (plus dependents)
// inside the writer's open transaction.
func resolveBatch(s *Session, w *store.Writer, ids []int64, noDeps bool, stats *Stats) error {
	groups, err := loadGroupsByName(s, ids)
	if err != nil {
		return fmt.Errorf("resolve: load edges: %w", err)
	}
	if !noDeps {
		deps, skipped, err := dependentGroups(s, ids, groups)
		if err != nil {
			return fmt.Errorf("resolve: load dependents: %w", err)
		}
		if skipped {
			stats.DependentsSkipped++
		}
		stats.Dependents += len(deps.list)
		groups.add(deps.list...)
	}
	return resolveGroups(s, w, groups, stats)
}

// resolveGroups resolves and rewrites every group of gs.
func resolveGroups(s *Session, w *store.Writer, gs *groupSet, stats *Stats) error {
	for _, g := range gs.list {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		targets, tier := s.resolveGroup(g)
		if s.err != nil {
			return fmt.Errorf("resolve: %w", s.err)
		}
		n, err := applyGroup(w, g, targets)
		if err != nil {
			return fmt.Errorf("resolve: write edges: %w", err)
		}
		stats.Refs++
		stats.Edges += n
		stats.ByTier[tier]++
		if tier == TierNone {
			stats.Unresolved++
		}
	}
	return s.err
}

// target is one resolved destination.
type target struct {
	dst  int64
	conf uint8
}

// applyGroup makes g's rows equal to targets (one row with dst NULL when
// targets is empty), reusing rows in id order. It returns the number of
// rows changed.
func applyGroup(w *store.Writer, g *group, targets []target) (int, error) {
	want := slices.Clone(targets)
	slices.SortFunc(want, func(a, b target) int { return cmpInt64(a.dst, b.dst) })
	if len(want) == 0 {
		want = []target{{}}
	}
	changed := 0
	for i, t := range want {
		if i < len(g.rows) {
			r := g.rows[i]
			if r.dst == t.dst && r.conf == t.conf {
				continue
			}
			if _, err := w.Exec(`UPDATE edges SET dst = ?, conf = ? WHERE id = ?`, nullID(t.dst), int(t.conf), r.id); err != nil {
				return changed, err
			}
		} else {
			k := g.key
			if _, err := w.Exec(`INSERT INTO edges(file_id, src, dst, kind, name_id, qual, line, col, conf)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, k.file, k.src, nullID(t.dst), int(k.kind), k.nameID,
				nullStr(k.qual), k.line, k.col, int(t.conf)); err != nil {
				return changed, err
			}
		}
		changed++
	}
	for _, r := range g.rows[min(len(want), len(g.rows)):] {
		if _, err := w.Exec(`DELETE FROM edges WHERE id = ?`, r.id); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
