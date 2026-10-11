package model

import "unsafe"

// LanguageSpec describes one language to the parse and resolve layers.
// Implementations live in internal/parse (lang_<x>.go) and self-register.
type LanguageSpec interface {
	// Lang returns the language this spec handles.
	Lang() Lang
	// Extensions returns the file extensions (with leading dot) handled.
	Extensions() []string
	// Grammar returns the raw TSLanguage pointer exported by the grammar's
	// Go binding (e.g. tree_sitter_go.Language()). The parse layer wraps it
	// with tree_sitter.NewLanguage; keeping it opaque here keeps model free
	// of cgo.
	Grammar() unsafe.Pointer
	// QuerySource returns the tree-sitter query (.scm) used for extraction.
	QuerySource() string
	// ExtractorVersion must be bumped whenever extraction output changes;
	// it invalidates cached facts for this language only.
	ExtractorVersion() int
	// ModuleOf returns the module identity of the repo-relative file path,
	// e.g. a Go import path, Python dotted module, Java package, or the
	// file path itself for TS/JS. facts may be nil.
	ModuleOf(path string, facts *Facts, ctx RepoContext) string
	// ResolveImport maps an import spec written in fromFile (repo-relative)
	// to its target.
	ResolveImport(spec string, fromFile string, ctx RepoContext) ImportTarget
}

// ImportTargetKind classifies the result of ResolveImport.
type ImportTargetKind uint8

// Import target kinds.
const (
	TargetUnresolved ImportTargetKind = iota // could not be resolved
	TargetFile                               // a single in-repo file
	TargetModule                             // an in-repo package/module (directory or namespace)
	TargetExternal                           // outside the repo (stdlib, npm, system header)
)

var importTargetNames = []string{"unresolved", "file", "module", "external"}

func (k ImportTargetKind) String() string {
	return enumString(importTargetNames, uint8(k), "target")
}

// ImportTarget is where an import points.
type ImportTarget struct {
	Kind ImportTargetKind
	// Path is the repo-relative target file (TargetFile) or directory
	// (TargetModule); empty otherwise.
	Path string
	// Module is the target module identity in the same form ModuleOf
	// returns, or the external module name for TargetExternal.
	Module string
}

// Resolved reports whether the import points into the repository.
func (t ImportTarget) Resolved() bool {
	return t.Kind == TargetFile || t.Kind == TargetModule
}
