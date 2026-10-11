package model

import (
	"path"
	"testing"
	"unsafe"
)

// fakeSpec proves the LanguageSpec contract is implementable without cgo.
type fakeSpec struct{}

func (fakeSpec) Lang() Lang                                        { return LangGo }
func (fakeSpec) Extensions() []string                              { return LangGo.Extensions() }
func (fakeSpec) Grammar() unsafe.Pointer                           { return nil }
func (fakeSpec) QuerySource() string                               { return "(identifier) @name" }
func (fakeSpec) ExtractorVersion() int                             { return 1 }
func (fakeSpec) ModuleOf(p string, _ *Facts, _ RepoContext) string { return path.Dir(p) }
func (fakeSpec) ResolveImport(spec, _ string, ctx RepoContext) ImportTarget {
	if ctx.DirExists(spec) {
		return ImportTarget{Kind: TargetModule, Path: spec, Module: spec}
	}
	return ImportTarget{Kind: TargetExternal, Module: spec}
}

func TestLanguageSpecContract(t *testing.T) {
	var s LanguageSpec = fakeSpec{}
	repo := NewMemRepo(map[string]string{"internal/a/a.go": "package a"})
	if got := s.ResolveImport("internal/a", "main.go", repo); !got.Resolved() || got.Path != "internal/a" {
		t.Fatalf("got %+v", got)
	}
	if got := s.ResolveImport("fmt", "main.go", repo); got.Kind != TargetExternal {
		t.Fatalf("got %+v", got)
	}
	if s.ModuleOf("internal/a/a.go", nil, repo) != "internal/a" {
		t.Fatal("ModuleOf")
	}
}
