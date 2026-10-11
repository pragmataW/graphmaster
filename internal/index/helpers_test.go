package index

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// fakeResolver is a deterministic stand-in for internal/resolve: every
// pending name is re-resolved globally (unique definition -> conf 50,
// otherwise unresolved), replacing the edges of that name, and so are the
// edges of every file on the per-file queue. Because its answer depends on
// all definitions of a name, an incremental update only matches a full
// build if index queued every name whose definitions changed.
type fakeResolver struct{ calls atomic.Int32 }

// CommonNameLimit and ReadsForeignSource implement IncrementalHints: the
// fake never reads sources and has no common-name cutoff.
func (r *fakeResolver) CommonNameLimit() int               { return 1 << 30 }
func (r *fakeResolver) ReadsForeignSource(model.Lang) bool { return false }

func (r *fakeResolver) ResolvePending(ctx context.Context, st *store.Store, _ model.RepoContext) error {
	r.calls.Add(1)
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Removed names are re-resolved like pending ones: every edge of the
	// fake is below the confidence the real resolver keeps.
	rows, err := tx.QueryContext(ctx, `SELECT name_id FROM pending_names UNION
		SELECT CAST(substr(key, ?) AS INTEGER) FROM meta WHERE key >= ? AND key < ? ORDER BY 1`,
		len(metaPendingRemoved)+1, metaPendingRemoved, metaPendingRemoved+"\xff")
	if err != nil {
		return err
	}
	var names []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		names = append(names, id)
	}
	rows.Close()
	if err := r.resolveFileQueue(ctx, tx); err != nil {
		return err
	}
	for _, id := range names {
		var dst sql.NullInt64
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*), min(id) FROM nodes WHERE name_id = ?`, id).Scan(&n, &dst); err != nil {
			return err
		}
		conf := 50
		if n != 1 {
			dst, conf = sql.NullInt64{}, 0
		}
		// Every edge this resolver touches is unresolved or conf 50, so the
		// extra predicate only lets SQLite use the edges_unresolved index.
		if _, err := tx.ExecContext(ctx, `UPDATE edges SET dst = ?, conf = ?
			WHERE name_id = ? AND (dst IS NULL OR conf < 80) AND kind <> ?`,
			dst, conf, id, int(model.EdgeContains)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM pending_names WHERE name_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// resolveFileQueue resolves the edges of every queued file like a pending
// name, and empties the queue.
func (r *fakeResolver) resolveFileQueue(ctx context.Context, tx *sql.Tx) error {
	lo, hi := metaPendingFile, metaPendingFile+"\xff"
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.name_id FROM meta m
		JOIN edges e ON e.file_id = CAST(substr(m.key, ?) AS INTEGER)
		WHERE m.key >= ? AND m.key < ? AND e.name_id IS NOT NULL AND e.kind <> ?`,
		len(metaPendingFile)+1, lo, hi, int(model.EdgeContains))
	if err != nil {
		return err
	}
	type edge struct{ id, name int64 }
	var edges []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.id, &e.name); err != nil {
			rows.Close()
			return err
		}
		edges = append(edges, e)
	}
	rows.Close()
	for _, e := range edges {
		var dst sql.NullInt64
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*), min(id) FROM nodes WHERE name_id = ?`, e.name).Scan(&n, &dst); err != nil {
			return err
		}
		conf := 50
		if n != 1 {
			dst, conf = sql.NullInt64{}, 0
		}
		if _, err := tx.ExecContext(ctx, `UPDATE edges SET dst = ?, conf = ? WHERE id = ?`, dst, conf, e.id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key >= ? AND key < ?`, lo, hi); err != nil {
		return err
	}
	// Receiver typing does not exist here: dependents need no work.
	for _, p := range []string{metaPendingDependents, metaPendingRemoved} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key >= ? AND key < ?`, p, p+"\xff"); err != nil {
			return err
		}
	}
	return nil
}

// futureClock makes files written by the test look older than the racy
// window, so a second scan can prove them clean.
func futureClock() time.Time { return time.Now().Add(3 * time.Second) }

func openIx(t testing.TB, dir string, opts Options) *Indexer {
	t.Helper()
	if opts.Resolver == nil {
		opts.Resolver = &fakeResolver{}
	}
	if opts.Logf == nil {
		opts.Logf = t.Logf
	}
	ix, err := Open(context.Background(), dir, opts)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	return ix
}

func freshen(t testing.TB, ix *Indexer) *Result {
	t.Helper()
	res, err := ix.Freshen(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("Freshen: %v", err)
	}
	return res
}

func dump(t testing.TB, ix *Indexer) string {
	t.Helper()
	return testutil.CanonicalDump(ix.Store().DB())
}

// scratchDump indexes the same working tree into a fresh state directory
// and returns its canonical dump. opts.Resolver defaults to fakeResolver.
func scratchDump(t testing.TB, dir string, opts Options) string {
	t.Helper()
	opts.stateDir = t.TempDir()
	if opts.Resolver == nil {
		opts.Resolver = &fakeResolver{}
	}
	opts.Logf = t.Logf
	ix, err := Open(context.Background(), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	res := freshen(t, ix)
	if !res.FullBuild && res.Changed() > 0 {
		t.Fatalf("scratch build was not a full build: %+v", res)
	}
	return dump(t, ix)
}

func requireSameDump(t testing.TB, label, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	inWant := map[string]bool{}
	for _, l := range wl {
		inWant[l] = true
	}
	inGot := map[string]bool{}
	for _, l := range gl {
		inGot[l] = true
	}
	var diff []string
	for _, l := range gl {
		if !inWant[l] {
			diff = append(diff, "+ "+l)
		}
	}
	for _, l := range wl {
		if !inGot[l] {
			diff = append(diff, "- "+l)
		}
	}
	if len(diff) > 60 {
		diff = append(diff[:60], "...")
	}
	t.Fatalf("%s: incremental dump differs from scratch build (+incremental -scratch):\n%s", label, strings.Join(diff, "\n"))
}

func writeFile(t testing.TB, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileRow(t testing.TB, ix *Indexer, rel string) *store.File {
	t.Helper()
	f, err := ix.Store().FileByPath(context.Background(), rel)
	if err != nil {
		t.Fatalf("FileByPath(%s): %v", rel, err)
	}
	return f
}
