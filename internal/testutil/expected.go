package testutil

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// ExpectedEdge is one line of testdata/fixture-repo/expected_edges.txt:
//
//	[prefix]<kind> <src> -> <dst> [conf>=N]
//
// Src and Dst are "path::Chain" (container chain joined with "."), a bare
// path (imports/includes) or "external:<qual.name>".
type ExpectedEdge struct {
	Section  string // "[lang]" header the line belongs to
	Line     int    // 1-based line number in the file
	Optional bool   // "?" prefix: desirable, logged when missing
	Negative bool   // "!" prefix: must not exist
	Kind     model.EdgeKind
	Src, Dst string
	MinConf  int
}

func (e ExpectedEdge) String() string {
	s := fmt.Sprintf("%v %s -> %s", e.Kind, e.Src, e.Dst)
	if e.MinConf > 0 {
		s += fmt.Sprintf(" conf>=%d", e.MinConf)
	}
	return s
}

// External reports whether Dst names a target outside the indexed code and
// returns its simple name (the part after the last "." or ":").
func (e ExpectedEdge) External() (name string, ok bool) {
	name, ok = strings.CutPrefix(e.Dst, "external:")
	if !ok {
		return "", false
	}
	if i := strings.LastIndexAny(name, ".:"); i >= 0 {
		name = name[i+1:]
	}
	return name, true
}

// ExpectedEdgesPath is the location of the fixture's edge expectations.
func ExpectedEdgesPath() string { return filepath.Join(FixtureDir(), "expected_edges.txt") }

// LoadExpectedEdges parses the expectations of one section, or of every
// section when section is "".
func LoadExpectedEdges(section string) ([]ExpectedEdge, error) {
	path := ExpectedEdgesPath()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var (
		out    []ExpectedEdge
		cur    string
		lineNo int
	)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = line[1 : len(line)-1]
			continue
		}
		if section != "" && cur != section {
			continue
		}
		e, err := ParseExpectedEdge(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		e.Section, e.Line = cur, lineNo
		out = append(out, e)
	}
	return out, sc.Err()
}

// ParseExpectedEdge parses one expectation line (without section or line
// number).
func ParseExpectedEdge(line string) (ExpectedEdge, error) {
	var e ExpectedEdge
	if line == "" {
		return e, fmt.Errorf("empty edge line")
	}
	switch line[0] {
	case '?':
		e.Optional, line = true, line[1:]
	case '!':
		e.Negative, line = true, line[1:]
	}
	f := strings.Fields(line)
	if len(f) < 4 || f[2] != "->" {
		return e, fmt.Errorf("malformed edge %q", line)
	}
	kind, err := model.ParseEdgeKind(f[0])
	if err != nil {
		return e, err
	}
	e.Kind, e.Src, e.Dst = kind, f[1], f[3]
	for _, extra := range f[4:] {
		v, ok := strings.CutPrefix(extra, "conf>=")
		if !ok {
			return e, fmt.Errorf("unknown attribute %q", extra)
		}
		if e.MinConf, err = strconv.Atoi(v); err != nil {
			return e, err
		}
	}
	return e, nil
}
