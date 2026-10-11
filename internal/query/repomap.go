package query

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
)

// dirNode aggregates the files below one directory.
type dirNode struct {
	name     string
	path     string // repo-relative, no trailing slash ("" = root)
	files    int
	loc      int
	children map[string]*dirNode
	direct   []int64 // ids of non-test files directly in this directory
	subtree  []int64 // ids of non-test files in the subtree
}

// topSampleFiles bounds how many files feed a directory's top symbols.
const topSampleFiles = 40

// topSymbolCount is the number of top symbols shown per directory.
const topSymbolCount = 3

func (d *dirNode) child(name string) *dirNode {
	if d.children == nil {
		d.children = map[string]*dirNode{}
	}
	c := d.children[name]
	if c == nil {
		p := name
		if d.path != "" {
			p = d.path + "/" + name
		}
		c = &dirNode{name: name, path: p}
		d.children[name] = c
	}
	return c
}

func (d *dirNode) sortedChildren() []*dirNode {
	out := make([]*dirNode, 0, len(d.children))
	for _, c := range d.children {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// RepoMap renders the directory / package tree below path_prefix with file
// counts, lines of code and the three most referenced exported top-level
// definitions of each directory (tests excluded). A directory whose
// subdirectories are listed shows only the symbols of its own files, so the
// same names are not repeated on every ancestor row.
func (e *Engine) RepoMap(ctx context.Context, in RepoMapInput) (string, error) {
	return e.run(ctx, "repo_map", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		prefix := strings.TrimSuffix(normPathPrefix(in.PathPrefix), "/")
		depth := orDefault(in.Depth, 2, 10)
		root, err := q.loadDirTree(ctx, prefix, depth)
		if err != nil {
			return err
		}
		if root.files == 0 {
			if prefix == "" {
				o.line("no indexed files (run `graphmaster index`)")
			} else {
				o.linef("no indexed files under %s/", prefix)
			}
			return nil
		}
		pkgs, err := q.packageDirs(ctx)
		if err != nil {
			return err
		}
		label := prefix + "/"
		if prefix == "" {
			label = "./"
		}
		rows := []*dirRow{{node: root, label: label}}
		var walk func(d *dirNode, level int)
		walk = func(d *dirNode, level int) {
			if level > depth {
				return
			}
			for _, c := range d.sortedChildren() {
				rows = append(rows, &dirRow{node: c, label: strings.Repeat("  ", level) + c.name + "/"})
				walk(c, level+1)
			}
		}
		walk(root, 1)

		o.list(len(rows), func(i int) string {
			r := rows[i]
			line := fmt.Sprintf("%s  %s, %d loc", r.label, plural(r.node.files, "file"), r.node.loc)
			if p := pkgs[r.node.path]; p != "" {
				line += "  pkg " + p
			}
			top, err := q.topSymbols(ctx, r.node, len(r.node.children) > 0)
			if err == nil && len(top) > 0 {
				line += "  · " + strings.Join(top, ", ")
			}
			return line
		}, 0)
		return nil
	})
}

type dirRow struct {
	node  *dirNode
	label string
}

// loadDirTree aggregates the files rows below prefix into a directory tree
// that keeps per-directory detail only down to depth levels.
func (q *querier) loadDirTree(ctx context.Context, prefix string, depth int) (*dirNode, error) {
	all, err := q.files.load(ctx, q)
	if err != nil {
		return nil, err
	}
	lo := ""
	if prefix != "" {
		lo = prefix + "/"
	}
	root := &dirNode{path: prefix}
	for _, f := range under(all, lo) {
		parts := strings.Split(f.path[len(lo):], "/")
		dirs := parts[:len(parts)-1]
		// Test files never contribute top symbols.
		source := f.indexed && !parse.IsTestPath(f.path)
		node := root
		node.add(f.id, f.loc, source, len(dirs) == 0)
		for i, d := range dirs {
			if i >= depth {
				break
			}
			node = node.child(d)
			node.add(f.id, f.loc, source, i == len(dirs)-1)
		}
	}
	return root, nil
}

func (d *dirNode) add(id int64, loc int, source, direct bool) {
	d.files++
	d.loc += loc
	if !source {
		return
	}
	if direct {
		d.direct = append(d.direct, id)
	}
	d.subtree = append(d.subtree, id)
}

// spread picks at most n ids evenly across ids (sorted by path), so a
// sample covers the whole subtree rather than its first directories.
func spread(ids []int64, n int) []int64 {
	if len(ids) <= n {
		return ids
	}
	out := make([]int64, n)
	for i := range out {
		out[i] = ids[i*len(ids)/n]
	}
	return out
}

// packageDirs maps a directory to the label of the package declared there
// (see pkgLabel). Directories holding several packages (e.g. one module
// per JS file) map to "".
func (q *querier) packageDirs(ctx context.Context) (map[string]string, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT coalesce(dir, ''), name, lang FROM packages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var dir, name string
		var lang int
		if err := rows.Scan(&dir, &name, &lang); err != nil {
			return nil, err
		}
		if _, dup := out[dir]; dup {
			out[dir] = ""
			continue
		}
		out[dir] = pkgLabel(dir, name, model.Lang(uint8(lang)))
	}
	return out, rows.Err()
}

// pkgLabel shortens a package name for a directory line the way
// displayQName shortens qualified names, and drops it when the line
// already says it: a Go import path keeps its last segment; a module named
// after the directory or one of its files (TS/JS, C), or whose last
// segment is the directory's name (Go import paths, Java and Python
// dotted packages), is not shown.
func pkgLabel(dir, name string, lang model.Lang) string {
	if name == "" || name == dir || strings.HasPrefix(name, dir+"/") {
		return ""
	}
	if lang == model.LangGo {
		if i := strings.LastIndexByte(name, '/'); i >= 0 && i < len(name)-1 {
			name = name[i+1:]
		}
	}
	last := name[strings.LastIndexAny(name, `/.\:`)+1:]
	if last == path.Base(dir) {
		return ""
	}
	return name
}

// topSymbols returns up to three exported top-level non-test definitions
// of a directory, highest fan-in first: the number of distinct non-test
// definitions that call, reference or extend them. Files directly in the
// directory are used; without such files, an expanded directory (its
// subdirectories are listed) shows nothing and a collapsed one samples its
// subtree. Unreferenced definitions are shown only when none is referenced.
func (q *querier) topSymbols(ctx context.Context, d *dirNode, expanded bool) ([]string, error) {
	ids := d.direct
	if len(ids) == 0 && !expanded {
		ids = d.subtree
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ids = spread(ids, topSampleFiles)
	notTest := func(n string) string {
		return fmt.Sprintf(`%[1]s.kind <> %[2]d AND %[1]s.flags & %[3]d = 0`, n, model.KindTest, model.FlagTest)
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`SELECT m.name,
			(SELECT count(DISTINCT e.src) FROM edges e JOIN nodes s ON s.id = e.src
				WHERE e.dst = n.id AND e.kind IN (%s) AND %s) AS refs
		FROM nodes n JOIN names m ON m.id = n.name_id
		WHERE n.file_id IN (%s) AND n.parent_id IS NULL AND n.flags & %d <> 0 AND %s
		ORDER BY refs DESC, m.name LIMIT %d`,
		edgeKindList(fanInEdges), notTest("s"),
		placeholders(len(ids)), model.FlagExported, notTest("n"), topSymbolCount), int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		var refs int
		if err := rows.Scan(&name, &refs); err != nil {
			return nil, err
		}
		if refs == 0 && len(out) > 0 {
			break // referenced definitions only, once there is one
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// fanInEdges are the edge kinds that count as uses for top symbols.
var fanInEdges = []model.EdgeKind{model.EdgeCalls, model.EdgeReferences,
	model.EdgeExtends, model.EdgeImplements, model.EdgeEmbeds}

// plural renders "1 file" / "3 files".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
