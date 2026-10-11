package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic payloads in the shapes Claude Code, Codex, Cursor and the
// generated OpenCode plugin send. They are not recordings.

func claudeTool(sid, id, cwd, event, tool string, input map[string]any) map[string]any {
	return map[string]any{
		"session_id": sid, "cwd": cwd, "hook_event_name": event,
		"tool_name": tool, "tool_input": input, "tool_use_id": id,
	}
}

func claudeGrep(sid, id, cwd, pattern string, extra ...string) map[string]any {
	in := map[string]any{"pattern": pattern}
	for i := 0; i+1 < len(extra); i += 2 {
		in[extra[i]] = extra[i+1]
	}
	return claudeTool(sid, id, cwd, "PreToolUse", "Grep", in)
}

func codexBash(sid, id, cwd, command string) map[string]any {
	return map[string]any{
		"session_id": sid, "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": "Bash",
		"tool_input": map[string]any{"command": command}, "tool_use_id": id,
	}
}

func cursorGrep(sid, gen, id, root, pattern string) map[string]any {
	return map[string]any{
		"conversation_id": sid, "generation_id": gen, "cursor_version": "1.7.2",
		"hook_event_name": "preToolUse", "tool_name": "Grep", "tool_use_id": id,
		"tool_input": map[string]any{"pattern": pattern}, "workspace_roots": []string{root},
	}
}

func opencodeTool(sid, id, cwd, tool string, args map[string]any) map[string]any {
	return map[string]any{"tool": tool, "sessionID": sid, "callID": id, "args": args, "cwd": cwd}
}

// hooks invokes `graphmaster hook` like an agent: one process per event,
// the payload on stdin, the repository as working directory.
type hooks struct {
	t    *testing.T
	root string
	env  []string
}

func (h hooks) run(agent, event string, payload map[string]any, extraEnv ...string) result {
	h.t.Helper()
	env := append(append([]string(nil), h.env...), extraEnv...)
	r := gm(h.t, h.root, env, mustJSON(h.t, payload), "hook", event, "--agent", agent)
	if r.code != 0 {
		h.t.Fatalf("hook %s --agent %s: exit %d\n%s", event, agent, r.code, r.stderr)
	}
	return r
}

// specific is the hookSpecificOutput of Claude Code and Codex.
type specific struct {
	Out struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func decodeSpecific(t *testing.T, r result) specific {
	t.Helper()
	var s specific
	if strings.TrimSpace(r.stdout) == "" {
		return s
	}
	if err := json.Unmarshal([]byte(r.stdout), &s); err != nil {
		t.Fatalf("stdout %q: %v", r.stdout, err)
	}
	return s
}

func requireDeny(t *testing.T, label string, r result, wantInReason ...string) {
	t.Helper()
	s := decodeSpecific(t, r)
	if s.Out.PermissionDecision != "deny" {
		t.Fatalf("%s: want deny, got %q", label, r.stdout)
	}
	for _, w := range wantInReason {
		if !strings.Contains(s.Out.PermissionDecisionReason, w) {
			t.Fatalf("%s: reason lacks %q:\n%s", label, w, s.Out.PermissionDecisionReason)
		}
	}
}

func requireNotDenied(t *testing.T, label string, r result) {
	t.Helper()
	if s := decodeSpecific(t, r); s.Out.PermissionDecision == "deny" {
		t.Fatalf("%s: want no deny, got %s", label, r.stdout)
	}
}

func TestHookFlows(t *testing.T) {
	binary(t)
	root := fixture(t)
	h := hooks{t: t, root: root, env: childEnv(t.TempDir())}
	graphDB := filepath.Join(root, ".graphmaster", "graph.db")

	t.Run("claude session start builds the index", func(t *testing.T) {
		if _, err := os.Stat(graphDB); err == nil {
			t.Fatal("graph.db exists before the session starts")
		}
		r := h.run("claude", "session-start", map[string]any{"session_id": "s-claude", "cwd": root, "hook_event_name": "SessionStart", "source": "startup"})
		if c := decodeSpecific(t, r).Out.AdditionalContext; !strings.Contains(c, "find_symbol") {
			t.Fatalf("session context = %q", c)
		}
		if _, err := os.Stat(graphDB); err != nil {
			t.Fatalf("session start did not build the index: %v", err)
		}
	})

	t.Run("claude grep deny then retry allow", func(t *testing.T) {
		requireDeny(t, "first grep", h.run("claude", "pre-tool-use", claudeGrep("s-claude", "t1", root, "PlaceOrder")),
			"PlaceOrder", "go/internal/service/order.go")
		requireNotDenied(t, "same grep again", h.run("claude", "pre-tool-use", claudeGrep("s-claude", "t2", root, "PlaceOrder")))
	})

	t.Run("claude grep on README allowed", func(t *testing.T) {
		requireNotDenied(t, "README.md", h.run("claude", "pre-tool-use", claudeGrep("s-claude", "t3", root, "NewMemory", "path", "README.md")))
	})

	t.Run("claude grep for an error message allowed", func(t *testing.T) {
		requireNotDenied(t, "error text", h.run("claude", "pre-tool-use", claudeGrep("s-claude", "t5", root, "out of stock")))
	})

	t.Run("codex rg denied", func(t *testing.T) {
		requireDeny(t, "rg OrderService", h.run("codex", "pre-tool-use", codexBash("s-codex", "c1", root, "rg OrderService")), "OrderService")
		requireNotDenied(t, "ps | grep", h.run("codex", "pre-tool-use", codexBash("s-codex", "c2", root, "ps aux | grep OrderService")))
	})

	t.Run("opencode grep denied", func(t *testing.T) {
		r := h.run("opencode", "pre-tool-use", opencodeTool("s-oc", "o1", root, "grep", map[string]any{"pattern": "NewOrderService"}))
		var rep struct {
			Action string `json:"action"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || rep.Action != "deny" || !strings.Contains(rep.Reason, "NewOrderService") {
			t.Fatalf("opencode reply %q (%v)", r.stdout, err)
		}
	})

	t.Run("cursor via claude hooks decides once", func(t *testing.T) {
		// No Cursor-native hooks in this HOME: both calls are answered,
		// the second one by replaying the first decision.
		p := cursorGrep("s-cursor", "g1", "tu1", root, "NewMemory")
		first := h.run("claude", "pre-tool-use", p)
		second := h.run("cursor", "pre-tool-use", p)
		if g := cursorDecision(t, first); g.Permission != "deny" || !strings.Contains(g.AgentMessage, "NewMemory") {
			t.Fatalf("first call = %s", first.stdout)
		}
		if first.stdout != second.stdout {
			t.Fatalf("duplicate must replay the decision:\nfirst  %s\nsecond %s", first.stdout, second.stdout)
		}
		// The next generation is a deliberate retry: allowed.
		if g := cursorDecision(t, h.run("cursor", "pre-tool-use", cursorGrep("s-cursor", "g2", "tu2", root, "NewMemory"))); g.Permission == "deny" {
			t.Fatal("retry must be allowed")
		}
	})

	t.Run("edit then pre-tool-use sees the new symbol", func(t *testing.T) {
		path := filepath.Join(root, "go", "internal", "service", "refund.go")
		writeFile(t, path, "package service\n\n// RefundOrderZebra reverses an order.\nfunc RefundOrderZebra(id string) error { return nil }\n")
		post := claudeTool("s-claude", "e1", root, "PostToolUse", "Write", map[string]any{"file_path": path, "content": "..."})
		if r := h.run("claude", "post-tool-use", post); strings.TrimSpace(r.stdout) != "" {
			t.Fatalf("post-tool-use output = %s", r.stdout)
		}
		requireDeny(t, "grep new symbol", h.run("claude", "pre-tool-use", claudeGrep("s-claude", "t4", root, "RefundOrderZebra")),
			"RefundOrderZebra", "go/internal/service/refund.go")
	})

	t.Run("bash edit then pre-tool-use sees the new symbol", func(t *testing.T) {
		// The shell command rewrites a file; PostToolUse(Bash) runs a full
		// freshen, so the next steering lookup knows the new name.
		path := filepath.Join(root, "go", "internal", "service", "order.go")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(b)+"\n// CancelOrderOtter cancels an order.\nfunc CancelOrderOtter(id string) error { return nil }\n")
		post := claudeTool("s-claude", "b1", root, "PostToolUse", "Bash", map[string]any{"command": "sed -i.bak s/x/x/ go/internal/service/order.go"})
		h.run("claude", "post-tool-use", post)
		requireDeny(t, "grep shell-added symbol", h.run("claude", "pre-tool-use", claudeGrep("s-claude", "t6", root, "CancelOrderOtter")),
			"CancelOrderOtter", "go/internal/service/order.go")
	})

	t.Run("strict mode from project config", func(t *testing.T) {
		cfg := filepath.Join(root, ".graphmaster", "config.toml")
		writeFile(t, cfg, "mode = \"strict\"\n")
		defer os.Remove(cfg)
		requireDeny(t, "strict symbol", h.run("claude", "pre-tool-use", claudeGrep("s-strict", "x1", root, "NewMemory")), "NewMemory")
		requireDeny(t, "strict retry", h.run("claude", "pre-tool-use", claudeGrep("s-strict", "x2", root, "NewMemory")), "NewMemory")
	})

	t.Run("mode off allows", func(t *testing.T) {
		requireNotDenied(t, "off", h.run("claude", "pre-tool-use", claudeGrep("s-off", "o1", root, "OrderService"), "GRAPHMASTER_MODE=off"))
	})

	if b, err := os.ReadFile(filepath.Join(root, ".graphmaster", "log")); err == nil && len(b) > 0 {
		t.Logf("hook log:\n%s", b)
	}
}
