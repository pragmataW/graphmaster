package parse

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

// normalizeFacts maps empty slices to nil, the only representational
// difference the codec does not preserve.
func normalizeFacts(f *model.Facts) *model.Facts {
	c := *f
	if len(c.Defs) == 0 {
		c.Defs = nil
	} else {
		c.Defs = append([]model.Def(nil), c.Defs...)
		for i := range c.Defs {
			if len(c.Defs[i].Scope) == 0 {
				c.Defs[i].Scope = nil
			}
		}
	}
	if len(c.Refs) == 0 {
		c.Refs = nil
	}
	if len(c.Imports) == 0 {
		c.Imports = nil
	} else {
		c.Imports = append([]model.Import(nil), c.Imports...)
		for i := range c.Imports {
			if len(c.Imports[i].Names) == 0 {
				c.Imports[i].Names = nil
			}
		}
	}
	if len(c.Inherits) == 0 {
		c.Inherits = nil
	}
	if len(c.Tests) == 0 {
		c.Tests = nil
	}
	return &c
}

func assertRoundTrip(t *testing.T, f *model.Facts) []byte {
	t.Helper()
	enc := EncodeFacts(f)
	got, err := DecodeFacts(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := normalizeFacts(f); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\nwant %+v\n got %+v", want, got)
	}
	return enc
}

func TestCodecRoundTripEmpty(t *testing.T) {
	assertRoundTrip(t, &model.Facts{})
	assertRoundTrip(t, &model.Facts{Lang: model.LangGo, Package: "p", TestFile: true})
}

func TestCodecRoundTripGolden(t *testing.T) {
	eng := NewEngine()
	defer eng.Close()
	paths, _ := filepath.Glob(filepath.Join(goldenRoot, "go", "*.go"))
	if len(paths) == 0 {
		t.Fatal("no golden Go sources")
	}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		res, err := eng.ParseLang(context.Background(), model.LangGo, src)
		if err != nil {
			t.Fatal(err)
		}
		enc := assertRoundTrip(t, res.Facts)
		t.Logf("%s: %d bytes source -> %d bytes facts", filepath.Base(p), len(src), len(enc))
	}
}

func TestCodecRoundTripExtremes(t *testing.T) {
	f := &model.Facts{
		Lang: model.Lang(255),
		Defs: []model.Def{
			{Kind: model.NodeKind(255), Name: "", Parent: math.MinInt32, Scope: []string{"", "a"},
				Span: model.Span{StartLine: math.MaxUint32, StartCol: math.MaxUint32, EndLine: 0, EndCol: 0,
					StartByte: math.MaxUint32, EndByte: 0}, Flags: math.MaxUint16},
			{Kind: 1, Name: "x", Parent: math.MaxInt32, Span: model.Span{}},
		},
		Refs:     []model.Ref{{Kind: 255, Enclosing: math.MinInt32, Line: math.MaxUint32, Col: math.MaxUint32}, {Line: 0}},
		Imports:  []model.Import{{Spec: "s", Names: []string{"a", "a", "b"}, Wildcard: true, Line: math.MaxUint32}},
		Inherits: []model.Inherit{{Child: -5, Kind: 255, Name: "n", Line: 7, Col: 1}},
		Tests:    []model.TestMark{{Def: math.MaxInt32, Target: "héllo"}},
	}
	assertRoundTrip(t, f)
}

func randString(r *rand.Rand, pool []string) string { return pool[r.IntN(len(pool))] }

func randFacts(r *rand.Rand) *model.Facts {
	pool := []string{"", "a", "Foo", "fmt", "s.db", "日本", "x\x00y", "Server"}
	u32 := func() uint32 {
		if r.IntN(4) == 0 {
			return r.Uint32()
		}
		return uint32(r.IntN(500))
	}
	i32 := func() int32 { return int32(r.Uint32()) }
	f := &model.Facts{Lang: model.Lang(r.IntN(256)), Package: randString(r, pool), TestFile: r.IntN(2) == 0}
	for range r.IntN(20) {
		var scope []string
		for range r.IntN(3) {
			scope = append(scope, randString(r, pool))
		}
		f.Defs = append(f.Defs, model.Def{
			Kind: model.NodeKind(r.IntN(256)), Name: randString(r, pool), Parent: i32(), Scope: scope,
			Span: model.Span{StartLine: u32(), StartCol: u32(), EndLine: u32(), EndCol: u32(), StartByte: u32(), EndByte: u32()},
			Sig:  randString(r, pool), Flags: model.DefFlags(r.IntN(1 << 16)),
		})
	}
	for range r.IntN(30) {
		f.Refs = append(f.Refs, model.Ref{Kind: model.RefKind(r.IntN(256)), Name: randString(r, pool),
			Qualifier: randString(r, pool), Receiver: randString(r, pool), Enclosing: i32(), Line: u32(), Col: u32()})
	}
	for range r.IntN(5) {
		var names []string
		for range r.IntN(3) {
			names = append(names, randString(r, pool))
		}
		f.Imports = append(f.Imports, model.Import{Spec: randString(r, pool), Alias: randString(r, pool),
			Names: names, Wildcard: r.IntN(2) == 0, Line: u32()})
	}
	for range r.IntN(5) {
		f.Inherits = append(f.Inherits, model.Inherit{Child: i32(), Kind: model.EdgeKind(r.IntN(256)),
			Name: randString(r, pool), Qualifier: randString(r, pool), Line: u32(), Col: u32()})
	}
	for range r.IntN(5) {
		f.Tests = append(f.Tests, model.TestMark{Def: i32(), Target: randString(r, pool)})
	}
	return f
}

func TestCodecRoundTripRandom(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		assertRoundTrip(t, randFacts(r))
	}
}

func TestCodecRejectsCorruptInput(t *testing.T) {
	good := EncodeFacts(&model.Facts{Lang: model.LangGo, Package: "p",
		Defs: []model.Def{{Kind: model.KindFunction, Name: "F", Parent: model.NoParent}}})
	cases := map[string][]byte{
		"empty":       nil,
		"bad magic":   []byte("XX\x01"),
		"bad version": []byte("GF\x09"),
		"truncated":   good[:len(good)-1],
		"trailing":    append(append([]byte(nil), good...), 0),
		"huge count":  []byte("GF\x01\x01\x00\x00\x00\xff\xff\xff\xff\x0f"),
		"bad str ref": []byte("GF\x01\x01\x05"),
	}
	for name, data := range cases {
		if _, err := DecodeFacts(data); !errors.Is(err, ErrCorruptFacts) {
			t.Errorf("%s: err = %v, want ErrCorruptFacts", name, err)
		}
	}
}

func FuzzDecodeFacts(f *testing.F) {
	f.Add(EncodeFacts(&model.Facts{}))
	f.Add(EncodeFacts(randFacts(rand.New(rand.NewPCG(3, 4)))))
	f.Add([]byte("GF\x01"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := DecodeFacts(data)
		if err != nil {
			if !errors.Is(err, ErrCorruptFacts) {
				t.Fatalf("error does not wrap ErrCorruptFacts: %v", err)
			}
			return
		}
		// Anything that decodes must re-encode to an equivalent value.
		again, err := DecodeFacts(EncodeFacts(got))
		if err != nil {
			t.Fatalf("re-decode: %v", err)
		}
		if !reflect.DeepEqual(again, got) {
			t.Fatalf("re-encode mismatch\n%+v\n%+v", got, again)
		}
	})
}

func FuzzFactsRoundTrip(f *testing.F) {
	f.Add(uint64(1), uint64(2))
	f.Fuzz(func(t *testing.T, a, b uint64) {
		assertRoundTrip(t, randFacts(rand.New(rand.NewPCG(a, b))))
	})
}

func BenchmarkEncodeFacts(b *testing.B) {
	f := randFacts(rand.New(rand.NewPCG(5, 6)))
	for b.Loop() {
		_ = EncodeFacts(f)
	}
}
