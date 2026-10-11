// Package query implements graphmaster's code-graph tools (PLAN "MCP
// tool'ları") as a plain Go API, independent of MCP: find_symbol, search,
// repo_map, file_outline, read_symbol, callers, callees, impact, imports,
// inheritance, tests_for, traverse and status.
//
// Every tool follows one output contract:
//
//   - the index is freshened first (Engine's Freshener) and the first line
//     is the banner "graphmaster <tool> · <freshness>";
//   - the answer is plain text bounded by max_tokens (default 1500, cap
//     6000, estimated as bytes/4);
//   - symbols render as "<kind> <qname>  <path>:<start>-<end>  <sig>", the
//     qname without prefixes the path already implies (sym.displayQName:
//     "steer.F" for a Go import path, "User.role" for a TS file module);
//   - cut lists end with "… +N more (narrow path_prefix / raise limit)";
//   - edges below confidence 80 are marked with "~".
//
// Symbol arguments accept a qualified name (or a dotted suffix of one),
// path:line, a bare name or a 16-digit skey; an ambiguous argument yields
// a numbered candidate list instead of an answer.
//
// The MCP server and the `graphmaster query` CLI both drive the tools
// through Tools, so they print identical text.
package query
