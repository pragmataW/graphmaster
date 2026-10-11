package store

import (
	"context"
	"testing"
)

func seedSymbols(t *testing.T, w *Writer) map[string]int64 {
	t.Helper()
	fid := mustFile(t, w, "pkg/user/service.go")
	ids := map[string]int64{}
	for _, n := range []string{"getUserByID", "HTTPServer", "get_user_name", "parseConfig", "$scope"} {
		ids[n] = mustNode(t, w, fid, n)
	}
	return ids
}

func TestFTSQueries(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	ids := seedSymbols(t, w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cases := []struct {
		q    string
		want []string
	}{
		{"user", []string{"getUserByID", "get_user_name"}},
		{"getUser", []string{"getUserByID", "get_user_name"}},
		{"getUserBy", []string{"getUserByID"}},
		{"getuserbyid", []string{"getUserByID"}},
		{"HTTP", []string{"HTTPServer"}},
		{"serv", []string{"HTTPServer"}},
		{"id", []string{"getUserByID"}},
		{"get_user", []string{"get_user_name", "getUserByID"}},
		{"config", []string{"parseConfig"}},
		{"scope", []string{"$scope"}},
		{"$scope", []string{"$scope"}},
		{"nomatch", nil},
		{"", nil},
	}
	for _, c := range cases {
		got, err := s.SearchSymbols(ctx, c.q, 10)
		if err != nil {
			t.Errorf("SearchSymbols(%q): %v", c.q, err)
			continue
		}
		var want []int64
		for _, n := range c.want {
			want = append(want, ids[n])
		}
		if !equalIDs(got, want) {
			t.Errorf("SearchSymbols(%q) = %v, want %v", c.q, got, want)
		}
	}
	// Exact-name hits rank first.
	got, _ := s.SearchSymbols(ctx, "getUserByID", 10)
	if len(got) == 0 || got[0] != ids["getUserByID"] {
		t.Errorf("ranking: %v", got)
	}
	// Limit is honored.
	if got, _ := s.SearchSymbols(ctx, "get", 1); len(got) != 1 {
		t.Errorf("limit ignored: %v", got)
	}
}

func TestTrigramSubstring(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	ids := seedSymbols(t, w)
	_ = w.Close()
	ctx := context.Background()
	for q, want := range map[string][]int64{
		"serBy":   {ids["getUserByID"]},
		"TTPSer":  {ids["HTTPServer"]},
		"user":    {ids["getUserByID"], ids["get_user_name"]},
		"ser":     {ids["getUserByID"], ids["HTTPServer"], ids["get_user_name"]},
		"zzzz":    nil,
		`a"b"c`:   nil,
		"ab":      nil, // shorter than a trigram
		"_user_n": {ids["get_user_name"]},
	} {
		got, err := s.SearchSubstring(ctx, q, 10)
		if err != nil {
			t.Errorf("SearchSubstring(%q): %v", q, err)
			continue
		}
		if !equalIDs(got, want) {
			t.Errorf("SearchSubstring(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestContentlessDelete(t *testing.T) {
	s := openTestStore(t)
	w := newTestWriter(t, s, WriterOptions{})
	fid := mustFile(t, w, "x.go")
	a := mustNode(t, w, fid, "alphaThing")
	b := mustNode(t, w, fid, "betaThing")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := w.deleteFTS(a); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, _ := s.SearchSymbols(ctx, "thing", 10); !equalIDs(got, []int64{b}) {
		t.Fatalf("after delete = %v want [%d]", got, b)
	}
	if got, _ := s.SearchSubstring(ctx, "Thing", 10); !equalIDs(got, []int64{b}) {
		t.Fatalf("trigram after delete = %v", got)
	}
}

func TestBulkDefersIndexesAndFTS(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	w, err := s.NewWriter(ctx, WriterOptions{Bulk: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := seedSymbols(t, w)
	fid2 := mustFile(t, w, "other.go")
	extra := mustNode(t, w, fid2, "getUserCount")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='nodes_name'`); n != 0 {
		t.Fatal("secondary index present during bulk")
	}
	if n := countRows(t, s, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='nodes_file'`); n != 1 {
		t.Fatal("core index dropped during bulk")
	}
	if got, _ := s.SearchSymbols(ctx, "user", 10); len(got) != 0 {
		t.Fatalf("FTS populated before FinishBulk: %v", got)
	}
	if v, ok, _ := s.Meta(ctx, MetaBulk); !ok || v != "1" {
		t.Fatal("bulk flag not set")
	}
	if err := s.FinishBulk(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='nodes_name'`); n != 1 {
		t.Fatal("secondary index not created by FinishBulk")
	}
	if _, ok, _ := s.Meta(ctx, MetaBulk); ok {
		t.Fatal("bulk flag not cleared")
	}
	got, _ := s.SearchSymbols(ctx, "user", 10)
	if !equalIDs(got, []int64{ids["getUserByID"], ids["get_user_name"], extra}) {
		t.Fatalf("FTS after FinishBulk = %v", got)
	}
	if got, _ := s.SearchSubstring(ctx, "serCou", 10); !equalIDs(got, []int64{extra}) {
		t.Fatalf("trigram after FinishBulk = %v", got)
	}
	// Idempotent.
	if err := s.FinishBulk(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SearchSymbols(ctx, "user", 10); len(got) != 3 {
		t.Fatalf("FinishBulk twice duplicated rows: %v", got)
	}
}
