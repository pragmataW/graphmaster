package model

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// sampleFacts models:
//
//	namespace outer { class Cls { void m(); }; void ns::Other::n() {} }
func sampleFacts() *Facts {
	f := &Facts{Lang: LangCPP}
	outer := f.AddDef(Def{Kind: KindNamespace, Name: "outer", Parent: NoParent})
	cls := f.AddDef(Def{Kind: KindClass, Name: "Cls", Parent: outer})
	m := f.AddDef(Def{Kind: KindMethod, Name: "m", Parent: cls, Flags: FlagDeclaration})
	n := f.AddDef(Def{Kind: KindMethod, Name: "n", Parent: outer, Scope: []string{"ns", "Other"}})
	f.Refs = append(f.Refs, Ref{Kind: RefCall, Name: "m", Receiver: "this", Enclosing: n, Line: 3})
	f.Inherits = append(f.Inherits, Inherit{Child: cls, Kind: EdgeExtends, Name: "Base", Qualifier: "outer"})
	f.Tests = append(f.Tests, TestMark{Def: m, Target: "m"})
	return f
}

func TestAddDefReturnsIndex(t *testing.T) {
	f := sampleFacts()
	if len(f.Defs) != 4 {
		t.Fatalf("defs = %d", len(f.Defs))
	}
	if f.Defs[2].Parent != 1 || f.Defs[3].Parent != 0 {
		t.Fatal("parents wired wrong")
	}
}

func TestQNameParts(t *testing.T) {
	f := sampleFacts()
	cases := []struct {
		i    int32
		want []string
	}{
		{0, []string{"outer"}},
		{1, []string{"outer", "Cls"}},
		{2, []string{"outer", "Cls", "m"}},
		{3, []string{"outer", "ns", "Other", "n"}},
	}
	for _, c := range cases {
		if got := f.QNameParts(c.i); !reflect.DeepEqual(got, c.want) {
			t.Errorf("QNameParts(%d) = %v, want %v", c.i, got, c.want)
		}
	}
	if f.QNameParts(-1) != nil || f.QNameParts(99) != nil {
		t.Fatal("out-of-range index must return nil")
	}
}

func TestGoMethodScope(t *testing.T) {
	// func (s *Server) Start() — receiver type becomes scope, no parent def.
	f := &Facts{Lang: LangGo, Package: "server"}
	i := f.AddDef(Def{Kind: KindMethod, Name: "Start", Parent: NoParent, Scope: []string{"Server"}})
	if got := f.QNameParts(i); !reflect.DeepEqual(got, []string{"Server", "Start"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFactsValidate(t *testing.T) {
	if err := sampleFacts().Validate(); err != nil {
		t.Fatalf("valid facts rejected: %v", err)
	}
	if err := (&Facts{}).Validate(); err != nil {
		t.Fatalf("empty facts rejected: %v", err)
	}

	cases := map[string]func(f *Facts){
		"forward parent":     func(f *Facts) { f.Defs[0].Parent = 2 },
		"self parent":        func(f *Facts) { f.Defs[1].Parent = 1 },
		"parent below -1":    func(f *Facts) { f.Defs[1].Parent = -2 },
		"empty name":         func(f *Facts) { f.Defs[1].Name = "" },
		"unknown kind":       func(f *Facts) { f.Defs[1].Kind = KindUnknown },
		"bad span":           func(f *Facts) { f.Defs[1].StartLine, f.Defs[1].EndLine = 5, 4 },
		"bad bytes":          func(f *Facts) { f.Defs[1].StartByte, f.Defs[1].EndByte = 9, 3 },
		"long sig":           func(f *Facts) { f.Defs[1].Sig = strings.Repeat("x", MaxSigLen+1) },
		"ref enclosing":      func(f *Facts) { f.Refs[0].Enclosing = 10 },
		"ref no name":        func(f *Facts) { f.Refs[0].Name = "" },
		"ref kind":           func(f *Facts) { f.Refs[0].Kind = 0 },
		"inherit child":      func(f *Facts) { f.Inherits[0].Child = -1 },
		"inherit kind":       func(f *Facts) { f.Inherits[0].Kind = EdgeCalls },
		"inherit no name":    func(f *Facts) { f.Inherits[0].Name = "" },
		"test def":           func(f *Facts) { f.Tests[0].Def = 7 },
		"import empty spec":  func(f *Facts) { f.Imports = []Import{{Spec: ""}} },
		"invalid lang value": func(f *Facts) { f.Lang = Lang(99) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := sampleFacts()
			mutate(f)
			if err := f.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNormalizeSig(t *testing.T) {
	cases := []struct{ in, want string }{
		{"func  Foo(a int,\n\tb string) error", "func Foo(a int, b string) error"},
		{"  def x():  ", "def x():"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeSig(c.in); got != c.want {
			t.Errorf("NormalizeSig(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	long := strings.Repeat("ab ", 200)
	got := NormalizeSig(long)
	if len(got) > MaxSigLen {
		t.Fatalf("len = %d > %d", len(got), MaxSigLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated sig must end with ellipsis: %q", got)
	}

	// Multi-byte runes must never be split.
	multi := strings.Repeat("ğ", 300)
	got = NormalizeSig(multi)
	if !utf8.ValidString(got) || len(got) > MaxSigLen {
		t.Fatalf("invalid truncation: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}
