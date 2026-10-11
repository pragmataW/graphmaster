package install

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	if d := unifiedDiff("/x", []byte("a\n"), []byte("a\n")); d != "" {
		t.Fatalf("equal inputs gave %q", d)
	}
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	b := "1\n2\n3\n4\nfive\n6\n7\n8\n9\n10\nextra\n"
	want := `--- a/x
+++ b/x
@@ -2,9 +2,10 @@
 2
 3
 4
-5
+five
 6
 7
 8
 9
 10
+extra
`
	if got := unifiedDiff("/x", []byte(a), []byte(b)); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestUnifiedDiffSeparateHunksAndCreate(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 30; i++ {
		a.WriteString("l\n")
		b.WriteString("l\n")
	}
	got := unifiedDiff("/f", []byte("x\n"+a.String()+"y\n"), []byte("X\n"+b.String()+"Y\n"))
	if strings.Count(got, "@@ ") != 2 {
		t.Fatalf("want 2 hunks:\n%s", got)
	}
	created := unifiedDiff("/n", nil, []byte("new"))
	if !strings.Contains(created, "@@ -0,0 +1,1 @@\n+new\n\\ No newline at end of file\n") {
		t.Fatalf("got:\n%s", created)
	}
}
