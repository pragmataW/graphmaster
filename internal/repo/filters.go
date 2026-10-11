package repo

import (
	"bytes"
	"regexp"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Content filter thresholds.
const (
	// BinarySniffLen is how many leading bytes are scanned for a NUL.
	BinarySniffLen = 8 << 10
	// MinifiedAvgLine is the average line length above which a file is
	// considered minified.
	MinifiedAvgLine = 300
	// MinifiedMaxLine is the single-line length above which a file is
	// considered minified.
	MinifiedMaxLine = 10_000
	// minifiedMinSize keeps tiny one-liners from tripping the average rule.
	minifiedMinSize = 1 << 10

	// generatedScanLen bounds the search for the Go-style marker, which
	// may follow a license header.
	generatedScanLen = 8 << 10
	// generatedHeaderLines bounds the search for the looser markers
	// (@generated, auto-generated), which are only meaningful in a header.
	generatedHeaderLines = 10
)

// goGenerated is the convention from https://go.dev/s/generatedcode, relaxed
// to the common comment leaders (//, #, /*, *, --, ;, <!--). Quotes and
// backquotes are deliberately not leaders, so generator sources that only
// print the marker in a string literal are not mistaken for generated code.
var goGenerated = regexp.MustCompile(`(?m)^[ \t]*[/#*;!<-]*[ \t]*Code generated .* DO NOT EDIT\.?`)

// IsBinary reports whether content has a NUL byte in its first
// BinarySniffLen bytes.
func IsBinary(content []byte) bool {
	return bytes.IndexByte(content[:min(len(content), BinarySniffLen)], 0) >= 0
}

// IsMinified reports whether content looks minified: average line length
// above MinifiedAvgLine (for files of at least 1KiB) or any line longer than
// MinifiedMaxLine.
func IsMinified(content []byte) bool {
	lines, longest, cur := 0, 0, 0
	for _, b := range content {
		if b == '\n' {
			lines++
			longest = max(longest, cur)
			cur = 0
			continue
		}
		cur++
	}
	if cur > 0 {
		lines++
		longest = max(longest, cur)
	}
	if longest > MinifiedMaxLine {
		return true
	}
	return lines > 0 && len(content) >= minifiedMinSize && len(content)/lines > MinifiedAvgLine
}

// IsGenerated reports whether content carries a generated-code marker:
// "Code generated ... DO NOT EDIT" on its own line within the first 8KiB,
// or "@generated" / "auto-generated" within the first 10 lines.
func IsGenerated(content []byte) bool {
	if goGenerated.Match(content[:min(len(content), generatedScanLen)]) {
		return true
	}
	head := content
	for i, n := 0, 0; i < len(head); i++ {
		if head[i] == '\n' {
			if n++; n == generatedHeaderLines {
				head = head[:i]
				break
			}
		}
	}
	return bytes.Contains(head, []byte("@generated")) ||
		bytes.Contains(bytes.ToLower(head), []byte("auto-generated"))
}

// Classify decides how a file should be treated by the indexer. size is the
// on-disk size; content may be nil when size already exceeds maxSize.
// Checks run in order: language support, size cap, binary, generated,
// minified.
func Classify(path string, size int64, content []byte, maxSize int64) model.FileStatus {
	switch {
	case DetectLang(path, content) == model.LangUnknown:
		return model.StatusUnsupported
	case maxSize > 0 && size > maxSize:
		return model.StatusTooBig
	case IsBinary(content):
		return model.StatusBinary
	case IsGenerated(content):
		return model.StatusGenerated
	case IsMinified(content):
		return model.StatusMinified
	default:
		return model.StatusOK
	}
}
