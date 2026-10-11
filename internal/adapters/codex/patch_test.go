package codex

import (
	"slices"
	"testing"
)

func TestPatchPaths(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		want  []string
	}{
		{
			name: "update add delete",
			patch: "*** Begin Patch\n" +
				"*** Update File: internal/store/db.go\n" +
				"@@ func Open\n-\told()\n+\tnew()\n" +
				"*** Add File: docs/new.md\n+# New\n" +
				"*** Delete File: legacy/old.go\n" +
				"*** End Patch\n",
			want: []string{"internal/store/db.go", "docs/new.md", "legacy/old.go"},
		},
		{
			name:  "move keeps source and destination",
			patch: "*** Begin Patch\n*** Update File: a/old.go\n*** Move to: b/new.go\n@@\n-x\n+y\n*** End Patch",
			want:  []string{"a/old.go", "b/new.go"},
		},
		{
			name:  "crlf and surrounding whitespace",
			patch: "*** Begin Patch\r\n*** Update File:   src/app.ts  \r\n@@\r\n*** End Patch\r\n",
			want:  []string{"src/app.ts"},
		},
		{
			name:  "duplicates collapse",
			patch: "*** Update File: a.go\n*** Update File: a.go\n",
			want:  []string{"a.go"},
		},
		{
			name:  "absolute path with spaces",
			patch: "*** Add File: /repo/my dir/x.py\n+print(1)\n",
			want:  []string{"/repo/my dir/x.py"},
		},
		{
			name:  "content lines that look like headers are ignored",
			patch: "*** Update File: a.go\n+*** Update File: fake.go\n- *** Delete File: nope.go\n",
			want:  []string{"a.go"},
		},
		{name: "empty", patch: "", want: nil},
		{name: "not a patch", patch: "rg -n Foo", want: nil},
		{name: "empty header path ignored", patch: "*** Update File:   \n", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := patchPaths(tt.patch); !slices.Equal(got, tt.want) {
				t.Fatalf("patchPaths() = %q, want %q", got, tt.want)
			}
		})
	}
}

func FuzzPatchPaths(f *testing.F) {
	f.Add("*** Begin Patch\n*** Update File: a.go\n*** End Patch")
	f.Add("*** Move to: \n")
	f.Fuzz(func(t *testing.T, s string) {
		for _, p := range patchPaths(s) {
			if p == "" {
				t.Fatal("empty path returned")
			}
		}
	})
}
