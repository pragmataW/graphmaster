package model

import "strings"

// FileStatus records why a file was or was not parsed. Values match the
// files.status column in graph.db.
type FileStatus uint8

// File statuses. Append only; never renumber.
const (
	StatusOK FileStatus = iota
	StatusTooBig
	StatusGenerated
	StatusMinified
	StatusBinary
	StatusParseErr
	StatusUnsupported
)

var fileStatusNames = []string{
	"ok", "too_big", "generated", "minified", "binary", "parse_err", "unsupported",
}

func (s FileStatus) String() string { return enumString(fileStatusNames, uint8(s), "status") }

// ParseFileStatus parses the name produced by String.
func ParseFileStatus(s string) (FileStatus, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "ok" { // enumParse skips index 0
		return StatusOK, nil
	}
	v, err := enumParse(fileStatusNames, s, "file status")
	return FileStatus(v), err
}

// Indexed reports whether the file's facts are stored. Files with syntax
// errors are still indexed best-effort from tree-sitter's error-tolerant tree.
func (s FileStatus) Indexed() bool { return s == StatusOK || s == StatusParseErr }

// DefFlags is a bit set of definition attributes stored in nodes.flags.
type DefFlags uint16

// Definition flags. Append only; never reorder bits.
const (
	FlagExported    DefFlags = 1 << iota // visible outside its module/package
	FlagTest                             // a test function/method/class
	FlagAsync                            // async function/method
	FlagStatic                           // static member
	FlagAbstract                         // abstract member or type
	FlagDeclaration                      // declaration without body (C prototype, forward decl)
)

var defFlagNames = []string{"exported", "test", "async", "static", "abstract", "declaration"}

// Has reports whether all bits of m are set in f.
func (f DefFlags) Has(m DefFlags) bool { return f&m == m }

// String renders set flags joined by "|", e.g. "exported|async".
func (f DefFlags) String() string {
	var parts []string
	for i, n := range defFlagNames {
		if f&(1<<i) != 0 {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, "|")
}
