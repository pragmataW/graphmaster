package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/testutil"
)

func TestIndexAndStatusCommands(t *testing.T) {
	dir := testutil.Materialize(t)
	t.Chdir(dir)

	out, err := execute(t, "status")
	if err != nil || !strings.Contains(out, "state: not indexed") {
		t.Fatalf("status before index: %q, %v", out, err)
	}

	out, err = execute(t, "index")
	if err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	if !strings.Contains(out, "indexed ") || !strings.Contains(out, "parsed ") {
		t.Fatalf("index output: %q", out)
	}

	out, err = execute(t, "index", "--quiet")
	if err != nil || out != "" {
		t.Fatalf("index --quiet: %q, %v", out, err)
	}

	out, err = execute(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state: ready", "mode: balanced", "too_big 1", "generated 1", "languages: ", "db size: ", "last scan: ",
		"pending names: 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	out, err = execute(t, "index", "--force")
	if err != nil || !strings.Contains(out, "rebuilt: forced rebuild") {
		t.Fatalf("index --force: %q, %v", out, err)
	}
}

func TestIndexOutsideGit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, err := execute(t, "index")
	if !errors.Is(err, index.ErrNotGitRepo) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("index outside git: %v", err)
	}
	var out, errw bytes.Buffer
	if err := runIndex(context.Background(), &out, &errw, dir, false, true); err != nil {
		t.Fatalf("index --force outside git: %v", err)
	}
	st, err := execute(t, "status")
	if err != nil || !strings.Contains(st, "(not git)") {
		t.Fatalf("status: %q, %v", st, err)
	}
}
