package replay

import (
	"path/filepath"
	"testing"
)

// Fixture shorthands.
const (
	claudeStart1      = "claude/real-session-start-1.json"
	claudeStart2      = "claude/real-session-start-2.json"
	claudeSubagent    = "claude/synthetic-subagent-start.json"
	claudeSubGrep     = "claude/synthetic-pre-tool-use-subagent-grep.json"
	claudeGrep        = "claude/synthetic-pre-tool-use-grep.json"
	claudeGrepPath    = "claude/synthetic-pre-tool-use-grep-path.json"
	claudeGlob        = "claude/synthetic-pre-tool-use-glob.json"
	claudeRead        = "claude/synthetic-pre-tool-use-read.json"
	claudeBash        = "claude/synthetic-pre-tool-use-bash.json"
	claudePostWrite   = "claude/synthetic-post-tool-use-write.json"
	claudePostEdit    = "claude/synthetic-post-tool-use-edit.json"
	claudePostBash    = "claude/synthetic-post-tool-use-bash.json"
	claudeCursorDup   = "claude/synthetic-cursor-dup-pre-tool-use-grep.json"
	codexStart        = "codex/synthetic-session-start.json"
	codexBash         = "codex/synthetic-pre-tool-use-bash.json"
	codexArgv         = "codex/synthetic-pre-tool-use-shell-argv.json"
	codexPrePatch     = "codex/synthetic-pre-tool-use-apply-patch.json"
	codexPostPatch    = "codex/synthetic-post-tool-use-apply-patch.json"
	codexPostPatchIn  = "codex/synthetic-post-tool-use-apply-patch-input.json"
	cursorStart       = "cursor/synthetic-session-start.json"
	cursorSubagent    = "cursor/synthetic-subagent-start.json"
	cursorGrep        = "cursor/synthetic-pre-tool-use-grep.json"
	cursorGlob        = "cursor/synthetic-pre-tool-use-glob.json"
	cursorRead        = "cursor/synthetic-pre-tool-use-read.json"
	cursorShell       = "cursor/synthetic-pre-tool-use-shell.json"
	cursorBeforeShell = "cursor/synthetic-before-shell-execution.json"
	cursorAfterEdit   = "cursor/synthetic-after-file-edit.json"
	opencodeStart1    = "opencode/real-session-start-1.json"
	opencodeStart2    = "opencode/real-session-start-2.json"
	opencodeGrep      = "opencode/real-pre-tool-use-1.json" // grep PlaceOrder
	opencodeGlob      = "opencode/synthetic-pre-tool-use-glob.json"
	opencodeRead      = "opencode/synthetic-pre-tool-use-read.json"
	opencodeBash      = "opencode/synthetic-pre-tool-use-bash.json"
	opencodePostEdit  = "opencode/synthetic-post-tool-use-edit.json"
	opencodeCompact   = "opencode/synthetic-pre-compact.json"
)

// v builds a vars map from key/value pairs.
func v(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// Source written by edit scenarios.
const (
	refundRel = "go/internal/service/refund.go"
	refundSrc = "package service\n\n// RefundOrderZebra reverses an order.\nfunc RefundOrderZebra(id string) error { return nil }\n"
	orderRel  = "go/internal/service/order.go"
	otterSrc  = "\n// CancelOrderOtter cancels an order.\nfunc CancelOrderOtter(id string) error { return nil }\n"
	walrusSrc = "\n// AuditOrderWalrus audits an order.\nfunc AuditOrderWalrus(id string) error { return nil }\n"
)

var (
	isDeny    = wantContains(`"deny"`)
	malformed = strPtr(`{"session_id": "{{SESSION_ID}}", "tool_input": {"pattern": `)
)

func writeRefund(t *testing.T, w *world) { writeFile(t, w.path(refundRel), refundSrc) }

func scenarios() []scenario {
	return []scenario{
		// ---- Claude Code ----
		{
			name: "claude-session-start",
			steps: []step{
				// No index yet: Claude waits for the foreground build.
				{fixture: claudeStart1, check: all(wantContains("find_symbol", "index: "), wantSpawns(0))},
				// A second session finds the graph ready.
				{fixture: claudeStart2, vars: v("SESSION_ID", "sess-claude-second"), check: wantContains("· fresh")},
			},
		},
		{
			name:  "claude-subagent-start",
			setup: buildIndex,
			steps: []step{
				{fixture: claudeStart1},
				{fixture: claudeSubagent, check: wantContains("subagent")},
				// The subagent has its own steering state and budget.
				{fixture: claudeSubGrep, vars: v("PATTERN", "PlaceOrder"), check: isDeny},
				{fixture: claudeSubGrep, vars: v("PATTERN", "PlaceOrder")},
				// The main agent's first search is still steered.
				{fixture: claudeGrep, vars: v("PATTERN", "PlaceOrder"), check: isDeny},
			},
		},
		{
			name:  "claude-grep-deny-retry",
			setup: buildIndex,
			steps: []step{
				{fixture: claudeGrep, vars: v("PATTERN", "PlaceOrder"), check: all(isDeny, wantContains("go/internal/service/order.go"))},
				{fixture: claudeGrep, vars: v("PATTERN", "PlaceOrder"), check: wantEmpty},
			},
		},
		{
			name: "claude-glob-read",
			setup: func(t *testing.T, w *world) {
				writeLedger(t, w)
				buildIndex(t, w)
			},
			steps: []step{
				{fixture: claudeGlob, vars: v("GLOB", "**/*.go")},
				{fixture: claudeGlob, vars: v("GLOB", "**/*.go")},
				// Read is never blocked; a long code file gets its outline.
				{fixture: claudeRead, vars: v("FILE", "{{REPO}}/go/internal/ledger/ledger.go"), check: wantContains("Post01")},
				{fixture: claudeRead, vars: v("FILE", "{{REPO}}/"+orderRel), check: wantEmpty},
				{fixture: claudeRead, vars: v("FILE", "{{REPO}}/README.md"), check: wantEmpty},
			},
		},
		{
			name:  "claude-shell",
			setup: buildIndex,
			steps: []step{
				{fixture: claudeBash, vars: v("COMMAND", "rg -n OrderService"), check: isDeny},
				{fixture: claudeBash, vars: v("COMMAND", "rg -n OrderService"), check: wantEmpty},
				{fixture: claudeBash, vars: v("COMMAND", "ps aux | grep OrderService"), check: wantEmpty},
				{fixture: claudeBash, vars: v("COMMAND", "grep -rn PlaceOrder go/"), check: isDeny},
				{fixture: claudeBash, vars: v("COMMAND", "go test ./..."), check: wantEmpty},
			},
		},
		{
			name:  "claude-non-code-allow",
			setup: buildIndex,
			steps: []step{
				{fixture: claudeGrepPath, vars: v("PATTERN", "PlaceOrder", "PATH", "README.md"), check: wantEmpty},
				// An error message is text, not a symbol.
				{fixture: claudeGrep, vars: v("PATTERN", "out of stock"), check: wantEmpty},
				{fixture: claudeGlob, vars: v("GLOB", "**/*.md"), check: wantEmpty},
				{fixture: claudeBash, vars: v("COMMAND", "cat README.md"), check: wantEmpty},
			},
		},
		{
			name:  "claude-strict",
			setup: buildIndex,
			steps: []step{
				{fixture: claudeStart1, env: []string{"GRAPHMASTER_MODE=strict"}, check: wantContains("strict")},
				{fixture: claudeGrep, vars: v("PATTERN", "out of stock"), env: []string{"GRAPHMASTER_MODE=strict"}, check: isDeny},
				// strict never lets the retry through.
				{fixture: claudeGrep, vars: v("PATTERN", "out of stock"), env: []string{"GRAPHMASTER_MODE=strict"}, check: isDeny},
				// Project config selects strict the same way.
				{
					fixture: claudeGrep, vars: v("PATTERN", "PlaceOrder"),
					before: func(t *testing.T, w *world) {
						writeFile(t, w.path(".graphmaster/config.toml"), "mode = \"strict\"\n")
					},
					check: isDeny,
				},
				{fixture: claudeGrep, vars: v("PATTERN", "PlaceOrder"), check: isDeny},
				// Docs stay readable in strict mode.
				{fixture: claudeGrepPath, vars: v("PATTERN", "PlaceOrder", "PATH", "README.md"), check: wantEmpty},
			},
		},
		{
			name:  "claude-off",
			setup: buildIndex,
			steps: []step{
				{fixture: claudeStart1, env: []string{"GRAPHMASTER_MODE=off"}, check: wantContains("mode off")},
				{fixture: claudeGrep, vars: v("PATTERN", "PlaceOrder"), env: []string{"GRAPHMASTER_MODE=off"}, check: wantEmpty},
				{fixture: claudeBash, vars: v("COMMAND", "rg OrderService"), env: []string{"GRAPHMASTER_MODE=off"}, check: wantEmpty},
			},
		},
		{
			name:  "claude-edit-then-fresh-query",
			setup: buildIndex,
			steps: []step{
				// Write: the path is hinted dirty and reindexed.
				{fixture: claudePostWrite, vars: v("FILE", "{{REPO}}/"+refundRel), before: writeRefund, check: all(wantEmpty, wantSymbol("RefundOrderZebra", 1))},
				{fixture: claudeGrep, vars: v("PATTERN", "RefundOrderZebra"), check: all(isDeny, wantContains(refundRel))},
				// Edit with a relative path.
				{
					fixture: claudePostEdit, vars: v("FILE", orderRel),
					before: func(t *testing.T, w *world) { appendFile(t, w.path(orderRel), otterSrc) },
					check:  all(wantEmpty, wantSymbol("CancelOrderOtter", 1)),
				},
				{fixture: claudeGrep, vars: v("PATTERN", "CancelOrderOtter"), check: all(isDeny, wantContains(orderRel))},
				// A shell edit may touch anything: full freshen.
				{
					fixture: claudePostBash, vars: v("COMMAND", "cat >> "+orderRel+" <<'EOF' ... EOF"),
					before: func(t *testing.T, w *world) { appendFile(t, w.path(orderRel), walrusSrc) },
					check:  all(wantEmpty, wantSymbol("AuditOrderWalrus", 1)),
				},
				{fixture: claudeGrep, vars: v("PATTERN", "AuditOrderWalrus"), check: all(isDeny, wantContains(orderRel))},
			},
		},

		// ---- Codex ----
		{
			name: "codex-session-start-shell",
			steps: []step{
				// Codex also waits for the first build.
				{fixture: codexStart, check: all(wantContains("find_symbol"), wantSpawns(0))},
				{fixture: codexBash, vars: v("COMMAND", "rg OrderService"), check: isDeny},
				{fixture: codexBash, vars: v("COMMAND", "rg OrderService"), check: wantEmpty},
				{fixture: codexBash, vars: v("COMMAND", "ps aux | grep OrderService"), check: wantEmpty},
				// argv form with a workdir below the repository root.
				{fixture: codexArgv, vars: v("COMMAND", "grep -rn PlaceOrder .", "WORKDIR", "go"), check: isDeny},
				{fixture: codexBash, vars: v("COMMAND", "rg 'out of stock'"), check: wantEmpty},
			},
		},
		{
			name:  "codex-apply-patch",
			setup: buildIndex,
			steps: []step{
				// The patch itself is never steered.
				{fixture: codexPrePatch, vars: v("PATCH", "*** Begin Patch\n*** Add File: "+refundRel+"\n+package service\n*** End Patch\n"), check: wantEmpty},
				{
					fixture: codexPostPatch, vars: v("PATCH", "*** Begin Patch\n*** Add File: "+refundRel+"\n+package service\n*** End Patch\n"),
					before: writeRefund, check: all(wantEmpty, wantSymbol("RefundOrderZebra", 1)),
				},
				{fixture: codexBash, vars: v("COMMAND", "rg RefundOrderZebra"), check: all(isDeny, wantContains(refundRel))},
				// Update + Move to: both paths are hinted.
				{
					fixture: codexPostPatchIn,
					vars:    v("PATCH", "*** Begin Patch\n*** Update File: "+refundRel+"\n*** Move to: go/internal/service/refunds.go\n@@\n-x\n+y\n*** End Patch\n"),
					before: func(t *testing.T, w *world) {
						removeFile(t, w.path(refundRel))
						writeFile(t, w.path("go/internal/service/refunds.go"), refundSrc)
					},
					check: all(wantEmpty, wantSymbol("RefundOrderZebra", 1)),
				},
				// A new session, so the search is not a retry of step 3.
				{fixture: codexBash, vars: v("COMMAND", "rg -n RefundOrderZebra", "SESSION_ID", "sess-codex-moved"), check: all(isDeny, wantContains("go/internal/service/refunds.go"))},
				// Delete: the symbol leaves the graph.
				{
					fixture: codexPostPatch, vars: v("PATCH", "*** Begin Patch\n*** Delete File: go/internal/service/refunds.go\n*** End Patch\n"),
					before: func(t *testing.T, w *world) { removeFile(t, w.path("go/internal/service/refunds.go")) },
					check:  all(wantEmpty, wantSymbol("RefundOrderZebra", 0)),
				},
				{fixture: codexBash, vars: v("COMMAND", "rg --line-number RefundOrderZebra", "SESSION_ID", "sess-codex-deleted"), check: wantEmpty},
			},
		},
		{
			name:  "codex-strict-off",
			setup: buildIndex,
			steps: []step{
				{fixture: codexBash, vars: v("COMMAND", "rg 'out of stock'"), env: []string{"GRAPHMASTER_MODE=strict"}, check: isDeny},
				{fixture: codexBash, vars: v("COMMAND", "rg 'out of stock'"), env: []string{"GRAPHMASTER_MODE=strict"}, check: isDeny},
				{fixture: codexBash, vars: v("COMMAND", "rg OrderService"), env: []string{"GRAPHMASTER_MODE=off"}, check: wantEmpty},
			},
		},

		// ---- Cursor ----
		{
			name:   "cursor-session-start",
			spawns: true,
			steps: []step{
				// Cursor cannot wait: the build goes to the background.
				{fixture: cursorStart, check: all(wantContains("background"), wantSpawns(1))},
				// Once built, the context has the index summary; a refresh
				// is still delegated.
				{fixture: cursorStart, before: buildIndex, check: all(wantContains("index: "), wantSpawns(2))},
			},
		},
		{
			name:  "cursor-pre-tool-use",
			setup: buildIndex,
			steps: []step{
				{fixture: cursorSubagent},
				{fixture: cursorGrep, vars: v("PATTERN", "PlaceOrder"), check: isDeny},
				{fixture: cursorGrep, vars: v("PATTERN", "PlaceOrder"), check: wantNoDeny},
				{fixture: cursorGlob, vars: v("GLOB", "**/*.go")},
				{fixture: cursorRead, vars: v("FILE", "{{REPO}}/"+orderRel)},
				{fixture: cursorShell, vars: v("COMMAND", "rg OrderService"), check: isDeny},
				{fixture: cursorBeforeShell, vars: v("COMMAND", "grep -rn NewMemory ."), check: isDeny},
				{fixture: cursorBeforeShell, vars: v("COMMAND", "git status")},
			},
		},
		{
			name:  "cursor-double-invoke",
			setup: buildIndex,
			steps: []step{
				// Cursor runs the Claude Code hook and its native hook for
				// the same call: one decision, replayed to the duplicate.
				{fixture: claudeCursorDup, vars: v("PATTERN", "PlaceOrder", "GENERATION_ID", "g1", "TOOL_USE_ID", "tu1"), check: isDeny},
				{fixture: cursorGrep, vars: v("PATTERN", "PlaceOrder", "GENERATION_ID", "g1", "TOOL_USE_ID", "tu1"), check: all(isDeny, wantSameAsStep(1))},
				// The next generation is a deliberate retry: allowed.
				{fixture: cursorGrep, vars: v("PATTERN", "PlaceOrder", "GENERATION_ID", "g2", "TOOL_USE_ID", "tu2"), check: wantNoDeny},
				// With Cursor-native hooks installed the Claude-side call
				// stays silent and the native hook decides.
				{fixture: claudeCursorDup, vars: v("PATTERN", "NewMemory", "GENERATION_ID", "g3", "TOOL_USE_ID", "tu3"), before: installCursorHooks, check: wantEmpty},
				{fixture: cursorGrep, vars: v("PATTERN", "NewMemory", "GENERATION_ID", "g3", "TOOL_USE_ID", "tu3"), check: isDeny},
			},
		},
		{
			name:  "cursor-after-file-edit",
			setup: buildIndex,
			steps: []step{
				{fixture: cursorAfterEdit, vars: v("FILE", "{{REPO}}/"+refundRel), before: writeRefund, check: wantSymbol("RefundOrderZebra", 1)},
				{fixture: cursorGrep, vars: v("PATTERN", "RefundOrderZebra"), check: all(isDeny, wantContains(refundRel))},
			},
		},

		// ---- OpenCode ----
		{
			name:   "opencode-session-start",
			spawns: true,
			steps: []step{
				// The plugin cannot carry start context; the build is
				// started in the background.
				{fixture: opencodeStart1, check: all(wantEmpty, wantSpawns(1))},
				// Without a graph the first search is not steered (and a
				// build is requested again).
				{fixture: opencodeGrep, check: wantEmpty},
				{fixture: opencodeStart2, vars: v("SESSIONID", "ses-opencode-second"), before: buildIndex, check: wantEmpty},
				{fixture: opencodeCompact, check: wantContains("find_symbol")},
			},
		},
		{
			name:  "opencode-pre-tool-use",
			setup: buildIndex,
			steps: []step{
				// Captured payload: the deny reply makes the plugin throw.
				{fixture: opencodeGrep, check: wantContains(`"action":"deny"`, "PlaceOrder")},
				{fixture: opencodeGrep, check: wantEmpty},
				{fixture: opencodeGlob, vars: v("GLOB", "**/*.go")},
				{fixture: opencodeRead, vars: v("FILE", "{{REPO}}/"+orderRel), check: wantEmpty},
				{fixture: opencodeBash, vars: v("COMMAND", "rg OrderService"), check: isDeny},
				{fixture: opencodeBash, vars: v("COMMAND", "ps aux | grep OrderService"), check: wantEmpty},
			},
		},
		{
			name:  "opencode-edit-then-fresh-query",
			setup: buildIndex,
			steps: []step{
				{fixture: opencodePostEdit, vars: v("FILE", "{{REPO}}/"+refundRel), before: writeRefund, check: all(wantEmpty, wantSymbol("RefundOrderZebra", 1))},
				{fixture: opencodeBash, vars: v("COMMAND", "rg RefundOrderZebra"), check: all(isDeny, wantContains(refundRel))},
			},
		},

		// ---- Fail-open on malformed input, every agent ----
		{
			name:  "malformed-fail-open",
			setup: buildIndex,
			steps: []step{
				{fixture: claudeGrep, stdin: malformed, check: all(wantEmpty, wantLog("parse payload"))},
				{fixture: claudeGrep, stdin: strPtr(""), check: wantEmpty},
				{fixture: claudeGrep, stdin: strPtr(`["not", "an", "object"]`), check: wantEmpty},
				{fixture: codexBash, stdin: malformed, check: wantEmpty},
				{fixture: codexBash, stdin: strPtr(`{"session_id": 42, "tool_input": "x"}`), check: wantEmpty},
				{fixture: cursorGrep, stdin: malformed, check: wantStdout("{}\n")},
				{fixture: cursorGrep, stdin: strPtr(`null`), check: wantStdout("{}\n")},
				{fixture: opencodeGrep, stdin: malformed, check: wantEmpty},
				{fixture: opencodeGrep, stdin: strPtr(`{"tool": "grep", "args": "not an object", "cwd": "{{REPO}}"}`), check: wantEmpty},
				// An agent graphmaster does not know is silent too.
				{fixture: claudeGrep, agent: "vim", vars: v("PATTERN", "PlaceOrder"), check: wantEmpty},
			},
		},
	}
}

// TestReplay replays every scenario through the in-process dispatcher and
// compares the transcript with testdata/golden/<scenario>.golden.
func TestReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("hook replay builds indexes; skipped with -short")
	}
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			got := play(t, sc, inProcess)
			compareGolden(t, sc.name, got, true)
		})
	}
}

// TestGoldenFilesHaveScenarios keeps testdata/golden free of stale files.
func TestGoldenFilesHaveScenarios(t *testing.T) {
	names := map[string]bool{}
	for _, sc := range scenarios() {
		if names[sc.name] {
			t.Fatalf("duplicate scenario %s", sc.name)
		}
		names[sc.name] = true
	}
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.golden"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		name = name[:len(name)-len(".golden")]
		if !names[name] {
			t.Errorf("%s has no scenario", f)
		}
	}
}

// TestScenarioFixturesExist fails fast on a misspelled fixture name.
func TestScenarioFixturesExist(t *testing.T) {
	for _, sc := range scenarios() {
		for i, st := range sc.steps {
			if _, err := Load(st.fixture); err != nil {
				t.Errorf("%s step %d: %v", sc.name, i+1, err)
			}
		}
	}
}
