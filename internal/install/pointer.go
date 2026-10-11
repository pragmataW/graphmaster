package install

import (
	"fmt"
	"strings"
)

// Pointer builds an RFC 6901 JSON pointer from unescaped reference tokens,
// e.g. Pointer("mcpServers", "graphmaster") == "/mcpServers/graphmaster".
func Pointer(tokens ...string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken(t))
	}
	return b.String()
}

func escapeToken(t string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(t)
}

func unescapeToken(t string) string {
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(t)
}

// splitPointer returns the unescaped tokens of ptr ("" is the root).
func splitPointer(ptr string) ([]string, error) {
	if ptr == "" {
		return nil, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q: must start with '/'", ptr)
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		parts[i] = unescapeToken(p)
	}
	return parts, nil
}
