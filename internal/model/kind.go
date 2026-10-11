package model

import (
	"fmt"
	"strings"
)

// enumString and enumParse are tiny helpers for uint8 enums with a name table.
func enumString(names []string, v uint8, typ string) string {
	if int(v) < len(names) && names[v] != "" {
		return names[v]
	}
	return fmt.Sprintf("%s(%d)", typ, v)
}

func enumParse(names []string, s, typ string) (uint8, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, n := range names {
		if i > 0 && n == s {
			return uint8(i), nil
		}
	}
	return 0, fmt.Errorf("unknown %s %q", typ, s)
}

// NodeKind classifies a definition node. Values are stored in graph.db.
type NodeKind uint8

// Node kinds. Append only; never renumber.
const (
	KindUnknown NodeKind = iota
	KindPackage
	KindModule
	KindFile
	KindNamespace
	KindClass
	KindStruct
	KindInterface
	KindTrait
	KindEnum
	KindType // type alias / typedef / named type
	KindFunction
	KindMethod
	KindConstructor
	KindField
	KindVariable
	KindConstant
	KindMacro
	KindTest
	KindEnumMember
	KindProperty

	nodeKindCount
)

var nodeKindNames = []string{
	"unknown", "package", "module", "file", "namespace", "class", "struct",
	"interface", "trait", "enum", "type", "function", "method", "constructor",
	"field", "variable", "constant", "macro", "test", "enum_member", "property",
}

// AllNodeKinds returns every known node kind (excluding KindUnknown).
func AllNodeKinds() []NodeKind {
	out := make([]NodeKind, 0, nodeKindCount-1)
	for k := KindPackage; k < nodeKindCount; k++ {
		out = append(out, k)
	}
	return out
}

func (k NodeKind) String() string { return enumString(nodeKindNames, uint8(k), "kind") }

// Valid reports whether k is a known, non-unknown kind.
func (k NodeKind) Valid() bool { return k > KindUnknown && k < nodeKindCount }

// ParseNodeKind parses the lowercase name produced by String.
func ParseNodeKind(s string) (NodeKind, error) {
	v, err := enumParse(nodeKindNames, s, "node kind")
	return NodeKind(v), err
}

// IsType reports whether k declares a type.
func (k NodeKind) IsType() bool {
	switch k {
	case KindClass, KindStruct, KindInterface, KindTrait, KindEnum, KindType:
		return true
	}
	return false
}

// IsCallable reports whether k can be the target of a call edge.
func (k NodeKind) IsCallable() bool {
	switch k {
	case KindFunction, KindMethod, KindConstructor, KindTest, KindMacro:
		return true
	}
	return false
}

// IsContainer reports whether k can lexically contain other definitions.
func (k NodeKind) IsContainer() bool {
	switch k {
	case KindPackage, KindModule, KindFile, KindNamespace,
		KindClass, KindStruct, KindInterface, KindTrait, KindEnum:
		return true
	}
	return false
}

// EdgeKind classifies a graph edge. Values are stored in graph.db.
type EdgeKind uint8

// Edge kinds. Append only; never renumber.
const (
	EdgeUnknown EdgeKind = iota
	EdgeContains
	EdgeCalls
	EdgeReferences
	EdgeImports
	EdgeExtends
	EdgeImplements
	EdgeEmbeds
	EdgeTests
	EdgeIncludes
	EdgeDeclares // header declaration -> definition (C/C++)

	edgeKindCount
)

var edgeKindNames = []string{
	"unknown", "contains", "calls", "references", "imports", "extends",
	"implements", "embeds", "tests", "includes", "declares",
}

// AllEdgeKinds returns every known edge kind (excluding EdgeUnknown).
func AllEdgeKinds() []EdgeKind {
	out := make([]EdgeKind, 0, edgeKindCount-1)
	for k := EdgeContains; k < edgeKindCount; k++ {
		out = append(out, k)
	}
	return out
}

func (k EdgeKind) String() string { return enumString(edgeKindNames, uint8(k), "edge") }

// Valid reports whether k is a known, non-unknown edge kind.
func (k EdgeKind) Valid() bool { return k > EdgeUnknown && k < edgeKindCount }

// ParseEdgeKind parses the lowercase name produced by String.
func ParseEdgeKind(s string) (EdgeKind, error) {
	v, err := enumParse(edgeKindNames, s, "edge kind")
	return EdgeKind(v), err
}

// IsInheritance reports whether k is extends, implements or embeds.
func (k EdgeKind) IsInheritance() bool {
	return k == EdgeExtends || k == EdgeImplements || k == EdgeEmbeds
}

// RefKind classifies an unresolved reference found in a file.
type RefKind uint8

// Reference kinds.
const (
	RefUnknown RefKind = iota
	RefCall            // foo(), x.foo()
	RefType            // type usage: annotations, new T, casts
	RefMember          // non-call member access: x.field
)

var refKindNames = []string{"unknown", "call", "type", "member"}

func (k RefKind) String() string { return enumString(refKindNames, uint8(k), "ref") }

// Valid reports whether k is a known, non-unknown reference kind.
func (k RefKind) Valid() bool { return k > RefUnknown && int(k) < len(refKindNames) }

// ParseRefKind parses the lowercase name produced by String.
func ParseRefKind(s string) (RefKind, error) {
	v, err := enumParse(refKindNames, s, "ref kind")
	return RefKind(v), err
}

// EdgeKind returns the edge kind a resolved reference produces.
func (k RefKind) EdgeKind() EdgeKind {
	if k == RefCall {
		return EdgeCalls
	}
	return EdgeReferences
}
