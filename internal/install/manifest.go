package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
)

// StateFileName is the manifest written next to graphmaster's config.toml.
const StateFileName = "install-state.json"

const stateVersion = 1

// InsertionKind names what an insertion added.
type InsertionKind string

const (
	KindJSONMember  InsertionKind = "json_member"  // object member at Pointer
	KindJSONElement InsertionKind = "json_element" // array element Value at Pointer
	KindMarker      InsertionKind = "marker"       // marker block Marker
	KindFile        InsertionKind = "file"         // whole generated file
)

// Insertion is one exact edit graphmaster made to a file.
type Insertion struct {
	Agent   string          `json:"agent"`
	Kind    InsertionKind   `json:"kind"`
	Pointer string          `json:"pointer,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	// Prev is the member value graphmaster replaced; restored on removal.
	Prev json.RawMessage `json:"prev,omitempty"`
	// Marker is the block id ("" for the default block).
	Marker string `json:"marker,omitempty"`
	// Sep is the separator inserted before an appended marker block.
	Sep string `json:"sep,omitempty"`
}

// key identifies an insertion for matching across installs.
func (i Insertion) key() string {
	switch i.Kind {
	case KindJSONElement:
		c, err := canonicalJSON(i.Value)
		if err != nil {
			c = string(i.Value)
		}
		return string(i.Kind) + "\x00" + i.Pointer + "\x00" + c
	case KindMarker:
		return string(i.Kind) + "\x00" + i.Marker
	default:
		return string(i.Kind) + "\x00" + i.Pointer
	}
}

// Container is a JSON object or array created by an agent's insertions; it
// is removed on uninstall once empty.
type Container struct {
	Agent   string `json:"agent"`
	Pointer string `json:"pointer"`
}

// FileRecord is the manifest entry for one touched file.
type FileRecord struct {
	Path   string `json:"path"`
	Format Format `json:"format"`
	// Existed reports whether the file existed before the first install.
	Existed bool `json:"existed"`
	// PreHash is the sha256 of the file before the first install ("" if absent).
	PreHash string `json:"pre_hash,omitempty"`
	// PostHash is the sha256 after graphmaster's latest write.
	PostHash string `json:"post_hash"`
	// Backup holds the pre-install bytes ("" if the file did not exist).
	Backup string `json:"backup,omitempty"`
	// Restorable is false once the user edited the file between two
	// graphmaster writes: a backup restore would then lose their edit.
	Restorable bool        `json:"restorable"`
	Insertions []Insertion `json:"insertions"`
	Created    []Container `json:"created,omitempty"`
	// CreatedDirs are parent directories graphmaster created, innermost last.
	CreatedDirs []string `json:"created_dirs,omitempty"`
}

// agents returns the distinct agents with insertions in r.
func (r *FileRecord) agents() []string {
	var out []string
	for _, in := range r.Insertions {
		if !slices.Contains(out, in.Agent) {
			out = append(out, in.Agent)
		}
	}
	return out
}

// insertionsOf returns r's insertions belonging to agent.
func (r *FileRecord) insertionsOf(agent string) []Insertion {
	var out []Insertion
	for _, in := range r.Insertions {
		if in.Agent == agent {
			out = append(out, in)
		}
	}
	return out
}

// State is the install-state.json manifest.
type State struct {
	Version int                    `json:"version"`
	Files   map[string]*FileRecord `json:"files"`
}

func newState() *State {
	return &State{Version: stateVersion, Files: map[string]*FileRecord{}}
}

// Agents returns every agent with at least one recorded insertion, sorted.
func (s *State) Agents() []string {
	var out []string
	for _, r := range s.Files {
		for _, a := range r.agents() {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	sort.Strings(out)
	return out
}

// FilesOf returns the records touched by agent, sorted by path.
func (s *State) FilesOf(agent string) []*FileRecord {
	var out []*FileRecord
	for _, r := range s.Files {
		if slices.Contains(r.agents(), agent) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// LoadState reads the manifest at path; a missing file is an empty state.
func LoadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, err
	}
	s := newState()
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", path, err)
	}
	if s.Version != stateVersion {
		return nil, fmt.Errorf("%s: unsupported version %d", path, s.Version)
	}
	if s.Files == nil {
		s.Files = map[string]*FileRecord{}
	}
	return s, nil
}

// save writes the manifest atomically (0600: it may reference secrets'
// backups).
func (s *State) save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}
