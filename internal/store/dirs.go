package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Dir is a row of the dirs table: the persisted untracked-cache entry for
// one directory (see repo.DirRow, which has the same shape). Path "" is the
// repository root. Names is the encoded directory listing; it is opaque to
// the store.
type Dir struct {
	Path       string
	MtimeNS    int64
	IgnoreStat string
	Names      []byte
}

// LoadDirs returns every cached directory in path order.
func (s *Store) LoadDirs(ctx context.Context) ([]Dir, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, mtime_ns, ignore_stat, names FROM dirs ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dir
	for rows.Next() {
		var (
			d  Dir
			ig sql.NullString
		)
		if err := rows.Scan(&d.Path, &d.MtimeNS, &ig, &d.Names); err != nil {
			return nil, err
		}
		d.IgnoreStat = ig.String
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpsertDir inserts or replaces a cached directory in the current batch.
func (w *Writer) UpsertDir(d *Dir) error {
	names := d.Names
	if names == nil {
		names = []byte{} // keep NOT NULL semantics simple for readers
	}
	_, err := w.exec(`INSERT OR REPLACE INTO dirs(path, mtime_ns, ignore_stat, names) VALUES(?, ?, ?, ?)`,
		d.Path, d.MtimeNS, nullStr(d.IgnoreStat), names)
	if err != nil {
		return fmt.Errorf("store: upsert dir %q: %w", d.Path, err)
	}
	return nil
}

// DeleteDir removes a cached directory in the current batch.
func (w *Writer) DeleteDir(path string) error {
	_, err := w.exec(`DELETE FROM dirs WHERE path = ?`, path)
	return err
}

// ClearDirs drops the whole directory cache (e.g. after config excludes or
// core.excludesFile change) in the current batch.
func (w *Writer) ClearDirs() error {
	_, err := w.exec(`DELETE FROM dirs`)
	return err
}
