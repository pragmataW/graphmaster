// Package deps pins the module's v1 dependency set.
//
// deps.go (build tag "tools") blank-imports every pinned module so that
// `go mod tidy` keeps them in go.mod before the packages that use them exist.
// The tests in this package double as smoke tests: they load every
// tree-sitter grammar into the pinned core (ABI compatibility) and verify the
// SQLite driver was built with FTS5.
package deps
