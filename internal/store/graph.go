package store

import (
	"errors"
	"fmt"
	"strings"
)

// UpsertFile inserts or updates the row for f.Path, keeping its id stable,
// and sets f.ID.
func (w *Writer) UpsertFile(f *File) (int64, error) {
	id, err := w.queryID(`INSERT INTO files(path, lang, size, mtime_ns, ino, mode, blob, tracked, status, loc, pkg_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET lang = excluded.lang, size = excluded.size,
			mtime_ns = excluded.mtime_ns, ino = excluded.ino, mode = excluded.mode,
			blob = excluded.blob, tracked = excluded.tracked, status = excluded.status,
			loc = excluded.loc, pkg_id = excluded.pkg_id
		RETURNING id`,
		f.Path, int(f.Lang), f.Size, f.MtimeNS, int64(f.Ino), int64(f.Mode), f.Blob,
		f.Tracked, int(f.Status), f.LOC, nullID(f.PkgID))
	if err != nil {
		return 0, fmt.Errorf("store: upsert file %s: %w", f.Path, err)
	}
	f.ID = id
	w.paths[id] = f.Path
	return id, nil
}

// UpsertPackage inserts or updates the package (Lang, Name) and sets p.ID.
func (w *Writer) UpsertPackage(p *Package) (int64, error) {
	id, err := w.queryID(`INSERT INTO packages(lang, name, dir, kind) VALUES(?, ?, ?, ?)
		ON CONFLICT(lang, name) DO UPDATE SET dir = excluded.dir, kind = excluded.kind
		RETURNING id`, int(p.Lang), p.Name, nullStr(p.Dir), int(p.Kind))
	if err != nil {
		return 0, fmt.Errorf("store: upsert package %s: %w", p.Name, err)
	}
	p.ID = id
	return id, nil
}

// InsertNode stores a definition, interning n.Name when n.NameID is 0, and
// indexes it for search (unless the writer is in bulk mode). Sets n.ID.
func (w *Writer) InsertNode(n *Node) (int64, error) {
	if n.NameID == 0 {
		if n.Name == "" {
			return 0, errors.New("store: node without name")
		}
		id, err := w.Intern(n.Name)
		if err != nil {
			return 0, err
		}
		n.NameID = id
	}
	res, err := w.exec(`INSERT INTO nodes(file_id, parent_id, kind, name_id, qname, start_line, start_col,
			end_line, start_byte, end_byte, sig, flags, skey)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.FileID, nullID(n.ParentID), int(n.Kind), n.NameID, n.QName, n.StartLine, n.StartCol,
		n.EndLine, n.StartByte, n.EndByte, nullStr(n.Sig), int(n.Flags), n.SKey)
	if err != nil {
		return 0, fmt.Errorf("store: insert node %s: %w", n.QName, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	n.ID = id
	if !w.opt.Bulk {
		if err := w.insertFTS(n); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// InsertEdge stores an edge, interning e.Name when e.NameID is 0 and Name
// is set. Sets e.ID.
func (w *Writer) InsertEdge(e *Edge) (int64, error) {
	if e.NameID == 0 && e.Name != "" {
		id, err := w.Intern(e.Name)
		if err != nil {
			return 0, err
		}
		e.NameID = id
	}
	res, err := w.exec(`INSERT INTO edges(file_id, src, dst, kind, name_id, qual, line, col, conf)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.FileID, e.Src, nullID(e.Dst), int(e.Kind), nullID(e.NameID), nullStr(e.Qual), e.Line, e.Col, int(e.Conf))
	if err != nil {
		return 0, fmt.Errorf("store: insert edge: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	e.ID = id
	return id, nil
}

// InsertImport stores an import row.
func (w *Writer) InsertImport(im *Import) error {
	_, err := w.exec(`INSERT INTO imports(file_id, spec, alias, names, target_file, target_pkg, line)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		im.FileID, im.Spec, nullStr(im.Alias), nullStr(strings.Join(im.Names, ",")),
		nullID(im.TargetFile), nullID(im.TargetPkg), im.Line)
	if err != nil {
		return fmt.Errorf("store: insert import %s: %w", im.Spec, err)
	}
	return nil
}

// EnqueuePending adds name ids to the resolution queue.
func (w *Writer) EnqueuePending(nameIDs ...int64) error {
	for _, id := range nameIDs {
		if _, err := w.exec(`INSERT OR IGNORE INTO pending_names(name_id) VALUES(?)`, id); err != nil {
			return err
		}
	}
	return nil
}

// ClearPending removes name ids from the resolution queue.
func (w *Writer) ClearPending(nameIDs ...int64) error {
	for _, id := range nameIDs {
		if _, err := w.exec(`DELETE FROM pending_names WHERE name_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// deleteFileGraphSQL removes one file's graph contribution. ?1 = file id.
// Order matters: inbound edges and FTS rows are found through the file's
// nodes, so nodes go last.
var deleteFileGraphSQL = []string{
	// Names targeted by other files' edges into this file must be re-resolved.
	`INSERT OR IGNORE INTO pending_names(name_id)
		SELECT DISTINCT e.name_id FROM nodes n JOIN edges e ON e.dst = n.id
		WHERE n.file_id = ?1 AND e.file_id <> ?1 AND e.name_id IS NOT NULL`,
	`UPDATE edges SET dst = NULL, conf = 0
		WHERE dst IN (SELECT id FROM nodes WHERE file_id = ?1) AND file_id <> ?1`,
	// Removing definitions can change other references' resolution
	// (e.g. a unique global becoming ambiguous or disappearing).
	`INSERT OR IGNORE INTO pending_names(name_id) SELECT DISTINCT name_id FROM nodes WHERE file_id = ?1`,
	`DELETE FROM sym_fts WHERE rowid IN (SELECT id FROM nodes WHERE file_id = ?1)`,
	`DELETE FROM sym_tri WHERE rowid IN (SELECT id FROM nodes WHERE file_id = ?1)`,
	`DELETE FROM edges WHERE file_id = ?1`,
	`DELETE FROM imports WHERE file_id = ?1`,
	// The file's dependency attributions are re-recorded when it is
	// processed again.
	`DELETE FROM dep_owners WHERE file_id = ?1`,
	`DELETE FROM nodes WHERE file_id = ?1`,
}

// DeleteFileGraph removes a file's nodes, out-edges, imports, dependency
// attributions (dep_owners) and FTS rows
// in the current batch. Edges from other files into it are kept with
// dst = NULL and their names (plus the file's old definition names) are
// queued in pending_names. The files row itself is kept so the id stays
// stable for reinsertion.
func (w *Writer) DeleteFileGraph(fileID int64) error {
	for _, q := range deleteFileGraphSQL {
		if _, err := w.exec(q, fileID); err != nil {
			return fmt.Errorf("store: delete file graph %d: %w", fileID, err)
		}
	}
	return nil
}

// deleteNodeSQL removes one definition. ?1 = node id, ?2 = its file id.
var deleteNodeSQL = []string{
	`INSERT OR IGNORE INTO pending_names(name_id)
		SELECT DISTINCT name_id FROM edges WHERE dst = ?1 AND file_id <> ?2 AND name_id IS NOT NULL`,
	`UPDATE edges SET dst = NULL, conf = 0 WHERE dst = ?1 AND file_id <> ?2`,
}

// DeleteNode removes one definition of file fileID whose other definitions
// stay (an in-place rewrite of the file), with its FTS rows. Like
// DeleteFileGraph, edges from other files into it are kept with dst = NULL
// and their names queued in pending_names; unlike it, the definition's own
// name is not queued, and the file's own edges are left to the caller
// (children and edges of the file must be replaced by it).
func (w *Writer) DeleteNode(fileID, nodeID int64) error {
	if err := w.deleteFTS(nodeID); err != nil {
		return fmt.Errorf("store: delete node %d: %w", nodeID, err)
	}
	for _, q := range deleteNodeSQL {
		if _, err := w.exec(q, nodeID, fileID); err != nil {
			return fmt.Errorf("store: delete node %d: %w", nodeID, err)
		}
	}
	if _, err := w.exec(`DELETE FROM nodes WHERE id = ?`, nodeID); err != nil {
		return fmt.Errorf("store: delete node %d: %w", nodeID, err)
	}
	return nil
}

// DeleteFile removes a file completely: its graph (see DeleteFileGraph),
// the files row, and import targets pointing at it.
func (w *Writer) DeleteFile(fileID int64) error {
	if err := w.DeleteFileGraph(fileID); err != nil {
		return err
	}
	if _, err := w.exec(`UPDATE imports SET target_file = NULL WHERE target_file = ?`, fileID); err != nil {
		return err
	}
	if _, err := w.exec(`DELETE FROM files WHERE id = ?`, fileID); err != nil {
		return err
	}
	delete(w.paths, fileID)
	return nil
}
