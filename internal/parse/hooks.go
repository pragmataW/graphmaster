package parse

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Optional per-language hooks. A model.LanguageSpec registered with this
// package may implement any subset of these interfaces to refine what the
// generic, query-driven extractor produces. Everything not covered by a hook
// falls back to language-neutral defaults, so a new language usually needs
// only a query and a few of these.

// exportChecker decides the exported flag of a definition.
// Default: nothing is exported.
type exportChecker interface {
	isExported(d *model.Def, node *sitter.Node, src []byte) bool
}

// sigFormatter renders the raw signature of a definition; the result is
// normalized and truncated by the engine. Return ok=false to use the default
// (node text up to its "body" field, or its first line).
type sigFormatter interface {
	signature(kind model.NodeKind, node *sitter.Node, src []byte) (sig string, ok bool)
}

// testNamer derives the tested-name hint of a test definition
// (e.g. TestParseFoo -> ParseFoo). Default: strip a leading "test"/"Test"
// and underscores.
type testNamer interface {
	testTarget(name string) string
}

// importCleaner turns the captured import path text into Import.Spec.
// Default: strip one layer of matching quotes.
type importCleaner interface {
	importSpec(raw string) string
}

// postProcessor gets the final facts for language-specific fixups such as
// reclassifying receivers as package qualifiers or dropping builtins. It must
// keep the model.Facts invariants (see model.Facts.Validate).
type postProcessor interface {
	postProcess(f *model.Facts)
}

// TestPathMatcher is implemented by specs whose test files are identified by
// path (e.g. Go's *_test.go). Facts are content-addressed and cached without
// the path, so the indexer combines this with Facts.TestFile.
type TestPathMatcher interface {
	IsTestPath(path string) bool
}
