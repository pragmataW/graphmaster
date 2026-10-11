package codex

import "strings"

// patchHeaders are the apply_patch envelope lines that name a file.
var patchHeaders = []string{
	"*** Update File:",
	"*** Add File:",
	"*** Delete File:",
	"*** Move to:",
}

// patchPaths extracts the target paths of a Codex apply_patch envelope in
// order of appearance, without duplicates. Paths are returned as written
// (relative to the session cwd or absolute). Only header lines count:
// content lines start with '+', '-', ' ' or "@@", so they never match.
func patchPaths(patch string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, h := range patchHeaders {
			rest, ok := strings.CutPrefix(line, h)
			if !ok {
				continue
			}
			if p := strings.TrimSpace(rest); p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			break
		}
	}
	return out
}
