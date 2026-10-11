package ui

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// groupExpr is the SQL expression naming the entry of a file path one
// level below the prefix (bound as ?1 = prefix length): "dir/" for files
// in a subdirectory, the file name for files directly in the prefix.
const groupExpr = `CASE WHEN instr(substr(path, ?1 + 1), '/') > 0
	THEN substr(path, ?1 + 1, instr(substr(path, ?1 + 1), '/'))
	ELSE substr(path, ?1 + 1) END`

// prefixRange restricts files to the prefix with an index-friendly range
// (?2 <= path < ?3).
const prefixRange = `path >= ?2 AND path < ?3`

// pkgGroup is one directory or file entry below the prefix.
type pkgGroup struct {
	name                string
	files, loc, symbols int
}

// packages answers /api/packages?prefix=: the entries one level below a
// directory prefix with file, LOC and symbol counts, and the dependency
// edges between them aggregated in SQL (GROUP BY source and target entry).
func (s *Server) packages(ctx context.Context, d *data, r *http.Request, banner string) (any, error) {
	prefix := normPath(r.URL.Query().Get("prefix"))
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	lo, hi := prefix, "\U0010FFFF"
	if prefix != "" {
		hi = prefix[:len(prefix)-1] + "0" // '0' follows '/'
	}
	res := PackagesResponse{Prefix: prefix, Parent: parentPrefix(prefix)}

	groups, err := d.pkgGroups(ctx, len(prefix), lo, hi)
	if err != nil {
		return nil, err
	}
	g := newGraph(d.max)
	for _, grp := range groups {
		kind := "file"
		if strings.HasSuffix(grp.name, "/") {
			kind = "dir"
		}
		nd := ElemData{
			ID: "g:" + prefix + grp.name, Label: grp.name, Kind: kind, Path: prefix + grp.name,
			Files: grp.files, LOC: grp.loc, Symbols: grp.symbols,
		}
		if !g.node(nd, "group", kind) {
			break
		}
	}
	if !g.full() {
		if err := d.addGroupEdges(ctx, g, prefix, lo, hi); err != nil {
			return nil, err
		}
	}
	res.GraphResponse = g.response(banner)
	return res, nil
}

// pkgGroups aggregates the files below prefix by entry, largest first.
func (d *data) pkgGroups(ctx context.Context, plen int, lo, hi string) ([]pkgGroup, error) {
	rows, err := d.db.QueryContext(ctx, `WITH f AS (
			SELECT id, loc, `+groupExpr+` AS g FROM files WHERE `+prefixRange+`
		), s AS (
			SELECT n.file_id, count(*) c FROM nodes n JOIN f ON f.id = n.file_id GROUP BY n.file_id
		)
		SELECT f.g, count(*), sum(f.loc), coalesce(sum(s.c), 0) FROM f LEFT JOIN s ON s.file_id = f.id
		GROUP BY f.g`, plen, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pkgGroup
	for rows.Next() {
		var grp pkgGroup
		if err := rows.Scan(&grp.name, &grp.files, &grp.loc, &grp.symbols); err != nil {
			return nil, err
		}
		out = append(out, grp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].symbols != out[j].symbols {
			return out[i].symbols > out[j].symbols
		}
		return out[i].name < out[j].name
	})
	return out, rows.Err()
}

// addGroupEdges adds one edge per (source entry, target entry) pair,
// heaviest first, counting the resolved non-containment edges between
// definitions of the two entries.
func (d *data) addGroupEdges(ctx context.Context, g *graph, prefix, lo, hi string) error {
	rows, err := d.db.QueryContext(ctx, `WITH f AS (
			SELECT id, `+groupExpr+` AS g FROM files WHERE `+prefixRange+`
		)
		SELECT fs.g, fd.g, count(*) c, max(e.conf) FROM edges e
			JOIN f fs ON fs.id = e.file_id
			JOIN nodes t ON t.id = e.dst
			JOIN f fd ON fd.id = t.file_id
		WHERE e.kind <> ?4 AND fs.g <> fd.g
		GROUP BY fs.g, fd.g ORDER BY c DESC, fs.g, fd.g`, len(prefix), lo, hi, int(model.EdgeContains))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var src, dst string
		var count, conf int
		if err := rows.Scan(&src, &dst, &count, &conf); err != nil {
			return err
		}
		s, t := "g:"+prefix+src, "g:"+prefix+dst
		if !g.edge(ElemData{ID: "ge:" + s + "->" + t, Source: s, Target: t, Kind: "uses", Count: count, Conf: conf}, "groupedge") {
			return nil
		}
	}
	return rows.Err()
}

// parentPrefix returns the directory prefix one level up.
func parentPrefix(prefix string) string {
	p := strings.TrimSuffix(prefix, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return ""
}
