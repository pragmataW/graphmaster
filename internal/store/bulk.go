package store

import (
	"context"
	"fmt"
)

// enterBulk marks the database as bulk-building and drops the secondary
// indexes so inserts stay cheap. Core indexes needed by DeleteFileGraph are
// kept. FTS rows are skipped by InsertNode until FinishBulk.
func (w *Writer) enterBulk() error {
	if err := w.SetMeta(MetaBulk, "1"); err != nil {
		return err
	}
	for _, name := range secondaryIndexes {
		if _, err := w.exec("DROP INDEX IF EXISTS " + name); err != nil {
			return fmt.Errorf("store: drop index %s: %w", name, err)
		}
	}
	return w.commit()
}

// finishBulkSQL recreates search rows from nodes in one pass.
var finishBulkSQL = []string{
	`INSERT INTO sym_fts(sym_fts) VALUES('delete-all')`,
	`INSERT INTO sym_tri(sym_tri) VALUES('delete-all')`,
	`INSERT INTO sym_fts(rowid, tokens, path)
		SELECT n.id, gm_tokens(m.name), f.path
		FROM nodes n JOIN names m ON m.id = n.name_id JOIN files f ON f.id = n.file_id
		ORDER BY n.id`,
	`INSERT INTO sym_tri(rowid, name)
		SELECT n.id, m.name FROM nodes n JOIN names m ON m.id = n.name_id ORDER BY n.id`,
	`DELETE FROM meta WHERE key = 'bulk'`,
}

// FinishBulk ends a bulk build: it creates the secondary indexes, rebuilds
// sym_fts/sym_tri from nodes, clears the bulk flag and checkpoints the WAL.
// Call it after the bulk Writer is closed and before the resolution pass.
// It is idempotent and may also be used to repair the search index.
func (s *Store) FinishBulk(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, indexesSQL); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return fmt.Errorf("store: create indexes: %w", err)
	}
	for _, q := range finishBulkSQL {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			return fmt.Errorf("store: finish bulk: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	// Best effort: keep the WAL small and refresh planner statistics.
	_, _ = conn.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	_, _ = conn.ExecContext(ctx, "PRAGMA optimize")
	return nil
}
