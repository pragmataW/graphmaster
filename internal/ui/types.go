package ui

// JSON shapes of the API. Graph responses carry cytoscape elements
// directly so the frontend can add them without translation.

// Symbol is a definition as the API reports it.
type Symbol struct {
	ID       int64  `json:"id"`
	FileID   int64  `json:"fileId"`
	ParentID int64  `json:"parentId,omitempty"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	QName    string `json:"qname"`
	Path     string `json:"path"`
	Lang     string `json:"lang"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Sig      string `json:"sig,omitempty"`
	Exported bool   `json:"exported,omitempty"`
	Test     bool   `json:"test,omitempty"`
	Decl     bool   `json:"declaration,omitempty"`
	SKey     string `json:"skey"`
}

// FileInfo summarizes a files row.
type FileInfo struct {
	ID      int64  `json:"id"`
	Path    string `json:"path"`
	Lang    string `json:"lang"`
	LOC     int    `json:"loc"`
	Status  string `json:"status"`
	Symbols int    `json:"symbols"`
}

// Element is one cytoscape element (group "nodes" or "edges").
type Element struct {
	Group   string   `json:"group"`
	Data    ElemData `json:"data"`
	Classes string   `json:"classes,omitempty"`
}

// ElemData is an element's data object. Element ids are "n<node id>" for
// symbols, "f<file id>" for files and "g:<path>" for directory groups.
type ElemData struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
	Label  string `json:"label,omitempty"`
	// Kind is the node kind, the edge kind, or "file"/"dir" for groups.
	Kind string `json:"kind,omitempty"`
	// Ref is the nodes.id of a symbol or the files.id of a file.
	Ref   int64  `json:"ref,omitempty"`
	QName string `json:"qname,omitempty"`
	Path  string `json:"path,omitempty"`
	Lang  string `json:"lang,omitempty"`
	Start int    `json:"start,omitempty"`
	End   int    `json:"end,omitempty"`
	// Conf is an edge's best confidence; Count the edges it aggregates.
	Conf  int `json:"conf,omitempty"`
	Count int `json:"count,omitempty"`
	// Depth is the distance from the ego center or impact seeds.
	Depth int `json:"depth,omitempty"`
	// Aggregates of file and directory nodes.
	Files   int `json:"files,omitempty"`
	Symbols int `json:"symbols,omitempty"`
	LOC     int `json:"loc,omitempty"`
}

// GraphResponse is the common part of every graph endpoint.
type GraphResponse struct {
	Banner   string    `json:"banner"`
	Elements []Element `json:"elements"`
	// Truncated reports that elements were dropped to honour a cap.
	Truncated bool `json:"truncated"`
}

// ErrorResponse is the body of every non-200 API answer.
type ErrorResponse struct {
	Error string `json:"error"`
}

// SearchResponse answers /api/search.
type SearchResponse struct {
	Banner  string     `json:"banner"`
	Query   string     `json:"query"`
	Symbols []Symbol   `json:"symbols"`
	Files   []FileInfo `json:"files"`
	More    bool       `json:"more"`
}

// Snippet is the source of a symbol, starting at line Start.
type Snippet struct {
	Start     int      `json:"start"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// NodeResponse answers /api/node: details for the side panel.
type NodeResponse struct {
	Banner   string   `json:"banner"`
	Symbol   Symbol   `json:"symbol"`
	Parent   *Symbol  `json:"parent,omitempty"`
	Children []Symbol `json:"children"`
	// MoreChildren counts children beyond the listed ones.
	MoreChildren int `json:"moreChildren,omitempty"`
	// In and Out count resolved edges by kind (incoming, outgoing).
	In  map[string]int `json:"in"`
	Out map[string]int `json:"out"`
	// Unresolved counts outgoing references with no indexed target.
	Unresolved int      `json:"unresolved"`
	Source     *Snippet `json:"source,omitempty"`
}

// EgoResponse answers /api/ego.
type EgoResponse struct {
	GraphResponse
	Center string `json:"center"`
}

// FileResponse answers /api/file.
type FileResponse struct {
	GraphResponse
	File FileInfo `json:"file"`
}

// PackagesResponse answers /api/packages: one directory level.
type PackagesResponse struct {
	GraphResponse
	Prefix string `json:"prefix"`
	// Parent is the prefix one level up ("" at the root).
	Parent string `json:"parent"`
}

// ImpactResponse answers /api/impact.
type ImpactResponse struct {
	GraphResponse
	// Seeds are the element ids of the starting definitions.
	Seeds []string `json:"seeds"`
	// Affected, Files and Tests count the whole reverse closure, even
	// when the elements are truncated.
	Affected int  `json:"affected"`
	Files    int  `json:"files"`
	Tests    int  `json:"tests"`
	Depth    int  `json:"depth"`
	MinConf  int  `json:"minConf"`
	Capped   bool `json:"walkCapped,omitempty"`
}
