package install

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// MarkerVersion is written into every begin marker.
const MarkerVersion = 1

// markerStyle renders and recognizes graphmaster marker lines for one
// comment syntax, e.g. "# graphmaster:begin v1" or
// "<!-- graphmaster:begin v1 -->". An optional id distinguishes several
// blocks in one file.
type markerStyle struct {
	open, close string // comment delimiters
	beginRE     *regexp.Regexp
	endRE       *regexp.Regexp
}

func newMarkerStyle(open, close string) markerStyle {
	o, c := regexp.QuoteMeta(open), regexp.QuoteMeta(close)
	return markerStyle{
		open:    open,
		close:   close,
		beginRE: regexp.MustCompile(`^\s*` + o + `\s*graphmaster:begin\s+v(\d+)(?:\s+(\S+?))?\s*` + c + `\s*$`),
		endRE:   regexp.MustCompile(`^\s*` + o + `\s*graphmaster:end(?:\s+(\S+?))?\s*` + c + `\s*$`),
	}
}

var (
	tomlMarkers = newMarkerStyle("#", "")
	mdMarkers   = newMarkerStyle("<!--", "-->")
)

func (s markerStyle) line(kind, id string) string {
	var b strings.Builder
	b.WriteString(s.open)
	b.WriteString(" graphmaster:")
	b.WriteString(kind)
	if id != "" {
		b.WriteString(" ")
		b.WriteString(id)
	}
	if s.close != "" {
		b.WriteString(" ")
		b.WriteString(s.close)
	}
	return b.String()
}

func (s markerStyle) beginLine(id string) string {
	return s.line(fmt.Sprintf("begin v%d", MarkerVersion), id)
}

func (s markerStyle) endLine(id string) string { return s.line("end", id) }

// markerBlock is the byte range [start,end) from the begin line through the
// end line's newline (or EOF).
type markerBlock struct {
	id         string
	start, end int
}

// scan locates all marker blocks, rejecting malformed nesting.
func (s markerStyle) scan(b []byte) ([]markerBlock, error) {
	var blocks []markerBlock
	var open *markerBlock
	seen := map[string]bool{}
	lineNo := 0
	for off := 0; off < len(b); {
		lineNo++
		end := bytes.IndexByte(b[off:], '\n')
		next := len(b)
		if end >= 0 {
			next = off + end + 1
		}
		line := strings.TrimRight(string(b[off:next]), "\r\n")
		if m := s.beginRE.FindStringSubmatch(line); m != nil {
			if open != nil {
				return nil, fmt.Errorf("line %d: graphmaster:begin inside an unterminated block", lineNo)
			}
			if seen[m[2]] {
				return nil, fmt.Errorf("line %d: duplicate graphmaster block %q", lineNo, m[2])
			}
			seen[m[2]] = true
			open = &markerBlock{id: m[2], start: off}
		} else if m := s.endRE.FindStringSubmatch(line); m != nil {
			if open == nil {
				return nil, fmt.Errorf("line %d: graphmaster:end without a matching begin", lineNo)
			}
			if m[1] != open.id {
				return nil, fmt.Errorf("line %d: graphmaster:end %q closes block %q", lineNo, m[1], open.id)
			}
			open.end = next
			blocks = append(blocks, *open)
			open = nil
		}
		off = next
	}
	if open != nil {
		return nil, fmt.Errorf("unterminated graphmaster block %q (missing graphmaster:end)", open.id)
	}
	return blocks, nil
}

func (s markerStyle) find(b []byte, id string) (markerBlock, bool, error) {
	blocks, err := s.scan(b)
	if err != nil {
		return markerBlock{}, false, err
	}
	for _, bl := range blocks {
		if bl.id == id {
			return bl, true, nil
		}
	}
	return markerBlock{}, false, nil
}

// has reports whether a well-formed block with id exists.
func (s markerStyle) has(b []byte, id string) bool {
	_, ok, err := s.find(b, id)
	return ok && err == nil
}

// render returns the full block text for body.
func (s markerStyle) render(id, body string) (string, error) {
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimRight(l, "\r")
		if s.beginRE.MatchString(l) || s.endRE.MatchString(l) {
			return "", errors.New("block body must not contain graphmaster marker lines")
		}
	}
	if strings.ContainsAny(id, " \t\r\n") || strings.Contains(id, "-->") {
		return "", fmt.Errorf("invalid marker id %q", id)
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return s.beginLine(id) + "\n" + body + s.endLine(id) + "\n", nil
}

// upsert replaces the block with id in place, or appends it. sep is the
// separator inserted before an appended block (to be recorded so removal
// can undo it exactly).
func (s markerStyle) upsert(src []byte, id, body string) (out []byte, sep string, changed bool, err error) {
	text, err := s.render(id, body)
	if err != nil {
		return nil, "", false, err
	}
	bl, ok, err := s.find(src, id)
	if err != nil {
		return nil, "", false, err
	}
	if ok {
		if string(src[bl.start:bl.end]) == text {
			return src, "", false, nil
		}
		out = append(append(append([]byte(nil), src[:bl.start]...), text...), src[bl.end:]...)
		return out, "", true, nil
	}
	switch {
	case len(src) == 0 || bytes.HasSuffix(src, []byte("\n\n")):
		sep = ""
	case bytes.HasSuffix(src, []byte("\n")):
		sep = "\n"
	default:
		sep = "\n\n"
	}
	out = append(append(append([]byte(nil), src...), sep...), text...)
	return out, sep, true, nil
}

// remove deletes the block with id plus the separator recorded at insert
// time when it still directly precedes the block.
func (s markerStyle) remove(src []byte, id, sep string) ([]byte, bool, error) {
	bl, ok, err := s.find(src, id)
	if err != nil || !ok {
		return src, false, err
	}
	head := src[:bl.start]
	if sep != "" && bytes.HasSuffix(head, []byte(sep)) {
		head = head[:len(head)-len(sep)]
	}
	out := append(append([]byte(nil), head...), src[bl.end:]...)
	return out, true, nil
}

// validateTOML reports whether b is a valid TOML document.
func validateTOML(b []byte) error {
	var v map[string]any
	if err := toml.Unmarshal(b, &v); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return fmt.Errorf("invalid TOML at line %d, column %d: %w", row, col, de)
		}
		return fmt.Errorf("invalid TOML: %w", err)
	}
	return nil
}

// checkTOMLBlockBody requires a TOML block to open with a table header:
// the block is appended at the end of the file, where bare keys would
// silently join the user's last table.
func checkTOMLBlockBody(body string) error {
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, "[") {
			return validateTOML([]byte(body))
		}
		return fmt.Errorf("TOML block must start with a [table] header, found %q", l)
	}
	return nil
}

// guessSep returns "\n" when the block with id is preceded by a blank line,
// which is the separator upsert adds to a file ending in a single newline.
func (s markerStyle) guessSep(src []byte, id string) string {
	bl, ok, err := s.find(src, id)
	if err != nil || !ok {
		return ""
	}
	if head := src[:bl.start]; len(head) > 2 && bytes.HasSuffix(head, []byte("\n\n")) {
		return "\n"
	}
	return ""
}
