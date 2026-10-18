// Command graphmaster builds and serves a code graph for AI coding agents.
// Install with: go install github.com/pragmataW/graphmaster@latest
package main

import (
	"os"

	"github.com/pragmataW/graphmaster/internal/cli"
)

func main() { os.Exit(cli.Main()) }
