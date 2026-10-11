package store

import (
	"context"
	"database/sql"
	"errors"
)

const fileCols = `id, path, lang, size, mtime_ns, ino, mode, blob, tracked, status, loc, pkg_id`

type scanner interface{ Scan(dest ...any) error }

func scanFile(sc scanner) (*File, error) {
	var (
		f            File
		lang, status int
		ino, mode    int64
		pkg          sql.NullInt64
	)
	if err := sc.Scan(&f.ID, &f.Path, &lang, &f.Size, &f.MtimeNS, &ino, &mode, &f.Blob,
		&f.Tracked, &status, &f.LOC, &pkg); err != nil {
		return nil, err
	}
	f.Lang = langOf(lang)
	f.Status = statusOf(status)
	f.Ino = uint64(ino)
	f.Mode = uint32(mode)
	f.PkgID = pkg.Int64
	return &f, nil
}

// FileByPath returns the files row for a repo-relative path, or ErrNotFound.
func (s *Store) FileByPath(ctx context.Context, path string) (*File, error) {
	f, err := scanFile(s.db.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE path = ?`, path))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// ForEachFile streams every files row in path order (for the stat sweep).
// Iteration stops at the first error returned by fn.
func (s *Store) ForEachFile(ctx context.Context, fn func(*File) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fileCols+` FROM files ORDER BY path`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return rows.Err()
}

// NodeByID returns a node with its Name filled in, or ErrNotFound.
func (s *Store) NodeByID(ctx context.Context, id int64) (*Node, error) {
	var (
		n           Node
		kind, flags int
		parent      sql.NullInt64
		sig         sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT n.id, n.file_id, n.parent_id, n.kind, n.name_id, m.name, n.qname,
			n.start_line, n.start_col, n.end_line, n.start_byte, n.end_byte, n.sig, n.flags, n.skey
		FROM nodes n JOIN names m ON m.id = n.name_id WHERE n.id = ?`, id).Scan(
		&n.ID, &n.FileID, &parent, &kind, &n.NameID, &n.Name, &n.QName,
		&n.StartLine, &n.StartCol, &n.EndLine, &n.StartByte, &n.EndByte, &sig, &flags, &n.SKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n.ParentID = parent.Int64
	n.Kind = nodeKindOf(kind)
	n.Flags = defFlagsOf(flags)
	n.Sig = sig.String
	return &n, nil
}
