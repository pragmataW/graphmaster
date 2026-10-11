package install

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const claudeLike = `{
  // user settings, keep me
  "theme": "dark",
  "mcpServers": {
    "other": {
      "command": "npx",
      "args": ["-y", "other-mcp"] // pinned
    }
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "lint.sh"}]
      }
    ]
  },
}
`

func mustDoc(t *testing.T, src string) *jsonDoc {
	t.Helper()
	d, err := parseJSONDoc([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestJSONSetNewMemberPreservesLayout(t *testing.T) {
	d := mustDoc(t, claudeLike)
	res, err := d.set("/mcpServers/graphmaster", raw(`{"command":"/usr/local/bin/graphmaster","args":["mcp"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.changed || res.prev != nil || len(res.created) != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
	want := strings.Replace(claudeLike, `["-y", "other-mcp"] // pinned
    }
  },`, `["-y", "other-mcp"] // pinned
    },
    "graphmaster": {
      "command": "/usr/local/bin/graphmaster",
      "args": [
        "mcp"
      ]
    }
  },`, 1)
	if got := string(d.bytes()); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// Removing restores the original bytes.
	if ok, err := d.remove("/mcpServers/graphmaster"); err != nil || !ok {
		t.Fatalf("remove = %v, %v", ok, err)
	}
	if got := string(d.bytes()); got != claudeLike {
		t.Fatalf("round trip mismatch:\n%s", got)
	}
}

func TestJSONSetCreatesParents(t *testing.T) {
	d := mustDoc(t, "{\n  \"a\": 1\n}\n")
	res, err := d.set("/mcpServers/graphmaster", raw(`{"command":"gm"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.created, []string{"/mcpServers"}) {
		t.Fatalf("created = %v", res.created)
	}
	want := "{\n  \"a\": 1,\n  \"mcpServers\": {\n    \"graphmaster\": {\n      \"command\": \"gm\"\n    }\n  }\n}\n"
	if got := string(d.bytes()); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestJSONSetIntoEmptyObject(t *testing.T) {
	for _, src := range []string{"{}\n", "{}", "{ }\n"} {
		d := mustDoc(t, src)
		if _, err := d.set("/x", raw(`1`)); err != nil {
			t.Fatal(err)
		}
		got := string(d.bytes())
		if !strings.HasPrefix(got, "{\n  \"x\": 1\n}") {
			t.Fatalf("src %q -> %q", src, got)
		}
		if ok, _ := d.remove("/x"); !ok {
			t.Fatal("remove failed")
		}
		if d.isEmptyContainer("") != true {
			t.Fatalf("root not empty: %q", d.bytes())
		}
	}
}

func TestJSONSetExistingEqualAndReplace(t *testing.T) {
	src := "{\n  \"s\": {\n    \"gm\": {\"command\": \"old\"}\n  }\n}\n"
	d := mustDoc(t, src)
	res, err := d.set("/s/gm", raw(`{ "command" : "old" }`))
	if err != nil || res.changed {
		t.Fatalf("equal value should be unchanged: %+v %v", res, err)
	}
	res, err = d.set("/s/gm", raw(`{"command":"new"}`))
	if err != nil || !res.changed {
		t.Fatalf("replace: %+v %v", res, err)
	}
	if string(res.prev) != `{"command":"old"}` {
		t.Fatalf("prev = %s", res.prev)
	}
	want := "{\n  \"s\": {\n    \"gm\": {\n      \"command\": \"new\"\n    }\n  }\n}\n"
	if got := string(d.bytes()); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestJSONAppendElement(t *testing.T) {
	d := mustDoc(t, claudeLike)
	elem := raw(`{"matcher":"Grep|Glob","hooks":[{"type":"command","command":"gm hook pre"}]}`)
	res, err := d.appendElem("/hooks/PreToolUse", elem)
	if err != nil || !res.changed {
		t.Fatalf("append: %+v %v", res, err)
	}
	got := string(d.bytes())
	if !strings.Contains(got, "    {\n        \"matcher\": \"Grep|Glob\",\n") {
		t.Fatalf("element not indented:\n%s", got)
	}
	// Idempotent.
	res, err = d.appendElem("/hooks/PreToolUse", elem)
	if err != nil || res.changed {
		t.Fatalf("second append changed: %+v %v", res, err)
	}
	n, err := d.removeElems("/hooks/PreToolUse", elem)
	if err != nil || n != 1 {
		t.Fatalf("removeElems = %d, %v", n, err)
	}
	if got := string(d.bytes()); got != claudeLike {
		t.Fatalf("round trip mismatch:\n%s", got)
	}
}

func TestJSONAppendCreatesArray(t *testing.T) {
	d := mustDoc(t, "{}\n")
	res, err := d.appendElem("/hooks/SessionStart", raw(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.created, []string{"/hooks", "/hooks/SessionStart"}) {
		t.Fatalf("created = %v", res.created)
	}
	v, ok := d.get("/hooks/SessionStart/0/a")
	if !ok || string(v) != "1" {
		t.Fatalf("get = %s %v", v, ok)
	}
}

func TestJSONTabIndent(t *testing.T) {
	src := "{\n\t\"a\": {\n\t\t\"b\": 1\n\t}\n}\n"
	d := mustDoc(t, src)
	if _, err := d.set("/a/c", raw(`{"d":2}`)); err != nil {
		t.Fatal(err)
	}
	want := "{\n\t\"a\": {\n\t\t\"b\": 1,\n\t\t\"c\": {\n\t\t\t\"d\": 2\n\t\t}\n\t}\n}\n"
	if got := string(d.bytes()); got != want {
		t.Fatalf("got:\n%q", got)
	}
}

func TestJSONNoHTMLEscape(t *testing.T) {
	d := mustDoc(t, "{}")
	if _, err := d.set("/c", raw(`"a && b > c"`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.bytes()), `"a && b > c"`) {
		t.Fatalf("escaped: %s", d.bytes())
	}
}

func TestJSONErrors(t *testing.T) {
	if _, err := parseJSONDoc([]byte(`{"a": 1,,}`)); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := parseJSONDoc([]byte(`[1]`)); err == nil {
		t.Fatal("expected non-object root error")
	}
	d := mustDoc(t, `{"a": 1, "arr": {}}`)
	if _, err := d.set("/a/b", raw(`1`)); err == nil {
		t.Fatal("expected error setting through a scalar")
	}
	if _, err := d.appendElem("/arr", raw(`1`)); err == nil {
		t.Fatal("expected error appending to an object")
	}
	if _, err := d.set("/x", raw(`{bad`)); err == nil {
		t.Fatal("expected invalid value error")
	}
}

func TestJSONPointerEscaping(t *testing.T) {
	d := mustDoc(t, "{}")
	p := Pointer("a/b", "c~d")
	if p != "/a~1b/c~0d" {
		t.Fatalf("Pointer = %q", p)
	}
	if _, err := d.set(p, raw(`true`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.bytes()), `"a/b": {`) || !strings.Contains(string(d.bytes()), `"c~d": true`) {
		t.Fatalf("got %s", d.bytes())
	}
}

func TestJSONRoundTripVariants(t *testing.T) {
	srcs := []string{
		claudeLike,
		"{\"a\":1}",
		"{\"a\": 1, \"b\": [1, 2]}\n",
		"{\n  \"a\": 1, // c\n}\n",
		"{\n  \"a\": 1\n  /* tail */\n}\n",
		"{\n    \"deep\": {\n        \"x\": []\n    }\n}\n",
	}
	for _, src := range srcs {
		for _, ptr := range []string{"/gm", "/deep/gm"} {
			d := mustDoc(t, src)
			if d.v.Find("/deep") == nil && ptr == "/deep/gm" {
				continue
			}
			if _, err := d.set(ptr, raw(`{"k":[1,{"z":true}]}`)); err != nil {
				t.Fatalf("%q: %v", src, err)
			}
			if _, err := StandardizeJSON(d.bytes()); err != nil {
				t.Fatalf("%q: result does not parse: %v", src, err)
			}
			if ok, err := d.remove(ptr); !ok || err != nil {
				t.Fatalf("%q: remove %v %v", src, ok, err)
			}
			if got := string(d.bytes()); got != src {
				t.Errorf("round trip of %q via %s gave %q", src, ptr, got)
			}
		}
		d := mustDoc(t, src)
		if n := d.v.Find("/deep/x"); n != nil {
			if _, err := d.appendElem("/deep/x", raw(`"e"`)); err != nil {
				t.Fatal(err)
			}
			if _, err := d.removeElems("/deep/x", raw(`"e"`)); err != nil {
				t.Fatal(err)
			}
			if got := string(d.bytes()); got != src {
				t.Errorf("array round trip of %q gave %q", src, got)
			}
		}
	}
}
