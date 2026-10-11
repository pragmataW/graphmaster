package resolve

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// tsFixture returns the ts/ and js/ parts of the fixture and the statuses
// the indexer assigns (the minified bundle is not parsed).
func tsFixture(t *testing.T) (map[string]string, map[string]model.FileStatus) {
	t.Helper()
	root := testutil.FixtureDir()
	files := map[string]string{}
	for _, dir := range []string{"ts", "js"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files, map[string]model.FileStatus{testutil.MinifiedPath: model.StatusMinified}
}

func TestTSFixtureExpectedEdges(t *testing.T) {
	files, status := tsFixture(t)
	r := fullBuild(t, files, status)
	for _, sec := range []string{"typescript", "javascript"} {
		exps := loadExpectations(t, sec)
		if len(exps) < 15 {
			t.Fatalf("only %d %s expectations loaded", len(exps), sec)
		}
		checkExpectations(t, r, exps)
		checkImportExpectations(t, r, exps)
	}
}

// checkImportExpectations verifies "imports" lines through the language
// spec's ResolveImport, the way the indexer links imports rows.
func checkImportExpectations(t *testing.T, r *testRepo, exps []expectation) {
	t.Helper()
	ctx := context.Background()
	rc := r.rc()
	for _, x := range exps {
		if x.Kind != model.EdgeImports {
			continue
		}
		f, err := r.st.FileByPath(ctx, x.Src)
		if err != nil {
			t.Fatalf("line %d: %v", x.Line, err)
		}
		spec, _ := parse.Lookup(f.Lang)
		var got []string
		rows, err := r.st.DB().Query(`SELECT spec FROM imports WHERE file_id = ?`, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			got = append(got, spec.ResolveImport(s, x.Src, rc).Path)
		}
		rows.Close()
		found := false
		for _, p := range got {
			found = found || p == x.Dst
		}
		if found == x.Negative {
			t.Errorf("line %d: %v: resolved imports %v", x.Line, x, got)
		}
	}
}

// TestTSFixtureConfidence pins exact tiers, including the test callbacks
// whose names (their titles) cannot be written in expected_edges.txt.
func TestTSFixtureConfidence(t *testing.T) {
	files, status := tsFixture(t)
	r := fullBuild(t, files, status)
	es := r.edges()

	const (
		svc     = "ts/src/services/userService.ts::UserService."
		user    = "ts/src/models/user.ts::"
		card    = "ts/src/components/UserCard.tsx::"
		greetIt = "ts/test/userService.test.ts::UserService.greets a known user"
		promIt  = "ts/test/userService.test.ts::UserService.promotes to admin"
		calc    = "js/lib/calc.js::"
		mainRun = "js/src/main.mjs::run"
		sumTest = "js/test/calc.test.js::sum adds and records"
		sqTest  = "js/test/calc.test.js::square multiplies"
	)
	cases := []struct {
		src, name, dst string
		conf           int
	}{
		{svc + "greet", "find", svc + "find", 80},
		{svc + "greet", "formatName", "ts/src/utils/format.ts::formatName", 100},
		{svc + "greet", "role", user + "User.role", 80},
		{svc + "greet", "name", user + "User.name", 80},
		{svc + "promote", "Admin", user + "Admin", 100},
		{svc + "promote", "id", user + "User.id", 80},
		{svc + "add", "set", "", 0}, // Map.set
		{svc + "add", "users", svc + "users", 80},
		{svc + "users", "User", user + "User", 100},
		{user + "Admin.canDelete", "role", user + "User.role", 80},
		{user + "Admin.role", "Role", user + "Role", 90},
		{card + "UserCard", "greet", svc + "greet", 80},
		{card + "UserCard", "Badge", card + "Badge", 90},
		{card + "UserCardProps.service", "UserService", "ts/src/services/userService.ts::UserService", 100},
		{"ts/src/utils/format.ts::formatName", "capitalize", "ts/src/utils/format.ts::capitalize", 90},
		{"ts/src/utils/format.ts::formatName", "trim", "", 0},
		{"ts/src/utils/format.ts::capitalize", "toUpperCase", "", 0},
		{greetIt, "UserService", "ts/src/services/userService.ts::UserService", 100},
		{greetIt, "User", user + "User", 100},
		{greetIt, "add", svc + "add", 80},
		{greetIt, "greet", svc + "greet", 80},
		{greetIt, "toBe", "", 0},
		{promIt, "promote", svc + "promote", 80},
		{promIt, "role", user + "Admin.role", 80}, // promote() returns Admin | undefined
		{calc + "Calculator.sum", "add", "js/lib/math.js::add", 100},
		{calc + "Calculator.sum", "record", calc + "Calculator.record", 80},
		{calc + "Calculator.product", "multiply", "js/lib/math.js::multiply", 100},
		{calc + "Calculator.record", "push", "", 0},
		{calc + "ScientificCalculator.square", "product", calc + "Calculator.product", 80},
		{mainRun, "ScientificCalculator", calc + "ScientificCalculator", 100},
		{mainRun, "square", calc + "ScientificCalculator.square", 80},
		{mainRun, "sum", calc + "Calculator.sum", 80},
		{mainRun, "formatTotal", "js/src/format.mjs::formatTotal", 100},
		{"js/src/format.mjs::formatTotal", "pad", "js/src/format.mjs::pad", 90},
		{"js/lib/math.js::multiply", "add", "js/lib/math.js::add", 90},
		{sumTest, "Calculator", calc + "Calculator", 100},
		{sumTest, "sum", calc + "Calculator.sum", 80},
		{sumTest, "strictEqual", "", 0}, // assert is node:assert
		{sqTest, "square", calc + "ScientificCalculator.square", 80},
	}
	for _, c := range cases {
		got := edgesFrom(es, c.src, c.name)
		if len(got) == 0 {
			t.Errorf("%s -> %s: no edge%s", c.src, c.name, describeFrom(es, c.src))
			continue
		}
		for _, e := range got {
			if e.Dst != c.dst || e.Conf != c.conf {
				t.Errorf("%s -> %s (line %d): got %q conf %d, want %q conf %d", c.src, c.name, e.Line, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}
	for _, e := range es {
		if strings.HasPrefix(e.Src, "js/public/") || strings.HasPrefix(e.Dst, "js/public/") {
			t.Errorf("minified bundle in the graph: %+v", e)
		}
	}
}

// TestTSFixtureIncrementalEquivalence applies edits one by one, resolving
// only pending names, and compares every step with a from-scratch build.
func TestTSFixtureIncrementalEquivalence(t *testing.T) {
	files, status := tsFixture(t)
	const (
		userPath   = "ts/src/models/user.ts"
		svcPath    = "ts/src/services/userService.ts"
		formatPath = "ts/src/utils/format.ts"
		calcPath   = "js/lib/calc.js"
	)
	inc := fullBuild(t, files, status)
	steps := []struct {
		name  string
		apply func(r *testRepo) []string
		del   []string
	}{
		{name: "find returns Admin", apply: func(r *testRepo) []string {
			r.files[svcPath] = strings.Replace(r.files[svcPath], "find(id: string): User | undefined", "find(id: string): Admin | undefined", 1)
			return []string{svcPath}
		}},
		{name: "rename formatName", apply: func(r *testRepo) []string {
			r.files[formatPath] = strings.ReplaceAll(r.files[formatPath], "formatName", "formatFullName")
			return []string{formatPath}
		}},
		{name: "User.role removed", apply: func(r *testRepo) []string {
			r.files[userPath] = strings.Replace(r.files[userPath], "  role(): Role {\n    return \"member\";\n  }\n", "", 1)
			return []string{userPath}
		}},
		{name: "second capitalize helper", apply: func(r *testRepo) []string {
			r.files["ts/src/utils/text.ts"] = "export const capitalize = (s: string) => s;\n"
			return []string{"ts/src/utils/text.ts"}
		}},
		{name: "delete user.ts", del: []string{userPath}},
		{name: "calc without product", apply: func(r *testRepo) []string {
			r.files[calcPath] = strings.Replace(r.files[calcPath], "product(a, b)", "times(a, b)", 1)
			return []string{calcPath}
		}},
	}
	for _, step := range steps {
		if step.apply != nil {
			inc.index(step.apply(inc)...)
		}
		if len(step.del) > 0 {
			inc.remove(step.del...)
		}
		inc.resolvePending(Options{BatchNames: 5})
		full := fullBuild(t, inc.files, status)
		if got, want := inc.dump(), full.dump(); got != want {
			t.Fatalf("step %q: incremental != full:\n%s", step.name, diffLines(want, got))
		}
	}
}
