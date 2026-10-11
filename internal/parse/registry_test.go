package parse

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/pragmataW/graphmaster/internal/model"
)

// fakeSpec is a minimal spec for registry and compile tests.
type fakeSpec struct {
	lang  model.Lang
	exts  []string
	query string
}

func (s fakeSpec) Lang() model.Lang                                        { return s.lang }
func (s fakeSpec) Extensions() []string                                    { return s.exts }
func (s fakeSpec) Grammar() unsafe.Pointer                                 { return goSpec{}.Grammar() }
func (s fakeSpec) QuerySource() string                                     { return s.query }
func (s fakeSpec) ExtractorVersion() int                                   { return 1 }
func (s fakeSpec) ModuleOf(string, *model.Facts, model.RepoContext) string { return "" }
func (s fakeSpec) ResolveImport(string, string, model.RepoContext) model.ImportTarget {
	return model.ImportTarget{}
}

func TestRegistryGo(t *testing.T) {
	spec, ok := Lookup(model.LangGo)
	if !ok || spec.Lang() != model.LangGo {
		t.Fatal("Go not registered")
	}
	for _, p := range []string{"a/b.go", `a\b.go`, "x_test.go", "B.GO"} {
		if s, ok := ForPath(p); !ok || s.Lang() != model.LangGo {
			t.Errorf("ForPath(%q) = %v, %v", p, s, ok)
		}
	}
	for _, p := range []string{"Makefile", ".go", "a.py.txt", "dir.go/file"} {
		if s, ok := ForPath(p); ok && s.Lang() == model.LangGo {
			t.Errorf("ForPath(%q) unexpectedly Go", p)
		}
	}
	if s, ok := ForExt(".go"); !ok || s.Lang() != model.LangGo {
		t.Error("ForExt(.go)")
	}
	found := false
	for _, l := range Langs() {
		found = found || l == model.LangGo
	}
	if !found {
		t.Error("Langs() missing Go")
	}
	if !IsTestPath("pkg/a_test.go") || IsTestPath("pkg/a.go") || IsTestPath("README.md") {
		t.Error("IsTestPath")
	}
}

func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q", want)
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic %v, want %q", r, want)
		}
	}()
	fn()
}

// freeLang returns a language with no registered spec, so these tests keep
// working as sibling language units register theirs.
func freeLang(t *testing.T) model.Lang {
	t.Helper()
	for _, l := range model.AllLangs() {
		if _, ok := Lookup(l); !ok {
			return l
		}
	}
	t.Skip("all languages registered")
	return model.LangUnknown
}

func TestRegisterRejectsBadSpecs(t *testing.T) {
	mustPanic(t, "invalid language", func() { Register(fakeSpec{lang: model.LangUnknown}) })
	mustPanic(t, "duplicate language", func() { Register(fakeSpec{lang: model.LangGo}) })
	free := freeLang(t)
	mustPanic(t, "bad extension", func() { Register(fakeSpec{lang: free, exts: []string{"zz"}}) })
	mustPanic(t, "already claimed", func() { Register(fakeSpec{lang: free, exts: []string{".go"}}) })
	if _, ok := Lookup(free); ok {
		t.Fatal("failed registration must not leave a partial entry")
	}
}

func TestRegisterAndUnregister(t *testing.T) {
	free := freeLang(t)
	Register(fakeSpec{lang: free, exts: []string{".zzfake"}})
	defer unregister(free)
	if s, ok := ForPath("x.zzfake"); !ok || s.Lang() != free {
		t.Fatal("registered fake spec not found by path")
	}
}

func TestCompileErrors(t *testing.T) {
	cases := map[string]string{
		"syntax":          "(function_declaration",
		"unknown capture": "(identifier) @bogus",
		"bad def kind":    "(identifier) @def.nonsense",
		"bad ref kind":    "(identifier) @ref.nonsense",
		"bad inherit":     "(identifier) @inherit.calls",
	}
	for name, q := range cases {
		if _, err := compileSpec(fakeSpec{lang: model.LangGo, query: q}); err == nil {
			t.Errorf("%s: expected compile error", name)
		}
	}
	if _, err := compileSpec(fakeSpec{lang: model.LangGo, query: "(identifier) @_helper @name"}); err != nil {
		t.Errorf("helper captures must be accepted: %v", err)
	}
}

func TestParseCaptureRoles(t *testing.T) {
	c, err := parseCapture("def.enum_member")
	if err != nil || c.role != roleDef || c.defKind != model.KindEnumMember {
		t.Fatalf("def.enum_member -> %+v, %v", c, err)
	}
	c, err = parseCapture("inherit.implements")
	if err != nil || c.role != roleInherit || c.inherit != model.EdgeImplements {
		t.Fatalf("inherit.implements -> %+v, %v", c, err)
	}
	c, err = parseCapture("ref.member")
	if err != nil || c.role != roleRef || c.refKind != model.RefMember {
		t.Fatalf("ref.member -> %+v, %v", c, err)
	}
}
