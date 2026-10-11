package resolve

import (
	"fmt"
	"sync"

	"github.com/pragmataW/graphmaster/internal/model"
)

// LangResolver customizes name resolution for one language. Implementations
// live in resolve/<lang>.go and self-register from init() with Register.
// Languages without one use Generic. Implementations usually embed Generic
// and override only what differs; the framework always calls hooks through
// the interface, so overrides take effect everywhere.
//
// Hooks must be deterministic functions of the Session state: incremental
// resolution is only equivalent to a full build if every edge resolves the
// same way regardless of processing order.
type LangResolver interface {
	// ImportBindings returns the local identifiers an import makes
	// available as qualifier roots in the importing file (e.g. the package
	// name of a Go import, an alias, a Python module name). Members listed
	// in Import.Names are handled by the framework and need not be
	// returned.
	ImportBindings(imp Import) []string

	// IsMember reports whether d belongs to a type (field, method, ...),
	// so it is only reachable through a receiver or qualifier and never as
	// a bare name from outside its type.
	IsMember(s *Session, d *Def) bool

	// OwnerName returns the name of the type a member belongs to when the
	// member is not lexically nested in it (Go methods declared at top
	// level), or "" otherwise. Nested members are matched by ParentID.
	OwnerName(s *Session, d *Def) string

	// MembersInScope reports whether members of the enclosing types are
	// visible as bare names (implicit this in Java, C#, C++).
	MembersInScope() bool

	// ResolveType resolves a type name written in file from (with its
	// textual qualifier, "" if none) to type definitions.
	ResolveType(s *Session, from *File, qual, name string) TypeResult

	// Receiver classifies the reference at site: whether it is a member
	// access and, if so, what the receiver's type is (tier T3).
	Receiver(s *Session, site *Site) Receiver
}

// ReExporter may be implemented by a LangResolver whose modules re-export
// definitions of other modules (TypeScript/JavaScript barrel files:
// export * from, export { X } from). ReExported returns the definitions
// named name that import target t provides through re-exports and that
// keep accepts. The framework consults it for named imports and import
// bindings when no definition of t itself matches.
type ReExporter interface {
	ReExported(s *Session, t model.ImportTarget, name string, keep func(*Def) bool) []*Def
}

// RecvKind classifies the receiver of a reference.
type RecvKind uint8

// Receiver kinds.
const (
	// RecvNone: a bare (or qualifier-only) name, not a member access.
	RecvNone RecvKind = iota
	// RecvUnknownShape: the source is unavailable, so it is unknown whether
	// the reference is a member access. No member filtering is applied.
	RecvUnknownShape
	// RecvUntyped: a member access whose receiver type is unknown;
	// candidates are restricted to members (T4/T5).
	RecvUntyped
	// RecvExternal: the receiver type lives outside the repository (stdlib,
	// third party, builtin); the reference stays unresolved.
	RecvExternal
	// RecvTyped: Receiver.Types holds the receiver's type definitions.
	RecvTyped
)

// Receiver is the outcome of LangResolver.Receiver.
type Receiver struct {
	Kind  RecvKind
	Types []*Def
}

// TypeResult is the outcome of resolving a type name.
type TypeResult struct {
	// Defs are the matching type definitions; empty if none.
	Defs []*Def
	// Conf is the confidence of the match (tier conf; 30/n if ambiguous).
	Conf uint8
	// External is set when the name provably refers to something outside
	// the repository (or to a bound import without such a definition), so
	// callers must not fall back to global guessing.
	External bool
}

// Found reports whether at least one definition matched.
func (t TypeResult) Found() bool { return len(t.Defs) > 0 }

var registry = struct {
	sync.RWMutex
	m map[model.Lang]LangResolver
}{m: map[model.Lang]LangResolver{}}

// Register installs the resolver for lang. It panics on an invalid language
// or a duplicate registration (programming errors caught at init time).
func Register(lang model.Lang, r LangResolver) {
	if !lang.Valid() {
		panic(fmt.Sprintf("resolve: register: invalid language %v", lang))
	}
	if r == nil {
		panic(fmt.Sprintf("resolve: register %v: nil resolver", lang))
	}
	registry.Lock()
	defer registry.Unlock()
	if _, dup := registry.m[lang]; dup {
		panic(fmt.Sprintf("resolve: register: duplicate resolver for %v", lang))
	}
	registry.m[lang] = r
}

// For returns the resolver registered for lang, or Generic.
func For(lang model.Lang) LangResolver {
	registry.RLock()
	r, ok := registry.m[lang]
	registry.RUnlock()
	if ok {
		return r
	}
	return Generic{}
}

// unregister removes a resolver; tests only.
func unregister(lang model.Lang) {
	registry.Lock()
	delete(registry.m, lang)
	registry.Unlock()
}
