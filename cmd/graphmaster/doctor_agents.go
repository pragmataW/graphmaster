package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/pragmataW/graphmaster/internal/adapters"
	"github.com/pragmataW/graphmaster/internal/hookapi"
	"github.com/pragmataW/graphmaster/internal/install"
	"github.com/pragmataW/graphmaster/internal/instructions"
)

// agentResult is what doctor learned about one agent.
type agentResult struct {
	name      hookapi.Agent
	detected  bool
	installed bool
	// present marks components with a graphmaster entry (current or stale).
	present map[install.Component]bool
	files   map[install.Component][]string
	sec     *section
}

// components in report order, with their labels.
var components = []struct {
	c     install.Component
	label string
}{
	{install.ComponentMCP, "MCP server registered"},
	{install.ComponentHooks, "hooks installed"},
	{install.ComponentInstructions, "instructions"},
	{install.ComponentOther, "settings"},
}

func doctorAgents(r *doctorReport, s setup, bin string) ([]*agentResult, error) {
	st, err := install.NewEngine(s.env).State()
	if err != nil {
		return nil, err
	}
	opts := adapters.InstallOptions{Binary: bin, Instructions: instructions.Text()}
	var results []*agentResult
	for _, a := range s.agents {
		results = append(results, doctorAgent(r.section(string(a.Name())), s, st, a, opts))
	}
	checkCursorDedupe(s.env, results)
	return results, nil
}

func doctorAgent(sec *section, s setup, st *install.State, a adapters.Agent, opts adapters.InstallOptions) *agentResult {
	res := &agentResult{name: a.Name(), detected: a.Detect(s.env), sec: sec,
		present: map[install.Component]bool{}, files: map[install.Component][]string{}}
	plan, err := a.InstallPlan(s.env, opts)
	if err != nil {
		sec.add(lvlFail, "cannot compute the expected configuration: %v", err)
		return res
	}
	checks := install.CheckPlan(plan, st)
	byComp := map[install.Component][]install.OpCheck{}
	for _, c := range checks {
		byComp[c.Component] = append(byComp[c.Component], c)
		if c.Status == install.CheckOK || c.Status == install.CheckStale {
			res.present[c.Component] = true
		}
		if !slices.Contains(res.files[c.Component], c.Path) {
			res.files[c.Component] = append(res.files[c.Component], c.Path)
		}
	}
	res.installed = len(st.FilesOf(plan.Agent)) > 0 || len(res.present) > 0

	if !res.installed {
		for _, c := range checks {
			if c.Status == install.CheckInvalid {
				sec.add(lvlWarn, "cannot read %s: %v", displayPath(s.env, c.Path), c.Err)
			}
		}
		if res.detected {
			sec.add(lvlWarn, "detected, but graphmaster is not installed (run graphmaster install --agents %s)", a.Name())
		} else {
			sec.add(lvlInfo, "not detected")
		}
		return res
	}
	if res.detected {
		sec.add(lvlOK, "detected")
	} else {
		sec.add(lvlWarn, "agent not detected, but graphmaster entries are installed for it")
	}
	for _, comp := range components {
		if cs := byComp[comp.c]; len(cs) > 0 {
			reportComponent(sec, s.env, comp.c, comp.label, cs, opts.Binary)
		} else if comp.c == install.ComponentInstructions {
			sec.add(lvlInfo, "no global instruction file for this agent; the MCP server carries the guidance")
		}
	}
	if res.name == hookapi.AgentCodex && res.present[install.ComponentHooks] {
		checkCodexTrust(sec, checks)
	}
	return res
}

// reportComponent adds one finding (or a few) for one component.
func reportComponent(sec *section, env install.Env, comp install.Component, label string, cs []install.OpCheck, bin string) {
	var files, missing []string
	var bins []string
	stale := false
	for _, c := range cs {
		p := displayPath(env, c.Path)
		if !slices.Contains(files, p) {
			files = append(files, p)
		}
		switch c.Status {
		case install.CheckInvalid:
			sec.add(lvlFail, "%s: cannot read %s: %v", label, p, c.Err)
			return
		case install.CheckMissing:
			if !slices.Contains(missing, p) {
				missing = append(missing, p)
			}
		case install.CheckStale:
			stale = true
		}
		for _, b := range c.Binaries {
			if !slices.Contains(bins, b) {
				bins = append(bins, b)
			}
		}
	}
	where := strings.Join(files, ", ")
	usesBinary := comp == install.ComponentMCP || comp == install.ComponentHooks

	if len(missing) > 0 {
		lvl := lvlWarn
		if usesBinary {
			lvl = lvlFail
		}
		sec.add(lvl, "%s: missing from %s (re-run graphmaster install)", label, strings.Join(missing, ", "))
		return
	}
	if usesBinary {
		broken := false
		for _, b := range bins {
			if !isExecutable(b) {
				sec.add(lvlFail, "%s in %s point to missing binary %s (re-run graphmaster install)", label, where, b)
				broken = true
			}
		}
		if broken {
			return
		}
	}
	if stale {
		var others []string
		for _, b := range bins {
			if b != bin {
				others = append(others, b)
			}
		}
		if len(others) > 0 {
			sec.add(lvlWarn, "%s in %s use %s, not this graphmaster (%s); re-run graphmaster install",
				label, where, strings.Join(others, ", "), bin)
		} else {
			sec.add(lvlWarn, "%s in %s differ from what this graphmaster installs; re-run graphmaster install", label, where)
		}
		return
	}
	if usesBinary && len(bins) > 0 {
		sec.add(lvlOK, "%s in %s -> %s", label, where, strings.Join(bins, ", "))
		return
	}
	sec.add(lvlOK, "%s in %s", label, where)
}

// checkCodexTrust inspects Codex's hook trust records. Codex runs a
// user hook only after it is approved in /hooks, which stores
// [hooks.state."<hooks.json>:<event>:<group>:<hook>"] trusted_hash = ...
// in config.toml next to hooks.json. The hash itself is Codex-internal, so
// a recorded entry is taken as trusted (best effort).
func checkCodexTrust(sec *section, checks []install.OpCheck) {
	var keys []string
	hooksPath := ""
	for _, c := range checks {
		ap, ok := c.Op.(install.JSONAppend)
		if !ok || c.Component != install.ComponentHooks || c.Index < 0 {
			continue
		}
		event := ap.Pointer[strings.LastIndexByte(ap.Pointer, '/')+1:]
		keys = append(keys, fmt.Sprintf("%s:%s:%d:0", c.Path, snakeCase(event), c.Index))
		hooksPath = c.Path
	}
	if len(keys) == 0 {
		return
	}
	const howTo = "start codex, run /hooks and approve the graphmaster hooks"
	trusted, err := codexTrustedHooks(filepath.Join(filepath.Dir(hooksPath), "config.toml"))
	if err != nil {
		sec.add(lvlWarn, "cannot read Codex hook trust (%v); if graphmaster never steers in Codex, %s", err, howTo)
		return
	}
	untrusted := 0
	for _, k := range keys {
		if trusted[k] == "" {
			untrusted++
		}
	}
	if untrusted == 0 {
		sec.add(lvlOK, "hooks trusted in Codex (approve them again in /hooks after graphmaster changes them)")
		return
	}
	sec.add(lvlWarn, "%d of %d graphmaster hooks not trusted yet: %s", untrusted, len(keys), howTo)
}

// codexTrustedHooks returns hooks.state keys with their trusted_hash.
func codexTrustedHooks(configPath string) (map[string]string, error) {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Hooks struct {
			State map[string]struct {
				TrustedHash string `toml:"trusted_hash"`
			} `toml:"state"`
		} `toml:"hooks"`
	}
	if err := toml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(doc.Hooks.State))
	for k, v := range doc.Hooks.State {
		out[k] = v.TrustedHash
	}
	return out, nil
}

// checkCursorDedupe explains how Cursor's habit of also running Claude
// Code's hooks interacts with graphmaster.
func checkCursorDedupe(env install.Env, results []*agentResult) {
	var claude, cursor *agentResult
	for _, r := range results {
		switch r.name {
		case hookapi.AgentClaude:
			claude = r
		case hookapi.AgentCursor:
			cursor = r
		}
	}
	if claude == nil || cursor == nil || !cursor.detected || !claude.present[install.ComponentHooks] {
		return
	}
	settings := "the Claude Code settings"
	if f := claude.files[install.ComponentHooks]; len(f) > 0 {
		settings = displayPath(env, f[0])
	}
	if cursor.present[install.ComponentHooks] {
		cursor.sec.add(lvlOK, "Cursor also runs the hooks in %s; dedupe active: Cursor-native graphmaster hooks answer and the duplicate calls stay silent", settings)
		return
	}
	cursor.sec.add(lvlInfo, "Cursor also runs the hooks in %s, so they steer Cursor too; dedupe inactive (no Cursor-native graphmaster hooks)", settings)
}

// snakeCase converts a Codex/Claude event name (PreToolUse) to the form
// Codex uses in trust keys (pre_tool_use).
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || fi.Mode().Perm()&0o111 != 0
}
