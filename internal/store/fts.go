package store

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"
)

// defaultSearchLimit applies when callers pass limit <= 0.
const defaultSearchLimit = 50

// insertFTS indexes a node in sym_fts and sym_tri (rowid = node id).
func (w *Writer) insertFTS(n *Node) error {
	name := n.Name
	if name == "" {
		if err := w.QueryRow(`SELECT name FROM names WHERE id = ?`, n.NameID).Scan(&name); err != nil {
			return err
		}
	}
	path, err := w.pathOf(n.FileID)
	if err != nil {
		return err
	}
	if _, err := w.exec(`INSERT INTO sym_fts(rowid, tokens, path) VALUES(?, ?, ?)`, n.ID, TokenizeIdent(name), path); err != nil {
		return err
	}
	_, err = w.exec(`INSERT INTO sym_tri(rowid, name) VALUES(?, ?)`, n.ID, name)
	return err
}

// deleteFTS removes a node's search rows.
func (w *Writer) deleteFTS(nodeID int64) error {
	if _, err := w.exec(`DELETE FROM sym_fts WHERE rowid = ?`, nodeID); err != nil {
		return err
	}
	_, err := w.exec(`DELETE FROM sym_tri WHERE rowid = ?`, nodeID)
	return err
}

func (w *Writer) pathOf(fileID int64) (string, error) {
	if p, ok := w.paths[fileID]; ok {
		return p, nil
	}
	var p string
	if err := w.QueryRow(`SELECT path FROM files WHERE id = ?`, fileID).Scan(&p); err != nil {
		return "", err
	}
	w.paths[fileID] = p
	return p, nil
}

// SearchSymbols runs a prefix token search (see MatchExpr) over symbol
// names and returns node ids, best bm25 match first.
func (s *Store) SearchSymbols(ctx context.Context, query string, limit int) ([]int64, error) {
	expr := MatchExpr(query, true)
	if expr == "" {
		return nil, nil
	}
	return s.MatchSymbols(ctx, "{tokens} : ("+expr+")", limit)
}

// MatchSymbols runs a raw FTS5 MATCH expression against sym_fts (columns
// tokens and path) and returns node ids ordered by rank.
func (s *Store) MatchSymbols(ctx context.Context, match string, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	return queryIDs(ctx, s.db, `SELECT rowid FROM sym_fts WHERE sym_fts MATCH ? ORDER BY rank LIMIT ?`, match, limit)
}

// SearchSubstring finds symbols whose name contains sub (case-insensitive)
// using the trigram index. Inputs shorter than three characters return nil;
// callers should fall back to SearchSymbols.
func (s *Store) SearchSubstring(ctx context.Context, sub string, limit int) ([]int64, error) {
	if utf8.RuneCountInString(sub) < 3 {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	match := `"` + strings.ReplaceAll(sub, `"`, `""`) + `"`
	return queryIDs(ctx, s.db, `SELECT rowid FROM sym_tri WHERE sym_tri MATCH ? ORDER BY rank LIMIT ?`, match, limit)
}

func queryIDs(ctx context.Context, db *sql.DB, q string, args ...any) ([]int64, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
