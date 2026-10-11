package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// web holds the frontend; cytoscape.min.js is cytoscape 3.34.3 (MIT),
// vendored unmodified from the npm package (dist/cytoscape.min.js,
// sha256 5f3b5b529546d5af1fc5628590af033b74511a5b6f789f5f4682845863228b91).
//
//go:embed web/index.html web/app.js web/style.css web/cytoscape.min.js
var web embed.FS

// tokenPlaceholder in index.html is replaced with the server token, so the
// asset URLs carry it like every other request.
const tokenPlaceholder = "__GRAPHMASTER_TOKEN__"

// contentTypes maps the embedded file extensions to media types.
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

// static serves the embedded frontend; "/" is index.html.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(web, "web/"+name)
	ctype, known := contentTypes[path.Ext(name)]
	if err != nil || !known || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	if name == "index.html" {
		data = bytes.ReplaceAll(data, []byte(tokenPlaceholder), []byte(s.token))
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(data)
}
