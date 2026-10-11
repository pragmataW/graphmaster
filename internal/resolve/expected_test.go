package resolve

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

// expectation is one line of testdata/fixture-repo/expected_edges.txt.
type expectation = testutil.ExpectedEdge

// loadExpectations parses the section for lang.
func loadExpectations(t *testing.T, lang string) []expectation {
	t.Helper()
	out, err := testutil.LoadExpectedEdges(lang)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// checkExpectations verifies resolver expectations against the edges of r.
// imports/includes lines belong to the indexer and are skipped.
func checkExpectations(t *testing.T, r *testRepo, exps []expectation) {
	t.Helper()
	es := r.edges()
	for _, x := range exps {
		if x.Kind == model.EdgeImports || x.Kind == model.EdgeIncludes {
			continue
		}
		var match []resolvedEdge
		if name, ok := x.External(); ok {
			refs := edgesFrom(es, x.Src, name)
			if len(refs) == 0 {
				t.Errorf("line %d: %v: no reference named %q from %s", x.Line, x, name, x.Src)
			}
			for _, e := range refs {
				if e.Dst != "" {
					t.Errorf("line %d: %v: external reference resolved to %s (conf %d)", x.Line, x, e.Dst, e.Conf)
				}
			}
			continue
		}
		for _, e := range es {
			if e.Kind == x.Kind && e.Src == x.Src && e.Dst == x.Dst {
				match = append(match, e)
			}
		}
		switch {
		case x.Negative:
			if len(match) > 0 {
				t.Errorf("line %d: unexpected edge %v (conf %d)", x.Line, x, match[0].Conf)
			}
		case len(match) == 0:
			if x.Optional {
				t.Logf("line %d: optional edge missing: %v", x.Line, x)
			} else {
				t.Errorf("line %d: missing edge %v; edges from src: %v", x.Line, x, describeFrom(es, x.Src))
			}
		case match[0].Conf < x.MinConf:
			if x.Optional {
				t.Logf("line %d: optional edge %v has conf %d", x.Line, x, match[0].Conf)
			} else {
				t.Errorf("line %d: edge %v has conf %d", x.Line, x, match[0].Conf)
			}
		}
	}
}

func describeFrom(es []resolvedEdge, src string) string {
	var b strings.Builder
	for _, e := range es {
		if e.Src == src {
			fmt.Fprintf(&b, "\n  %v %s -> %q conf=%d", e.Kind, e.Name, e.Dst, e.Conf)
		}
	}
	return b.String()
}
