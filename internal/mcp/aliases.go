package mcp

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// argAliases are argument names models guess for a tool's main argument
// (find_symbol {"name": ...}, read_symbol {"qname": ...}). Without a rewrite
// the SDK rejects the call with "unexpected additional properties" and the
// model has to spend a turn on the schema.
var argAliases = map[string]bool{
	"name": true, "query": true, "symbol": true, "qname": true, "identifier": true,
	"target": true, "start": true, "q": true, "pattern": true,
}

// pathAliases are guesses for a "path" argument.
var pathAliases = map[string]bool{"file": true, "file_path": true, "filePath": true, "filepath": true}

// toolArgs describes the arguments of one tool: its JSON property names and
// the main argument aliases map to.
type toolArgs struct {
	props map[string]bool
	main  string
}

// mainArgs lists the candidates for a tool's main argument, best first.
var mainArgs = []string{"query", "symbol", "start"}

// argsOf derives toolArgs from an input struct's json tags.
func argsOf(t reflect.Type) toolArgs {
	a := toolArgs{props: map[string]bool{}}
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			a.props[name] = true
		}
	}
	for _, m := range mainArgs {
		if a.props[m] {
			a.main = m
			break
		}
	}
	return a
}

// rewriteArgs renames alias keys of args to the tool's own argument names.
// Unknown keys that are not aliases, and aliases whose target is already
// set, are left for the SDK to reject. It returns nil when nothing changes.
func rewriteArgs(a toolArgs, raw json.RawMessage) json.RawMessage {
	var args map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
		return nil
	}
	changed := false
	for _, k := range slices.Sorted(maps.Keys(args)) {
		v := args[k]
		if a.props[k] {
			continue
		}
		var to string
		switch {
		case argAliases[k] && a.main != "":
			to = a.main
		case pathAliases[k] && a.props["path"]:
			to = "path"
		default:
			continue
		}
		if _, ok := args[to]; ok {
			continue
		}
		args[to] = v
		delete(args, k)
		changed = true
	}
	if !changed {
		return nil
	}
	out, err := json.Marshal(args)
	if err != nil {
		return nil
	}
	return out
}

// aliasMiddleware applies rewriteArgs to tools/call requests. tools maps
// tool names to their arguments.
func aliasMiddleware(tools map[string]toolArgs) gomcp.Middleware {
	return func(next gomcp.MethodHandler) gomcp.MethodHandler {
		return func(ctx context.Context, method string, req gomcp.Request) (gomcp.Result, error) {
			if r, ok := req.(*gomcp.CallToolRequest); ok && r.Params != nil {
				if a, ok := tools[r.Params.Name]; ok {
					if out := rewriteArgs(a, r.Params.Arguments); out != nil {
						r.Params.Arguments = out
					}
				}
			}
			return next(ctx, method, req)
		}
	}
}
