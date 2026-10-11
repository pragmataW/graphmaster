package install

import (
	"bytes"
	"fmt"
	"io/fs"
	"slices"
)

// work is the in-memory state of one file while an operation is planned.
// Nothing touches the disk until every file has been computed.
type work struct {
	path   string
	format Format
	mode   fs.FileMode // FormatGenerated create mode

	orig   []byte // bytes on disk at start
	exists bool   // file existed at start

	content    []byte // proposed bytes
	keep       bool   // proposed: file exists afterwards
	rec        *FileRecord
	ins        map[string][]Insertion // new insertions per applied agent
	agentOrder []string               // applied agents, in plan order
	restored   bool                   // content comes from the backup
	created    []Container            // containers created in this run
	dropAgents []string               // agents whose insertions are removed
	action     Action
	note       string
}

func (w *work) changed() bool {
	return w.keep != w.exists || !bytes.Equal(w.content, w.orig)
}

// priorOf returns agent's recorded insertions for this file.
func (w *work) priorOf(agent string) []Insertion {
	if w.rec == nil {
		return nil
	}
	return w.rec.insertionsOf(agent)
}

func findPrior(prior []Insertion, key string) (Insertion, bool) {
	for _, p := range prior {
		if p.key() == key {
			return p, true
		}
	}
	return Insertion{}, false
}

// apply merges agent's desired change into w.content.
func (w *work) apply(agent string, fc FileChange) error {
	if w.format != "" && w.format != fc.Format {
		return fmt.Errorf("%s: format %s conflicts with %s", w.path, fc.Format, w.format)
	}
	w.format = fc.Format
	prior := w.priorOf(agent)
	var err error
	switch fc.Format {
	case FormatJSON:
		err = w.applyJSON(agent, fc, prior)
	case FormatTOML, FormatMarkdown:
		err = w.applyMarkers(agent, fc, prior)
	case FormatGenerated:
		w.content = append([]byte(nil), fc.Content...)
		w.keep = true
		w.mode = fc.Mode.Perm()
		w.ins[agent] = append(w.ins[agent], Insertion{Agent: agent, Kind: KindFile})
	}
	return err
}

func (w *work) applyJSON(agent string, fc FileChange, prior []Insertion) error {
	doc, err := parseJSONDoc(w.content)
	if err != nil {
		return fmt.Errorf("refusing to modify %s: invalid JSON: %w", w.path, err)
	}
	type desired struct {
		ins    Insertion
		raw    []byte
		append bool
	}
	var want []desired
	keys := map[string]bool{}
	for _, op := range fc.Ops {
		var d desired
		switch o := op.(type) {
		case JSONSet:
			d.raw, _ = toRaw(o.Value)
			d.ins = Insertion{Agent: agent, Kind: KindJSONMember, Pointer: o.Pointer}
		case JSONAppend:
			d.raw, _ = toRaw(o.Value)
			d.ins = Insertion{Agent: agent, Kind: KindJSONElement, Pointer: o.Pointer}
			d.append = true
		}
		canon, err := canonicalJSON(d.raw)
		if err != nil {
			return fmt.Errorf("%s %s: %w", w.path, d.ins.Pointer, err)
		}
		d.ins.Value = []byte(canon)
		keys[d.ins.key()] = true
		want = append(want, d)
	}

	dirty := false
	// Revert recorded insertions this plan no longer wants (e.g. a hook
	// whose command changed), newest first.
	for _, p := range slices.Backward(prior) {
		if !keys[p.key()] {
			ch, err := revertJSON(doc, p)
			if err != nil {
				return fmt.Errorf("%s: %w", w.path, err)
			}
			dirty = dirty || ch
		}
	}
	for _, d := range want {
		var res editResult
		if d.append {
			res, err = doc.appendElem(d.ins.Pointer, d.raw)
		} else {
			res, err = doc.set(d.ins.Pointer, d.raw)
		}
		if err != nil {
			return fmt.Errorf("refusing to modify %s: %w", w.path, err)
		}
		ins := d.ins
		if p, ok := findPrior(prior, ins.key()); ok {
			ins.Prev = p.Prev // the value before graphmaster's first write
		} else {
			ins.Prev = res.prev
		}
		for _, c := range res.created {
			w.created = append(w.created, Container{Agent: agent, Pointer: c})
		}
		dirty = dirty || res.changed
		w.ins[agent] = append(w.ins[agent], ins)
	}
	if dirty {
		w.content = doc.bytes()
		w.keep = true
	}
	return nil
}

// revertJSON undoes one insertion; a missing target is not an error.
func revertJSON(doc *jsonDoc, in Insertion) (bool, error) {
	switch in.Kind {
	case KindJSONMember:
		if in.Prev != nil {
			res, err := doc.set(in.Pointer, in.Prev)
			return res.changed, err
		}
		return doc.remove(in.Pointer)
	case KindJSONElement:
		n, err := doc.removeElems(in.Pointer, in.Value)
		return n > 0, err
	}
	return false, nil
}

func (w *work) applyMarkers(agent string, fc FileChange, prior []Insertion) error {
	style := fc.markers()
	if fc.Format == FormatTOML {
		if err := validateTOML(w.content); err != nil {
			return fmt.Errorf("refusing to modify %s: %w", w.path, err)
		}
	}
	if _, err := style.scan(w.content); err != nil {
		return fmt.Errorf("refusing to modify %s: %w", w.path, err)
	}
	keys := map[string]bool{}
	for _, op := range fc.Ops {
		keys[(Insertion{Kind: KindMarker, Marker: op.(Block).ID}).key()] = true
	}
	content := w.content
	for _, p := range slices.Backward(prior) {
		if p.Kind == KindMarker && !keys[p.key()] {
			out, _, err := style.remove(content, p.Marker, p.Sep)
			if err != nil {
				return fmt.Errorf("%s: %w", w.path, err)
			}
			content = out
		}
	}
	for _, op := range fc.Ops {
		b := op.(Block)
		existed := style.has(content, b.ID)
		out, sep, _, err := style.upsert(content, b.ID, b.Body)
		if err != nil {
			return fmt.Errorf("refusing to modify %s: %w", w.path, err)
		}
		ins := Insertion{Agent: agent, Kind: KindMarker, Marker: b.ID, Sep: sep}
		if existed {
			if p, ok := findPrior(prior, ins.key()); ok {
				ins.Sep = p.Sep
			} else {
				// Adopting a block without a record (state lost): assume the
				// blank line before it is the separator we would have added.
				ins.Sep = style.guessSep(content, b.ID)
			}
		}
		content = out
		w.ins[agent] = append(w.ins[agent], ins)
	}
	if fc.Format == FormatTOML {
		if err := validateTOML(content); err != nil {
			return fmt.Errorf("refusing to modify %s: the graphmaster block would make it %w (is the same table already defined outside the block?)", w.path, err)
		}
	}
	if !bytes.Equal(content, w.content) {
		w.content = content
		w.keep = true
	}
	return nil
}

// removeSurgically reverts every insertion of agents from w.content and
// drops the containers they created once empty. Generated files are
// dropped entirely (w.keep = false).
func (w *work) removeSurgically(agents []string) error {
	var mine []Insertion
	for _, in := range w.rec.Insertions {
		if slices.Contains(agents, in.Agent) {
			mine = append(mine, in)
		}
	}
	switch w.rec.Format {
	case FormatJSON:
		doc, err := parseJSONDoc(w.content)
		if err != nil {
			return fmt.Errorf("refusing to modify %s: invalid JSON: %w", w.path, err)
		}
		dirty := false
		for _, in := range slices.Backward(mine) {
			ch, err := revertJSON(doc, in)
			if err != nil {
				return fmt.Errorf("%s: %w", w.path, err)
			}
			dirty = dirty || ch
		}
		var conts []string
		for _, c := range w.rec.Created {
			if slices.Contains(agents, c.Agent) {
				conts = append(conts, c.Pointer)
			}
		}
		// Deepest first so a parent emptied by its child goes too.
		slices.SortFunc(conts, func(a, b string) int { return len(b) - len(a) })
		for _, c := range conts {
			if doc.isEmptyContainer(c) {
				if _, err := doc.remove(c); err != nil {
					return fmt.Errorf("%s: %w", w.path, err)
				}
				dirty = true
			}
		}
		if dirty {
			w.content = doc.bytes()
		}
	case FormatTOML, FormatMarkdown:
		style := FileChange{Format: w.rec.Format}.markers()
		for _, in := range slices.Backward(mine) {
			out, _, err := style.remove(w.content, in.Marker, in.Sep)
			if err != nil {
				return fmt.Errorf("refusing to modify %s: %w", w.path, err)
			}
			w.content = out
		}
	case FormatGenerated:
		w.keep = false
		w.content = nil
	}
	return nil
}

// isEmptyResult reports whether content holds nothing but an empty
// skeleton, so a file graphmaster created can be deleted.
func isEmptyResult(f Format, content []byte) bool {
	t := bytes.TrimSpace(content)
	if f == FormatJSON {
		return len(t) == 0 || string(t) == "{}"
	}
	return len(t) == 0
}
