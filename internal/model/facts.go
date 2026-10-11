package model

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NoParent marks a top-level definition (Def.Parent) or a file-scope
// reference (Ref.Enclosing).
const NoParent int32 = -1

// MaxSigLen is the maximum byte length of a stored signature.
const MaxSigLen = 200

// Span locates a syntax node. Lines are 1-based; columns are 0-based byte
// offsets within the line; bytes are 0-based offsets into the file with an
// exclusive end.
type Span struct {
	StartLine uint32
	StartCol  uint32
	EndLine   uint32
	EndCol    uint32
	StartByte uint32
	EndByte   uint32
}

// Def is a definition extracted from one file.
type Def struct {
	Kind NodeKind
	Name string
	// Parent is the index into Facts.Defs of the lexically enclosing
	// definition, or NoParent. Parents always precede their children.
	Parent int32
	// Scope holds extra qualifier segments that are not represented by a
	// parent def in this file, e.g. the receiver type of a Go method
	// ("Server") or "ns::Cls" of an out-of-line C++ method definition.
	Scope []string
	Span
	Sig   string // normalized, at most MaxSigLen bytes (see NormalizeSig)
	Flags DefFlags
}

// Ref is an unresolved use of a name.
type Ref struct {
	Kind RefKind
	Name string // the referenced identifier, e.g. "Foo" in pkg.Foo()
	// Qualifier is the textual qualifier when it is a module/type path,
	// e.g. "pkg" in pkg.Foo() or "std::vector" in std::vector::push_back.
	Qualifier string
	// Receiver is the receiver expression text for member access on a
	// value, e.g. "s" in s.Start() or "this" / "self" / "$this".
	Receiver string
	// Enclosing is the index of the innermost enclosing def, or NoParent.
	Enclosing int32
	Line      uint32
	Col       uint32
}

// Import is an import / include / use declaration.
type Import struct {
	// Spec is the import path as written, without quotes. C/C++ includes
	// keep their delimiters ("<stdio.h>" or "\"util.h\"") so resolvers can
	// tell system and local includes apart.
	Spec  string
	Alias string   // local alias, "" if none ("_" and "." kept verbatim for Go)
	Names []string // imported member names (Python from-import, JS named, Java single-type)
	// Wildcard is set for on-demand imports: Java a.b.*, Python from x import *,
	// C# using / C++ using namespace, JS export * from.
	Wildcard bool
	Line     uint32
}

// Inherit is a type relationship declared in this file.
type Inherit struct {
	Child     int32    // index into Facts.Defs
	Kind      EdgeKind // EdgeExtends, EdgeImplements or EdgeEmbeds
	Name      string   // target type name
	Qualifier string   // target qualifier text, "" if unqualified
	Line      uint32
	Col       uint32
}

// TestMark records that a def is a test, optionally hinting the tested name
// (e.g. TestParseFoo -> "ParseFoo").
type TestMark struct {
	Def    int32
	Target string
}

// Facts is the language-neutral parse output for one file. It is cached in
// cache.db keyed by content, so it must not contain path-dependent data.
type Facts struct {
	Lang Lang
	// Package is the package / namespace declared in the file, if the
	// language has such a clause (Go package, Java package, C#/PHP namespace).
	Package  string
	TestFile bool // file is a test file by naming convention or content
	Defs     []Def
	Refs     []Ref
	Imports  []Import
	Inherits []Inherit
	Tests    []TestMark
}

// AddDef appends d and returns its index.
func (f *Facts) AddDef(d Def) int32 {
	f.Defs = append(f.Defs, d)
	return int32(len(f.Defs) - 1)
}

// QNameParts returns the qualified name segments of def i, from outermost
// container to the def itself, including Scope segments. Module identity is
// not included; resolvers prepend it. Out-of-range indices return nil.
func (f *Facts) QNameParts(i int32) []string {
	if i < 0 || int(i) >= len(f.Defs) {
		return nil
	}
	var chain []int32
	// Parents precede children, so this terminates for valid Facts; the
	// length guard protects against malformed input.
	for j := i; j >= 0 && int(j) < len(f.Defs) && len(chain) <= len(f.Defs); j = f.Defs[j].Parent {
		chain = append(chain, j)
	}
	var parts []string
	for k := len(chain) - 1; k >= 0; k-- {
		d := &f.Defs[chain[k]]
		parts = append(parts, d.Scope...)
		parts = append(parts, d.Name)
	}
	return parts
}

// Validate checks structural invariants that downstream layers rely on.
func (f *Facts) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if f.Lang != LangUnknown && !f.Lang.Valid() {
		bad("invalid lang %d", uint8(f.Lang))
	}
	n := int32(len(f.Defs))
	defIdx := func(i int32) bool { return i >= 0 && i < n }

	for i := range f.Defs {
		d := &f.Defs[i]
		idx := int32(i)
		if !d.Kind.Valid() {
			bad("def %d: invalid kind %d", i, uint8(d.Kind))
		}
		if d.Name == "" {
			bad("def %d: empty name", i)
		}
		if d.Parent != NoParent && (d.Parent < 0 || d.Parent >= idx) {
			bad("def %d: parent %d must be NoParent or precede it", i, d.Parent)
		}
		if err := d.validate(); err != nil {
			bad("def %d: %v", i, err)
		}
		if len(d.Sig) > MaxSigLen {
			bad("def %d: sig is %d bytes (max %d)", i, len(d.Sig), MaxSigLen)
		}
	}
	for i, r := range f.Refs {
		if !r.Kind.Valid() {
			bad("ref %d: invalid kind %d", i, uint8(r.Kind))
		}
		if r.Name == "" {
			bad("ref %d: empty name", i)
		}
		if r.Enclosing != NoParent && !defIdx(r.Enclosing) {
			bad("ref %d: enclosing %d out of range", i, r.Enclosing)
		}
	}
	for i, im := range f.Imports {
		if im.Spec == "" {
			bad("import %d: empty spec", i)
		}
	}
	for i, in := range f.Inherits {
		if !defIdx(in.Child) {
			bad("inherit %d: child %d out of range", i, in.Child)
		}
		if !in.Kind.IsInheritance() {
			bad("inherit %d: kind %v is not an inheritance kind", i, in.Kind)
		}
		if in.Name == "" {
			bad("inherit %d: empty name", i)
		}
	}
	for i, tm := range f.Tests {
		if !defIdx(tm.Def) {
			bad("test %d: def %d out of range", i, tm.Def)
		}
	}
	return errors.Join(errs...)
}

func (s Span) validate() error {
	if s.EndLine < s.StartLine || (s.EndLine == s.StartLine && s.EndCol < s.StartCol) {
		return fmt.Errorf("span end %d:%d before start %d:%d", s.EndLine, s.EndCol, s.StartLine, s.StartCol)
	}
	if s.EndByte < s.StartByte {
		return fmt.Errorf("span end byte %d before start byte %d", s.EndByte, s.StartByte)
	}
	return nil
}

// NormalizeSig collapses whitespace runs to single spaces, trims, and
// truncates to at most MaxSigLen bytes on a rune boundary, appending "…"
// when truncated.
func NormalizeSig(s string) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	if len(s) <= MaxSigLen {
		return s
	}
	const ellipsis = "…"
	cut := MaxSigLen - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRightFunc(s[:cut], unicode.IsSpace) + ellipsis
}
