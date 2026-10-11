package install

import (
	"fmt"
	"slices"
	"strings"
)

// Action is what an operation did (or would do) to a file.
type Action string

const (
	ActionNone     Action = "unchanged"
	ActionCreate   Action = "create"
	ActionUpdate   Action = "update"
	ActionRestore  Action = "restore"        // byte-exact restore from backup
	ActionDelete   Action = "delete"         // file graphmaster created removed
	ActionSurgical Action = "remove-entries" // only graphmaster entries removed
	ActionMissing  Action = "missing"        // recorded file no longer exists
)

// FileResult reports one file.
type FileResult struct {
	Path   string
	Agents []string
	Action Action
	// Diff is a unified diff of the change ("" when unchanged).
	Diff string
	Note string
}

// Report is the outcome of Install or Uninstall.
type Report struct {
	DryRun bool
	Files  []FileResult
}

// Changed reports whether any file was (or would be) modified.
func (r *Report) Changed() bool {
	for _, f := range r.Files {
		if f.Action != ActionNone && f.Action != ActionMissing {
			return true
		}
	}
	return false
}

// String renders a human-readable summary with diffs.
func (r *Report) String() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("dry run: no files written\n")
	}
	for _, f := range r.Files {
		fmt.Fprintf(&b, "%s %s", f.Action, f.Path)
		if len(f.Agents) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(f.Agents, ","))
		}
		if f.Note != "" {
			fmt.Fprintf(&b, " (%s)", f.Note)
		}
		b.WriteByte('\n')
		b.WriteString(f.Diff)
	}
	return b.String()
}

// finalize derives the action from the planned content.
func (w *work) finalize() {
	if w.action == ActionMissing {
		return
	}
	switch {
	case !w.changed():
		w.action = ActionNone
	case !w.keep:
		w.action = ActionDelete
	case !w.exists:
		w.action = ActionCreate
	case w.restored:
		w.action = ActionRestore
	case len(w.agentOrder) == 0:
		w.action = ActionSurgical
	default:
		w.action = ActionUpdate
	}
}

func (w *work) result() FileResult {
	agents := slices.Clone(w.dropAgents)
	for _, a := range w.agentOrder {
		if !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
	}
	var after []byte
	if w.keep {
		after = w.content
	}
	return FileResult{
		Path:   w.path,
		Agents: agents,
		Action: w.action,
		Diff:   unifiedDiff(w.path, w.orig, after),
		Note:   w.note,
	}
}
