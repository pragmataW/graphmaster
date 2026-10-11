package query

import (
	"testing"
)

// goldenCases cover every tool on the fixture repository with the real
// indexer and resolver. Regenerate with: go test ./internal/query -update
var goldenCases = []struct {
	name, tool, args string
}{
	{"find_symbol_exact", "find_symbol", `{"query":"PlaceOrder"}`},
	{"find_symbol_prefix", "find_symbol", `{"query":"Format","limit":5}`},
	{"find_symbol_filters", "find_symbol", `{"query":"area","lang":"rust","kind":"function"}`},
	{"find_symbol_qname", "find_symbol", `{"query":"Memory.Put","exact":true}`},
	{"find_symbol_none", "find_symbol", `{"query":"Zzyzx"}`},
	{"search_symbols_files", "search", `{"query":"order"}`},
	{"search_kinds", "search", `{"query":"user","kinds":["class","file"],"path_prefix":"ts/"}`},
	{"repo_map_root", "repo_map", `{"depth":1}`},
	{"repo_map_go", "repo_map", `{"path_prefix":"go","depth":3}`},
	{"file_outline_go", "file_outline", `{"path":"go/internal/store/store.go"}`},
	{"file_outline_public_top", "file_outline", `{"path":"go/internal/store/store.go","depth":1,"include_private":false}`},
	{"file_outline_missing", "file_outline", `{"path":"go/internal/store/stor.go"}`},
	{"read_symbol_qname", "read_symbol", `{"symbol":"example.com/fixture/internal/service.OrderService.PlaceOrder"}`},
	{"read_symbol_pathline", "read_symbol", `{"symbol":"go/internal/store/store.go:26","context_lines":1}`},
	{"read_symbol_maxlines", "read_symbol", `{"symbol":"OrderService.PlaceOrder","max_lines":3}`},
	{"read_symbol_ambiguous", "read_symbol", `{"symbol":"Put"}`},
	{"read_symbol_unknown", "read_symbol", `{"symbol":"PlaceOrdr"}`},
	{"callers_depth1", "callers", `{"symbol":"OrderService.PlaceOrder"}`},
	{"callers_depth2", "callers", `{"symbol":"NewMemory","depth":2}`},
	{"callers_type", "callers", `{"symbol":"store.Item"}`},
	{"callers_lowconf_cpp", "callers", `{"symbol":"geo::Shape::area","min_confidence":10}`},
	{"callees", "callees", `{"symbol":"OrderService.PlaceOrder"}`},
	{"callees_lowconf", "callees", `{"symbol":"example.com/fixture/cmd/server.main","min_confidence":1}`},
	{"impact_symbol", "impact", `{"symbol":"Memory.Put"}`},
	{"impact_path", "impact", `{"path":"go/internal/util/format.go"}`},
	{"impact_python", "impact", `{"symbol":"shop.models.Item"}`},
	{"imports_out", "imports", `{"path":"go/cmd/server/main.go"}`},
	{"imports_in", "imports", `{"path":"go/internal/store/store.go","direction":"in"}`},
	{"imports_module_in", "imports", `{"module":"example.com/fixture/internal/service","direction":"in"}`},
	{"imports_package_out", "imports", `{"path":"go/internal/service"}`},
	{"imports_ts", "imports", `{"path":"ts/src/services/userService.ts"}`},
	{"inheritance_both", "inheritance", `{"symbol":"com.example.zoo.Animal"}`},
	{"inheritance_up", "inheritance", `{"symbol":"com.example.zoo.Cat","direction":"up"}`},
	{"inheritance_cpp", "inheritance", `{"symbol":"geo::Square"}`},
	{"tests_for_go", "tests_for", `{"symbol":"OrderService.PlaceOrder"}`},
	{"tests_for_python", "tests_for", `{"symbol":"shop.services.pricing.apply_discount"}`},
	{"traverse_out", "traverse", `{"start":"example.com/fixture/cmd/server.main","max_depth":2}`},
	{"traverse_in_calls", "traverse", `{"start":"Memory.Put","direction":"in","edge_kinds":["calls"]}`},
	{"traverse_both", "traverse", `{"start":"com.example.zoo.Dog","direction":"both","max_depth":1}`},
}

func TestGolden(t *testing.T) {
	e := fixtureEngine(t)
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			checkGolden(t, c.name, runTool(t, e, c.tool, c.args))
		})
	}
}

// TestGoldenCoversEveryTool keeps the golden table in sync with the
// registry (status is covered by TestStatus: its output is host-specific).
func TestGoldenCoversEveryTool(t *testing.T) {
	covered := map[string]bool{"status": true}
	for _, c := range goldenCases {
		covered[c.tool] = true
	}
	for _, tl := range Tools() {
		if !covered[tl.Name] {
			t.Errorf("tool %s has no golden case", tl.Name)
		}
	}
}
