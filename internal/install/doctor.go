package install

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
)

// CheckStatus is how far a file matches one desired plan edit.
type CheckStatus string

const (
	CheckOK      CheckStatus = "ok"      // exactly what the plan wants
	CheckStale   CheckStatus = "stale"   // a graphmaster entry exists but differs
	CheckMissing CheckStatus = "missing" // no graphmaster entry
	CheckInvalid CheckStatus = "invalid" // the file cannot be read or parsed
)

// Component groups plan edits by what they do for the agent.
type Component string

const (
	ComponentMCP          Component = "mcp"
	ComponentHooks        Component = "hooks"
	ComponentInstructions Component = "instructions"
	ComponentOther        Component = "other"
)

// OpCheck is the read-only verdict for one op of a plan (or one generated
// file, whose Op is nil).
type OpCheck struct {
	Path      string
	Format    Format
	Op        Op
	Component Component
	Status    CheckStatus
	// Found is graphmaster's current text in the file: the JSON member, the
	// array elements recognized as graphmaster's, the marker block or the
	// whole generated file.
	Found []string
	// Index is the array position of the matching (or stale) element for
	// JSONAppend; -1 otherwise.
	Index int
	// Binaries are the absolute graphmaster executable paths Found refers to.
	Binaries []string
	// Err explains CheckInvalid.
	Err error
}

// CheckPlan compares p with the files on disk without modifying anything.
// st (may be nil) identifies array elements graphmaster wrote earlier, so a
// hook installed with another binary path is reported stale, not missing;
// without it, elements mentioning graphmaster are recognized by content.
func CheckPlan(p Plan, st *State) []OpCheck {
	var out []OpCheck
	for _, fc := range p.Files {
		var prior []Insertion
		if st != nil && st.Files[fc.Path] != nil {
			prior = st.Files[fc.Path].insertionsOf(p.Agent)
		}
		out = append(out, checkFile(fc, prior)...)
	}
	for i := range out {
		for _, f := range out[i].Found {
			for _, b := range binaryRefs(f) {
				if !slices.Contains(out[i].Binaries, b) {
					out[i].Binaries = append(out[i].Binaries, b)
				}
			}
		}
	}
	return out
}

// checkFile returns one check per op (one for a generated file).
func checkFile(fc FileChange, prior []Insertion) []OpCheck {
	ops := fc.Ops
	if fc.Format == FormatGenerated {
		ops = []Op{nil}
	}
	checks := make([]OpCheck, len(ops))
	for i, op := range ops {
		checks[i] = OpCheck{Path: fc.Path, Format: fc.Format, Op: op, Component: Classify(fc, op),
			Status: CheckMissing, Index: -1}
	}
	b, exists, err := readOptional(fc.Path)
	if err != nil || !exists {
		for i := range checks {
			if err != nil {
				checks[i].Status, checks[i].Err = CheckInvalid, err
			}
		}
		return checks
	}
	switch fc.Format {
	case FormatJSON:
		checkJSON(b, checks, prior)
	case FormatTOML, FormatMarkdown:
		checkMarkers(fc, b, checks)
	case FormatGenerated:
		checks[0].Found = []string{string(b)}
		checks[0].Status = CheckStale
		if string(b) == string(fc.Content) {
			checks[0].Status = CheckOK
		}
	}
	return checks
}

func checkJSON(b []byte, checks []OpCheck, prior []Insertion) {
	doc, err := parseJSONDoc(b)
	if err != nil {
		for i := range checks {
			checks[i].Status, checks[i].Err = CheckInvalid, fmt.Errorf("invalid JSON: %w", err)
		}
		return
	}
	for i := range checks {
		c := &checks[i]
		switch o := c.Op.(type) {
		case JSONSet:
			want, err := canonicalOp(o.Value)
			if err != nil {
				c.Status, c.Err = CheckInvalid, err
				continue
			}
			got, ok := doc.get(o.Pointer)
			if !ok {
				continue
			}
			c.Found = []string{string(got)}
			c.Status = CheckStale
			if cg, _ := canonicalJSON(got); cg == want {
				c.Status = CheckOK
			}
		case JSONAppend:
			want, err := canonicalOp(o.Value)
			if err != nil {
				c.Status, c.Err = CheckInvalid, err
				continue
			}
			checkAppend(doc, o.Pointer, want, prior, c)
		}
	}
}

// checkAppend looks for want among the elements of the array at ptr; an
// element graphmaster recorded earlier, or one mentioning graphmaster,
// makes the op stale instead of missing.
func checkAppend(doc *jsonDoc, ptr, want string, prior []Insertion, c *OpCheck) {
	n := doc.v.Find(ptr)
	if n == nil {
		return
	}
	arr, ok := n.Value.(*hujson.Array)
	if !ok {
		return
	}
	recorded := map[string]bool{}
	for _, p := range prior {
		if p.Kind == KindJSONElement && p.Pointer == ptr {
			if cv, err := canonicalJSON(p.Value); err == nil {
				recorded[cv] = true
			}
		}
	}
	for i := range arr.Elements {
		raw := nodeJSON(&arr.Elements[i])
		cv, _ := canonicalJSON(raw)
		if cv == want {
			c.Status, c.Index, c.Found = CheckOK, i, []string{string(raw)}
			return
		}
		if recorded[cv] || strings.Contains(cv, BinaryName) {
			if c.Index < 0 {
				c.Index = i
			}
			c.Status = CheckStale
			c.Found = append(c.Found, string(raw))
		}
	}
}

func checkMarkers(fc FileChange, b []byte, checks []OpCheck) {
	if fc.Format == FormatTOML {
		if err := validateTOML(b); err != nil {
			for i := range checks {
				checks[i].Status, checks[i].Err = CheckInvalid, err
			}
			return
		}
	}
	style := fc.markers()
	blocks, err := style.scan(b)
	if err != nil {
		for i := range checks {
			checks[i].Status, checks[i].Err = CheckInvalid, err
		}
		return
	}
	for i := range checks {
		c := &checks[i]
		blk, ok := c.Op.(Block)
		if !ok {
			continue
		}
		for _, bl := range blocks {
			if bl.id != blk.ID {
				continue
			}
			got := string(b[bl.start:bl.end])
			c.Found = []string{got}
			c.Status = CheckStale
			if want, err := style.render(blk.ID, blk.Body); err == nil && strings.TrimSuffix(got, "\n") == strings.TrimSuffix(want, "\n") {
				c.Status = CheckOK
			}
		}
	}
}

func canonicalOp(v any) (string, error) {
	raw, err := toRaw(v)
	if err != nil {
		return "", err
	}
	return canonicalJSON(raw)
}

// Classify guesses what an edit is for from its target and content; it
// needs no knowledge of a particular agent.
func Classify(fc FileChange, op Op) Component {
	base := strings.ToLower(filepath.Base(fc.Path))
	switch fc.Format {
	case FormatMarkdown:
		return ComponentInstructions
	case FormatGenerated:
		switch strings.ToLower(filepath.Ext(base)) {
		case ".md", ".mdc", ".txt":
			return ComponentInstructions
		case ".js", ".mjs", ".ts", ".sh":
			return ComponentHooks
		}
	case FormatTOML:
		if b, ok := op.(Block); ok {
			switch {
			case strings.Contains(b.Body, "mcp_servers"):
				return ComponentMCP
			case strings.Contains(b.Body, "hooks"):
				return ComponentHooks
			}
		}
	case FormatJSON:
		var ptr string
		switch o := op.(type) {
		case JSONSet:
			ptr = o.Pointer
		case JSONAppend:
			ptr = o.Pointer
		}
		first := ""
		if toks, err := splitPointer(ptr); err == nil && len(toks) > 0 {
			first = strings.ToLower(toks[0])
		}
		switch {
		case strings.Contains(first, "mcp"):
			return ComponentMCP
		case first == "hooks" || strings.Contains(base, "hook"):
			return ComponentHooks
		}
	}
	return ComponentOther
}

// quotedRE matches double-quoted strings with JSON/TOML-style escapes.
var quotedRE = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"`)

// binaryRefs returns the absolute graphmaster executable paths mentioned in
// text: string literals that are such a path, or shell commands whose first
// word is one.
func binaryRefs(text string) []string {
	var out []string
	for _, lit := range quotedRE.FindAllString(text, -1) {
		var s string
		if err := json.Unmarshal([]byte(lit), &s); err != nil {
			continue
		}
		// A bare path (MCP "command") may contain spaces; a hook command is
		// a shell line whose first word is the binary.
		cand := s
		if !isBinaryPath(cand) {
			cand = firstShellWord(s)
		}
		if isBinaryPath(cand) && !slices.Contains(out, cand) {
			out = append(out, cand)
		}
	}
	return out
}

func isBinaryPath(p string) bool {
	base := filepath.Base(p)
	return filepath.IsAbs(p) && (base == BinaryName || base == BinaryName+".exe")
}

// firstShellWord returns the first POSIX-shell word of s with quoting
// removed (single quotes, double quotes and backslash escapes).
func firstShellWord(s string) string {
	s = strings.TrimLeft(s, " \t")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n', ';', '|', '&':
			return b.String()
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return b.String() + s[i+1:]
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
