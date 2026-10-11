package ui

import (
	"context"
	"net/http"

	"github.com/pragmataW/graphmaster/internal/model"
)

// maxExtFiles bounds the neighbouring files shown around a file view.
const maxExtFiles = 25

// file answers /api/file?path=: a compound node for the file with its
// definitions nested by containment, the edges among them, and the files
// it depends on or is used by as aggregated neighbours.
func (s *Server) file(ctx context.Context, d *data, r *http.Request, banner string) (any, error) {
	path := normPath(r.URL.Query().Get("path"))
	if path == "" {
		return nil, badRequest("path is required")
	}
	info, err := d.fileInfo(ctx, path)
	if err != nil {
		return nil, err
	}
	g := newGraph(d.max)
	self := fileID(info.ID)
	fd := fileNode(info.ID, info.Path, info.Lang)
	fd.LOC, fd.Symbols = info.LOC, info.Symbols
	g.node(fd, "file", "focus")

	// Start order puts a container before what it contains.
	syms, err := d.symsWhere(ctx, `n.file_id = ? ORDER BY n.start_byte, n.end_byte DESC, n.id`, info.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(syms))
	for _, sym := range syms {
		parent := self
		if sym.ParentID != 0 && g.has(symID(sym.ParentID)) {
			parent = symID(sym.ParentID)
		}
		if !g.node(symNode(sym, parent), symClasses(sym)...) {
			break
		}
		ids = append(ids, sym.ID)
	}

	if !g.full() {
		if err := d.addNeighbourFiles(ctx, g, info.ID); err != nil {
			return nil, err
		}
	}
	if !g.full() {
		edges, err := d.edgesAmong(ctx, ids, nil)
		if err != nil {
			return nil, err
		}
		g.addSymEdges(edges)
	}
	return FileResponse{GraphResponse: g.response(banner), File: *info}, nil
}

// addNeighbourFiles adds the files with the most edges into (out) and out
// of (in) file fid, each with one aggregated file-to-file edge.
func (d *data) addNeighbourFiles(ctx context.Context, g *graph, fid int64) error {
	contains := int(model.EdgeContains)
	queries := []struct {
		q   string
		out bool
	}{
		{`SELECT fd.id, fd.path, fd.lang, count(*) c FROM edges e
			JOIN nodes d ON d.id = e.dst JOIN files fd ON fd.id = d.file_id
			WHERE e.file_id = ?1 AND +e.dst IS NOT NULL AND d.file_id <> ?1 AND e.kind <> ?2
			GROUP BY fd.id ORDER BY c DESC, fd.path LIMIT ?3`, true},
		{`SELECT fs.id, fs.path, fs.lang, count(*) c FROM nodes d
			JOIN edges e ON e.dst = d.id JOIN files fs ON fs.id = e.file_id
			WHERE d.file_id = ?1 AND e.file_id <> ?1 AND e.kind <> ?2
			GROUP BY fs.id ORDER BY c DESC, fs.path LIMIT ?3`, false},
	}
	self := fileID(fid)
	for _, q := range queries {
		rows, err := d.db.QueryContext(ctx, q.q, fid, contains, maxExtFiles)
		if err != nil {
			return err
		}
		type hit struct {
			id    int64
			path  string
			lang  int
			count int
		}
		var hits []hit
		for rows.Next() {
			var h hit
			if err := rows.Scan(&h.id, &h.path, &h.lang, &h.count); err != nil {
				rows.Close()
				return err
			}
			hits = append(hits, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, h := range hits {
			other := fileID(h.id)
			if !g.node(fileNode(h.id, h.path, model.Lang(uint8(h.lang)).String()), "file", "ext") {
				return nil
			}
			src, dst := self, other
			if !q.out {
				src, dst = other, self
			}
			if !g.edge(ElemData{ID: "fe" + src + "-" + dst, Source: src, Target: dst, Kind: "uses", Count: h.count}, "fileedge") {
				return nil
			}
		}
	}
	return nil
}
