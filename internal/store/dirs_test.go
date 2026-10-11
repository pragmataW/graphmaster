package store

import (
	"bytes"
	"context"
	"testing"
)

func TestDirsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	w := newTestWriter(t, s, WriterOptions{})

	in := []Dir{
		{Path: "", MtimeNS: 10, IgnoreStat: "1:2:3", Names: []byte("fa.go\x00dsub\x00")},
		{Path: "sub", MtimeNS: 20, Names: []byte("lb\x00")},
		{Path: "gone", MtimeNS: 30},
	}
	for i := range in {
		if err := w.UpsertDir(&in[i]); err != nil {
			t.Fatalf("UpsertDir(%q): %v", in[i].Path, err)
		}
	}
	// Update in place and delete one.
	in[1].MtimeNS = 21
	if err := w.UpsertDir(&in[1]); err != nil {
		t.Fatal(err)
	}
	if err := w.DeleteDir("gone"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadDirs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadDirs = %d rows, want 2: %+v", len(got), got)
	}
	want := in[:2]
	for i := range want {
		g, e := got[i], want[i]
		if g.Path != e.Path || g.MtimeNS != e.MtimeNS || g.IgnoreStat != e.IgnoreStat || !bytes.Equal(g.Names, e.Names) {
			t.Errorf("row %d = %+v, want %+v", i, g, e)
		}
	}

	if err := w.ClearDirs(); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadDirs(ctx); err != nil || len(got) != 0 {
		t.Fatalf("after ClearDirs: %v %v", got, err)
	}
}
