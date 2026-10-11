package ui

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

func countNodes(els []Element) int {
	n := 0
	for _, e := range els {
		if e.Group == "nodes" {
			n++
		}
	}
	return n
}

func TestEgoLimitCappedAt300(t *testing.T) {
	s := hubServer(t)
	hub := strconv.FormatInt(findSymbol(t, s, "Hub", "Hub"), 10)

	res := getJSON[EgoResponse](t, s, "/api/ego", url.Values{"id": {hub}, "limit": {"5000"}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if n := countNodes(res.Elements); n != maxEgoLimit {
		t.Fatalf("limit=5000: %d nodes, want the %d cap", n, maxEgoLimit)
	}
	if !res.Truncated {
		t.Fatal("truncated not reported")
	}

	res = getJSON[EgoResponse](t, s, "/api/ego", url.Values{"id": {hub}, "limit": {"10"}, "group": {"file"}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if n := countNodes(res.Elements); n > 10+2 { // + file compound parents
		t.Fatalf("limit=10: %d nodes", n)
	}
}

func TestImpactCappedAt1500Elements(t *testing.T) {
	s := hubServer(t)
	hub := strconv.FormatInt(findSymbol(t, s, "Hub", "Hub"), 10)
	res := getJSON[ImpactResponse](t, s, "/api/impact", url.Values{"id": {hub}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if len(res.Elements) != maxElements || !res.Truncated {
		t.Fatalf("%d elements, truncated=%v; want exactly %d and truncated", len(res.Elements), res.Truncated, maxElements)
	}
	if res.Affected < hubCallers {
		t.Fatalf("affected %d, want >= %d (counts are not capped)", res.Affected, hubCallers)
	}
}

func TestFileCappedAt1500Elements(t *testing.T) {
	s := hubServer(t)
	res := getJSON[FileResponse](t, s, "/api/file", url.Values{"path": {"hub/hub.go"}}, http.StatusOK)
	checkGraph(t, res.GraphResponse, maxElements)
	if len(res.Elements) != maxElements || !res.Truncated {
		t.Fatalf("%d elements, truncated=%v", len(res.Elements), res.Truncated)
	}
	if res.File.Symbols != hubCallers+1 {
		t.Fatalf("file symbols %d, want %d", res.File.Symbols, hubCallers+1)
	}
}

func TestSearchLimitCapped(t *testing.T) {
	s := hubServer(t)
	res := getJSON[SearchResponse](t, s, "/api/search", url.Values{"q": {"Caller"}, "limit": {"100000"}}, http.StatusOK)
	if len(res.Symbols) != maxSearchLimit || !res.More {
		t.Fatalf("%d symbols (more=%v), want %d", len(res.Symbols), res.More, maxSearchLimit)
	}
}

func TestPackagesCapped(t *testing.T) {
	ix := fixtureIndex(t)
	s := New(ix, Options{Freshener: &countingFreshener{}, Token: testToken, MaxElements: 4})
	res := getJSON[PackagesResponse](t, s, "/api/packages", nil, http.StatusOK)
	checkGraph(t, res.GraphResponse, 4)
	if len(res.Elements) != 4 || !res.Truncated {
		t.Fatalf("%d elements, truncated=%v", len(res.Elements), res.Truncated)
	}
}
