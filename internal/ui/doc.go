// Package ui serves the graphmaster graph explorer (`graphmaster ui`): a
// small JSON API over graph.db plus a static, build-free frontend
// (plain JavaScript and a vendored cytoscape).
//
// The graph is never loaded whole. Every endpoint answers one bounded
// question — a search, one symbol, an ego graph, a file, one directory level
// or an impact set — and no response carries more than MaxElements
// cytoscape elements.
//
// The server binds to 127.0.0.1 only. Every request must carry the random
// per-process token in its URL (?token=...) and a Host header naming
// localhost or 127.0.0.1, which blocks other local users without the URL
// and DNS-rebinding pages alike. API requests freshen the index first with a
// short lock budget, like the query tools.
package ui
