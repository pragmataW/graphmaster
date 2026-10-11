package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tailscale/hujson"
)

// jsonDoc is a JSON/JSONC document edited through the hujson AST. Every
// mutation re-packs and re-parses the document so byte offsets used for
// indentation detection are always current.
type jsonDoc struct {
	src  []byte
	v    hujson.Value
	unit string // one indentation step detected from the file
}

// editResult describes what a set/append did.
type editResult struct {
	changed bool
	created []string        // container pointers created, outermost first
	prev    json.RawMessage // previous member value (set only)
}

func parseJSONDoc(b []byte) (*jsonDoc, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}\n")
	}
	d := &jsonDoc{}
	if err := d.load(b); err != nil {
		return nil, err
	}
	if _, ok := d.v.Value.(*hujson.Object); !ok {
		return nil, errors.New("top-level JSON value is not an object")
	}
	d.unit = detectIndentUnit(d.src)
	return d, nil
}

func (d *jsonDoc) load(b []byte) error {
	v, err := hujson.Parse(b)
	if err != nil {
		return err
	}
	d.src, d.v = b, v
	return nil
}

func (d *jsonDoc) reload() error {
	b := d.v.Pack()
	if err := d.load(b); err != nil {
		// Indicates a bug in our AST surgery, never user input.
		return fmt.Errorf("internal error: edited JSON no longer parses: %w", err)
	}
	return nil
}

func (d *jsonDoc) bytes() []byte { return append([]byte(nil), d.src...) }

// get returns the minimized standard-JSON value at ptr.
func (d *jsonDoc) get(ptr string) (json.RawMessage, bool) {
	n := d.v.Find(ptr)
	if n == nil {
		return nil, false
	}
	return nodeJSON(n), true
}

// isEmptyContainer reports whether ptr is an object or array with no entries.
func (d *jsonDoc) isEmptyContainer(ptr string) bool {
	n := d.v.Find(ptr)
	if n == nil {
		return false
	}
	switch c := n.Value.(type) {
	case *hujson.Object:
		return len(c.Members) == 0
	case *hujson.Array:
		return len(c.Elements) == 0
	}
	return false
}

// set makes the object member at ptr equal to val, creating missing parent
// objects. An existing different value is replaced and returned as prev.
func (d *jsonDoc) set(ptr string, val json.RawMessage) (editResult, error) {
	canon, err := canonicalJSON(val)
	if err != nil {
		return editResult{}, fmt.Errorf("value for %s: %w", ptr, err)
	}
	toks, err := splitPointer(ptr)
	if err != nil {
		return editResult{}, err
	}
	if len(toks) == 0 {
		return editResult{}, errors.New("cannot set the document root")
	}
	if cur, ok := d.get(ptr); ok {
		if c, _ := canonicalJSON(cur); c == canon {
			return editResult{}, nil
		}
		if err := d.replace(ptr, val); err != nil {
			return editResult{}, err
		}
		return editResult{changed: true, prev: cur}, nil
	}
	return d.insertPath(toks, val, false)
}

// appendElem appends val to the array at ptr unless an equal element is
// already present, creating the array and missing parent objects.
func (d *jsonDoc) appendElem(ptr string, val json.RawMessage) (editResult, error) {
	canon, err := canonicalJSON(val)
	if err != nil {
		return editResult{}, fmt.Errorf("value for %s: %w", ptr, err)
	}
	toks, err := splitPointer(ptr)
	if err != nil {
		return editResult{}, err
	}
	if n := d.v.Find(ptr); n != nil {
		arr, ok := n.Value.(*hujson.Array)
		if !ok {
			return editResult{}, fmt.Errorf("%s is not an array", ptr)
		}
		for i := range arr.Elements {
			if c, _ := canonicalJSON(nodeJSON(&arr.Elements[i])); c == canon {
				return editResult{}, nil
			}
		}
		if err := d.insertInto(ptr, "", val); err != nil {
			return editResult{}, err
		}
		return editResult{changed: true}, nil
	}
	if len(toks) == 0 {
		return editResult{}, errors.New("document root is not an array")
	}
	return d.insertPath(toks, val, true)
}

// insertPath adds the missing tail of toks below its deepest existing
// ancestor, wrapping val in objects (and an array when asArray is set).
func (d *jsonDoc) insertPath(toks []string, val json.RawMessage, asArray bool) (editResult, error) {
	i := len(toks) - 1
	for i > 0 && d.v.Find(Pointer(toks[:i]...)) == nil {
		i--
	}
	anc := Pointer(toks[:i]...)
	if _, ok := d.v.Find(anc).Value.(*hujson.Object); !ok {
		return editResult{}, fmt.Errorf("cannot create %s: %s is not an object", Pointer(toks...), displayPtr(anc))
	}
	var res editResult
	for j := i + 1; j <= len(toks); j++ {
		if j < len(toks) || asArray {
			res.created = append(res.created, Pointer(toks[:j]...))
		}
	}
	// Build the nested value from the inside out.
	v := val
	if asArray {
		v = json.RawMessage("[" + string(val) + "]")
	}
	for j := len(toks) - 1; j > i; j-- {
		k, _ := json.Marshal(toks[j])
		v = json.RawMessage("{" + string(k) + ":" + string(v) + "}")
	}
	if err := d.insertInto(anc, toks[i], v); err != nil {
		return editResult{}, err
	}
	res.changed = true
	return res, nil
}

// insertInto appends a member named name (object) or an element (array,
// name ignored) to the container at ptr, matching the file's indentation.
func (d *jsonDoc) insertInto(ptr, name string, val json.RawMessage) error {
	c := d.v.Find(ptr)
	lineInd := lineIndentAt(d.src, c.StartOffset)
	var first, after []byte
	var n int
	path := ptr + "/-"
	switch t := c.Value.(type) {
	case *hujson.Object:
		n = len(t.Members)
		if n > 0 {
			first = t.Members[0].Name.BeforeExtra
		}
		after = t.AfterExtra
		path = ptr + "/" + escapeToken(name)
	case *hujson.Array:
		n = len(t.Elements)
		if n > 0 {
			first = t.Elements[0].BeforeExtra
		}
		after = t.AfterExtra
	default:
		return fmt.Errorf("%s is not a container", displayPtr(ptr))
	}
	// A trailing comma after the last entry is user style worth keeping.
	trailingComma := n > 0 && lastValue(c).AfterExtra != nil
	multiline := n == 0 || bytes.ContainsRune(first, '\n') || bytes.ContainsRune(after, '\n')
	childInd := lineInd + d.unit
	if n > 0 && bytes.ContainsRune(first, '\n') {
		childInd = trailingIndent(first)
	}

	text, err := encodeJSON(val, childInd, d.unit, multiline)
	if err != nil {
		return err
	}
	if err := d.patch("add", path, text); err != nil {
		return err
	}
	c = d.v.Find(ptr)
	var lead, afterExtra *hujson.Extra
	switch t := c.Value.(type) {
	case *hujson.Object:
		m := &t.Members[len(t.Members)-1]
		lead, afterExtra = &m.Name.BeforeExtra, &t.AfterExtra
		m.Value.BeforeExtra = hujson.Extra(" ")
	case *hujson.Array:
		e := &t.Elements[len(t.Elements)-1]
		lead, afterExtra = &e.BeforeExtra, &t.AfterExtra
	}
	if trailingComma {
		if last := lastValue(c); last.AfterExtra == nil {
			last.AfterExtra = hujson.Extra{}
		}
	}
	if multiline {
		*lead = withIndent(*lead, childInd)
		if !bytes.ContainsRune(*afterExtra, '\n') {
			*afterExtra = withIndent(*afterExtra, lineInd)
		}
	} else if len(bytes.TrimSpace(*lead)) == 0 {
		*lead = hujson.Extra(" ")
	}
	return d.reload()
}

// replace overwrites the value at ptr, indenting it like its line.
func (d *jsonDoc) replace(ptr string, val json.RawMessage) error {
	n := d.v.Find(ptr)
	ind := lineIndentAt(d.src, n.StartOffset)
	text, err := encodeJSON(val, ind, d.unit, true)
	if err != nil {
		return err
	}
	if err := d.patch("replace", ptr, text); err != nil {
		return err
	}
	return d.reload()
}

// remove deletes the member or element at ptr. A container left empty with
// only whitespace inside collapses to {} / [].
func (d *jsonDoc) remove(ptr string) (bool, error) {
	if d.v.Find(ptr) == nil {
		return false, nil
	}
	toks, _ := splitPointer(ptr)
	parent := Pointer(toks[:len(toks)-1]...)
	pc := d.v.Find(parent)
	closeExtra := append([]byte(nil), *afterExtraOf(pc)...)
	trailingComma := lastValue(pc).AfterExtra != nil
	if err := d.patch("remove", ptr, nil); err != nil {
		return false, err
	}
	pc = d.v.Find(parent)
	// Removing the last entry can move its leading comment and indentation
	// in front of the closing bracket; restore the original closing indent
	// and trailing-comma style.
	if ae := afterExtraOf(pc); bytes.ContainsRune(closeExtra, '\n') && bytes.ContainsRune(*ae, '\n') {
		*ae = append(bytes.TrimRight(*ae, " \t"), trailingIndent(closeExtra)...)
	}
	if last := lastValue(pc); trailingComma && last != nil && last.AfterExtra == nil {
		last.AfterExtra = hujson.Extra{}
	}
	d.collapseIfEmpty(parent)
	return true, d.reload()
}

// removeElems deletes every element of the array at ptr equal to val.
func (d *jsonDoc) removeElems(ptr string, val json.RawMessage) (int, error) {
	canon, err := canonicalJSON(val)
	if err != nil {
		return 0, err
	}
	removed := 0
	for {
		n := d.v.Find(ptr)
		if n == nil {
			return removed, nil
		}
		arr, ok := n.Value.(*hujson.Array)
		if !ok {
			return removed, nil
		}
		idx := -1
		for i := range arr.Elements {
			if c, _ := canonicalJSON(nodeJSON(&arr.Elements[i])); c == canon {
				idx = i
				break
			}
		}
		if idx < 0 {
			return removed, nil
		}
		if _, err := d.remove(fmt.Sprintf("%s/%d", ptr, idx)); err != nil {
			return removed, err
		}
		removed++
	}
}

// afterExtraOf returns the extra before container c's closing bracket.
func afterExtraOf(c *hujson.Value) *hujson.Extra {
	switch t := c.Value.(type) {
	case *hujson.Object:
		return &t.AfterExtra
	case *hujson.Array:
		return &t.AfterExtra
	}
	return new(hujson.Extra)
}

// lastValue returns the value of the last member/element of container c.
func lastValue(c *hujson.Value) *hujson.Value {
	switch t := c.Value.(type) {
	case *hujson.Object:
		if len(t.Members) > 0 {
			return &t.Members[len(t.Members)-1].Value
		}
	case *hujson.Array:
		if len(t.Elements) > 0 {
			return &t.Elements[len(t.Elements)-1]
		}
	}
	return nil
}

func (d *jsonDoc) collapseIfEmpty(ptr string) {
	n := d.v.Find(ptr)
	if n == nil {
		return
	}
	switch c := n.Value.(type) {
	case *hujson.Object:
		if len(c.Members) == 0 && len(bytes.TrimSpace(c.AfterExtra)) == 0 {
			c.AfterExtra = nil
		}
	case *hujson.Array:
		if len(c.Elements) == 0 && len(bytes.TrimSpace(c.AfterExtra)) == 0 {
			c.AfterExtra = nil
		}
	}
}

// patch applies one RFC 6902 operation; value is pre-encoded JSON text.
func (d *jsonDoc) patch(op, path string, value []byte) error {
	p, _ := json.Marshal(path)
	var b bytes.Buffer
	fmt.Fprintf(&b, `[{"op":%q,"path":%s`, op, p)
	if value != nil {
		b.WriteString(`,"value":`)
		b.Write(value)
	}
	b.WriteString("}]")
	if err := d.v.Patch(b.Bytes()); err != nil {
		return fmt.Errorf("%s %s: %w", op, displayPtr(path), err)
	}
	return nil
}

// nodeJSON renders a hujson node as minimized standard JSON.
func nodeJSON(n *hujson.Value) json.RawMessage {
	c := n.Clone()
	c.BeforeExtra, c.AfterExtra = nil, nil
	c.Minimize()
	return c.Pack()
}

// canonicalJSON normalizes JSON text (key order, whitespace, number form)
// so semantically equal values compare equal as strings.
func canonicalJSON(b []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return "", errors.New("invalid JSON: trailing data")
	}
	out, err := marshalNoEscape(v, "", "")
	return string(out), err
}

// encodeJSON renders val either indented (continuation lines prefixed with
// prefix) or compact, without HTML escaping and without reordering keys.
func encodeJSON(val json.RawMessage, prefix, unit string, indented bool) ([]byte, error) {
	if _, err := canonicalJSON(val); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	var err error
	if indented {
		err = json.Indent(&buf, val, prefix, unit)
	} else {
		err = json.Compact(&buf, val)
	}
	return buf.Bytes(), err
}

func marshalNoEscape(v any, prefix, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// lineIndentAt returns the leading whitespace of the line containing off.
func lineIndentAt(src []byte, off int) string {
	start := bytes.LastIndexByte(src[:off], '\n') + 1
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}

// trailingIndent returns the whitespace after the last newline of extra.
func trailingIndent(extra []byte) string {
	i := bytes.LastIndexByte(extra, '\n')
	rest := extra[i+1:]
	j := 0
	for j < len(rest) && (rest[j] == ' ' || rest[j] == '\t') {
		j++
	}
	return string(rest[:j])
}

// withIndent keeps any comments in extra, drops trailing blanks, and ends it
// with a newline followed by ind.
func withIndent(extra []byte, ind string) hujson.Extra {
	t := bytes.TrimRight(extra, " \t")
	out := append([]byte(nil), t...)
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return append(out, ind...)
}

// detectIndentUnit returns the indentation of the first indented line, or
// two spaces.
func detectIndentUnit(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n")) {
		ind := lineIndentAt(line, 0)
		if ind != "" && len(bytes.TrimSpace(line)) > 0 {
			if ind[0] == '\t' {
				return "\t"
			}
			return ind
		}
	}
	return "  "
}

func displayPtr(p string) string {
	if p == "" {
		return "the document root"
	}
	return p
}

// StandardizeJSON converts JSONC (comments, trailing commas) to standard
// JSON suitable for encoding/json, e.g. for doctor checks.
func StandardizeJSON(b []byte) ([]byte, error) {
	return hujson.Standardize(append([]byte(nil), b...))
}
