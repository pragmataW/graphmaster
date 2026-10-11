package opencode

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pragmataW/graphmaster/internal/hookapi"
)

func TestRenderPluginEscapesBinary(t *testing.T) {
	src, err := renderPlugin(pluginData{Binary: `/opt/my "odd" dir/<graphmaster>`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `const BIN = "/opt/my \"odd\" dir/<graphmaster>";`) {
		t.Errorf("binary not embedded as a JS string literal:\n%s", src)
	}
}

func TestRenderPluginUsesCLIEventNames(t *testing.T) {
	src, err := renderPlugin(pluginData{Binary: testBin})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []hookapi.Event{hookapi.EventSessionStart, hookapi.EventSubagentStart,
		hookapi.EventPreToolUse, hookapi.EventPostToolUse, hookapi.EventPreCompact} {
		if !strings.Contains(string(src), `"`+string(e)+`"`) {
			t.Errorf("plugin never sends %s", e)
		}
	}
	for _, tool := range append(append([]string{}, preTools...), postTools...) {
		if normalizeTool(tool) == hookapi.ToolOther || normalizeTool(tool) == hookapi.ToolMCP {
			t.Errorf("forwarded tool %q has no steering category", tool)
		}
	}
}

// fakeBin replies with $FAKE_REPLY, exits with $FAKE_EXIT and logs
// "<argv>\t<stdin>" lines to $FAKE_LOG.
const fakeBin = `#!/bin/sh
input=$(cat)
printf '%s\t%s\n' "$*" "$input" >> "$FAKE_LOG"
if [ -n "$FAKE_SLEEP" ]; then sleep "$FAKE_SLEEP"; fi
printf '%s' "$FAKE_REPLY"
exit ${FAKE_EXIT:-0}
`

// driver exercises the generated plugin the way OpenCode calls it.
const driver = `
import { GraphmasterPlugin } from "./graphmaster.js";
const hooks = await GraphmasterPlugin({ directory: process.env.REPO });
const res = {};
const env = (reply, extra = {}) => {
  process.env.FAKE_REPLY = reply;
  for (const k of ["FAKE_EXIT", "FAKE_SLEEP"]) delete process.env[k];
  Object.assign(process.env, extra);
};
const before = async (tool, sessionID, args) => {
  const output = { args };
  try {
    await hooks["tool.execute.before"]({ tool, sessionID, callID: "call_" + tool }, output);
    return { ok: true, args: output.args };
  } catch (e) {
    return { threw: e.message };
  }
};
const deny = JSON.stringify({ action: "deny", reason: "use find_symbol Foo" });

env(deny);
res.deny = await before("grep", "ses_1", { pattern: "Foo" });
env("");
res.empty = await before("grep", "ses_1", { pattern: "Foo" });
env(JSON.stringify({ action: "allow" }));
res.allow = await before("read", "ses_1", { filePath: "/r/a.go" });
env("not json");
res.garbage = await before("glob", "ses_1", { pattern: "**/*.go" });
env(deny, { FAKE_EXIT: "1" });
res.nonzero = await before("grep", "ses_1", { pattern: "Foo" });
env(deny, { FAKE_SLEEP: "8" });
res.timeout = await before("grep", "ses_1", { pattern: "Foo" });
env(JSON.stringify({ action: "allow", args: { pattern: "Bar" } }));
res.rewrite = await before("grep", "ses_1", { pattern: "Foo", path: "x" });
env(deny);
res.unforwarded = await before("webfetch", "ses_1", { url: "https://example.com" });
res.task = await before("task", "ses_1", { subagent_type: "explore", prompt: "find" });

env("");
await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_1", directory: process.env.REPO } } } });
await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_child", parentID: "ses_1", directory: process.env.REPO } } } });
res.child = await before("grep", "ses_child", { pattern: "Foo" });
await hooks.event({ event: { type: "file.edited", properties: { file: "/r/a.go" } } });
await hooks.event({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
await hooks.event({});
res.bash = await before("bash", "ses_1", { command: "sed -i s/a/b/ a.go" });
await hooks["tool.execute.after"]({ tool: "bash", sessionID: "ses_1", callID: "call_bash" }, { title: "", output: "", metadata: {} });
await hooks["tool.execute.after"]({ tool: "edit", sessionID: "ses_1", callID: "call_edit" }, { title: "", output: "", metadata: {} });

env(JSON.stringify({ context: "graphmaster: prefer find_symbol" }));
const compact = { context: [] };
await hooks["experimental.session.compacting"]({ sessionID: "ses_1" }, compact);
res.compact = compact.context;

// Wait for fire-and-forget children to log.
const want = Number(process.env.WANT_LINES);
const fs = await import("node:fs");
for (let i = 0; i < 200; i++) {
  const n = fs.existsSync(process.env.FAKE_LOG) ? fs.readFileSync(process.env.FAKE_LOG, "utf8").trim().split("\n").length : 0;
  if (n >= want) break;
  await new Promise((r) => setTimeout(r, 25));
}
console.log(JSON.stringify(res));
`

type logLine struct {
	argv    string
	payload map[string]any
}

func TestGeneratedPluginWithNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-graphmaster")
	writeFile(t, bin, fakeBin)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	data := defaultPluginData(bin)
	// Generous enough that a loaded machine (go test ./... -race) still
	// answers the non-timeout cases in time; the timeout case sleeps longer.
	data.Timeouts[hookapi.EventPreToolUse] = 5000
	src, err := renderPlugin(data)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "graphmaster.js"), string(src))
	writeFile(t, filepath.Join(dir, "package.json"), `{"type":"module"}`)
	writeFile(t, filepath.Join(dir, "driver.js"), driver)
	logPath := filepath.Join(dir, "calls.log")

	// Expected spawns: 9 pre-tool-use (deny, empty, allow, garbage, nonzero,
	// timeout, rewrite, child, bash) + session-start + subagent-start +
	// file.edited + bash after + pre-compact.
	const wantLines = 14
	cmd := exec.Command(node, "driver.js")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FAKE_LOG="+logPath, "REPO="+dir, "WANT_LINES=14")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver: %v\n%s", err, out)
	}
	var res map[string]json.RawMessage
	if err := json.Unmarshal(lastLine(out), &res); err != nil {
		t.Fatalf("driver output: %v\n%s", err, out)
	}
	expect := map[string]string{
		"deny":        `{"threw":"use find_symbol Foo"}`,
		"empty":       `{"ok":true,"args":{"pattern":"Foo"}}`,
		"allow":       `{"ok":true,"args":{"filePath":"/r/a.go"}}`,
		"garbage":     `{"ok":true,"args":{"pattern":"**/*.go"}}`,
		"nonzero":     `{"ok":true,"args":{"pattern":"Foo"}}`,
		"timeout":     `{"ok":true,"args":{"pattern":"Foo"}}`,
		"rewrite":     `{"ok":true,"args":{"pattern":"Bar"}}`,
		"unforwarded": `{"ok":true,"args":{"url":"https://example.com"}}`,
		"task":        `{"ok":true,"args":{"subagent_type":"explore","prompt":"find"}}`,
		"child":       `{"ok":true,"args":{"pattern":"Foo"}}`,
		"bash":        `{"ok":true,"args":{"command":"sed -i s/a/b/ a.go"}}`,
		"compact":     `["graphmaster: prefer find_symbol"]`,
	}
	for k, want := range expect {
		if got := string(res[k]); got != want {
			t.Errorf("%s = %s, want %s", k, got, want)
		}
	}

	lines := readLog(t, logPath)
	if len(lines) != wantLines {
		for _, l := range lines {
			t.Logf("%s %v", l.argv, l.payload)
		}
		t.Fatalf("binary spawned %d times, want %d", len(lines), wantLines)
	}
	byEvent := map[string][]map[string]any{}
	for _, l := range lines {
		f := strings.Fields(l.argv)
		if len(f) != 4 || f[0] != "hook" || f[2] != "--agent" || f[3] != "opencode" {
			t.Fatalf("argv = %q", l.argv)
		}
		byEvent[f[1]] = append(byEvent[f[1]], l.payload)
	}
	if n := len(byEvent["pre-tool-use"]); n != 9 {
		t.Errorf("pre-tool-use calls = %d, want 9", n)
	}
	first := byEvent["pre-tool-use"][0]
	if first["tool"] != "grep" || first["sessionID"] != "ses_1" || first["callID"] != "call_grep" || first["cwd"] != dir {
		t.Errorf("pre-tool-use payload = %v", first)
	}
	var child map[string]any
	for _, p := range byEvent["pre-tool-use"] {
		if p["agentID"] == "ses_child" {
			child = p
		}
	}
	if child == nil || child["sessionID"] != "ses_1" || child["agentType"] != "explore" {
		t.Errorf("subagent tool call payload = %v", child)
	}
	if s := byEvent["session-start"]; len(s) != 1 || s[0]["sessionID"] != "ses_1" {
		t.Errorf("session-start = %v", s)
	}
	if s := byEvent["subagent-start"]; len(s) != 1 || s[0]["sessionID"] != "ses_1" || s[0]["agentID"] != "ses_child" || s[0]["agentType"] != "explore" {
		t.Errorf("subagent-start = %v", s)
	}
	post := byEvent["post-tool-use"]
	if len(post) != 2 {
		t.Fatalf("post-tool-use = %v", post)
	}
	var sawEdit, sawBash bool
	for _, p := range post {
		args, _ := p["args"].(map[string]any)
		switch p["tool"] {
		case "edit":
			sawEdit = args["filePath"] == "/r/a.go"
		case "bash":
			sawBash = args["command"] == "sed -i s/a/b/ a.go" && p["callID"] == "call_bash"
		}
	}
	if !sawEdit || !sawBash {
		t.Errorf("post-tool-use payloads = %v", post)
	}
	if s := byEvent["pre-compact"]; len(s) != 1 || s[0]["sessionID"] != "ses_1" {
		t.Errorf("pre-compact = %v", s)
	}

	// Every payload the plugin sends must parse.
	for evName, ps := range byEvent {
		e, err := hookapi.ParseEvent(evName)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			b, _ := json.Marshal(p)
			if _, err := (Adapter{}).ParseEvent(e, b, nil); err != nil {
				t.Errorf("%s payload %s does not parse: %v", evName, b, err)
			}
		}
	}
}

func lastLine(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return []byte(s)
}

func readLog(t *testing.T, p string) []logLine {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []logLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		argv, payload, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			t.Fatalf("bad log line %q", sc.Text())
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatalf("payload %q: %v", payload, err)
		}
		out = append(out, logLine{argv: argv, payload: m})
	}
	return out
}
