package store

import "github.com/pragmataW/graphmaster/internal/model"

// File is a row of the files table. Zero ID means "not yet stored".
type File struct {
	ID      int64
	Path    string // repo-relative, forward slashes
	Lang    model.Lang
	Size    int64
	MtimeNS int64
	Ino     uint64
	Mode    uint32
	Blob    []byte // git blob SHA-1 (20 bytes) or nil
	Tracked bool
	Status  model.FileStatus
	LOC     int
	PkgID   int64 // 0 = none
}

// Node is a row of the nodes table (a definition).
type Node struct {
	ID       int64
	FileID   int64
	ParentID int64 // 0 = top level
	Kind     model.NodeKind
	NameID   int64  // interned name; InsertNode fills it from Name when 0
	Name     string // plain name; filled by NodeByID
	QName    string
	// Lines are 1-based, columns 0-based, bytes 0-based with exclusive end.
	StartLine, StartCol, EndLine uint32
	StartByte, EndByte           uint32
	Sig                          string
	Flags                        model.DefFlags
	SKey                         []byte // 8 bytes, see SymbolKey
}

// Edge is a row of the edges table. Dst 0 means unresolved (NULL).
type Edge struct {
	ID     int64
	FileID int64 // file whose facts produced the edge; Src lives in it
	Src    int64
	Dst    int64
	Kind   model.EdgeKind
	NameID int64  // target name; InsertEdge fills it from Name when 0
	Name   string // optional, used only to intern NameID
	Qual   string
	Line   uint32
	Col    uint32
	Conf   uint8 // 0..100
}

// Import is a row of the imports table.
type Import struct {
	FileID     int64
	Spec       string
	Alias      string
	Names      []string
	TargetFile int64 // 0 = none
	TargetPkg  int64 // 0 = none
	Line       uint32
}

// Package is a row of the packages table, unique per (Lang, Name).
type Package struct {
	ID   int64
	Lang model.Lang
	Name string
	Dir  string
	Kind uint8
}

// nullID maps 0 to SQL NULL.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// nullStr maps "" to SQL NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
