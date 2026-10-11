package deps_test

import (
	"testing"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tscsharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tsphp "github.com/tree-sitter/tree-sitter-php/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// grammarCase pairs a grammar with a minimal valid snippet and the root node
// kind the grammar is expected to produce for it.
type grammarCase struct {
	name     string
	lang     func() unsafe.Pointer
	src      string
	rootKind string
}

var grammarCases = []grammarCase{
	{"go", tsgo.Language, "package main\n\nfunc main() {}\n", "source_file"},
	{"python", tspython.Language, "def f(x):\n    return x\n", "module"},
	{"javascript", tsjs.Language, "function f(x) { return x; }\n", "program"},
	{"typescript", tsts.LanguageTypescript, "function f(x: number): number { return x; }\n", "program"},
	{"tsx", tsts.LanguageTSX, "const A = () => <div>hi</div>;\n", "program"},
	{"rust", tsrust.Language, "fn main() {}\n", "source_file"},
	{"java", tsjava.Language, "class A { void f() {} }\n", "program"},
	{"c", tsc.Language, "int main(void) { return 0; }\n", "translation_unit"},
	{"cpp", tscpp.Language, "namespace n { int f() { return 0; } }\n", "translation_unit"},
	{"c_sharp", tscsharp.Language, "class A { void F() {} }\n", "compilation_unit"},
	{"php", tsphp.LanguagePHP, "<?php function f($x) { return $x; }\n", "program"},
}

// TestGrammarABI proves every pinned grammar is ABI-compatible with the pinned
// go-tree-sitter core: SetLanguage must accept it and a trivial snippet must
// parse without syntax errors.
func TestGrammarABI(t *testing.T) {
	for _, tc := range grammarCases {
		t.Run(tc.name, func(t *testing.T) {
			lang := sitter.NewLanguage(tc.lang())
			if v := lang.AbiVersion(); v < sitter.MIN_COMPATIBLE_LANGUAGE_VERSION || v > sitter.LANGUAGE_VERSION {
				t.Fatalf("ABI version %d outside supported range [%d, %d]",
					v, sitter.MIN_COMPATIBLE_LANGUAGE_VERSION, sitter.LANGUAGE_VERSION)
			}

			parser := sitter.NewParser()
			defer parser.Close()
			if err := parser.SetLanguage(lang); err != nil {
				t.Fatalf("SetLanguage: %v", err)
			}

			tree := parser.Parse([]byte(tc.src), nil)
			if tree == nil {
				t.Fatal("Parse returned nil tree")
			}
			defer tree.Close()

			root := tree.RootNode()
			if root.HasError() {
				t.Fatalf("unexpected syntax error in tree: %s", root.ToSexp())
			}
			if got := root.Kind(); got != tc.rootKind {
				t.Fatalf("root kind = %q, want %q", got, tc.rootKind)
			}
		})
	}
}
