package query

import (
	"context"
	"fmt"
	"sort"

	"github.com/pragmataW/graphmaster/internal/model"
)

// testsForKinds are followed backwards from the symbol to find tests.
var testsForKinds = []model.EdgeKind{model.EdgeCalls, model.EdgeTests, model.EdgeReferences}

const (
	testsForDepth   = 2
	testsForMinConf = 30
	testsByNamePool = 200
)

// TestsFor lists test functions that reach a symbol within two hops of
// calls / test edges, plus tests whose name contains the symbol's name.
func (e *Engine) TestsFor(ctx context.Context, in TestsForInput) (string, error) {
	return e.run(ctx, "tests_for", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		s, err := q.symbolArg(ctx, o, in.Symbol)
		if s == nil || err != nil {
			return err
		}
		seeds, err := q.descendants(ctx, s.ID)
		if err != nil {
			return err
		}
		reached, _, err := q.reverseClosure(ctx, seeds, testsForKinds, testsForDepth, testsForMinConf)
		if err != nil {
			return err
		}
		var viaCalls []*reach
		seen := map[int64]bool{s.ID: true}
		for _, r := range reached {
			if r.IsTest() {
				viaCalls = append(viaCalls, r)
				seen[r.ID] = true
			}
		}
		sort.SliceStable(viaCalls, func(i, j int) bool {
			a, b := viaCalls[i], viaCalls[j]
			if a.depth != b.depth {
				return a.depth < b.depth
			}
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			return a.Start < b.Start
		})
		byName, err := q.testsByName(ctx, s.Name, seen)
		if err != nil {
			return err
		}

		o.line("tests for " + s.short())
		if len(viaCalls) == 0 && len(byName) == 0 {
			o.line("no tests found")
			return nil
		}
		rows := []string{countHead(fmt.Sprintf("reaching it within %d hops", testsForDepth), len(viaCalls))}
		for _, r := range viaCalls {
			rows = append(rows, fmt.Sprintf("  %s%s  d%d", confMark(r.conf), r.short(), r.depth))
		}
		rows = append(rows, countHead("by name", len(byName)))
		for _, t := range byName {
			rows = append(rows, "  "+t.short())
		}
		o.list(len(rows), func(i int) string { return rows[i] }, 0)
		return nil
	})
}

// testsByName finds test definitions whose name contains name
// (case-insensitive trigram search), skipping ids in seen.
func (q *querier) testsByName(ctx context.Context, name string, seen map[int64]bool) ([]*sym, error) {
	ids, err := q.st.SearchSubstring(ctx, name, testsByNamePool)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	ss, err := q.symsInOrder(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []*sym
	for _, t := range ss {
		if t.IsTest() && !seen[t.ID] {
			out = append(out, t)
		}
	}
	sortByLocation(out)
	return out, nil
}

// countHead renders a section heading with its item count.
func countHead(label string, n int) string {
	if n == 0 {
		return label + ": none"
	}
	return fmt.Sprintf("%s (%d):", label, n)
}
