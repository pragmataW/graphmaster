package testutil

import (
	"bytes"
	"fmt"
)

// vendorFiles are committed in Materialize's second commit. Go's vendor
// directory is complete enough for `go build -mod=vendor` to succeed.
var vendorFiles = map[string]string{
	"go/vendor/modules.txt": `# github.com/acme/strutil v1.0.0
## explicit
github.com/acme/strutil
`,
	"go/vendor/github.com/acme/strutil/strutil.go": `// Package strutil is a vendored third-party helper.
package strutil

// Reverse returns s with its runes in reverse order.
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
`,
	"php/vendor/autoload.php": `<?php

// Minimal stand-in for Composer's generated autoloader.
spl_autoload_register(static function (string $class): void {
    $prefix = 'App\\';
    if (strncmp($class, $prefix, strlen($prefix)) === 0) {
        require __DIR__ . '/../src/' . str_replace('\\', '/', substr($class, strlen($prefix))) . '.php';
    }
});
`,
}

// untrackedFiles exist only in the working tree and are not ignored.
var untrackedFiles = map[string]string{
	// Untracked source that calls tracked code: must still be indexed.
	"go/internal/util/slug.go": `package util

import "strings"

// Slug turns an ID into a URL-friendly slug.
func Slug(id string) string {
	return strings.ReplaceAll(strings.ToLower(FormatID(id)), " ", "-")
}
`,
	"python/shop/experimental.py": `"""Not yet committed: flash-sale pricing."""

from .services.pricing import apply_discount


def flash_sale(amount: int) -> int:
    return apply_discount(amount * 2)
`,
	"notes/scratch.md": "# Scratch\n\n- try caching OrderService lookups\n",
	// Re-included by the root "!important.log" negation.
	"logs/important.log": "keep me\n",
	// Re-included by the nested js/.gitignore "!keep.tmp" negation.
	"js/keep.tmp": "kept\n",
}

// ignoredFiles are matched by the fixture's .gitignore rules.
var ignoredFiles = map[string]string{
	"debug.log":        "noise\n",
	".env":             "FIXTURE_TOKEN=not-a-real-secret\n",
	"build/output.js":  "function built(){return 1}\n",
	"ts/dist/index.js": "export const compiled = true;\n",
	"js/cache.tmp":     "cache\n",
	"python/shop/__pycache__/models.cpython-312.pyc": "\x00\x00\x00\x00binary\x00bytecode\n",
}

// bigFileSize is the target size of BigFilePath: comfortably above the
// 1 MiB indexing cap.
const bigFileSize = 1<<20 + 256<<10

// bigFileContent returns a deterministic, valid Python module larger than
// 1 MiB made of short lines (so it is "too big", not "minified").
func bigFileContent() []byte {
	var b bytes.Buffer
	b.Grow(bigFileSize + 64)
	b.WriteString("\"\"\"Generated product catalog (large on purpose).\"\"\"\n\nCATALOG = {\n")
	for i := 0; b.Len() < bigFileSize; i++ {
		fmt.Fprintf(&b, "    \"sku-%06d\": (\"product %06d\", %d),\n", i, i, (i*7919)%100000)
	}
	b.WriteString("}\n")
	return b.Bytes()
}
