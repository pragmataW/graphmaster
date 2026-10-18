package resolve

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// PendingFilePrefix starts the meta keys of the per-file resolution queue:
// "pending_file:<files.id>". A queued file has had its own references
// rewritten without any definition visible to other files changing (the
// indexer kept its definitions in place), so only that file's edges must
// be re-resolved: queueing their names instead would re-resolve every
// reference of those names in the repository. Like pending_names, the keys
// commit with the facts and are removed in the transaction that resolves
// the file. internal/index writes them with the same prefix.
const PendingFilePrefix = "pending_file:"

// PendingDependentsPrefix starts the meta keys "pending_dependents:<names.id>"
// of names whose definitions kept their identity but whose types may read
// differently (a body-only edit of a language whose resolver reads other
// files' definition text, or a file whose import targets changed): the
// references of the name itself cannot resolve differently, but receiver
// typing through its definitions can, so only the name's dependents (see
// dependentGroups) are re-resolved.
const PendingDependentsPrefix = "pending_dependents:"

// PendingRemovedPrefix starts the meta keys "pending_removed:<names.id>" of
// names that only lost definitions. A reference resolved with high
// confidence (one target, T1-T3) to a surviving definition keeps its
// target when other candidates disappear: every tier filters the
// candidates and keeps the best, so a candidate outside the result cannot
// change it by leaving. Only the name's unresolved and low-confidence
// references (references into the removed definitions were unlinked with
// them) and its dependents are re-resolved.
const PendingRemovedPrefix = "pending_removed:"

// PendingRemovedKey returns the meta key queueing name id as removed.
func PendingRemovedKey(id int64) string { return PendingRemovedPrefix + strconv.FormatInt(id, 10) }

// PendingFileKey returns the meta key queueing file id.
func PendingFileKey(id int64) string { return PendingFilePrefix + strconv.FormatInt(id, 10) }

// PendingDependentsKey returns the meta key queueing the dependents of
// name id.
func PendingDependentsKey(id int64) string {
	return PendingDependentsPrefix + strconv.FormatInt(id, 10)
}

// pendingFileID parses a key made by PendingFileKey.
func pendingFileID(key string) (int64, bool) { return queuedID(key, PendingFilePrefix) }

func queuedID(key, prefix string) (int64, bool) {
	s, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// queuedKeys returns up to n meta keys starting with prefix, in key order.
func queuedKeys(s *Session, prefix string, n int) ([]string, error) {
	var keys []string
	err := s.rows(`SELECT key FROM meta WHERE key >= ? AND key < ? ORDER BY key LIMIT ?`, func(r *sql.Rows) error {
		var k string
		if err := r.Scan(&k); err != nil {
			return err
		}
		keys = append(keys, k)
		return nil
	}, prefix, prefix+"\xff", n)
	return keys, err
}

func deleteKeys(w *store.Writer, keys []string) error {
	for _, k := range keys {
		if _, err := w.Exec(`DELETE FROM meta WHERE key = ?`, k); err != nil {
			return fmt.Errorf("resolve: clear queue: %w", err)
		}
	}
	return nil
}

// drainNameQueue drains the names queued under prefix,
// s.opts.batchNames() per committed batch: for removed names their
// unresolved and low-confidence references, then (unless disabled) the
// dependents of every name are re-resolved.
func drainNameQueue(s *Session, w *store.Writer, prefix string, stats *Stats) error {
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		keys, err := queuedKeys(s, prefix, s.opts.batchNames())
		if err != nil {
			return fmt.Errorf("resolve: read queue: %w", err)
		}
		if len(keys) == 0 {
			return nil
		}
		var ids []int64
		for _, k := range keys {
			if id, ok := queuedID(k, prefix); ok {
				ids = append(ids, id)
			}
		}
		gs := newGroupSet()
		if prefix == PendingRemovedPrefix {
			for _, id := range ids {
				if err := s.rows(`SELECT `+edgeCols+` FROM edges e
					WHERE e.name_id = ?1 AND (e.dst IS NULL OR e.conf < 80)`, scanEdgeInto(gs), id); err != nil {
					return fmt.Errorf("resolve: load edges: %w", err)
				}
			}
			gs.finish()
		}
		if !s.opts.NoDependents {
			deps, skipped, err := dependentGroups(s, ids, gs)
			if err != nil {
				return fmt.Errorf("resolve: load dependents: %w", err)
			}
			if skipped {
				stats.DependentsSkipped++
			}
			stats.Dependents += len(deps.list)
			gs.add(deps.list...)
		}
		if err := resolveGroups(s, w, gs, stats); err != nil {
			return err
		}
		if err := deleteKeys(w, keys); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("resolve: commit: %w", err)
		}
		stats.Batches++
		s.endBatch()
	}
}

// drainFileQueue re-resolves the edges of every queued file,
// s.opts.batchFiles() files per committed batch. Keys of files that no
// longer exist are dropped.
func drainFileQueue(s *Session, w *store.Writer, stats *Stats) error {
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		keys, err := queuedKeys(s, PendingFilePrefix, s.opts.batchFiles())
		if err != nil {
			return fmt.Errorf("resolve: read file queue: %w", err)
		}
		if len(keys) == 0 {
			return nil
		}
		gs := newGroupSet()
		for _, k := range keys {
			id, ok := pendingFileID(k)
			if !ok || s.File(id) == nil {
				continue
			}
			stats.Files++
			s.markResolved(id)
			if err := s.rows(`SELECT `+edgeCols+` FROM edges e WHERE e.file_id = ? AND e.name_id IS NOT NULL ORDER BY e.id`,
				scanEdgeInto(gs), id); err != nil {
				return fmt.Errorf("resolve: load file edges: %w", err)
			}
		}
		if s.err != nil {
			return fmt.Errorf("resolve: %w", s.err)
		}
		gs.finish()
		if err := resolveGroups(s, w, gs, stats); err != nil {
			return err
		}
		if err := deleteKeys(w, keys); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return fmt.Errorf("resolve: commit: %w", err)
		}
		stats.Batches++
		s.endBatch()
	}
}

// clearQueues empties the meta queues (a full run resolved every
// reference).
func clearQueues(w *store.Writer) error {
	for _, p := range []string{PendingFilePrefix, PendingDependentsPrefix, PendingRemovedPrefix} {
		if _, err := w.Exec(`DELETE FROM meta WHERE key >= ? AND key < ?`, p, p+"\xff"); err != nil {
			return err
		}
	}
	return nil
}

// foreignSourceLangs are the languages whose resolvers read the source text
// of definitions in files other than the referencing one (tsDefNode,
// pyTree on a definition's file): a body-only edit there can change how
// references elsewhere resolve.
var foreignSourceLangs = map[model.Lang]bool{
	model.LangTypeScript: true, model.LangTSX: true, model.LangJavaScript: true, model.LangPython: true,
}

// CommonNameLimit returns the effective Options.CommonNameLimit, so the
// indexer can tell when a change of a name's reference count flips its
// common-name status (index.IncrementalHints).
func (r *Resolver) CommonNameLimit() int { return r.Options.commonLimit() }

// ReadsForeignSource reports whether resolving references of lang may read
// the source text of definitions in other files (index.IncrementalHints).
// Unknown languages answer true (conservative).
func (r *Resolver) ReadsForeignSource(lang model.Lang) bool {
	registry.RLock()
	_, known := registry.m[lang]
	registry.RUnlock()
	return !known || foreignSourceLangs[lang]
}
