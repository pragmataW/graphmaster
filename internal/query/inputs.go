package query

// Tool inputs. Field names follow the MCP argument names (json tags); a
// zero value selects the documented default.

// FindSymbolInput are the find_symbol arguments.
type FindSymbolInput struct {
	Query      string `json:"query" jsonschema:"symbol name, qualified name, prefix or substring"`
	Kind       string `json:"kind,omitempty" jsonschema:"only this definition kind (function, method, class, struct, interface, ...)"`
	Lang       string `json:"lang,omitempty" jsonschema:"only this language (go, python, typescript, ...)"`
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"only files whose repo-relative path starts with this"`
	Exact      bool   `json:"exact,omitempty" jsonschema:"exact name match only"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum results (default 20)"`
	MaxTokens  int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// SearchInput are the search arguments.
type SearchInput struct {
	Query      string   `json:"query" jsonschema:"words or identifier fragments to search in symbol and file names"`
	Kinds      []string `json:"kinds,omitempty" jsonschema:"restrict to these definition kinds; include \"file\" for file names"`
	PathPrefix string   `json:"path_prefix,omitempty" jsonschema:"only files whose repo-relative path starts with this"`
	Limit      int      `json:"limit,omitempty" jsonschema:"maximum results (default 30)"`
	MaxTokens  int      `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// RepoMapInput are the repo_map arguments.
type RepoMapInput struct {
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"directory to map (default: repository root)"`
	Depth      int    `json:"depth,omitempty" jsonschema:"directory levels to show (default 2)"`
	MaxTokens  int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// FileOutlineInput are the file_outline arguments.
type FileOutlineInput struct {
	Path           string `json:"path" jsonschema:"repo-relative file path"`
	Depth          int    `json:"depth,omitempty" jsonschema:"nesting levels to show (default 2)"`
	IncludePrivate *bool  `json:"include_private,omitempty" jsonschema:"include non-exported definitions (default true)"`
	MaxTokens      int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// ReadSymbolInput are the read_symbol arguments.
type ReadSymbolInput struct {
	Symbol       string `json:"symbol" jsonschema:"qname, path:line, bare name or skey"`
	ContextLines int    `json:"context_lines,omitempty" jsonschema:"extra lines before and after (default 0)"`
	MaxLines     int    `json:"max_lines,omitempty" jsonschema:"maximum source lines (default 200)"`
	MaxTokens    int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// CallsInput are the callers and callees arguments.
type CallsInput struct {
	Symbol        string `json:"symbol" jsonschema:"qname, path:line, bare name or skey"`
	Depth         int    `json:"depth,omitempty" jsonschema:"levels to follow (default 1, max 5)"`
	MinConfidence int    `json:"min_confidence,omitempty" jsonschema:"minimum edge confidence 1-100 (default 50)"`
	Limit         int    `json:"limit,omitempty" jsonschema:"maximum symbols (default 50)"`
	MaxTokens     int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// ImpactInput are the impact arguments; exactly one of Symbol, Path and
// Changed selects the starting set.
type ImpactInput struct {
	Symbol        string `json:"symbol,omitempty" jsonschema:"qname, path:line, bare name or skey"`
	Path          string `json:"path,omitempty" jsonschema:"repo-relative file path"`
	Changed       bool   `json:"changed,omitempty" jsonschema:"start from files changed against HEAD / the git index"`
	Depth         int    `json:"depth,omitempty" jsonschema:"levels to follow (default 3, max 6)"`
	MinConfidence int    `json:"min_confidence,omitempty" jsonschema:"minimum edge confidence 1-100 (default 30)"`
	MaxTokens     int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// ImportsInput are the imports arguments.
type ImportsInput struct {
	Path      string `json:"path,omitempty" jsonschema:"repo-relative file path (or package directory)"`
	Module    string `json:"module,omitempty" jsonschema:"package / module name or import spec"`
	Direction string `json:"direction,omitempty" jsonschema:"out (what it imports, default) or in (who imports it)"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// InheritanceInput are the inheritance arguments.
type InheritanceInput struct {
	Symbol    string `json:"symbol" jsonschema:"qname, path:line, bare name or skey of a type"`
	Direction string `json:"direction,omitempty" jsonschema:"up (supertypes), down (subtypes) or both (default)"`
	Depth     int    `json:"depth,omitempty" jsonschema:"levels to follow (default 3)"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// TestsForInput are the tests_for arguments.
type TestsForInput struct {
	Symbol    string `json:"symbol" jsonschema:"qname, path:line, bare name or skey"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// TraverseInput are the traverse arguments.
type TraverseInput struct {
	Start         string   `json:"start" jsonschema:"qname, path:line, bare name or skey"`
	EdgeKinds     []string `json:"edge_kinds,omitempty" jsonschema:"edge kinds to follow (default: calls, references, extends, implements, embeds, tests, declares)"`
	Direction     string   `json:"direction,omitempty" jsonschema:"out (default), in or both"`
	MaxDepth      int      `json:"max_depth,omitempty" jsonschema:"levels to follow (default 4, max 8)"`
	MinConfidence int      `json:"min_confidence,omitempty" jsonschema:"minimum edge confidence 1-100 (default 50)"`
	MaxTokens     int      `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// StatusInput are the status arguments.
type StatusInput struct {
	MaxTokens int `json:"max_tokens,omitempty" jsonschema:"output budget in tokens (default 1500, max 6000)"`
}

// orDefault returns v, or def when v is not positive, clamped to hi.
func orDefault(v, def, hi int) int {
	if v <= 0 {
		v = def
	}
	return min(v, hi)
}
