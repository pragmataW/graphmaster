package resolve

import (
	"database/sql"
	"slices"

	"github.com/pragmataW/graphmaster/internal/model"
)

// groupKey identifies one reference: every edge row of a T5 fan-out shares
// it, so the rows of a group are replaced together.
type groupKey struct {
	file, src int64
	kind      model.EdgeKind
	nameID    int64
	qual      string
	line, col uint32
}

type edgeRow struct {
	id, dst int64
	conf    uint8
}

type group struct {
	key  groupKey
	rows []edgeRow // sorted by id
}

// groupSet keeps groups unique by key, in insertion order.
type groupSet struct {
	byKey map[groupKey]*group
	list  []*group
}

func newGroupSet() *groupSet { return &groupSet{byKey: map[groupKey]*group{}} }

func (gs *groupSet) has(k groupKey) bool { _, ok := gs.byKey[k]; return ok }

func (gs *groupSet) row(k groupKey, r edgeRow) {
	g, ok := gs.byKey[k]
	if !ok {
		g = &group{key: k}
		gs.byKey[k] = g
		gs.list = append(gs.list, g)
	}
	if !slices.ContainsFunc(g.rows, func(o edgeRow) bool { return o.id == r.id }) {
		g.rows = append(g.rows, r)
	}
}

func (gs *groupSet) add(gs2 ...*group) {
	for _, g := range gs2 {
		if !gs.has(g.key) {
			gs.byKey[g.key] = g
			gs.list = append(gs.list, g)
		}
	}
}

func (gs *groupSet) finish() {
	for _, g := range gs.list {
		slices.SortFunc(g.rows, func(a, b edgeRow) int { return cmpInt64(a.id, b.id) })
	}
}

// resolvable reports whether the resolver owns edges of kind k. Contains
// edges are structural and imports/includes live in the imports table.
// Declares edges (src = a model.FlagDeclaration definition, name = its
// name) resolve through DeclarationResolver.
func resolvable(k model.EdgeKind) bool {
	switch k {
	case model.EdgeCalls, model.EdgeReferences, model.EdgeTests, model.EdgeDeclares,
		model.EdgeExtends, model.EdgeImplements, model.EdgeEmbeds:
		return true
	}
	return false
}

const edgeCols = `e.id, e.file_id, e.src, coalesce(e.dst, 0), e.kind, e.name_id, coalesce(e.qual, ''),
	e.line, e.col, e.conf`

func scanEdgeInto(gs *groupSet) func(*sql.Rows) error {
	return func(r *sql.Rows) error {
		var (
			k    groupKey
			row  edgeRow
			kind int
			conf int
		)
		if err := r.Scan(&row.id, &k.file, &k.src, &row.dst, &kind, &k.nameID, &k.qual, &k.line, &k.col, &conf); err != nil {
			return err
		}
		k.kind = model.EdgeKind(uint8(kind))
		row.conf = uint8(conf)
		if resolvable(k.kind) {
			gs.row(k, row)
		}
		return nil
	}
}

// loadGroupsByName loads every edge carrying one of the names. Resolved
// edges always point at a definition of their own name, so the two indexed
// paths (unresolved/low-confidence by name, high-confidence by target name)
// together cover all of them.
func loadGroupsByName(s *Session, ids []int64) (*groupSet, error) {
	gs := newGroupSet()
	for _, id := range ids {
		if err := s.rows(`SELECT `+edgeCols+` FROM edges e
			WHERE e.name_id = ?1 AND (e.dst IS NULL OR e.conf < 80)`, scanEdgeInto(gs), id); err != nil {
			return nil, err
		}
		if err := s.rows(`SELECT `+edgeCols+` FROM nodes n JOIN edges e ON e.dst = n.id
			WHERE n.name_id = ?1 AND e.name_id = ?1`, scanEdgeInto(gs), id); err != nil {
			return nil, err
		}
	}
	gs.finish()
	return gs, nil
}

// dependentGroups returns receiver-shaped references (unqualified calls and
// references at T3 or below) in files that reference a pending name,
// excluding groups already loaded. Receiver typing reads definitions other
// than the target (variable types, function results, fields), so adding,
// changing or deleting such a definition may change how these references
// resolve; every file mentioning the definition's name is re-checked.
// Names above the common-name limit are skipped, and when more than
// Options.MaxDependentFiles files qualify the dependents of this batch are
// skipped altogether (skipped = true) to bound incremental work.
func dependentGroups(s *Session, ids []int64, have *groupSet) (out *groupSet, skipped bool, err error) {
	limit := s.opts.commonLimit()
	files := map[int64]bool{}
	for _, id := range ids {
		if len(s.defsByNameID(id)) > limit || s.refCount(id) > limit {
			continue
		}
		collect := func(r *sql.Rows) error {
			var f int64
			if err := r.Scan(&f); err != nil {
				return err
			}
			files[f] = true
			return nil
		}
		if err := s.rows(`SELECT DISTINCT file_id FROM edges WHERE name_id = ?1 AND (dst IS NULL OR conf < 80)`, collect, id); err != nil {
			return nil, false, err
		}
		if err := s.rows(`SELECT DISTINCT e.file_id FROM nodes n JOIN edges e ON e.dst = n.id
			WHERE n.name_id = ?1 AND e.name_id = ?1`, collect, id); err != nil {
			return nil, false, err
		}
	}
	if len(files) > s.opts.maxDependentFiles() {
		return newGroupSet(), true, nil
	}
	fids := make([]int64, 0, len(files))
	for f := range files {
		fids = append(fids, f)
	}
	slices.Sort(fids)

	all := newGroupSet()
	for _, f := range fids {
		if err := s.rows(`SELECT `+edgeCols+` FROM edges e
			WHERE e.file_id = ? AND e.name_id IS NOT NULL AND coalesce(e.qual, '') = ''
			AND e.kind IN (?, ?) AND (e.dst IS NULL OR e.conf <= 80)`, scanEdgeInto(all),
			f, int(model.EdgeCalls), int(model.EdgeReferences)); err != nil {
			return nil, false, err
		}
	}
	// A dependent group must carry all of its rows: rows of a T5 fan-out
	// are all < 80, and a T3 row is a single row, so the file query above
	// already returned complete groups.
	all.finish()
	out = newGroupSet()
	for _, g := range all.list {
		if !have.has(g.key) {
			out.add(g)
		}
	}
	return out, false, nil
}
