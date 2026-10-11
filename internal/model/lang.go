package model

import (
	"fmt"
	"strings"
)

// Lang identifies a source language. Values are stored in graph.db.
type Lang uint8

// Supported languages. Append only; never renumber.
const (
	LangUnknown Lang = iota
	LangGo
	LangPython
	LangTypeScript
	LangTSX
	LangJavaScript
	LangRust
	LangJava
	LangC
	LangCPP
	LangCSharp
	LangPHP

	langCount // sentinel, keep last
)

type langInfo struct {
	name string
	exts []string
}

// langTable is indexed by Lang. The first extension is the canonical one.
var langTable = [langCount]langInfo{
	LangUnknown:    {name: "unknown"},
	LangGo:         {name: "go", exts: []string{".go"}},
	LangPython:     {name: "python", exts: []string{".py", ".pyi"}},
	LangTypeScript: {name: "typescript", exts: []string{".ts", ".mts", ".cts"}},
	LangTSX:        {name: "tsx", exts: []string{".tsx"}},
	LangJavaScript: {name: "javascript", exts: []string{".js", ".mjs", ".cjs", ".jsx"}},
	LangRust:       {name: "rust", exts: []string{".rs"}},
	LangJava:       {name: "java", exts: []string{".java"}},
	// .h defaults to C; C++ headers are recognized by their own extensions.
	LangC:      {name: "c", exts: []string{".c", ".h"}},
	LangCPP:    {name: "cpp", exts: []string{".cpp", ".cc", ".cxx", ".c++", ".hpp", ".hh", ".hxx", ".h++", ".ipp", ".tpp", ".C", ".H"}},
	LangCSharp: {name: "csharp", exts: []string{".cs"}},
	LangPHP:    {name: "php", exts: []string{".php"}},
}

var (
	extToLang  = map[string]Lang{}
	nameToLang = map[string]Lang{
		"golang": LangGo, "py": LangPython, "ts": LangTypeScript, "js": LangJavaScript,
		"jsx": LangJavaScript, "rs": LangRust, "c++": LangCPP, "cxx": LangCPP,
		"cs": LangCSharp, "c#": LangCSharp,
	}
)

func init() {
	for l := LangGo; l < langCount; l++ {
		nameToLang[langTable[l].name] = l
		for _, e := range langTable[l].exts {
			extToLang[e] = l
		}
	}
}

// AllLangs returns every supported language in stable order.
func AllLangs() []Lang {
	out := make([]Lang, 0, langCount-1)
	for l := LangGo; l < langCount; l++ {
		out = append(out, l)
	}
	return out
}

// Valid reports whether l is a supported (non-unknown) language.
func (l Lang) Valid() bool { return l > LangUnknown && l < langCount }

// String returns the canonical lowercase name (e.g. "cpp", "csharp").
func (l Lang) String() string {
	if l < langCount {
		return langTable[l].name
	}
	return fmt.Sprintf("lang(%d)", uint8(l))
}

// Extensions returns a copy of the file extensions (with leading dot)
// claimed by l.
func (l Lang) Extensions() []string {
	if !l.Valid() {
		return nil
	}
	return append([]string(nil), langTable[l].exts...)
}

// MarshalText implements encoding.TextMarshaler.
func (l Lang) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (l *Lang) UnmarshalText(b []byte) error {
	v, err := ParseLang(string(b))
	if err != nil {
		return err
	}
	*l = v
	return nil
}

// ParseLang parses a canonical name or common alias, case-insensitively.
func ParseLang(s string) (Lang, error) {
	if l, ok := nameToLang[strings.ToLower(strings.TrimSpace(s))]; ok {
		return l, nil
	}
	return LangUnknown, fmt.Errorf("unknown language %q", s)
}

// LangFromExt maps an extension (with leading dot) to a language. Exact case
// is tried first so ".C"/".H" map to C++; then a lowercase match.
func LangFromExt(ext string) Lang {
	if l, ok := extToLang[ext]; ok {
		return l
	}
	return extToLang[strings.ToLower(ext)]
}

// LangFromPath maps a file path (slash or backslash separated) to a language
// by its extension. Unknown or extensionless files return LangUnknown.
func LangFromPath(path string) Lang {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 { // no extension, or dotfile like ".gitignore"
		return LangUnknown
	}
	return LangFromExt(base[dot:])
}

// IsCodePath reports whether path has an extension of a supported language.
func IsCodePath(path string) bool { return LangFromPath(path) != LangUnknown }
