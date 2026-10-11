package util

import (
	"fmt"
	"strings"

	"github.com/acme/strutil"
)

// FormatID normalizes an order or item ID for display.
func FormatID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

// FormatMoney renders cents as a decimal amount.
func FormatMoney(cents int) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// Mirror reverses s using the vendored strutil package.
func Mirror(s string) string {
	return strutil.Reverse(s)
}
