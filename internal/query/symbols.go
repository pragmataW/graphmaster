package query

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// querier bundles read access to one graph.db for the duration of a call.
type querier struct {
	st    *store.Store
	db    *sql.DB
	files *fileCache // shared by an engine's calls
}

// sym is a definition joined with its file, as rendered by the tools.
type sym struct {
	ID, FileID, ParentID int64
	Kind                 model.NodeKind
	Name, QName          string
	Path                 string
	Lang                 model.Lang
	Start, End           int
	Sig                  string
	Flags                model.DefFlags
	SKey                 []byte
}

// Exported reports whether the definition is visible outside its module.
func (s *sym) Exported() bool { return s.Flags.Has(model.FlagExported) }

// IsTest reports whether the definition is a test function or test type.
func (s *sym) IsTest() bool { return s.Kind == model.KindTest || s.Flags.Has(model.FlagTest) }

// row renders the canonical symbol row "<kind> <qname>  <path>:<s>-<e>  <sig>".
func (s *sym) row() string {
	r := s.short()
	if s.Sig != "" {
		r += "  " + s.Sig
	}
	return r
}

// short is row without the signature (trees, grouped lists).
func (s *sym) short() string {
	return fmt.Sprintf("%s %s  %s:%d-%d", s.displayKind(), s.displayQName(), s.Path, s.Start, s.End)
}

// displayKind labels test methods (JUnit @Test, xUnit [Fact], PHPUnit
// test*) "test" like test functions; they keep the method kind in the
// index for resolution.
func (s *sym) displayKind() model.NodeKind {
	if s.Kind == model.KindMethod && s.Flags.Has(model.FlagTest) {
		return model.KindTest
	}
	return s.Kind
}

// displayQName is the qualified name as rendered next to the symbol's path.
// Prefixes the path column already carries are dropped: a Go import path
// keeps only its last segment ("github.com/a/b/internal/steer.F" becomes
// "steer.F") and a module named after the file (TS/JS) is dropped
// ("ts/src/user.ts.User.role" becomes "User.role"). The result is a
// segment-boundary suffix of the stored qname (or a bare name), so it is
// accepted back as a symbol argument; stored qnames are unchanged.
func (s *sym) displayQName() string {
	q := s.QName
	if s.Path != "" && len(q) > len(s.Path)+1 && strings.HasPrefix(q, s.Path) && q[len(s.Path)] == '.' {
		return q[len(s.Path)+1:]
	}
	if s.Lang == model.LangGo {
		if i := strings.LastIndexByte(q, '/'); i >= 0 && i < len(q)-1 {
			return q[i+1:]
		}
	}
	return q
}

// skeyHex returns the symbol's stable handle.
func (s *sym) skeyHex() string { return hex.EncodeToString(s.SKey) }

// confMark is the "~" marker for edges below the trusted tier.
func confMark(conf int) string {
	if conf < 80 {
		return "~"
	}
	return ""
}

const symCols = `n.id, n.file_id, coalesce(n.parent_id, 0), n.kind, m.name, n.qname, f.path, f.lang,
	n.start_line, n.end_line, coalesce(n.sig, ''), n.flags, n.skey`

const symFrom = ` FROM nodes n JOIN names m ON m.id = n.name_id JOIN files f ON f.id = n.file_id`

func scanSym(rows *sql.Rows) (*sym, error) {
	var (
		s                 sym
		kind, lang, flags int
	)
	if err := rows.Scan(&s.ID, &s.FileID, &s.ParentID, &kind, &s.Name, &s.QName, &s.Path, &lang,
		&s.Start, &s.End, &s.Sig, &flags, &s.SKey); err != nil {
		return nil, err
	}
	s.Kind = model.NodeKind(uint8(kind))
	s.Lang = model.Lang(uint8(lang))
	s.Flags = model.DefFlags(uint16(flags))
	return &s, nil
}

// symsWhere loads definitions matching a SQL condition on n/f/m.
func (q *querier) symsWhere(ctx context.Context, cond string, args ...any) ([]*sym, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+symCols+symFrom+` WHERE `+cond, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sym
	for rows.Next() {
		s, err := scanSym(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Query-plan notes. Two planner hazards matter on large graphs:
//   - in recursive CTEs the walk table must drive the join (CROSS JOIN fixes
//     the order), otherwise SQLite may scan an edge index per step;
//   - "dst IS NOT NULL" alone qualifies the partial index edges_dst, which
//     SQLite then range-scans even when the query is keyed by src; writing
//     "+dst IS NOT NULL" keeps the filter but not the index choice.

// inChunk bounds the number of bound variables per IN list.
const inChunk = 500

// symsByID loads the given node ids (missing ids are skipped) keyed by id.
func (q *querier) symsByID(ctx context.Context, ids []int64) (map[int64]*sym, error) {
	out := make(map[int64]*sym, len(ids))
	err := forChunks(ids, func(chunk []int64) error {
		ss, err := q.symsWhere(ctx, `n.id IN (`+placeholders(len(chunk))+`)`, int64Args(chunk)...)
		for _, s := range ss {
			out[s.ID] = s
		}
		return err
	})
	return out, err
}

// symsInOrder loads ids and returns them in the given order.
func (q *querier) symsInOrder(ctx context.Context, ids []int64) ([]*sym, error) {
	m, err := q.symsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*sym, 0, len(ids))
	for _, id := range ids {
		if s := m[id]; s != nil {
			out = append(out, s)
		}
	}
	return out, nil
}

// symsByName returns every definition with exactly this name.
func (q *querier) symsByName(ctx context.Context, name string) ([]*sym, error) {
	return q.symsWhere(ctx, `n.name_id = (SELECT id FROM names WHERE name = ?)`, name)
}

// descendants returns id plus the ids of every definition nested in it.
func (q *querier) descendants(ctx context.Context, id int64) ([]int64, error) {
	return queryInt64s(ctx, q.db, `WITH RECURSIVE sub(id) AS (
		SELECT ? UNION SELECT n.id FROM sub CROSS JOIN nodes n ON n.parent_id = sub.id)
		SELECT id FROM sub`, id)
}

func forChunks(ids []int64, fn func([]int64) error) error {
	for i := 0; i < len(ids); i += inChunk {
		if err := fn(ids[i:min(i+inChunk, len(ids))]); err != nil {
			return err
		}
	}
	return nil
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.Repeat("?,", n-1) + "?"
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func edgeKindList(kinds []model.EdgeKind) string {
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprint(int(k))
	}
	return strings.Join(parts, ",")
}

func queryInt64s(ctx context.Context, db *sql.DB, q string, args ...any) ([]int64, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// sortByLocation orders symbols by path, then start line, then qname.
func sortByLocation(ss []*sym) {
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := ss[i], ss[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.QName < b.QName
	})
}
