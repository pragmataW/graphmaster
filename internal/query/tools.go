package query

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool describes one query tool for the MCP server and the CLI.
type Tool struct {
	// Name is the MCP tool name, e.g. "find_symbol".
	Name string
	// Description is shown to agents and in CLI help.
	Description string
	// Primary is the json name of the argument the CLI takes positionally
	// ("" when the tool has none).
	Primary string
	// NewInput returns a pointer to a zero input struct (e.g.
	// *FindSymbolInput) to decode arguments into.
	NewInput func() any
	// Run executes the tool with an input created by NewInput.
	Run func(ctx context.Context, e *Engine, in any) (string, error)
}

// RunJSON decodes JSON arguments into the tool's input and runs it.
func (t Tool) RunJSON(ctx context.Context, e *Engine, args []byte) (string, error) {
	in := t.NewInput()
	if len(args) > 0 {
		if err := json.Unmarshal(args, in); err != nil {
			return "", fmt.Errorf("%w: %s: %w", ErrInvalidArgument, t.Name, err)
		}
	}
	return t.Run(ctx, e, in)
}

// tool adapts a typed Engine method to the Tool registry.
func tool[In any](name, primary, desc string, fn func(*Engine, context.Context, In) (string, error)) Tool {
	return Tool{
		Name:        name,
		Description: desc,
		Primary:     primary,
		NewInput:    func() any { return new(In) },
		Run: func(ctx context.Context, e *Engine, in any) (string, error) {
			switch v := in.(type) {
			case *In:
				return fn(e, ctx, *v)
			case In:
				return fn(e, ctx, v)
			}
			return "", fmt.Errorf("%w: %s: unexpected input %T", ErrInvalidArgument, name, in)
		},
	}
}

var tools = []Tool{
	tool("find_symbol", "query",
		"Find definitions by name: exact matches first, then prefix and fuzzy matches; exported before private.",
		(*Engine).FindSymbol),
	tool("search", "query",
		"Full-text and substring search over symbol names and file paths.",
		(*Engine).Search),
	tool("repo_map", "path_prefix",
		"Directory / package tree with file counts, lines of code and the most referenced exported symbols.",
		(*Engine).RepoMap),
	tool("file_outline", "path",
		"Nested definitions of a file with line ranges and signatures.",
		(*Engine).FileOutline),
	tool("read_symbol", "symbol",
		"Source code of one definition with line numbers.",
		(*Engine).ReadSymbol),
	tool("callers", "symbol",
		"Tree of definitions that call a symbol (references for non-callables), including calls through an interface or base method it implements (~via); ~ marks confidence below 80.",
		(*Engine).Callers),
	tool("callees", "symbol",
		"Tree of definitions a symbol calls, with the implementations of called interface methods, plus its unresolved (library) calls; ~ marks confidence below 80.",
		(*Engine).Callees),
	tool("impact", "symbol",
		"What may break if a symbol, a file or the uncommitted changes (changed=true) are modified, grouped by file, with affected tests.",
		(*Engine).Impact),
	tool("imports", "path",
		"Imports of a file or package (direction=out) or its importers (direction=in).",
		(*Engine).Imports),
	tool("inheritance", "symbol",
		"Type hierarchy around a type: supertypes, subtypes or both.",
		(*Engine).Inheritance),
	tool("tests_for", "symbol",
		"Tests that reach a symbol within two hops, plus tests named after it.",
		(*Engine).TestsFor),
	tool("traverse", "start",
		"Breadth-first exploration around a symbol over chosen edge kinds, ranked by confidence and specificity; hubs are summarized.",
		(*Engine).Traverse),
	tool("status", "",
		"Index status: file states, languages, database size, last scan and steering mode.",
		(*Engine).Status),
}

// Tools returns every query tool in a stable order.
func Tools() []Tool { return append([]Tool(nil), tools...) }

// Lookup returns the tool with the given name.
func Lookup(name string) (Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
