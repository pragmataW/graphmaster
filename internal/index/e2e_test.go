package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/resolve"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// End-to-end tests: the real indexer with the real resolver
// (internal/resolve) on the materialized fixture repository.

func realResolver() Resolver { return &resolve.Resolver{} }

// e2eEdge is a stored edge with both endpoints rendered as "path::Chain".
type e2eEdge struct {
	kind     model.EdgeKind
	src, dst string // dst "" when unresolved
	name     string
	conf     int
}

// e2eGraph is the rendered graph of an index, for expectation checks.
type e2eGraph struct {
	edges   []e2eEdge
	imports map[string][]string // importing file -> target files and package dirs
}

// loadE2EGraph renders every non-contains edge and every linked import.
func loadE2EGraph(t *testing.T, db *sql.DB) *e2eGraph {
	t.Helper()
	keys := nodeChains(t, db)
	g := &e2eGraph{imports: map[string][]string{}}
	rows, err := db.Query(`SELECT e.kind, e.src, coalesce(e.dst, 0), coalesce(m.name, ''), e.conf
		FROM edges e LEFT JOIN names m ON m.id = e.name_id WHERE e.kind <> ?`, int(model.EdgeContains))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind, conf int
		var src, dst int64
		var name string
		if err := rows.Scan(&kind, &src, &dst, &name, &conf); err != nil {
			t.Fatal(err)
		}
		e := e2eEdge{kind: model.EdgeKind(kind), src: keys[src], name: name, conf: conf}
		if dst != 0 {
			e.dst = keys[dst]
		}
		g.edges = append(g.edges, e)
	}
	rows.Close()
	rows, err = db.Query(`SELECT f.path, coalesce(tf.path, ''), coalesce(tp.dir, '')
		FROM imports i JOIN files f ON f.id = i.file_id
		LEFT JOIN files tf ON tf.id = i.target_file LEFT JOIN packages tp ON tp.id = i.target_pkg`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var from, file, dir string
		if err := rows.Scan(&from, &file, &dir); err != nil {
			t.Fatal(err)
		}
		for _, to := range []string{file, dir} {
			if to != "" {
				g.imports[from] = append(g.imports[from], to)
			}
		}
	}
	rows.Close()
	return g
}

// nodeChains maps node ids to "path::Chain": the qname without the file's
// module identity (packages.name), with "::" separators normalized to ".".
// Languages whose QNamer leaves the module out (C, C++, C#) keep the whole
// qname as the chain.
func nodeChains(t *testing.T, db *sql.DB) map[int64]string {
	t.Helper()
	rows, err := db.Query(`SELECT n.id, f.path, n.qname, coalesce(p.name, '')
		FROM nodes n JOIN files f ON f.id = n.file_id LEFT JOIN packages p ON p.id = f.pkg_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var path, qname, module string
		if err := rows.Scan(&id, &path, &qname, &module); err != nil {
			t.Fatal(err)
		}
		chain := qname
		if module != "" {
			for _, sep := range []string{".", "::"} {
				if rest, ok := strings.CutPrefix(qname, module+sep); ok {
					chain = rest
					break
				}
			}
		}
		out[id] = path + "::" + strings.ReplaceAll(chain, "::", ".")
	}
	return out
}

func (g *e2eGraph) from(src string) string {
	var b strings.Builder
	for _, e := range g.edges {
		if e.src == src {
			fmt.Fprintf(&b, "\n  %v %s -> %q conf=%d", e.kind, e.name, e.dst, e.conf)
		}
	}
	return b.String()
}

// check verifies one expectation; it returns "" when it holds and a
// description of the mismatch otherwise.
func (g *e2eGraph) check(x testutil.ExpectedEdge) string {
	switch x.Kind {
	case model.EdgeImports, model.EdgeIncludes:
		path, _, _ := strings.Cut(x.Src, "::")
		has := slices.Contains(g.imports[path], x.Dst)
		switch {
		case x.Negative && has:
			return "unexpected import link"
		case !x.Negative && !has:
			return fmt.Sprintf("missing import link; %s links to %v", path, g.imports[path])
		}
		return ""
	}
	if name, ok := x.External(); ok {
		found := false
		for _, e := range g.edges {
			if e.src != x.Src || e.name != name {
				continue
			}
			found = true
			if e.dst != "" {
				return fmt.Sprintf("external reference resolved to %s (conf %d)", e.dst, e.conf)
			}
		}
		if !found {
			return fmt.Sprintf("no reference named %q; edges from src:%s", name, g.from(x.Src))
		}
		return ""
	}
	var match []e2eEdge
	for _, e := range g.edges {
		if e.kind == x.Kind && e.src == x.Src && e.dst == x.Dst {
			match = append(match, e)
		}
	}
	switch {
	case x.Negative:
		if len(match) > 0 {
			return fmt.Sprintf("unexpected edge (conf %d)", match[0].conf)
		}
	case len(match) == 0:
		return "missing edge; edges from src:" + g.from(x.Src)
	case match[0].conf < x.MinConf:
		return fmt.Sprintf("conf %d", match[0].conf)
	}
	return ""
}

// TestE2EFixtureExpectedEdges indexes the fixture with the real resolver
// and checks every section of expected_edges.txt against the stored graph.
func TestE2EFixtureExpectedEdges(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Resolver: realResolver()})
	res := freshen(t, ix)
	if !res.FullBuild || res.ParseErrors != 0 {
		t.Fatalf("full build: %+v", *res)
	}
	exps, err := testutil.LoadExpectedEdges("")
	if err != nil {
		t.Fatal(err)
	}
	g := loadE2EGraph(t, ix.Store().DB())
	perLang := map[string][2]int{}
	for _, x := range exps {
		c := perLang[x.Section]
		c[0]++
		if msg := g.check(x); msg != "" {
			if x.Optional {
				t.Logf("[%s] line %d: optional %v: %s", x.Section, x.Line, x, msg)
			} else {
				c[1]++
				t.Errorf("[%s] line %d: %v: %s", x.Section, x.Line, x, msg)
			}
		}
		perLang[x.Section] = c
	}
	for _, lang := range []string{"go", "python", "typescript", "javascript", "rust", "java", "c", "cpp", "csharp", "php"} {
		c := perLang[lang]
		if c[0] == 0 {
			t.Errorf("[%s]: no expectations", lang)
		}
		t.Logf("[%s] %d expectations, %d failed", lang, c[0], c[1])
	}
	requireLanguagesResolved(t, ix.Store().DB())
}

// requireLanguagesResolved checks that every fixture language produced
// definitions and at least one resolved non-contains edge.
func requireLanguagesResolved(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT f.lang, count(DISTINCT n.id),
		(SELECT count(*) FROM edges e JOIN files ef ON ef.id = e.file_id
		  WHERE ef.lang = f.lang AND e.dst IS NOT NULL AND e.kind <> ?)
		FROM nodes n JOIN files f ON f.id = n.file_id GROUP BY f.lang`, int(model.EdgeContains))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[model.Lang]bool{}
	for rows.Next() {
		var lang model.Lang
		var nodes, resolved int
		if err := rows.Scan(&lang, &nodes, &resolved); err != nil {
			t.Fatal(err)
		}
		t.Logf("%-10v nodes=%d resolved edges=%d", lang, nodes, resolved)
		if nodes > 0 && resolved > 0 {
			seen[lang] = true
		}
	}
	for _, l := range []model.Lang{model.LangGo, model.LangPython, model.LangTypeScript, model.LangTSX,
		model.LangJavaScript, model.LangRust, model.LangJava, model.LangC, model.LangCPP,
		model.LangCSharp, model.LangPHP} {
		if !seen[l] {
			t.Errorf("%v: no definitions or no resolved edges", l)
		}
	}
}

// TestE2EIncrementalEqualsFull runs the equivalence oracle (random edits,
// renames, deletes, branch switches, ignore and staging changes) with the
// real resolver: after every step the incrementally maintained graph must
// equal a from-scratch build.
func TestE2EIncrementalEqualsFull(t *testing.T) {
	steps := 25
	if testing.Short() {
		steps = 6
	}
	seed := uint64(7)
	if v, err := strconv.ParseUint(os.Getenv("GRAPHMASTER_ORACLE_SEED"), 10, 64); err == nil {
		seed = v
	}
	runOracle(t, seed, steps, realResolver)
}

// TestE2EResolveIdempotent checks that a no-change Freshen keeps the
// resolved graph byte-identical and leaves nothing queued.
func TestE2EResolveIdempotent(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Resolver: realResolver(), Now: futureClock})
	freshen(t, ix)
	before := dump(t, ix)
	if res := freshen(t, ix); !res.Clean() {
		t.Fatalf("second Freshen not clean: %+v", *res)
	}
	requireSameDump(t, "no-change", dump(t, ix), before)
	var pending int
	if err := ix.Store().DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM pending_names`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending names after resolve: %d, %v", pending, err)
	}
}

// TestE2EImportRetargeting covers references whose resolution depends on
// another file's imports (a TypeScript barrel re-export): deleting or
// restoring the barrel must re-resolve the importers, as a full build would.
func TestE2EImportRetargeting(t *testing.T) {
	dir := testutil.Materialize(t)
	writeFile(t, dir, "ts/src/bar/base.ts", "export class Base {\n  run(): number { return 1; }\n}\nexport function helper(): number { return 2; }\n")
	writeFile(t, dir, "ts/src/bar/index.ts", "export * from './base';\n")
	writeFile(t, dir, "ts/src/bar/child.ts", "import { Base, helper } from './index';\n\nexport class Child extends Base {\n  go(): number { return helper(); }\n}\n")
	ix := openIx(t, dir, Options{Resolver: realResolver()})
	freshen(t, ix)
	requireSameDump(t, "initial", dump(t, ix), scratchDump(t, dir, Options{Resolver: realResolver()}))
	g := loadE2EGraph(t, ix.Store().DB())
	for _, line := range []string{
		"extends ts/src/bar/child.ts::Child -> ts/src/bar/base.ts::Base conf>=100",
		"calls ts/src/bar/child.ts::Child.go -> ts/src/bar/base.ts::helper conf>=100",
	} {
		x, err := testutil.ParseExpectedEdge(line)
		if err != nil {
			t.Fatal(err)
		}
		if msg := g.check(x); msg != "" {
			t.Errorf("%v: %s", x, msg)
		}
	}

	steps := []struct {
		name string
		do   func()
	}{
		{"delete barrel", func() { removeFile(t, dir, "ts/src/bar/index.ts") }},
		{"restore barrel", func() { writeFile(t, dir, "ts/src/bar/index.ts", "export * from './base';\n") }},
		{"barrel drops re-export", func() { writeFile(t, dir, "ts/src/bar/index.ts", "export const unrelated = 1;\n") }},
		{"barrel re-exports again", func() {
			writeFile(t, dir, "ts/src/bar/index.ts", "export { helper } from './base';\nexport * from './base';\n")
		}},
		{"move definitions", func() {
			removeFile(t, dir, "ts/src/bar/base.ts")
			writeFile(t, dir, "ts/src/bar/base2.ts", "export class Base {}\nexport function helper(): number { return 3; }\n")
			writeFile(t, dir, "ts/src/bar/index.ts", "export * from './base2';\n")
		}},
	}
	for _, st := range steps {
		st.do()
		freshen(t, ix)
		requireSameDump(t, st.name, dump(t, ix), scratchDump(t, dir, Options{Resolver: realResolver()}))
	}
}

func removeFile(t testing.TB, dir, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// TestE2ECrossLanguageEdits removes and restores definitions in one central
// file per language (cutting it in half) and requires the incremental
// graph to equal a full build after every step, with the real resolver.
func TestE2ECrossLanguageEdits(t *testing.T) {
	files := []string{
		"python/shop/models.py",
		"ts/src/models/user.ts",
		"js/lib/math.js",
		"rust/src/geometry.rs",
		"java/src/main/java/com/example/zoo/Animal.java",
		"c/include/vec.h",
		"cpp/src/canvas.cpp",
		"csharp/src/Billing/Invoice.Tax.cs",
		"php/src/Model/Product.php",
	}
	if testing.Short() {
		files = files[:3]
	}
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Resolver: realResolver()})
	freshen(t, ix)
	for _, rel := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		orig, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(orig), "\n")
		writeFile(t, dir, rel, strings.Join(lines[:len(lines)/2], "\n")+"\n")
		if res := freshen(t, ix); res.Changed() == 0 && res.Rebuilt == "" {
			t.Fatalf("truncate %s: nothing reindexed: %+v", rel, *res)
		}
		requireSameDump(t, "truncate "+rel, dump(t, ix), scratchDump(t, dir, Options{Resolver: realResolver()}))
		writeFile(t, dir, rel, string(orig))
		if res := freshen(t, ix); res.Changed() == 0 && res.Rebuilt == "" {
			t.Fatalf("restore %s: nothing reindexed: %+v", rel, *res)
		}
		requireSameDump(t, "restore "+rel, dump(t, ix), scratchDump(t, dir, Options{Resolver: realResolver()}))
	}
}

// TestE2EDependencyChangeIsTargeted deletes and re-adds files whose
// existence other files' module identity or imports depend on, and edits a
// manifest: only the dependent files may be reprocessed (no full graph
// reset), and the graph must still equal a full build.
func TestE2EDependencyChangeIsTargeted(t *testing.T) {
	dir := testutil.Materialize(t)
	ix := openIx(t, dir, Options{Resolver: realResolver(), Now: futureClock})
	freshen(t, ix)
	var total int
	if err := ix.Store().DB().QueryRow(`SELECT count(*) FROM files`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	type step struct {
		name string
		do   func()
	}
	saved := map[string][]byte{}
	del := func(rel string) func() {
		return func() {
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatal(err)
			}
			saved[rel] = b
			removeFile(t, dir, rel)
		}
	}
	restore := func(rel string) func() { return func() { writeFile(t, dir, rel, string(saved[rel])) } }
	steps := []step{
		{"delete ts/src/utils/format.ts", del("ts/src/utils/format.ts")},
		{"restore ts/src/utils/format.ts", restore("ts/src/utils/format.ts")},
		{"delete python/shop/services/pricing.py", del("python/shop/services/pricing.py")},
		{"restore python/shop/services/pricing.py", restore("python/shop/services/pricing.py")},
		{"delete php/src/Model/Priceable.php", del("php/src/Model/Priceable.php")},
		{"restore php/src/Model/Priceable.php", restore("php/src/Model/Priceable.php")},
		{"delete java/src/main/java/com/example/zoo/util/Strings.java", del("java/src/main/java/com/example/zoo/util/Strings.java")},
		{"restore java/src/main/java/com/example/zoo/util/Strings.java", restore("java/src/main/java/com/example/zoo/util/Strings.java")},
		{"add csharp/src/Billing/Extra.cs", func() {
			writeFile(t, dir, "csharp/src/Billing/Extra.cs", "namespace Fixture.Billing {\n  public class Extra { public int Fee() { return 1; } }\n}\n")
		}},
		{"delete rust/src/geometry.rs", del("rust/src/geometry.rs")},
		{"restore rust/src/geometry.rs", restore("rust/src/geometry.rs")},
		{"edit ts/tsconfig.json", func() {
			b, err := os.ReadFile(filepath.Join(dir, "ts", "tsconfig.json"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, "ts/tsconfig.json", string(b)+"\n")
		}},
	}
	for _, st := range steps {
		st.do()
		res := freshen(t, ix)
		t.Logf("%s: %+v", st.name, *res)
		if res.Rebuilt != "" || res.Changed() >= total/2 {
			t.Errorf("%s: not targeted (rebuilt %q, %d of %d files changed)", st.name, res.Rebuilt, res.Changed(), total)
		}
		requireSameDump(t, st.name, dump(t, ix), scratchDump(t, dir, Options{Resolver: realResolver()}))
	}
}
