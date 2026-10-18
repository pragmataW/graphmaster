package mcp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/query"
)

func TestRewriteArgs(t *testing.T) {
	find := argsOf(reflect.TypeFor[query.FindSymbolInput]())
	read := argsOf(reflect.TypeFor[query.ReadSymbolInput]())
	outline := argsOf(reflect.TypeFor[query.FileOutlineInput]())
	traverse := argsOf(reflect.TypeFor[query.TraverseInput]())
	cases := []struct {
		args toolArgs
		in   string
		want string // "" = unchanged
	}{
		{find, `{"name":"Foo","limit":3}`, `{"limit":3,"query":"Foo"}`},
		{find, `{"query":"Foo"}`, ""},
		{find, `{"name":"Foo","query":"Bar"}`, ""}, // target set: left for the SDK to reject
		{find, `{"bogus":1}`, ""},
		{read, `{"qname":"a.B"}`, `{"symbol":"a.B"}`},
		{read, `{"name":"a.B","qname":"c.D"}`, `{"symbol":"a.B"}` + "|qname"},
		{outline, `{"file_path":"x.go"}`, `{"path":"x.go"}`},
		{outline, `{"name":"x.go"}`, ""}, // no main argument
		{traverse, `{"symbol":"main"}`, `{"start":"main"}`},
		{find, `not json`, ""},
		{find, ``, ""},
	}
	for _, c := range cases {
		got := string(rewriteArgs(c.args, []byte(c.in)))
		want, keep, _ := strings.Cut(c.want, "|")
		if keep != "" {
			// The first alias in key order wins; the other stays and is
			// rejected by the SDK.
			if !strings.Contains(got, `"symbol":"a.B"`) || !strings.Contains(got, `"`+keep+`"`) {
				t.Errorf("rewriteArgs(%s) = %s, want symbol from name and %s kept", c.in, got, keep)
			}
			continue
		}
		if got != want {
			t.Errorf("rewriteArgs(%s) = %s, want %s", c.in, got, want)
		}
	}
}

func TestAliasedCall(t *testing.T) {
	cs, _ := connect(t, sharedFixture(t))
	mustContain(t, ok(t, cs, "find_symbol", `{"name":"FormatID"}`), "util.FormatID")
	mustContain(t, ok(t, cs, "file_outline", `{"file_path":"go/internal/util/format.go"}`), "FormatID")
	res := call(t, cs, "find_symbol", `{"bogus":"x","query":"FormatID"}`)
	if !res.IsError {
		t.Errorf("unknown argument accepted: %s", text(t, res))
	}
}
