package parse

import (
	"fmt"
	"strings"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/pragmataW/graphmaster/internal/model"
)

// role is what a capture name means to the extractor.
type role uint8

const (
	roleIgnore         role = iota // "_..." predicate helpers
	roleDef                        // @def.<kind>
	roleName                       // @name
	roleScope                      // @scope
	roleRef                        // @ref.<kind>
	roleReceiver                   // @receiver
	roleQualifier                  // @qualifier
	roleImportPath                 // @import.path
	roleImportAlias                // @import.alias
	roleImportName                 // @import.name
	roleImportWildcard             // @import.wildcard
	roleInherit                    // @inherit.<kind>
	roleTest                       // @test
	rolePackage                    // @package
	roleLocal                      // @local
)

// capture describes one capture index of a compiled query.
type capture struct {
	role    role
	defKind model.NodeKind
	refKind model.RefKind
	inherit model.EdgeKind
}

var simpleRoles = map[string]role{
	"name":            roleName,
	"scope":           roleScope,
	"receiver":        roleReceiver,
	"qualifier":       roleQualifier,
	"import.path":     roleImportPath,
	"import.alias":    roleImportAlias,
	"import.name":     roleImportName,
	"import.wildcard": roleImportWildcard,
	"test":            roleTest,
	"package":         rolePackage,
	"local":           roleLocal,
}

// parseCapture maps a capture name to its meaning. Unknown names are errors
// so that typos in .scm files fail loudly at compile time.
func parseCapture(name string) (capture, error) {
	if strings.HasPrefix(name, "_") {
		return capture{role: roleIgnore}, nil
	}
	if r, ok := simpleRoles[name]; ok {
		return capture{role: r}, nil
	}
	prefix, suffix, _ := strings.Cut(name, ".")
	switch prefix {
	case "def":
		k, err := model.ParseNodeKind(suffix)
		if err != nil {
			return capture{}, err
		}
		return capture{role: roleDef, defKind: k}, nil
	case "ref":
		k, err := model.ParseRefKind(suffix)
		if err != nil {
			return capture{}, err
		}
		return capture{role: roleRef, refKind: k}, nil
	case "inherit":
		k, err := model.ParseEdgeKind(suffix)
		if err != nil || !k.IsInheritance() {
			return capture{}, fmt.Errorf("inherit kind %q must be extends, implements or embeds", suffix)
		}
		return capture{role: roleInherit, inherit: k}, nil
	}
	return capture{}, fmt.Errorf("unknown capture @%s", name)
}

// compiled is the immutable, shareable part of a language: the grammar and
// its query. A tree-sitter Query is safe for concurrent read-only use by
// multiple QueryCursors, so one instance serves every worker.
type compiled struct {
	lang     *sitter.Language
	query    *sitter.Query
	captures []capture
}

type compileKey struct {
	lang  model.Lang
	query string
}

type compileEntry struct {
	once sync.Once
	c    *compiled
	err  error
}

// compileCache lives for the whole process; queries are never closed.
var compileCache sync.Map // compileKey -> *compileEntry

// compileSpec returns the compiled form of spec, compiling at most once per
// (language, query source).
func compileSpec(spec model.LanguageSpec) (*compiled, error) {
	key := compileKey{spec.Lang(), spec.QuerySource()}
	v, _ := compileCache.LoadOrStore(key, &compileEntry{})
	e := v.(*compileEntry)
	e.once.Do(func() { e.c, e.err = compile(spec) })
	return e.c, e.err
}

func compile(spec model.LanguageSpec) (*compiled, error) {
	ptr := spec.Grammar()
	if ptr == nil {
		return nil, fmt.Errorf("parse: %v: nil grammar", spec.Lang())
	}
	lang := sitter.NewLanguage(ptr)
	q, qerr := sitter.NewQuery(lang, spec.QuerySource())
	if qerr != nil {
		return nil, fmt.Errorf("parse: %v query: %w", spec.Lang(), qerr)
	}
	names := q.CaptureNames()
	caps := make([]capture, len(names))
	for i, n := range names {
		c, err := parseCapture(n)
		if err != nil {
			q.Close()
			return nil, fmt.Errorf("parse: %v query: %w", spec.Lang(), err)
		}
		caps[i] = c
	}
	return &compiled{lang: lang, query: q, captures: caps}, nil
}
