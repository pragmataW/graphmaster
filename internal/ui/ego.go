package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Ego-graph bounds (PLAN: limit ≤ 300).
const (
	defaultEgoDepth = 1
	maxEgoDepth     = 3
	defaultEgoLimit = 100
	maxEgoLimit     = 300
)

// ego answers /api/ego?id=&depth=&limit=&kinds=&group=: the definitions
// within depth hops of id (either direction, most connected first), at
// most limit of them, with the edges among them. group=file nests every
// symbol in a compound node for its file.
func (s *Server) ego(ctx context.Context, d *data, r *http.Request, banner string) (any, error) {
	id, err := idParam(r)
	if err != nil {
		return nil, err
	}
	depth, err := intParam(r, "depth", defaultEgoDepth, maxEgoDepth)
	if err != nil {
		return nil, err
	}
	limit, err := intParam(r, "limit", defaultEgoLimit, maxEgoLimit)
	if err != nil {
		return nil, err
	}
	kinds, err := kindsParam(r)
	if err != nil {
		return nil, err
	}
	center, err := d.symByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Breadth-first walk; dist records the hop count of every kept node.
	dist := map[int64]int{id: 0}
	order := []int64{id}
	frontier := []int64{id}
	truncated := false
	for hop := 1; hop <= depth && len(frontier) > 0 && !truncated; hop++ {
		room := limit - len(order)
		nbs, err := d.neighbors(ctx, frontier, kinds, room+len(order)+1)
		if err != nil {
			return nil, err
		}
		var next []int64
		for _, nb := range nbs {
			if _, ok := dist[nb]; ok {
				continue
			}
			if len(order) >= limit {
				truncated = true
				break
			}
			dist[nb] = hop
			order = append(order, nb)
			next = append(next, nb)
		}
		frontier = next
	}

	syms, err := d.symsByID(ctx, order)
	if err != nil {
		return nil, err
	}
	syms[id] = center
	g := newGraph(d.max)
	g.truncated = truncated
	group := r.URL.Query().Get("group") == "file"
	for _, nid := range order {
		sym := syms[nid]
		if sym == nil {
			continue
		}
		parent := ""
		if group {
			parent = fileID(sym.FileID)
			if !g.has(parent) && !g.node(fileNode(sym.FileID, sym.Path, sym.Lang), "file") {
				break
			}
		}
		nd := symNode(sym, parent)
		nd.Depth = dist[nid]
		extra := []string{}
		if nid == id {
			extra = append(extra, "center")
		}
		if !g.node(nd, symClasses(sym, extra...)...) {
			break
		}
	}
	edges, err := d.edgesAmong(ctx, order, kinds)
	if err != nil {
		return nil, err
	}
	g.addSymEdges(edges)
	return EgoResponse{GraphResponse: g.response(banner), Center: symID(id)}, nil
}

// fileNode is the element data of a file compound node.
func fileNode(id int64, path, lang string) ElemData {
	return ElemData{ID: fileID(id), Label: path, Kind: "file", Ref: id, Path: path, Lang: lang}
}

// kindsParam parses the optional comma-separated edge kinds filter.
func kindsParam(r *http.Request) ([]model.EdgeKind, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("kinds"))
	if raw == "" {
		return nil, nil
	}
	var out []model.EdgeKind
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		k, err := model.ParseEdgeKind(part)
		if err != nil {
			return nil, badRequest("%v", err)
		}
		out = append(out, k)
	}
	return out, nil
}
