// Package mcp serves graphmaster's query tools over the Model Context
// Protocol (stdio in production, any transport in tests).
//
// Every tool of internal/query is registered with a typed input
// (mcp.AddTool[In, any]) so the client sees a JSON schema with field
// descriptions. A call routes absolute path arguments to the repository
// that contains them, opens (once) and freshens that repository's index
// through query.Engine, and returns the tool's plain text as a single
// TextContent block. Failures become tool errors ("isError") with a
// banner-style text; the server never crashes on a tool call.
package mcp
