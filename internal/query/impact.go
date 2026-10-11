package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// impactKinds are the dependency edges followed backwards by impact and
// tests_for: anything that breaks when its target changes.
var impactKinds = []model.EdgeKind{
	model.EdgeCalls, model.EdgeReferences, model.EdgeExtends, model.EdgeImplements,
	model.EdgeEmbeds, model.EdgeTests, model.EdgeDeclares,
}

// impactWalkLimit bounds the rows of one reverse closure.
const impactWalkLimit = 5000

// reach is a definition found by a reverse walk.
type reach struct {
	*sym
	depth int
	conf  int // best confidence of an edge into the reached set
}

// reverseClosure walks dependency edges backwards from seeds with a
// recursive CTE (UNION dedupe, depth column, LIMIT) and returns every
// definition reached that is not itself a seed, at its minimal depth.
func (q *querier) reverseClosure(ctx context.Context, seeds []int64, kinds []model.EdgeKind, depth, minConf int) (out []*reach, capped bool, err error) {
	seedJSON, err := json.Marshal(seeds)
	if err != nil {
		return nil, false, err
	}
	kl := edgeKindList(kinds)
	limit := len(seeds) + impactWalkLimit
	// Methods also step, at the same depth, to the interface and base
	// methods they implement (virtual dispatch, see dispatch.go): their
	// callers may reach the method. virt marks rows reached that way, disp
	// the dispatch steps themselves, whose other implementations (implements
	// edges into them) are not affected.
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`WITH RECURSIVE walk(id, depth, virt, disp) AS (
			SELECT value, 0, 0, 0 FROM json_each(?1)
			UNION
			SELECT e.src, w.depth + 1, w.virt, 0 FROM walk w CROSS JOIN edges e ON e.dst = w.id
			WHERE w.depth < ?2 AND e.kind IN (%s) AND e.conf >= ?3 AND NOT (w.disp = 1 AND e.kind = %d)%s
			LIMIT ?4
		) SELECT id, depth, virt, disp FROM walk`, kl, model.EdgeImplements,
		dispatchUpSQL(func(id string) string { return id + ", w.depth, 1, 1" })),
		string(seedJSON), depth, minConf, limit)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	isSeed := make(map[int64]bool, len(seeds))
	for _, id := range seeds {
		isSeed[id] = true
	}
	minDepth := map[int64]int{}
	direct := map[int64]bool{} // reached without virtual dispatch
	n := 0
	for rows.Next() {
		var id int64
		var d int
		var virt, disp bool
		if err := rows.Scan(&id, &d, &virt, &disp); err != nil {
			return nil, false, err
		}
		n++
		// Interface and base methods reached only as dispatch steps are
		// not affected themselves.
		if isSeed[id] || disp {
			continue
		}
		if old, ok := minDepth[id]; !ok || d < old {
			minDepth[id] = d
		}
		if !virt {
			direct[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	capped = n >= limit
	ids := make([]int64, 0, len(minDepth))
	for id := range minDepth {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	syms, err := q.symsByID(ctx, ids)
	if err != nil {
		return nil, false, err
	}
	conf, err := q.bestConf(ctx, ids, func(dst int64) bool { _, ok := minDepth[dst]; return ok || isSeed[dst] }, kl, minConf)
	if err != nil {
		return nil, false, err
	}
	for _, id := range ids {
		if s := syms[id]; s != nil {
			c := conf[id]
			if !direct[id] && (c == 0 || c > virtualConf) {
				c = virtualConf
			}
			out = append(out, &reach{sym: s, depth: minDepth[id], conf: c})
		}
	}
	return out, capped, nil
}

// bestConf returns, for each src, the highest confidence of its edges
// whose target satisfies inSet.
func (q *querier) bestConf(ctx context.Context, srcs []int64, inSet func(int64) bool, kl string, minConf int) (map[int64]int, error) {
	best := map[int64]int{}
	err := forChunks(srcs, func(chunk []int64) error {
		rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`SELECT src, dst, conf FROM edges
			WHERE src IN (%s) AND kind IN (%s) AND +dst IS NOT NULL AND conf >= ?`, placeholders(len(chunk)), kl),
			append(int64Args(chunk), minConf)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var src, dst int64
			var c int
			if err := rows.Scan(&src, &dst, &c); err != nil {
				return err
			}
			if inSet(dst) && c > best[src] {
				best[src] = c
			}
		}
		return rows.Err()
	})
	return best, err
}

// fileNodeIDs returns the definitions of the given files.
func (q *querier) fileNodeIDs(ctx context.Context, fileIDs []int64) ([]int64, error) {
	var out []int64
	err := forChunks(fileIDs, func(chunk []int64) error {
		ids, err := queryInt64s(ctx, q.db, `SELECT id FROM nodes WHERE file_id IN (`+placeholders(len(chunk))+`) ORDER BY id`, int64Args(chunk)...)
		out = append(out, ids...)
		return err
	})
	return out, err
}

// Impact lists what may break when a symbol, a file or the uncommitted
// changes are modified: the reverse transitive closure over dependency
// edges, grouped by file, with the number of affected tests.
func (e *Engine) Impact(ctx context.Context, in ImpactInput) (string, error) {
	return e.run(ctx, "impact", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		selectors := 0
		for _, set := range []bool{in.Symbol != "", in.Path != "", in.Changed} {
			if set {
				selectors++
			}
		}
		if selectors != 1 {
			return invalidf("pass exactly one of symbol, path or changed=true")
		}
		depth := orDefault(in.Depth, 3, 6)
		minConf := orDefault(in.MinConfidence, 30, 100)
		seeds, ok, err := e.impactSeeds(ctx, q, o, in)
		if err != nil || !ok {
			return err
		}
		if len(seeds) == 0 {
			o.line("no definitions to start from")
			return nil
		}
		affected, capped, err := q.reverseClosure(ctx, seeds, impactKinds, depth, minConf)
		if err != nil {
			return err
		}
		renderImpact(o, affected, capped, depth, minConf)
		return nil
	})
}

// impactSeeds resolves the starting definitions and writes the title line.
// ok is false when a message replaced the answer (unknown symbol, ...).
func (e *Engine) impactSeeds(ctx context.Context, q *querier, o *output, in ImpactInput) (seeds []int64, ok bool, err error) {
	switch {
	case in.Symbol != "":
		s, err := q.symbolArg(ctx, o, in.Symbol)
		if s == nil || err != nil {
			return nil, false, err
		}
		o.line("impact of " + s.short())
		seeds, err = q.descendants(ctx, s.ID)
		return seeds, true, err
	case in.Path != "":
		path := normPathPrefix(in.Path)
		f, err := q.st.FileByPath(ctx, path)
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, q.fileNotFound(ctx, o, path)
		}
		if err != nil {
			return nil, false, err
		}
		o.line("impact of " + f.Path)
		seeds, err = q.fileNodeIDs(ctx, []int64{f.ID})
		return seeds, true, err
	}
	if e.noGit {
		o.line("changed=true needs a git repository; pass symbol or path")
		return nil, false, nil
	}
	files, err := q.changedFiles(ctx, e.b.Root())
	if err != nil {
		return nil, false, err
	}
	if len(files) == 0 {
		o.line("no changed files (working tree matches the git index and HEAD)")
		return nil, false, nil
	}
	names := make([]string, 0, len(files))
	ids := make([]int64, 0, len(files))
	for _, f := range files {
		names = append(names, f.Path+" ("+f.Why+")")
		ids = append(ids, f.ID)
	}
	o.line(fmt.Sprintf("impact of %d changed files: %s", len(files), strings.Join(names, ", ")))
	seeds, err = q.fileNodeIDs(ctx, ids)
	return seeds, true, err
}

// renderImpact groups affected definitions by file (closest files first).
func renderImpact(o *output, affected []*reach, capped bool, depth, minConf int) {
	tests := 0
	byFile := map[string][]*reach{}
	fileDepth := map[string]int{}
	for _, r := range affected {
		if r.IsTest() {
			tests++
		}
		if d, ok := fileDepth[r.Path]; !ok || r.depth < d {
			fileDepth[r.Path] = r.depth
		}
		byFile[r.Path] = append(byFile[r.Path], r)
	}
	files := make([]string, 0, len(byFile))
	for p := range byFile {
		files = append(files, p)
	}
	sort.Slice(files, func(i, j int) bool {
		if fileDepth[files[i]] != fileDepth[files[j]] {
			return fileDepth[files[i]] < fileDepth[files[j]]
		}
		return files[i] < files[j]
	})
	more := ""
	if capped {
		more = " (walk limit reached)"
	}
	o.linef("depth %d, min_confidence %d: %d symbols in %d files · affected tests: %d%s",
		depth, minConf, len(affected), len(files), tests, more)
	shown := 0
	for _, p := range files {
		rs := byFile[p]
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].depth != rs[j].depth {
				return rs[i].depth < rs[j].depth
			}
			return rs[i].Start < rs[j].Start
		})
		if !o.line(p) {
			break
		}
		for _, r := range rs {
			if !o.linef("  %s%s %s  %d-%d  d%d", confMark(r.conf), r.Kind, r.displayQName(), r.Start, r.End, r.depth) {
				break
			}
			shown++
		}
		if o.full {
			break
		}
	}
	o.more(len(affected) - shown)
}
