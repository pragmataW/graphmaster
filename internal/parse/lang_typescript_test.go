package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func TestJSResolveImport(t *testing.T) {
	repo := model.NewMemRepo(map[string]string{
		"app/tsconfig.json": `{
			// comments and trailing commas are allowed
			"extends": "./tsconfig.base.json",
			"compilerOptions": { "baseUrl": "src", },
		}`,
		"app/tsconfig.base.json":        `{"compilerOptions": {"paths": {"@app/*": ["*"], "@lib": ["lib/main"], "@gone/*": ["missing/*"]}}}`,
		"app/src/main.ts":               "",
		"app/src/models/index.ts":       "",
		"app/src/models/user.ts":        "",
		"app/src/lib/main.ts":           "",
		"app/src/util.ts":               "",
		"app/src/util.js":               "",
		"app/src/esm.ts":                "",
		"app/src/data.json":             "{}",
		"app/src/dir/package.json":      `{"main": "./lib/entry.js"}`,
		"app/src/dir/lib/entry.js":      "",
		"web/jsconfig.json":             `{"compilerOptions": {"baseUrl": "."}}`,
		"web/components/Button.jsx":     "",
		"web/pages/home.js":             "",
		"package.json":                  `{"name": "root", "workspaces": ["packages/*", "tools/cli"]}`,
		"packages/core/package.json":    `{"name": "@acme/core", "types": "src/index.ts"}`,
		"packages/core/src/index.ts":    "",
		"packages/core/src/util/str.ts": "",
		"tools/cli/package.json":        `{"name": "acme-cli"}`,
		"tools/cli/index.js":            "",
		"plain/a.js":                    "",
		"plain/b.js":                    "",
	})
	ts, _ := Lookup(model.LangTypeScript)
	js, _ := Lookup(model.LangJavaScript)
	file := func(p string) model.ImportTarget {
		return model.ImportTarget{Kind: model.TargetFile, Path: p, Module: p}
	}
	ext := func(m string) model.ImportTarget { return model.ImportTarget{Kind: model.TargetExternal, Module: m} }
	unres := func(m string) model.ImportTarget { return model.ImportTarget{Kind: model.TargetUnresolved, Module: m} }
	cases := []struct {
		spec model.LanguageSpec
		from string
		imp  string
		want model.ImportTarget
	}{
		{ts, "app/src/main.ts", "./models/user", file("app/src/models/user.ts")},
		{ts, "app/src/main.ts", "./models", file("app/src/models/index.ts")},
		{ts, "app/src/models/user.ts", "..", unres("..")},
		{ts, "app/src/models/user.ts", ".", file("app/src/models/index.ts")},
		{ts, "app/src/main.ts", "./esm.js", file("app/src/esm.ts")},
		{ts, "app/src/main.ts", "./util", file("app/src/util.ts")},
		{js, "app/src/main.js", "./util", file("app/src/util.js")},
		{ts, "app/src/main.ts", "./data.json", file("app/src/data.json")},
		{ts, "app/src/main.ts", "./data", file("app/src/data.json")},
		{ts, "app/src/main.ts", "./dir", file("app/src/dir/lib/entry.js")},
		{ts, "app/src/main.ts", "./util?raw", file("app/src/util.ts")},
		{ts, "app/src/main.ts", "./nope", unres("./nope")},
		{ts, "app/src/main.ts", "../../../escape", unres("../../../escape")},
		{ts, "app/src/main.ts", "@app/models/user", file("app/src/models/user.ts")},
		{ts, "app/src/main.ts", "@lib", file("app/src/lib/main.ts")},
		{ts, "app/src/main.ts", "@gone/x", unres("@gone/x")},
		{ts, "app/src/main.ts", "models/user", file("app/src/models/user.ts")}, // baseUrl
		{ts, "app/src/main.ts", "react", ext("react")},
		{ts, "app/src/main.ts", "react-dom/client", ext("react-dom")},
		{ts, "app/src/main.ts", "@scope/pkg/deep/x", ext("@scope/pkg")},
		{ts, "app/src/main.ts", "node:fs", ext("node:fs")},
		{js, "web/pages/home.js", "components/Button", file("web/components/Button.jsx")},
		{ts, "app/src/main.ts", "@acme/core", file("packages/core/src/index.ts")},
		{ts, "app/src/main.ts", "@acme/core/util/str", file("packages/core/src/util/str.ts")},
		{js, "plain/a.js", "acme-cli", file("tools/cli/index.js")},
		{js, "plain/a.js", "./b.js", file("plain/b.js")},
		{js, "plain/a.js", "/abs/path", unres("/abs/path")},
		{js, "plain/a.js", "", model.ImportTarget{Kind: model.TargetUnresolved}},
	}
	for _, c := range cases {
		if got := c.spec.ResolveImport(c.imp, c.from, repo); got != c.want {
			t.Errorf("%v ResolveImport(%q from %s) = %+v, want %+v", c.spec.Lang(), c.imp, c.from, got, c.want)
		}
	}
	if got := ts.ResolveImport("./x", "a.ts", nil); got.Kind != model.TargetUnresolved {
		t.Errorf("nil ctx relative = %+v", got)
	}
	if got := ts.ResolveImport("react", "a.ts", nil); got != ext("react") {
		t.Errorf("nil ctx bare = %+v", got)
	}
}

func TestJSResolveImportExtendsCycle(t *testing.T) {
	repo := model.NewMemRepo(map[string]string{
		"tsconfig.json":            `{"extends": "./b.json", "compilerOptions": {"paths": {"~/*": ["src/*"]}}}`,
		"b.json":                   `{"extends": "./tsconfig.json"}`,
		"src/x.ts":                 "",
		"src/broken/t.ts":          "",
		"src/broken/tsconfig.json": `{ not json`,
	})
	ts, _ := Lookup(model.LangTypeScript)
	if got := ts.ResolveImport("~/x", "src/y.ts", repo); got.Path != "src/x.ts" {
		t.Errorf("cyclic extends: %+v", got)
	}
	if got := ts.ResolveImport("~/x", "src/broken/t.ts", repo); got.Kind != model.TargetExternal {
		t.Errorf("broken tsconfig must not resolve aliases: %+v", got)
	}
}

// TestJSResolveFixtureAlias resolves the fixture's @app/* alias through the
// real (commented) tsconfig.json.
func TestJSResolveFixtureAlias(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "fixture-repo", "ts")
	files := map[string]string{}
	for _, p := range []string{"tsconfig.json", "package.json", "src/models/user.ts", "src/services/userService.ts", "src/utils/format.ts"} {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		files["ts/"+p] = string(b)
	}
	repo := model.NewMemRepo(files)
	ts, _ := Lookup(model.LangTypeScript)
	got := ts.ResolveImport("@app/models/user", "ts/src/services/userService.ts", repo)
	if got.Kind != model.TargetFile || got.Path != "ts/src/models/user.ts" {
		t.Fatalf("got %+v", got)
	}
	if got := ts.ResolveImport("../utils/format", "ts/src/services/userService.ts", repo); got.Path != "ts/src/utils/format.ts" {
		t.Fatalf("relative: %+v", got)
	}
}
