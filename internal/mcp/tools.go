package mcp

import (
	"context"
	"fmt"
	"reflect"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pragmataW/graphmaster/internal/query"
)

// instructions is sent to the client at initialization.
const instructions = `graphmaster serves a code graph of this repository (definitions, calls, references, imports, inheritance, tests). The index is refreshed before every call, so answers include uncommitted edits.

Prefer these tools over grep/rg/find/ls/cat for code structure:
1. Orient: repo_map for the layout; find_symbol (by name) or search (fragments, file names) to locate code.
2. Read: file_outline to see a file's definitions, then read_symbol for just the definition you need instead of whole files.
3. Relate: callers, callees, imports, inheritance; traverse for wider neighborhoods.
4. Change safely: impact (symbol, path or changed=true) and tests_for before editing or to review a change.

Symbol arguments take a qualified name or a dotted suffix of one (pkg.Type.Method), path:line, a bare name, or a 16-hex skey from earlier output; an ambiguous name returns numbered candidates with their skeys. Paths are repo-relative; absolute paths also work, including paths in another repository. Output is plain text bounded by max_tokens (default 1500, max 6000); "… +N more" means narrow path_prefix or raise limit/max_tokens; "~" marks lower-confidence edges. Use grep only for literal text such as strings, comments and config values.`

// descriptions tell the model when to call each tool (query's own
// descriptions say only what the tool returns).
var descriptions = map[string]string{
	"find_symbol":  "Find where a function, method, type, class, variable or constant is defined, by name or qualified name. Use instead of grep/rg when looking for a definition; returns ranked definitions with path:line-range and signature.",
	"search":       "Fuzzy search over symbol names and file paths by words or identifier fragments. Use instead of grep/find/glob when you only know part of a name or are looking for files.",
	"repo_map":     "Directory/package tree with file counts, lines of code and key exported symbols. Use instead of ls/tree/find to get oriented in the repository or one directory.",
	"file_outline": "All definitions of one file, nested, with line ranges and signatures. Use before reading a file, then read_symbol for the part you need.",
	"read_symbol":  "Source code of one definition with line numbers. Use instead of reading a whole file when you need a specific function, method or type.",
	"callers":      "Who calls a function or references a symbol, as a tree (depth up to 5). Use instead of grepping for call sites or usages.",
	"callees":      "What a function calls: resolved definitions plus external/library calls. Use to follow control flow without opening each file.",
	"impact":       "Reverse dependencies: what may break if a symbol, a file or the uncommitted changes (changed=true) are modified, grouped by file with affected tests. Use before refactoring or when reviewing a change.",
	"imports":      "Imports of a file or package (direction=out) or who imports it (direction=in). Use instead of grepping import statements.",
	"inheritance":  "Type hierarchy: supertypes, subtypes and implementations of a class, struct or interface. Use instead of grepping for extends/implements.",
	"tests_for":    "Tests that exercise a symbol (callers within two hops plus tests named after it). Use to decide which tests to run or update after a change.",
	"traverse":     "Breadth-first exploration from a symbol over chosen edge kinds (calls, references, extends, implements, embeds, tests, declares) in either direction, with hubs summarized. Use for questions that need more than one callers/callees step.",
	"status":       "Index health: freshness, file states, languages, database size, last scan and steering mode. Use when results look stale, empty or surprising.",
}

// binding registers one query tool with its concrete input type.
type binding struct {
	name string
	add  func(s *Server, t query.Tool) error
}

// bind returns the binding of tool name to input type In.
func bind[In any](name string) binding {
	return binding{name: name, add: func(s *Server, t query.Tool) error {
		if got, want := reflect.TypeOf(t.NewInput()), reflect.TypeFor[*In](); got != want {
			return fmt.Errorf("mcp: tool %s: input is %v, binding expects %v", name, got, want)
		}
		desc := descriptions[name]
		if desc == "" {
			desc = t.Description
		}
		gomcp.AddTool(s.srv, &gomcp.Tool{
			Name:        name,
			Description: desc,
			Annotations: &gomcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(bool)},
		}, handler[In](s, t))
		return nil
	}}
}

// bindings gives every query tool a typed input so the client receives a
// JSON schema for it.
var bindings = []binding{
	bind[query.FindSymbolInput]("find_symbol"),
	bind[query.SearchInput]("search"),
	bind[query.RepoMapInput]("repo_map"),
	bind[query.FileOutlineInput]("file_outline"),
	bind[query.ReadSymbolInput]("read_symbol"),
	bind[query.CallsInput]("callers"),
	bind[query.CallsInput]("callees"),
	bind[query.ImpactInput]("impact"),
	bind[query.ImportsInput]("imports"),
	bind[query.InheritanceInput]("inheritance"),
	bind[query.TestsForInput]("tests_for"),
	bind[query.TraverseInput]("traverse"),
	bind[query.StatusInput]("status"),
}

// register adds every query tool, in query's order. A query tool without a
// binding is a programming error caught at startup (and by tests).
func (s *Server) register() error {
	byName := make(map[string]binding, len(bindings))
	for _, b := range bindings {
		byName[b.name] = b
	}
	for _, t := range query.Tools() {
		b, ok := byName[t.Name]
		if !ok {
			return fmt.Errorf("mcp: query tool %q has no typed binding", t.Name)
		}
		if err := b.add(s, t); err != nil {
			return err
		}
	}
	return nil
}

// handler runs tool t for one call: route paths, open/freshen the index
// through the engine and return its text.
func handler[In any](s *Server, t query.Tool) gomcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *gomcp.CallToolRequest, in In) (*gomcp.CallToolResult, any, error) {
		res := safeCall(t.Name, func() (string, error) {
			root, err := s.route(&in)
			if err != nil {
				return "", err
			}
			e, err := s.engine(ctx, root)
			if err != nil {
				return "", err
			}
			return t.Run(ctx, e, &in)
		})
		// A nil output keeps the SDK from adding structuredContent.
		return res, nil, nil
	}
}
