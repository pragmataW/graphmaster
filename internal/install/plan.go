package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
)

// Format selects how a file is edited.
type Format string

const (
	FormatJSON      Format = "json"      // JSON or JSONC via hujson
	FormatTOML      Format = "toml"      // # graphmaster:begin v1 marker blocks
	FormatMarkdown  Format = "markdown"  // <!-- graphmaster:begin v1 --> blocks
	FormatGenerated Format = "generated" // whole file owned by graphmaster
)

// Plan is the set of edits one agent adapter wants.
type Plan struct {
	// Agent identifies the adapter ("claude", "codex", ...). Insertions are
	// recorded per agent so agents can be uninstalled independently.
	Agent string
	Files []FileChange
}

// FileChange describes the desired graphmaster state of one file.
type FileChange struct {
	// Path is absolute.
	Path   string
	Format Format
	// Ops apply to FormatJSON (JSONSet, JSONAppend) and to FormatTOML /
	// FormatMarkdown (Block).
	Ops []Op
	// Content is the full file for FormatGenerated.
	Content []byte
	// Mode is used when FormatGenerated creates the file (default 0644).
	Mode fs.FileMode
}

// Op is one desired edit; see JSONSet, JSONAppend and Block.
type Op interface{ isOp() }

// JSONSet makes the object member at Pointer equal Value, creating missing
// parent objects. A different pre-existing value is replaced and restored
// on uninstall.
type JSONSet struct {
	Pointer string
	// Value is any JSON-marshalable value; json.RawMessage is used verbatim
	// (key order preserved).
	Value any
}

// JSONAppend ensures the array at Pointer contains Value, creating the array
// and missing parents. Elements recorded by an earlier install that are no
// longer desired are removed first, so changing a hook command replaces the
// old element instead of accumulating copies.
type JSONAppend struct {
	Pointer string
	Value   any
}

// Block is a marker-delimited text block for TOML and Markdown files.
type Block struct {
	// ID distinguishes several blocks in one file; "" is the default block.
	ID   string
	Body string
}

func (JSONSet) isOp()    {}
func (JSONAppend) isOp() {}
func (Block) isOp()      {}

var agentRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Validate checks the plan's shape without touching the filesystem.
func (p Plan) Validate() error {
	if !agentRE.MatchString(p.Agent) {
		return fmt.Errorf("invalid agent name %q", p.Agent)
	}
	seen := map[string]bool{}
	for _, f := range p.Files {
		if !filepath.IsAbs(f.Path) {
			return fmt.Errorf("%s: path must be absolute", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("%s: listed twice in plan %s", f.Path, p.Agent)
		}
		seen[f.Path] = true
		if err := f.validate(); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	return nil
}

func (f FileChange) validate() error {
	switch f.Format {
	case FormatJSON:
		for _, op := range f.Ops {
			switch o := op.(type) {
			case JSONSet:
				if _, err := toRaw(o.Value); err != nil {
					return err
				}
				if toks, err := splitPointer(o.Pointer); err != nil || len(toks) == 0 {
					return fmt.Errorf("invalid JSONSet pointer %q", o.Pointer)
				}
			case JSONAppend:
				if _, err := toRaw(o.Value); err != nil {
					return err
				}
				if toks, err := splitPointer(o.Pointer); err != nil || len(toks) == 0 {
					return fmt.Errorf("invalid JSONAppend pointer %q", o.Pointer)
				}
			default:
				return fmt.Errorf("op %T not valid for JSON", op)
			}
		}
	case FormatTOML, FormatMarkdown:
		ids := map[string]bool{}
		for _, op := range f.Ops {
			b, ok := op.(Block)
			if !ok {
				return fmt.Errorf("op %T not valid for %s", op, f.Format)
			}
			if ids[b.ID] {
				return fmt.Errorf("block %q listed twice", b.ID)
			}
			ids[b.ID] = true
			if _, err := f.markers().render(b.ID, b.Body); err != nil {
				return err
			}
			if f.Format == FormatTOML {
				if err := checkTOMLBlockBody(b.Body); err != nil {
					return err
				}
			}
		}
	case FormatGenerated:
		if len(f.Ops) > 0 {
			return errors.New("generated files take Content, not Ops")
		}
	default:
		return fmt.Errorf("unknown format %q", f.Format)
	}
	return nil
}

func (f FileChange) markers() markerStyle {
	if f.Format == FormatTOML {
		return tomlMarkers
	}
	return mdMarkers
}

// toRaw converts an op value to JSON text without HTML escaping.
func toRaw(v any) (json.RawMessage, error) {
	switch t := v.(type) {
	case json.RawMessage:
		if _, err := canonicalJSON(t); err != nil {
			return nil, err
		}
		return t, nil
	case []byte:
		return toRaw(json.RawMessage(t))
	}
	b, err := marshalNoEscape(v, "", "")
	if err != nil {
		return nil, fmt.Errorf("marshal value: %w", err)
	}
	return b, nil
}
