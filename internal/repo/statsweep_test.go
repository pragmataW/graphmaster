package repo

import (
	"context"
	"fmt"
	"testing"
)

func TestStatPaths(t *testing.T) {
	dir := newRepo(t)
	files := map[string]string{"f.go": "package f\n"}
	var rels []string
	for i := range 600 {
		p := fmt.Sprintf("d%d/f%03d.go", i%5, i)
		files[p] = "package d\n"
		rels = append(rels, p)
	}
	writeFiles(t, dir, files)
	rels = append(rels, "missing.go", "f.go/child") // gone; parent is a file
	r, err := FindRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := StatPaths(context.Background(), r, rels, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range rels[:600] {
		if !res[i].Exists || res[i].Stat.Size != 10 || res[i].Err != nil {
			t.Fatalf("%s: %+v", p, res[i])
		}
	}
	for _, ps := range res[600:] {
		if ps.Exists || ps.Err != nil {
			t.Fatalf("missing path: %+v", ps)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StatPaths(ctx, r, rels, 2); err == nil {
		t.Fatal("cancelled sweep must fail")
	}
	if out, err := StatPaths(context.Background(), r, nil, 0); err != nil || len(out) != 0 {
		t.Fatalf("empty input: %v %v", out, err)
	}
}
