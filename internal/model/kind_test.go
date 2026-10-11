package model

import "testing"

func TestNodeKindRoundTrip(t *testing.T) {
	kinds := AllNodeKinds()
	if len(kinds) < 18 {
		t.Fatalf("expected at least 18 node kinds, got %d", len(kinds))
	}
	for _, k := range kinds {
		got, err := ParseNodeKind(k.String())
		if err != nil || got != k {
			t.Fatalf("ParseNodeKind(%q) = %v, %v", k.String(), got, err)
		}
	}
	if _, err := ParseNodeKind("bogus"); err == nil {
		t.Fatal("expected error")
	}
}

func TestNodeKindStableValues(t *testing.T) {
	want := map[NodeKind]uint8{
		KindUnknown: 0, KindPackage: 1, KindModule: 2, KindFile: 3, KindNamespace: 4,
		KindClass: 5, KindStruct: 6, KindInterface: 7, KindTrait: 8, KindEnum: 9,
		KindType: 10, KindFunction: 11, KindMethod: 12, KindConstructor: 13,
		KindField: 14, KindVariable: 15, KindConstant: 16, KindMacro: 17, KindTest: 18,
		KindEnumMember: 19, KindProperty: 20,
	}
	for k, v := range want {
		if uint8(k) != v {
			t.Errorf("%v = %d, want %d", k, uint8(k), v)
		}
	}
}

func TestNodeKindPredicates(t *testing.T) {
	cases := []struct {
		k                      NodeKind
		typ, callable, contain bool
	}{
		{KindClass, true, false, true},
		{KindStruct, true, false, true},
		{KindInterface, true, false, true},
		{KindTrait, true, false, true},
		{KindEnum, true, false, true},
		{KindType, true, false, false},
		{KindFunction, false, true, false},
		{KindMethod, false, true, false},
		{KindConstructor, false, true, false},
		{KindTest, false, true, false},
		{KindMacro, false, true, false},
		{KindPackage, false, false, true},
		{KindNamespace, false, false, true},
		{KindModule, false, false, true},
		{KindFile, false, false, true},
		{KindField, false, false, false},
		{KindVariable, false, false, false},
	}
	for _, c := range cases {
		if c.k.IsType() != c.typ || c.k.IsCallable() != c.callable || c.k.IsContainer() != c.contain {
			t.Errorf("%v: IsType=%v IsCallable=%v IsContainer=%v", c.k, c.k.IsType(), c.k.IsCallable(), c.k.IsContainer())
		}
	}
}

func TestEdgeKindRoundTrip(t *testing.T) {
	want := []string{"contains", "calls", "references", "imports", "extends", "implements", "embeds", "tests", "includes", "declares"}
	got := AllEdgeKinds()
	if len(got) != len(want) {
		t.Fatalf("AllEdgeKinds() len = %d, want %d", len(got), len(want))
	}
	for i, k := range got {
		if k.String() != want[i] {
			t.Errorf("edge kind %d = %q, want %q", i, k.String(), want[i])
		}
		if uint8(k) != uint8(i+1) {
			t.Errorf("edge kind %q = %d, want %d", k, uint8(k), i+1)
		}
		p, err := ParseEdgeKind(want[i])
		if err != nil || p != k {
			t.Errorf("ParseEdgeKind(%q) = %v, %v", want[i], p, err)
		}
	}
	if !EdgeExtends.IsInheritance() || !EdgeImplements.IsInheritance() || !EdgeEmbeds.IsInheritance() {
		t.Fatal("inheritance edge kinds misclassified")
	}
	if EdgeCalls.IsInheritance() {
		t.Fatal("calls is not inheritance")
	}
}

func TestRefKind(t *testing.T) {
	for _, k := range []RefKind{RefCall, RefType, RefMember} {
		p, err := ParseRefKind(k.String())
		if err != nil || p != k {
			t.Fatalf("ParseRefKind(%q) = %v, %v", k, p, err)
		}
	}
	if RefCall.EdgeKind() != EdgeCalls || RefType.EdgeKind() != EdgeReferences || RefMember.EdgeKind() != EdgeReferences {
		t.Fatal("RefKind.EdgeKind mapping wrong")
	}
}

func TestFileStatus(t *testing.T) {
	want := []string{"ok", "too_big", "generated", "minified", "binary", "parse_err", "unsupported"}
	for i, s := range want {
		st := FileStatus(i)
		if st.String() != s {
			t.Errorf("FileStatus(%d) = %q, want %q", i, st, s)
		}
		p, err := ParseFileStatus(s)
		if err != nil || p != st {
			t.Errorf("ParseFileStatus(%q) = %v, %v", s, p, err)
		}
	}
	if !StatusOK.Indexed() || !StatusParseErr.Indexed() {
		t.Fatal("ok and parse_err files keep their node rows")
	}
	if StatusBinary.Indexed() || StatusTooBig.Indexed() {
		t.Fatal("binary/too_big files are not parsed")
	}
}

func TestDefFlags(t *testing.T) {
	f := FlagExported | FlagAsync
	if !f.Has(FlagExported) || !f.Has(FlagAsync) || f.Has(FlagTest) {
		t.Fatal("Has broken")
	}
	if f.String() != "exported|async" {
		t.Fatalf("String = %q", f.String())
	}
	if DefFlags(0).String() != "" {
		t.Fatal("zero flags must render empty")
	}
}
