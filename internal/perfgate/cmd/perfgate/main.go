// Command perfgate checks benchmark results against bench/thresholds.json.
// See internal/perfgate and bench/README.md.
package main

import (
	"os"

	"github.com/pragmataW/graphmaster/internal/perfgate"
)

func main() {
	os.Exit(perfgate.Run(os.Args[1:], os.Stdout, os.Stderr))
}
