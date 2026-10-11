package install

import (
	"strings"
	"testing"
)

const codexTOML = `# my codex config
model = "gpt-5"

[mcp_servers.other]
command = "other" # keep this comment
`

const gmBlock = `[mcp_servers.graphmaster]
command = "/opt/homebrew/bin/graphmaster"
args = ["mcp"]
`

func TestMarkerUpsertAppendAndRemove(t *testing.T) {
	cases := []struct{ name, src, sep string }{
		{"trailing newline", codexTOML, "\n"},
		{"no trailing newline", strings.TrimSuffix(codexTOML, "\n"), "\n\n"},
		{"blank line already", codexTOML + "\n", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, sep, changed, err := tomlMarkers.upsert([]byte(c.src), "", gmBlock)
			if err != nil || !changed {
				t.Fatalf("upsert: %v changed=%v", err, changed)
			}
			if sep != c.sep {
				t.Fatalf("sep = %q, want %q", sep, c.sep)
			}
			wantTail := "# graphmaster:begin v1\n" + gmBlock + "# graphmaster:end\n"
			if !strings.HasSuffix(string(out), wantTail) {
				t.Fatalf("got:\n%s", out)
			}
			// Idempotent.
			again, _, changed, err := tomlMarkers.upsert(out, "", gmBlock)
			if err != nil || changed || string(again) != string(out) {
				t.Fatalf("second upsert changed=%v err=%v", changed, err)
			}
			back, removed, err := tomlMarkers.remove(out, "", sep)
			if err != nil || !removed {
				t.Fatalf("remove: %v %v", removed, err)
			}
			if string(back) != c.src {
				t.Fatalf("round trip:\n%q\nwant\n%q", back, c.src)
			}
		})
	}
}

func TestMarkerReplaceInPlace(t *testing.T) {
	src := "a = 1\n\n# graphmaster:begin v1\n[mcp_servers.graphmaster]\ncommand = \"old\"\n# graphmaster:end\n\n[tail]\nx = 1\n"
	out, _, changed, err := tomlMarkers.upsert([]byte(src), "", gmBlock)
	if err != nil || !changed {
		t.Fatal(err)
	}
	want := "a = 1\n\n# graphmaster:begin v1\n" + gmBlock + "# graphmaster:end\n\n[tail]\nx = 1\n"
	if string(out) != want {
		t.Fatalf("got:\n%s", out)
	}
	// Removal in the middle leaves surroundings intact.
	back, removed, err := tomlMarkers.remove(out, "", "")
	if err != nil || !removed {
		t.Fatal(err)
	}
	if string(back) != "a = 1\n\n\n[tail]\nx = 1\n" {
		t.Fatalf("got %q", back)
	}
}

func TestMarkerIDsAndOldVersions(t *testing.T) {
	src := "<!-- graphmaster:begin v0 rules -->\nold\n<!-- graphmaster:end rules -->\n"
	out, _, changed, err := mdMarkers.upsert([]byte(src), "rules", "new\n")
	if err != nil || !changed {
		t.Fatal(err)
	}
	if string(out) != "<!-- graphmaster:begin v1 rules -->\nnew\n<!-- graphmaster:end rules -->\n" {
		t.Fatalf("got %q", out)
	}
	// A different id is a different block.
	out2, _, _, err := mdMarkers.upsert(out, "", "default\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out2), "graphmaster:begin") != 2 {
		t.Fatalf("got %q", out2)
	}
	if !mdMarkers.has(out2, "rules") || !mdMarkers.has(out2, "") || mdMarkers.has(out2, "zzz") {
		t.Fatal("has() mismatch")
	}
}

func TestMarkerErrors(t *testing.T) {
	bad := []string{
		"# graphmaster:begin v1\nx = 1\n", // unterminated
		"# graphmaster:end\n",             // stray end
		"# graphmaster:begin v1\n# graphmaster:begin v1\n# graphmaster:end\n",                    // nested
		"# graphmaster:begin v1\n# graphmaster:end\n# graphmaster:begin v1\n# graphmaster:end\n", // duplicate
	}
	for _, src := range bad {
		if _, _, _, err := tomlMarkers.upsert([]byte(src), "", gmBlock); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
	if _, _, _, err := tomlMarkers.upsert(nil, "", "# graphmaster:end\n"); err == nil {
		t.Error("expected error for body containing a marker")
	}
}

func TestMarkdownBlock(t *testing.T) {
	src := "# My rules\n\nBe nice.\n"
	out, sep, changed, err := mdMarkers.upsert([]byte(src), "", "Use graphmaster tools.\n")
	if err != nil || !changed {
		t.Fatal(err)
	}
	want := src + "\n<!-- graphmaster:begin v1 -->\nUse graphmaster tools.\n<!-- graphmaster:end -->\n"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
	// User appends after our block; surgical removal keeps their text.
	edited := string(out) + "\nMore user text.\n"
	back, _, err := mdMarkers.remove([]byte(edited), "", sep)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != src+"\nMore user text.\n" {
		t.Fatalf("got %q", back)
	}
}

func TestTOMLValidation(t *testing.T) {
	if err := validateTOML([]byte(codexTOML)); err != nil {
		t.Fatal(err)
	}
	if err := validateTOML([]byte("[a\nb = ")); err == nil {
		t.Fatal("expected error")
	}
	dup := codexTOML + "\n[mcp_servers.graphmaster]\ncommand = \"x\"\n"
	out, _, _, err := tomlMarkers.upsert([]byte(dup), "", gmBlock)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTOML(out); err == nil {
		t.Fatal("expected duplicate table error")
	}
	if err := checkTOMLBlockBody("model = \"x\"\n"); err == nil {
		t.Fatal("expected error: block body must start with a table header")
	}
	if err := checkTOMLBlockBody("# c\n\n[mcp_servers.graphmaster]\n"); err != nil {
		t.Fatal(err)
	}
}
