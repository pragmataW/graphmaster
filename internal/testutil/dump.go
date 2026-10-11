package testutil

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// This file is the single place that knows how graph.db maps onto the
// canonical dump. If the store schema changes, adjust dumpTables and the
// key-building queries below; tests elsewhere only compare dump strings.

// colRender says how one column is rendered.
type colRender int

const (
	colPlain    colRender = iota
	colFile               // files.id      -> path
	colNode               // nodes.id      -> node key
	colName               // names.id      -> identifier
	colPkg                // packages.id   -> "lang:name"
	colLang               // model.Lang
	colNodeKind           // model.NodeKind
	colEdgeKind           // model.EdgeKind
	colStatus             // model.FileStatus
	colFlags              // model.DefFlags
)

// dumpTable describes how to dump one table.
type dumpTable struct {
	name string
	// skip lists row ids and volatile columns (stat data, timestamps).
	skip []string
	// cols maps columns to renderers; unlisted columns render as plain values.
	cols map[string]colRender
	// head builds the line prefix from rendered column values.
	head func(v map[string]string) string
}

// dumpTables lists the dumped tables in output order. Caches (dirs,
// parse cache), session/steering state, interned names (which may keep
// orphans) and the contentless FTS tables are deliberately left out.
var dumpTables = []dumpTable{
	{
		name: "packages",
		skip: []string{"id"},
		cols: map[string]colRender{"lang": colLang},
		head: func(v map[string]string) string { return v["lang"] + ":" + v["name"] },
	},
	{
		name: "files",
		skip: []string{"id", "mtime_ns", "ino", "mode"},
		cols: map[string]colRender{"lang": colLang, "status": colStatus, "pkg_id": colPkg},
		head: func(v map[string]string) string { return v["path"] },
	},
	{
		name: "nodes",
		skip: []string{"id"},
		cols: map[string]colRender{
			"file_id": colFile, "parent_id": colNode, "kind": colNodeKind,
			"name_id": colName, "flags": colFlags,
		},
		head: func(v map[string]string) string {
			return nodeKey(v["file"], v["qname"], v["kind"], v["start_line"])
		},
	},
	{
		name: "edges",
		skip: []string{"id"},
		cols: map[string]colRender{
			"file_id": colFile, "src": colNode, "dst": colNode,
			"kind": colEdgeKind, "name_id": colName,
		},
		head: func(v map[string]string) string { return v["kind"] + " " + v["src"] + " -> " + v["dst"] },
	},
	{
		name: "imports",
		skip: []string{"id"},
		cols: map[string]colRender{"file_id": colFile, "target_file": colFile, "target_pkg": colPkg},
		head: func(v map[string]string) string { return v["file"] },
	},
	{
		name: "pending_names",
		cols: map[string]colRender{"name_id": colName},
		head: func(v map[string]string) string { return v["name"] },
	},
}

// metaKeys selects the stable meta entries; progress and scan timestamps
// are volatile and excluded.
func metaKey(k string) bool {
	return k == "schema_ver" || k == "state" || strings.HasPrefix(k, "extractor_ver:")
}

// nodeKey is the stable identity of a node: path, qualified name, kind and
// start line (the line disambiguates overloads and redeclarations).
func nodeKey(path, qname, kind, line string) string {
	return path + "::" + qname + "#" + kind + ":" + line
}

// CanonicalDump renders the graph stored in db as sorted text that does not
// depend on row ids or stat data, so two databases describing the same
// graph produce identical dumps. It panics on query errors; use
// CanonicalDumpE to handle them.
func CanonicalDump(db *sql.DB) string {
	s, err := CanonicalDumpE(db)
	if err != nil {
		panic("testutil.CanonicalDump: " + err.Error())
	}
	return s
}

// CanonicalDumpE is CanonicalDump returning errors instead of panicking.
// Missing tables are reported as "(absent)" sections rather than errors.
func CanonicalDumpE(db *sql.DB) (string, error) {
	d := &dumper{db: db}
	if err := d.loadKeys(); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := d.dumpMeta(&b); err != nil {
		return "", err
	}
	for _, t := range dumpTables {
		if err := d.dumpTable(&b, t); err != nil {
			return "", fmt.Errorf("dump %s: %w", t.name, err)
		}
	}
	return b.String(), nil
}

type dumper struct {
	db     *sql.DB
	tables map[string]bool
	files  map[int64]string
	nodes  map[int64]string
	names  map[int64]string
	pkgs   map[int64]string
}

// loadKeys builds id -> stable key maps for every referenced table.
func (d *dumper) loadKeys() error {
	d.tables = map[string]bool{}
	rows, err := d.db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table','view')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		d.tables[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	load := func(table, q string, scan func(id int64, rest []any) string, n int) (map[int64]string, error) {
		m := map[int64]string{}
		if !d.tables[table] {
			return m, nil
		}
		rows, err := d.db.Query(q)
		if err != nil {
			return nil, fmt.Errorf("%s keys: %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			rest := make([]any, n)
			ptrs := []any{&id}
			for i := range rest {
				ptrs = append(ptrs, &rest[i])
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			m[id] = scan(id, rest)
		}
		return m, rows.Err()
	}

	if d.files, err = load("files", `SELECT id, path FROM files`,
		func(_ int64, r []any) string { return plain(r[0]) }, 1); err != nil {
		return err
	}
	if d.names, err = load("names", `SELECT id, name FROM names`,
		func(_ int64, r []any) string { return plain(r[0]) }, 1); err != nil {
		return err
	}
	if d.pkgs, err = load("packages", `SELECT id, lang, name FROM packages`,
		func(_ int64, r []any) string { return enumValue(r[0], colLang) + ":" + plain(r[1]) }, 2); err != nil {
		return err
	}
	d.nodes, err = load("nodes", `SELECT id, file_id, qname, kind, start_line FROM nodes`,
		func(_ int64, r []any) string {
			return nodeKey(d.ref(d.files, r[0]), plain(r[1]), enumValue(r[2], colNodeKind), plain(r[3]))
		}, 4)
	return err
}

func (d *dumper) dumpMeta(b *strings.Builder) error {
	if !d.tables["meta"] {
		b.WriteString("== meta (absent)\n")
		return nil
	}
	rows, err := d.db.Query(`SELECT key, value FROM meta`)
	if err != nil {
		return fmt.Errorf("dump meta: %w", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var k string
		var v any
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if metaKey(k) {
			lines = append(lines, k+"="+quoteIfNeeded(plain(v)))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeSection(b, "meta", lines)
	return nil
}

func (d *dumper) dumpTable(b *strings.Builder, t dumpTable) error {
	if !d.tables[t.name] {
		fmt.Fprintf(b, "== %s (absent)\n", t.name)
		return nil
	}
	rows, err := d.db.Query(`SELECT * FROM ` + t.name)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	var lines []string
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		vals := map[string]string{}
		for i, c := range cols {
			if slices.Contains(t.skip, c) {
				continue
			}
			vals[strings.TrimSuffix(c, "_id")] = d.render(raw[i], t.cols[c])
		}
		lines = append(lines, formatLine(t.head(vals), vals))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeSection(b, t.name, lines)
	return nil
}

// formatLine renders "head | k=v k=v ..." with keys sorted.
func formatLine(head string, vals map[string]string) string {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(" |")
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(quoteIfNeeded(vals[k]))
	}
	return b.String()
}

func writeSection(b *strings.Builder, name string, lines []string) {
	slices.Sort(lines)
	fmt.Fprintf(b, "== %s (%d)\n", name, len(lines))
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
}

// render converts one raw column value according to r.
func (d *dumper) render(v any, r colRender) string {
	switch r {
	case colFile:
		return d.ref(d.files, v)
	case colNode:
		return d.ref(d.nodes, v)
	case colName:
		return d.ref(d.names, v)
	case colPkg:
		return d.ref(d.pkgs, v)
	case colLang, colNodeKind, colEdgeKind, colStatus, colFlags:
		return enumValue(v, r)
	}
	return plain(v)
}

// ref resolves a foreign key through m. NULL stays "NULL"; a dangling id is
// rendered with its raw value so the (buggy) state is visible in diffs.
func (d *dumper) ref(m map[int64]string, v any) string {
	id, ok := v.(int64)
	if !ok {
		return plain(v)
	}
	if k, ok := m[id]; ok {
		return k
	}
	return fmt.Sprintf("!dangling(%d)", id)
}

// enumValue renders integer enum columns by name; text values pass through.
func enumValue(v any, r colRender) string {
	n, ok := v.(int64)
	if !ok {
		return plain(v)
	}
	switch r {
	case colLang:
		return model.Lang(n).String()
	case colNodeKind:
		return model.NodeKind(n).String()
	case colEdgeKind:
		return model.EdgeKind(n).String()
	case colStatus:
		return model.FileStatus(n).String()
	case colFlags:
		if n == 0 {
			return "0"
		}
		return model.DefFlags(n).String()
	}
	return strconv.FormatInt(n, 10)
}

// plain renders a scalar SQLite value.
func plain(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case []byte:
		return "x" + hex.EncodeToString(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func quoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\r\n|=\"\\") {
		return strconv.Quote(s)
	}
	return s
}
