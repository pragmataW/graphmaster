package ui

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/repo"
	"github.com/pragmataW/graphmaster/internal/store"
)

// data is read access to one graph.db for the duration of a request.
type data struct {
	st   *store.Store
	db   *sql.DB
	root *repo.Root
	max  int // element cap
}

const symCols = `n.id, n.file_id, coalesce(n.parent_id, 0), n.kind, m.name, n.qname, f.path, f.lang,
	n.start_line, n.end_line, coalesce(n.sig, ''), n.flags, n.skey`

const symFrom = ` FROM nodes n JOIN names m ON m.id = n.name_id JOIN files f ON f.id = n.file_id`

// symsWhere loads definitions matching a condition on n, m and f.
func (d *data) symsWhere(ctx context.Context, cond string, args ...any) ([]*Symbol, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+symCols+symFrom+` WHERE `+cond, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Symbol
	for rows.Next() {
		var (
			s                 Symbol
			kind, lang, flags int
			skey              []byte
		)
		if err := rows.Scan(&s.ID, &s.FileID, &s.ParentID, &kind, &s.Name, &s.QName, &s.Path, &lang,
			&s.Start, &s.End, &s.Sig, &flags, &skey); err != nil {
			return nil, err
		}
		k, fl := model.NodeKind(uint8(kind)), model.DefFlags(uint16(flags))
		s.Kind, s.Lang = k.String(), model.Lang(uint8(lang)).String()
		s.Exported = fl.Has(model.FlagExported)
		s.Test = k == model.KindTest || fl.Has(model.FlagTest)
		s.Decl = fl.Has(model.FlagDeclaration)
		s.SKey = hex.EncodeToString(skey)
		out = append(out, &s)
	}
	return out, rows.Err()
}

// symByID loads one definition or returns a 404 error.
func (d *data) symByID(ctx context.Context, id int64) (*Symbol, error) {
	ss, err := d.symsWhere(ctx, `n.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(ss) == 0 {
		return nil, notFound("no symbol with id %d", id)
	}
	return ss[0], nil
}

// symsByID loads the given ids (missing ones are skipped) keyed by id.
func (d *data) symsByID(ctx context.Context, ids []int64) (map[int64]*Symbol, error) {
	out := make(map[int64]*Symbol, len(ids))
	err := forChunks(ids, func(chunk []int64) error {
		ss, err := d.symsWhere(ctx, `n.id IN (`+placeholders(len(chunk))+`)`, int64Args(chunk)...)
		for _, s := range ss {
			out[s.ID] = s
		}
		return err
	})
	return out, err
}

// fileInfo loads a files row by path with its symbol count.
func (d *data) fileInfo(ctx context.Context, path string) (*FileInfo, error) {
	f, err := d.st.FileByPath(ctx, path)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("no indexed file %s", path)
	}
	if err != nil {
		return nil, err
	}
	info := &FileInfo{ID: f.ID, Path: f.Path, Lang: f.Lang.String(), LOC: f.LOC, Status: f.Status.String()}
	err = d.db.QueryRowContext(ctx, `SELECT count(*) FROM nodes WHERE file_id = ?`, f.ID).Scan(&info.Symbols)
	return info, err
}

// edgeAgg is the aggregate of the edges src -> dst of one kind.
type edgeAgg struct {
	src, dst    int64
	kind        model.EdgeKind
	conf, count int
}

// kindFilter is an SQL condition on e.kind; nil kinds means "all but
// contains" (containment is drawn as compound nesting).
func kindFilter(kinds []model.EdgeKind) string {
	if len(kinds) == 0 {
		return fmt.Sprintf("e.kind <> %d", model.EdgeContains)
	}
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprint(int(k))
	}
	return "e.kind IN (" + strings.Join(parts, ",") + ")"
}

// edgesAmong returns the aggregated edges whose both ends are in ids,
// strongest and most frequent first.
func (d *data) edgesAmong(ctx context.Context, ids []int64, kinds []model.EdgeKind) ([]edgeAgg, error) {
	in := make(map[int64]bool, len(ids))
	for _, id := range ids {
		in[id] = true
	}
	var out []edgeAgg
	err := forChunks(ids, func(chunk []int64) error {
		rows, err := d.db.QueryContext(ctx, `SELECT e.src, e.dst, e.kind, max(e.conf), count(*) FROM edges e
			WHERE e.src IN (`+placeholders(len(chunk))+`) AND +e.dst IS NOT NULL AND `+kindFilter(kinds)+`
			GROUP BY e.src, e.dst, e.kind`, int64Args(chunk)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e edgeAgg
			var kind int
			if err := rows.Scan(&e.src, &e.dst, &kind, &e.conf, &e.count); err != nil {
				return err
			}
			e.kind = model.EdgeKind(uint8(kind))
			if in[e.dst] {
				out = append(out, e)
			}
		}
		return rows.Err()
	})
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.conf != b.conf {
			return a.conf > b.conf
		}
		if a.count != b.count {
			return a.count > b.count
		}
		if a.src != b.src {
			return a.src < b.src
		}
		if a.dst != b.dst {
			return a.dst < b.dst
		}
		return a.kind < b.kind
	})
	return out, err
}

// neighbors returns the definitions adjacent to frontier in either
// direction, most connected first, at most limit of them.
func (d *data) neighbors(ctx context.Context, frontier []int64, kinds []model.EdgeKind, limit int) ([]int64, error) {
	weight := map[int64]int{}
	kf := kindFilter(kinds)
	err := forChunks(frontier, func(chunk []int64) error {
		ph := placeholders(len(chunk))
		args := append(int64Args(chunk), int64Args(chunk)...)
		args = append(args, limit)
		rows, err := d.db.QueryContext(ctx, `SELECT nb, count(*) c FROM (
				SELECT e.dst AS nb FROM edges e WHERE e.src IN (`+ph+`) AND +e.dst IS NOT NULL AND `+kf+`
				UNION ALL
				SELECT e.src FROM edges e WHERE e.dst IN (`+ph+`) AND `+kf+`
			) GROUP BY nb ORDER BY c DESC, nb LIMIT ?`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var c int
			if err := rows.Scan(&id, &c); err != nil {
				return err
			}
			weight[id] += c
		}
		return rows.Err()
	})
	out := make([]int64, 0, len(weight))
	for id := range weight {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if weight[out[i]] != weight[out[j]] {
			return weight[out[i]] > weight[out[j]]
		}
		return out[i] < out[j]
	})
	return out, err
}

// inChunk bounds the bound variables of one IN list.
const inChunk = 400

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
