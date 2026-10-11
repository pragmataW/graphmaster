package ui

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"os"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Side-panel bounds.
const (
	maxChildren     = 200
	maxSnippetLines = 300
	maxLineBytes    = 400
	maxSourceBytes  = 8 << 20
)

// node answers /api/node?id= (or ?skey=): one definition with its parent,
// children, edge counts and source snippet.
func (s *Server) node(ctx context.Context, d *data, r *http.Request, banner string) (any, error) {
	sym, err := d.symbolParam(ctx, r)
	if err != nil {
		return nil, err
	}
	res := NodeResponse{Banner: banner, Symbol: *sym, Children: []Symbol{}, In: map[string]int{}, Out: map[string]int{}}
	if sym.ParentID != 0 {
		if p, err := d.symsWhere(ctx, `n.id = ?`, sym.ParentID); err != nil {
			return nil, err
		} else if len(p) == 1 {
			res.Parent = p[0]
		}
	}
	children, err := d.symsWhere(ctx, `n.parent_id = ? ORDER BY n.start_line, n.id LIMIT ?`, sym.ID, maxChildren+1)
	if err != nil {
		return nil, err
	}
	if len(children) > maxChildren {
		var total int
		if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM nodes WHERE parent_id = ?`, sym.ID).Scan(&total); err != nil {
			return nil, err
		}
		children, res.MoreChildren = children[:maxChildren], total-maxChildren
	}
	for _, c := range children {
		res.Children = append(res.Children, *c)
	}
	if err := d.edgeCounts(ctx, sym.ID, &res); err != nil {
		return nil, err
	}
	res.Source = d.snippet(sym)
	return res, nil
}

// symbolParam resolves ?id= or ?skey= to a definition.
func (d *data) symbolParam(ctx context.Context, r *http.Request) (*Symbol, error) {
	if raw := strings.TrimSpace(r.URL.Query().Get("skey")); raw != "" {
		key, err := hex.DecodeString(raw)
		if err != nil || len(key) != 8 {
			return nil, badRequest("skey must be 16 hex digits")
		}
		ss, err := d.symsWhere(ctx, `n.skey = ? ORDER BY n.id LIMIT 1`, key)
		if err != nil {
			return nil, err
		}
		if len(ss) == 0 {
			return nil, notFound("no symbol with skey %s", raw)
		}
		return ss[0], nil
	}
	id, err := idParam(r)
	if err != nil {
		return nil, err
	}
	return d.symByID(ctx, id)
}

// edgeCounts fills the per-kind counts of resolved incoming and outgoing
// edges (containment excluded) and of unresolved outgoing references.
func (d *data) edgeCounts(ctx context.Context, id int64, res *NodeResponse) error {
	count := func(q string, into map[string]int) error {
		rows, err := d.db.QueryContext(ctx, q, id, int(model.EdgeContains))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, n int
			if err := rows.Scan(&kind, &n); err != nil {
				return err
			}
			into[model.EdgeKind(uint8(kind)).String()] = n
		}
		return rows.Err()
	}
	if err := count(`SELECT kind, count(*) FROM edges WHERE dst = ? AND kind <> ? GROUP BY kind`, res.In); err != nil {
		return err
	}
	if err := count(`SELECT kind, count(*) FROM edges WHERE src = ? AND +dst IS NOT NULL AND kind <> ? GROUP BY kind`, res.Out); err != nil {
		return err
	}
	return d.db.QueryRowContext(ctx, `SELECT count(*) FROM edges WHERE src = ? AND dst IS NULL`, id).Scan(&res.Unresolved)
}

// snippet reads the symbol's lines from the working tree.
func (d *data) snippet(sym *Symbol) *Snippet {
	sn := &Snippet{Start: sym.Start, Lines: []string{}}
	if d.root == nil {
		sn.Error = "no working tree"
		return sn
	}
	abs := d.root.Abs(sym.Path)
	if fi, err := os.Stat(abs); err != nil {
		sn.Error = "cannot read source: " + err.Error()
		return sn
	} else if fi.Size() > maxSourceBytes {
		sn.Error = "file too large to show"
		return sn
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		sn.Error = "cannot read source: " + err.Error()
		return sn
	}
	lines := bytes.Split(src, []byte("\n"))
	last := min(sym.End, len(lines))
	if sym.Start < 1 || sym.Start > last {
		sn.Error = "lines are beyond the end of the file; the index may be stale"
		return sn
	}
	if last-sym.Start+1 > maxSnippetLines {
		last, sn.Truncated = sym.Start+maxSnippetLines-1, true
	}
	for _, l := range lines[sym.Start-1 : last] {
		l = bytes.TrimRight(l, "\r")
		if len(l) > maxLineBytes {
			l = append(bytes.ToValidUTF8(l[:maxLineBytes], nil), "…"...)
		}
		sn.Lines = append(sn.Lines, string(bytes.ToValidUTF8(l, []byte("�"))))
	}
	return sn
}
