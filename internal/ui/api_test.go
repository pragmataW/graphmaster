package ui

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestSearch(t *testing.T) {
	s, _ := fixtureServer(t)
	res := getJSON[SearchResponse](t, s, "/api/search", url.Values{"q": {"NewMemory"}}, http.StatusOK)
	if res.Banner == "" || len(res.Symbols) == 0 {
		t.Fatalf("search NewMemory: %+v", res)
	}
	first := res.Symbols[0]
	if first.Name != "NewMemory" || first.Kind != "function" || first.Path != "go/internal/store/store.go" ||
		first.Start == 0 || first.End < first.Start || len(first.SKey) != 16 {
		t.Fatalf("exact match not first: %+v", first)
	}

	// Substring matches and file paths.
	res = getJSON[SearchResponse](t, s, "/api/search", url.Values{"q": {"store"}}, http.StatusOK)
	if len(res.Files) == 0 || !strings.Contains(res.Files[0].Path, "store") {
		t.Fatalf("search store: files %+v", res.Files)
	}

	// Limits are honoured and reported.
	res = getJSON[SearchResponse](t, s, "/api/search", url.Values{"q": {"put"}, "limit": {"1"}}, http.StatusOK)
	if len(res.Symbols) != 1 || !res.More {
		t.Fatalf("limit 1: %d symbols, more=%v", len(res.Symbols), res.More)
	}

	res = getJSON[SearchResponse](t, s, "/api/search", url.Values{"q": {"zzqqxxnothing"}}, http.StatusOK)
	if len(res.Symbols) != 0 || len(res.Files) != 0 {
		t.Fatalf("nonsense query matched: %+v", res)
	}
	getJSON[ErrorResponse](t, s, "/api/search", nil, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/search", url.Values{"q": {"x"}, "limit": {"abc"}}, http.StatusBadRequest)
}

func TestNode(t *testing.T) {
	s, _ := fixtureServer(t)
	id := findSymbol(t, s, "Put", "Audited.Put")
	res := getJSON[NodeResponse](t, s, "/api/node", url.Values{"id": {strconv.FormatInt(id, 10)}}, http.StatusOK)
	if res.Symbol.ID != id || res.Symbol.Kind != "method" {
		t.Fatalf("node: %+v", res.Symbol)
	}
	if res.Parent == nil || res.Parent.Name != "Audited" {
		t.Fatalf("parent: %+v", res.Parent)
	}
	if res.Source == nil || res.Source.Start != res.Symbol.Start || len(res.Source.Lines) == 0 ||
		!strings.Contains(res.Source.Lines[0], "func (a *Audited) Put") {
		t.Fatalf("source: %+v", res.Source)
	}
	if res.Out["calls"] == 0 {
		t.Fatalf("Audited.Put calls Memory.Put; out = %v", res.Out)
	}
	if res.In["calls"] == 0 {
		t.Fatalf("main calls Audited.Put; in = %v", res.In)
	}

	// Lookup by skey gives the same symbol; containers list children.
	bySKey := getJSON[NodeResponse](t, s, "/api/node", url.Values{"skey": {res.Symbol.SKey}}, http.StatusOK)
	if bySKey.Symbol.ID != id {
		t.Fatalf("skey lookup: %+v", bySKey.Symbol)
	}
	parent := getJSON[NodeResponse](t, s, "/api/node", url.Values{"id": {strconv.FormatInt(res.Parent.ID, 10)}}, http.StatusOK)
	found := false
	for _, c := range parent.Children {
		found = found || c.ID == id
	}
	if !found {
		t.Fatalf("Audited children lack Put: %+v", parent.Children)
	}

	getJSON[ErrorResponse](t, s, "/api/node", nil, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/node", url.Values{"id": {"x"}}, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/node", url.Values{"id": {"999999999"}}, http.StatusNotFound)
	getJSON[ErrorResponse](t, s, "/api/node", url.Values{"skey": {"00000000000000ff"}}, http.StatusNotFound)
}

func TestEgo(t *testing.T) {
	s, _ := fixtureServer(t)
	id := findSymbol(t, s, "NewMemory", "NewMemory")
	center := "n" + strconv.FormatInt(id, 10)
	res := getJSON[EgoResponse](t, s, "/api/ego", url.Values{"id": {strconv.FormatInt(id, 10)}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if res.Center != center {
		t.Fatalf("center %q, want %q", res.Center, center)
	}
	nodes := nodesByLabel(res.Elements)
	for _, want := range []string{"NewMemory", "NewAudited", "TestPlaceOrder"} {
		if _, ok := nodes[want]; !ok {
			t.Errorf("ego of NewMemory lacks %s: %v", want, keys(nodes))
		}
	}
	if !strings.Contains(nodes["NewMemory"].Classes, "center") {
		t.Errorf("center node classes %q", nodes["NewMemory"].Classes)
	}
	if _, ok := nodes["main"]; ok {
		t.Error("depth 1 reached main (two hops away)")
	}
	edge := false
	for _, e := range res.Elements {
		if e.Group == "edges" && e.Data.Target == center && e.Data.Source == nodes["NewAudited"].Data.ID && e.Data.Kind == "calls" {
			edge = true
		}
	}
	if !edge {
		t.Error("missing NewAudited -> NewMemory calls edge")
	}

	// Depth 2 reaches main through NewAudited.
	res = getJSON[EgoResponse](t, s, "/api/ego", url.Values{"id": {strconv.FormatInt(id, 10)}, "depth": {"2"}}, http.StatusOK)
	if _, ok := nodesByLabel(res.Elements)["main"]; !ok {
		t.Errorf("depth 2 lacks main")
	}

	// Grouping wraps symbols in their file.
	res = getJSON[EgoResponse](t, s, "/api/ego", url.Values{"id": {strconv.FormatInt(id, 10)}, "group": {"file"}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	file, ok := nodesByLabel(res.Elements)["go/internal/store/store.go"]
	if !ok || nodesByLabel(res.Elements)["NewMemory"].Data.Parent != file.Data.ID {
		t.Errorf("group=file: NewMemory not inside its file")
	}

	// Kind filter.
	res = getJSON[EgoResponse](t, s, "/api/ego", url.Values{"id": {strconv.FormatInt(id, 10)}, "kinds": {"embeds"}}, http.StatusOK)
	if n := len(nodesByLabel(res.Elements)); n != 1 {
		t.Errorf("kinds=embeds: %d nodes, want only the center", n)
	}

	getJSON[ErrorResponse](t, s, "/api/ego", nil, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/ego", url.Values{"id": {"1"}, "kinds": {"bogus"}}, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/ego", url.Values{"id": {"1"}, "depth": {"x"}}, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/ego", url.Values{"id": {"999999999"}}, http.StatusNotFound)
}

func TestFile(t *testing.T) {
	s, _ := fixtureServer(t)
	res := getJSON[FileResponse](t, s, "/api/file", url.Values{"path": {"go/internal/store/store.go"}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if res.File.Path != "go/internal/store/store.go" || res.File.Lang != "go" || res.File.Symbols == 0 {
		t.Fatalf("file info: %+v", res.File)
	}
	nodes := nodesByLabel(res.Elements)
	file := nodes["go/internal/store/store.go"]
	if file.Data.ID == "" || !strings.Contains(file.Classes, "file") {
		t.Fatalf("missing file compound node: %v", keys(nodes))
	}
	if nodes["NewMemory"].Data.Parent != file.Data.ID {
		t.Errorf("NewMemory parent %q, want file %q", nodes["NewMemory"].Data.Parent, file.Data.ID)
	}
	// Methods nest inside their type.
	var put Element
	for _, e := range res.Elements {
		if e.Data.QName != "" && strings.HasSuffix(e.Data.QName, "Audited.Put") {
			put = e
		}
	}
	if put.Data.Parent != nodes["Audited"].Data.ID {
		t.Errorf("Audited.Put parent %q, want Audited %q", put.Data.Parent, nodes["Audited"].Data.ID)
	}
	// Files that use this one appear as aggregated external nodes.
	ext := false
	for _, e := range res.Elements {
		if e.Group == "nodes" && strings.Contains(e.Classes, "ext") && e.Data.Path == "go/cmd/server/main.go" {
			ext = true
		}
	}
	if !ext {
		t.Errorf("main.go (a caller) not shown as an external file")
	}

	getJSON[ErrorResponse](t, s, "/api/file", nil, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/file", url.Values{"path": {"no/such/file.go"}}, http.StatusNotFound)
}

func TestPackages(t *testing.T) {
	s, _ := fixtureServer(t)
	res := getJSON[PackagesResponse](t, s, "/api/packages", nil, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	nodes := nodesByLabel(res.Elements)
	for _, want := range []string{"go/", "python/", "rust/"} {
		n, ok := nodes[want]
		if !ok {
			t.Fatalf("root packages lack %s: %v", want, keys(nodes))
		}
		if n.Data.Kind != "dir" || n.Data.Files == 0 || n.Data.Symbols == 0 {
			t.Errorf("%s aggregate: %+v", want, n.Data)
		}
	}

	res = getJSON[PackagesResponse](t, s, "/api/packages", url.Values{"prefix": {"go/internal"}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if res.Prefix != "go/internal/" || res.Parent != "go/" {
		t.Fatalf("prefix %q parent %q", res.Prefix, res.Parent)
	}
	nodes = nodesByLabel(res.Elements)
	store, service := nodes["store/"], nodes["service/"]
	if store.Data.Path != "go/internal/store/" || service.Data.ID == "" {
		t.Fatalf("go/internal groups: %v", keys(nodes))
	}
	dep := false
	for _, e := range res.Elements {
		if e.Group == "edges" && e.Data.Source == service.Data.ID && e.Data.Target == store.Data.ID && e.Data.Count > 0 {
			dep = true
		}
	}
	if !dep {
		t.Error("missing aggregated service/ -> store/ dependency")
	}

	// A prefix that is a file directory lists its files.
	res = getJSON[PackagesResponse](t, s, "/api/packages", url.Values{"prefix": {"go/internal/store/"}}, http.StatusOK)
	if n, ok := nodesByLabel(res.Elements)["store.go"]; !ok || n.Data.Kind != "file" || n.Data.Path != "go/internal/store/store.go" {
		t.Fatalf("file group: %v", keys(nodesByLabel(res.Elements)))
	}

	res = getJSON[PackagesResponse](t, s, "/api/packages", url.Values{"prefix": {"no/such/"}}, http.StatusOK)
	if len(res.Elements) != 0 {
		t.Fatalf("unknown prefix: %d elements", len(res.Elements))
	}
}

func TestImpact(t *testing.T) {
	s, _ := fixtureServer(t)
	id := findSymbol(t, s, "NewMemory", "NewMemory")
	res := getJSON[ImpactResponse](t, s, "/api/impact", url.Values{"id": {strconv.FormatInt(id, 10)}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	nodes := nodesByLabel(res.Elements)
	if !strings.Contains(nodes["NewMemory"].Classes, "seed") {
		t.Fatalf("seed not marked: %+v", nodes["NewMemory"])
	}
	for name, depth := range map[string]int{"NewAudited": 1, "TestPlaceOrder": 1, "main": 2} {
		n, ok := nodes[name]
		if !ok {
			t.Errorf("impact lacks %s: %v", name, keys(nodes))
			continue
		}
		if n.Data.Depth != depth || !strings.Contains(n.Classes, "impact") {
			t.Errorf("%s: depth %d classes %q, want depth %d", name, n.Data.Depth, n.Classes, depth)
		}
	}
	if res.Affected < 3 || res.Tests < 1 || res.Files < 2 || len(res.Seeds) != 1 {
		t.Errorf("summary: %+v", res)
	}

	// depth=1 stops before main.
	res = getJSON[ImpactResponse](t, s, "/api/impact", url.Values{"id": {strconv.FormatInt(id, 10)}, "depth": {"1"}}, http.StatusOK)
	if _, ok := nodesByLabel(res.Elements)["main"]; ok {
		t.Error("depth 1 reached main")
	}

	// A file seeds every definition in it.
	res = getJSON[ImpactResponse](t, s, "/api/impact", url.Values{"path": {"go/internal/util/format.go"}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if len(res.Seeds) < 2 {
		t.Fatalf("path seeds: %v", res.Seeds)
	}
	if _, ok := nodesByLabel(res.Elements)["PlaceOrder"]; !ok {
		t.Errorf("impact of format.go lacks PlaceOrder")
	}

	getJSON[ErrorResponse](t, s, "/api/impact", nil, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/impact", url.Values{"id": {"1"}, "path": {"a.go"}}, http.StatusBadRequest)
	getJSON[ErrorResponse](t, s, "/api/impact", url.Values{"path": {"no/such.go"}}, http.StatusNotFound)
	getJSON[ErrorResponse](t, s, "/api/impact", url.Values{"id": {"999999999"}}, http.StatusNotFound)
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
