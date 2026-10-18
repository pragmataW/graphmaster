//go:build tools

package deps

// Blank imports keep every pinned v1 dependency required in go.mod.
// The "tools" tag keeps these out of normal builds; go mod tidy still sees them.
import (
	_ "github.com/go-git/go-git/v5/plumbing/format/gitignore"
	_ "github.com/go-git/go-git/v5/plumbing/format/index"
	_ "github.com/gofrs/flock"
	_ "github.com/mattn/go-sqlite3"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/spf13/cobra"
	_ "github.com/tailscale/hujson"
	_ "github.com/tree-sitter/go-tree-sitter"
	_ "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-c/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-go/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-java/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-php/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-python/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	_ "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
	_ "golang.org/x/sync/errgroup"
	_ "golang.org/x/sync/singleflight"
	_ "modernc.org/sqlite"
	_ "mvdan.cc/sh/v3/syntax"
)
