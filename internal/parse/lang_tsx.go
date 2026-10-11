package parse

import (
	_ "embed"
	"unsafe"

	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/tsx.scm
var tsxQueryExtra string

// tsxSpec is the TSX language spec: TypeScript plus JSX, where using a
// capitalized element is a call of that component.
type tsxSpec struct{ jsFamily }

func init() { Register(tsxSpec{}) }

func (tsxSpec) Lang() model.Lang        { return model.LangTSX }
func (tsxSpec) Extensions() []string    { return model.LangTSX.Extensions() }
func (tsxSpec) Grammar() unsafe.Pointer { return tsts.LanguageTSX() }
func (tsxSpec) QuerySource() string     { return typescriptQuery + "\n" + tsxQueryExtra }
func (tsxSpec) ExtractorVersion() int   { return 1 }

// ResolveImport implements model.LanguageSpec (see jsResolveImport).
func (tsxSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	return jsResolveImport(spec, fromFile, ctx, true)
}
