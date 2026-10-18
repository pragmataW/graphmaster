package index

import (
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
)

func TestQNameJoin(t *testing.T) {
	cases := []struct {
		lang   model.Lang
		module string
		parts  []string
		want   string
	}{
		{model.LangJavaScript, "", []string{"res", ".json(obj)", "should send"}, "res.json(obj).should send"},
		{model.LangJavaScript, "", []string{"a.", "b"}, "a.b"},
		{model.LangJavaScript, "", []string{"Calc", "add"}, "Calc.add"},
		{model.LangRust, "rg", []string{"Searcher", "search"}, "rg::Searcher::search"},
		{model.LangRust, "rg", nil, "rg"},
	}
	for _, c := range cases {
		if got := QName(c.lang, c.module, c.parts); got != c.want {
			t.Errorf("QName(%v, %q, %q) = %q, want %q", c.lang, c.module, c.parts, got, c.want)
		}
	}
}
