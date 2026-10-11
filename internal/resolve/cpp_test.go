package resolve

import "testing"

func TestCPPFixtureExpectedEdges(t *testing.T) {
	r := fullBuild(t, cfamFixture(t, "cpp"), nil)
	exps := loadExpectations(t, "cpp")
	if len(exps) < 25 {
		t.Fatalf("only %d C++ expectations loaded", len(exps))
	}
	edges, includes, declares := cfamSplitStructural(exps)
	// Declares lines hold both as stored edges and through
	// DeclarationResolver directly.
	checkExpectations(t, r, append(edges, declares...))
	cfamCheckIncludes(t, r, includes)
	cfamCheckDeclares(t, r, declares)
}

// cfamEdgeTo finds the edges of reference name from src.
func cfamEdgeTo(r *testRepo, src, name string) []resolvedEdge {
	return edgesFrom(r.edges(), src, name)
}

func TestCPPReceiversAndScopes(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"p/include/w.hpp": `#pragma once
#include <memory>
#include <vector>
namespace w {
class Base {
public:
    virtual int run() { return 1; }
};
class Widget : public Base {
public:
    int size() const { return n_; }
    int grow();
    int twice() { return size() * 2; }
private:
    int n_ = 0;
    std::vector<int> items_;
};
using WidgetPtr = std::unique_ptr<Widget>;
}
`,
		"p/src/w.cpp": `#include "w.hpp"
namespace w {
int Widget::grow() {
    items_.push_back(1);
    return this->size() + size() + run();
}
}
`,
		"p/src/main.cpp": `#include <memory>
#include "w.hpp"
int main() {
    auto p = std::make_unique<w::Widget>();
    w::WidgetPtr q;
    w::Widget* raw = new w::Widget();
    return p->size() + q->twice() + raw->run() + p->grow();
}
`,
	}, nil)
	cases := []struct {
		src, name, dst string
		conf           int
	}{
		{"p/src/w.cpp::w.Widget.grow", "size", "p/include/w.hpp::w.Widget.size", 80},
		{"p/src/w.cpp::w.Widget.grow", "run", "p/include/w.hpp::w.Base.run", 80},
		{"p/src/w.cpp::w.Widget.grow", "push_back", "", 0},
		{"p/include/w.hpp::w.Widget.twice", "size", "p/include/w.hpp::w.Widget.size", 90},
		{"p/src/main.cpp::main", "size", "p/include/w.hpp::w.Widget.size", 80},
		{"p/src/main.cpp::main", "twice", "p/include/w.hpp::w.Widget.twice", 80},
		{"p/src/main.cpp::main", "run", "p/include/w.hpp::w.Base.run", 80},
		{"p/src/main.cpp::main", "make_unique", "", 0},
	}
	for _, c := range cases {
		es := cfamEdgeTo(r, c.src, c.name)
		if len(es) == 0 {
			t.Errorf("%s: no %s reference", c.src, c.name)
			continue
		}
		for _, e := range es {
			if e.Dst != c.dst || e.Conf < c.conf {
				t.Errorf("%s -> %s: got %q conf %d, want %q conf>=%d", c.src, c.name, e.Dst, e.Conf, c.dst, c.conf)
			}
		}
	}
	// Widget::grow is declared in the class and defined out of line.
	for _, e := range cfamEdgeTo(r, "p/src/main.cpp::main", "grow") {
		if e.Dst != "p/src/w.cpp::w.Widget.grow" && e.Dst != "p/include/w.hpp::w.Widget.grow" {
			t.Errorf("grow -> %q", e.Dst)
		}
	}
}

func TestCPPExternalAndQualified(t *testing.T) {
	r := fullBuild(t, map[string]string{
		"p/a.cpp": `#include <algorithm>
namespace fs = std::filesystem;
using std::max;
namespace n { namespace detail { int calc(int x) { return x; } } }
int min(int a, int b) { return a; }
int use() {
    fs::path p;
    return std::min(1, 2) + max(1, 2) + n::detail::calc(3) + detail::calc(4);
}
`,
		"p/b.cpp": `int max(int a, int b) { return b; }
`,
	}, nil)
	for name, want := range map[string]string{"min": "", "max": "", "calc": "p/a.cpp::n.detail.calc"} {
		for _, e := range cfamEdgeTo(r, "p/a.cpp::use", name) {
			if e.Dst != want {
				t.Errorf("%s (line %d) -> %q conf %d, want %q", name, e.Line, e.Dst, e.Conf, want)
			}
		}
	}
}
