package parse

import (
	"sort"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/pragmataW/graphmaster/internal/model"
)

// maxExprText caps receiver/qualifier expression text stored in a Ref.
const maxExprText = 100

// Candidate records collected from query matches before assembly. Byte
// offsets are uint32 because model spans are, and files are size-capped.

type defCand struct {
	kind       model.NodeKind
	node       sitter.Node
	name       sitter.Node
	start, end uint32 // def node bytes
	nameStart  uint32
	scope      string

	parent int // index into the sorted candidate slice, or -1
	final  int32
}

type refCand struct {
	kind      model.RefKind
	name      sitter.Node
	start     uint32
	receiver  string
	qualifier string
}

type importCand struct {
	start    uint32
	line     uint32
	spec     string
	alias    string
	names    []string
	wildcard bool
}

type inheritCand struct {
	kind  model.EdgeKind
	node  sitter.Node
	start uint32
}

type localCand struct {
	name  string
	start uint32
}

type span2 struct{ start, end uint32 }

// extractor accumulates candidates for one file.
type extractor struct {
	spec model.LanguageSpec
	src  []byte

	pkg      string
	defs     []defCand
	refs     []refCand
	imports  []importCand
	inherits []inheritCand
	locals   []localCand
	tests    []span2
}

// extract runs the compiled query over root and assembles model.Facts.
func extract(spec model.LanguageSpec, c *compiled, cursor *sitter.QueryCursor, root *sitter.Node, src []byte) *model.Facts {
	x := &extractor{spec: spec, src: src}
	matches := cursor.Matches(c.query, root, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		x.addMatch(c, m)
	}
	return x.build()
}

func (x *extractor) text(n *sitter.Node) string {
	return string(x.src[n.StartByte():n.EndByte()])
}

// addMatch copies what it needs out of m; the match memory is reused by the
// cursor on the next call.
func (x *extractor) addMatch(c *compiled, m *sitter.QueryMatch) {
	var (
		primary                    *capture
		pnode                      sitter.Node
		names                      []sitter.Node
		scope, receiver, qualifier *sitter.Node
		impPath, impAlias          *sitter.Node
		impNames                   []string
		impWildcard                bool
	)
	for i := range m.Captures {
		cp := &m.Captures[i]
		info := &c.captures[cp.Index]
		node := cp.Node
		switch info.role {
		case roleDef, roleRef, roleInherit:
			primary, pnode = info, node
		case roleName:
			names = append(names, node)
		case roleScope:
			scope = &node
		case roleReceiver:
			receiver = &node
		case roleQualifier:
			qualifier = &node
		case roleImportPath:
			impPath = &node
		case roleImportAlias:
			impAlias = &node
		case roleImportName:
			impNames = append(impNames, x.text(&node))
		case roleImportWildcard:
			impWildcard = true
		case roleTest:
			x.tests = append(x.tests, span2{uint32(node.StartByte()), uint32(node.EndByte())})
		case rolePackage:
			if x.pkg == "" {
				x.pkg = x.text(&node)
			}
		case roleLocal:
			x.locals = append(x.locals, localCand{name: x.text(&node), start: uint32(node.StartByte())})
		case roleIgnore:
		}
	}

	if impPath != nil {
		im := importCand{
			start:    uint32(impPath.StartByte()),
			line:     uint32(impPath.StartPosition().Row) + 1,
			spec:     x.importSpec(x.text(impPath)),
			names:    impNames,
			wildcard: impWildcard,
		}
		if impAlias != nil {
			im.alias = x.text(impAlias)
		}
		if im.spec != "" {
			x.imports = append(x.imports, im)
		}
	}
	if primary == nil {
		return
	}

	switch primary.role {
	case roleDef:
		var scopeText string
		if scope != nil {
			scopeText = x.text(scope)
		}
		for _, n := range names {
			if n.StartByte() == n.EndByte() { // MISSING node in an erroneous tree
				continue
			}
			x.defs = append(x.defs, defCand{
				kind:      primary.defKind,
				node:      pnode,
				name:      n,
				start:     uint32(pnode.StartByte()),
				end:       uint32(pnode.EndByte()),
				nameStart: uint32(n.StartByte()),
				scope:     scopeText,
			})
		}
	case roleRef:
		name := pnode
		if len(names) > 0 {
			name = names[0]
		}
		if name.StartByte() == name.EndByte() {
			return
		}
		r := refCand{kind: primary.refKind, name: name, start: uint32(name.StartByte())}
		if receiver != nil {
			r.receiver = compactText(x.text(receiver), maxExprText)
		}
		if qualifier != nil {
			r.qualifier = compactText(x.text(qualifier), maxExprText)
		}
		x.refs = append(x.refs, r)
	case roleInherit:
		x.inherits = append(x.inherits, inheritCand{kind: primary.inherit, node: pnode, start: uint32(pnode.StartByte())})
	}
}

func (x *extractor) importSpec(raw string) string {
	if h, ok := x.spec.(importCleaner); ok {
		return h.importSpec(raw)
	}
	return unquote(raw)
}

// build assembles the collected candidates into Facts.
func (x *extractor) build() *model.Facts {
	f := &model.Facts{Lang: x.spec.Lang(), Package: x.pkg}
	defs := x.buildDefs(f)
	x.buildRefs(f, defs)
	x.buildImports(f)
	x.buildInherits(f, defs)
	x.buildTests(f, defs)
	f.TestFile = len(f.Tests) > 0
	if h, ok := x.spec.(postProcessor); ok {
		h.postProcess(f)
	}
	return f
}

// buildDefs dedupes, nests and orders definitions. It returns the candidates
// sorted by span (outer before inner) with final indices filled in, which
// the span sweeps below rely on.
func (x *extractor) buildDefs(f *model.Facts) []defCand {
	defs := x.defs
	sort.SliceStable(defs, func(i, j int) bool {
		a, b := &defs[i], &defs[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if a.end != b.end {
			return a.end > b.end
		}
		return a.nameStart < b.nameStart
	})
	// Dedupe: the same node+name matched by several patterns keeps the first.
	out := defs[:0]
	for i := range defs {
		if n := len(out); n > 0 && out[n-1].start == defs[i].start &&
			out[n-1].end == defs[i].end && out[n-1].nameStart == defs[i].nameStart {
			continue
		}
		out = append(out, defs[i])
	}
	defs = out

	// Lexical nesting by span. Identical spans (e.g. "a, b int") are siblings.
	var stack []int
	for i := range defs {
		d := &defs[i]
		for len(stack) > 0 && !strictlyContains(&defs[stack[len(stack)-1]], d) {
			stack = stack[:len(stack)-1]
		}
		d.parent = -1
		if len(stack) > 0 {
			d.parent = stack[len(stack)-1]
		}
		stack = append(stack, i)
	}

	x.attachScopes(defs)
	order := dfsOrder(defs)
	for fi, si := range order {
		defs[si].final = int32(fi)
	}

	exp, _ := x.spec.(exportChecker)
	f.Defs = make([]model.Def, 0, len(defs))
	for _, si := range order {
		d := &defs[si]
		md := model.Def{
			Kind:   d.kind,
			Name:   x.text(&d.name),
			Parent: model.NoParent,
			Span:   nodeSpan(&d.node),
			Sig:    model.NormalizeSig(x.signature(d)),
		}
		if d.parent >= 0 {
			md.Parent = defs[d.parent].final
		}
		if d.scope != "" && (d.parent < 0 || !defs[d.parent].kind.IsType() || defs[d.parent].nameText(x) != d.scope) {
			md.Scope = splitScope(d.scope)
		}
		if exp != nil && exp.isExported(&md, &d.node, x.src) {
			md.Flags |= model.FlagExported
		}
		f.Defs = append(f.Defs, md)
	}
	return defs
}

func (d *defCand) nameText(x *extractor) string { return x.text(&d.name) }

// attachScopes makes a scoped top-level def (a Go method) a child of the
// same-file type its scope names, so qnames come out as Type.Method.
func (x *extractor) attachScopes(defs []defCand) {
	types := map[string]int{}
	for i := range defs {
		d := &defs[i]
		if d.parent < 0 && d.kind.IsType() {
			name := x.text(&d.name)
			if _, dup := types[name]; !dup {
				types[name] = i
			}
		}
	}
	for i := range defs {
		d := &defs[i]
		if d.scope == "" || d.parent >= 0 || d.kind.IsType() {
			continue
		}
		if t, ok := types[d.scope]; ok && t != i {
			d.parent = t
		}
	}
}

// dfsOrder returns sorted-candidate indices in an order where every parent
// precedes its children: roots in source order, each followed by its subtree.
func dfsOrder(defs []defCand) []int {
	children := make([][]int, len(defs))
	var roots []int
	for i := range defs {
		if p := defs[i].parent; p >= 0 {
			children[p] = append(children[p], i)
		} else {
			roots = append(roots, i)
		}
	}
	order := make([]int, 0, len(defs))
	var visit func(int)
	visit = func(i int) {
		order = append(order, i)
		for _, c := range children[i] {
			visit(c)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	return order
}

func strictlyContains(outer, inner *defCand) bool {
	if outer.start == inner.start && outer.end == inner.end {
		return false
	}
	return outer.start <= inner.start && inner.end <= outer.end
}

func (x *extractor) signature(d *defCand) string {
	if h, ok := x.spec.(sigFormatter); ok {
		if s, ok := h.signature(d.kind, &d.node, x.src); ok {
			return s
		}
	}
	return defaultSignature(&d.node, x.src)
}

// defaultSignature is the node text up to its body, or its first line.
func defaultSignature(n *sitter.Node, src []byte) string {
	start := n.StartByte()
	if body := n.ChildByFieldName("body"); body != nil {
		return string(src[start:body.StartByte()])
	}
	return firstLine(string(src[start:n.EndByte()]))
}

// spanSweep answers "which defs contain this byte offset" for offsets
// visited in ascending order, in amortized O(1) per query.
type spanSweep struct {
	defs  []defCand
	next  int
	stack []int
}

// at returns the containing defs, outermost first. The slice is reused.
func (s *spanSweep) at(pos uint32) []int {
	for s.next < len(s.defs) && s.defs[s.next].start <= pos {
		d := &s.defs[s.next]
		for len(s.stack) > 0 && s.defs[s.stack[len(s.stack)-1]].end <= d.start {
			s.stack = s.stack[:len(s.stack)-1]
		}
		s.stack = append(s.stack, s.next)
		s.next++
	}
	for len(s.stack) > 0 && s.defs[s.stack[len(s.stack)-1]].end <= pos {
		s.stack = s.stack[:len(s.stack)-1]
	}
	return s.stack
}

type localKey struct {
	def  int
	name string
}

var refPriority = [...]int{model.RefCall: 3, model.RefType: 2, model.RefMember: 1}

func (x *extractor) buildRefs(f *model.Facts, defs []defCand) {
	// Names of definitions are not references to themselves.
	defNames := make(map[uint32]bool, len(defs))
	for i := range defs {
		defNames[defs[i].nameStart] = true
	}

	// One reference per name node: call > type > member, qualified first.
	best := map[uint32]int{}
	var refs []refCand
	for _, r := range x.refs {
		if defNames[r.start] {
			continue
		}
		if j, ok := best[r.start]; ok {
			o := &refs[j]
			if refPriority[r.kind] > refPriority[o.kind] ||
				(r.kind == o.kind && o.qualifier == "" && r.qualifier != "") {
				refs[j] = r
			}
			continue
		}
		best[r.start] = len(refs)
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].start < refs[j].start })

	locals := x.localScopes(defs)
	sw := spanSweep{defs: defs}
	f.Refs = make([]model.Ref, 0, len(refs))
	for i := range refs {
		r := &refs[i]
		stack := sw.at(r.start)
		name := x.text(&r.name)
		if r.kind == model.RefType && r.qualifier == "" && r.receiver == "" && isLocal(locals, stack, name) {
			continue
		}
		enc := model.NoParent
		if len(stack) > 0 {
			enc = defs[stack[len(stack)-1]].final
		}
		pos := r.name.StartPosition()
		f.Refs = append(f.Refs, model.Ref{
			Kind:      r.kind,
			Name:      name,
			Qualifier: r.qualifier,
			Receiver:  r.receiver,
			Enclosing: enc,
			Line:      uint32(pos.Row) + 1,
			Col:       uint32(pos.Column),
		})
	}
}

// localScopes maps each @local name to its innermost enclosing def
// (sorted-candidate index, or -1 at file scope).
func (x *extractor) localScopes(defs []defCand) map[localKey]bool {
	if len(x.locals) == 0 {
		return nil
	}
	sort.Slice(x.locals, func(i, j int) bool { return x.locals[i].start < x.locals[j].start })
	out := make(map[localKey]bool, len(x.locals))
	sw := spanSweep{defs: defs}
	for _, l := range x.locals {
		stack := sw.at(l.start)
		d := -1
		if len(stack) > 0 {
			d = stack[len(stack)-1]
		}
		out[localKey{d, l.name}] = true
	}
	return out
}

func isLocal(locals map[localKey]bool, stack []int, name string) bool {
	if locals == nil {
		return false
	}
	if locals[localKey{-1, name}] {
		return true
	}
	for _, d := range stack {
		if locals[localKey{d, name}] {
			return true
		}
	}
	return false
}

func (x *extractor) buildImports(f *model.Facts) {
	sort.SliceStable(x.imports, func(i, j int) bool { return x.imports[i].start < x.imports[j].start })
	var last uint32
	for i, im := range x.imports {
		if i > 0 && im.start == last {
			continue
		}
		last = im.start
		f.Imports = append(f.Imports, model.Import{
			Spec: im.spec, Alias: im.alias, Names: im.names, Wildcard: im.wildcard, Line: im.line,
		})
	}
}

func (x *extractor) buildInherits(f *model.Facts, defs []defCand) {
	sort.SliceStable(x.inherits, func(i, j int) bool { return x.inherits[i].start < x.inherits[j].start })
	sw := spanSweep{defs: defs}
	seen := map[span2]bool{}
	for i := range x.inherits {
		in := &x.inherits[i]
		key := span2{in.start, uint32(in.kind)}
		if seen[key] {
			continue
		}
		seen[key] = true
		stack := sw.at(in.start)
		child := -1
		for k := len(stack) - 1; k >= 0; k-- {
			if defs[stack[k]].kind.IsType() {
				child = stack[k]
				break
			}
		}
		if child < 0 {
			continue
		}
		qual, name := splitTypeName(x.text(&in.node))
		if name == "" {
			continue
		}
		pos := in.node.StartPosition()
		f.Inherits = append(f.Inherits, model.Inherit{
			Child:     defs[child].final,
			Kind:      in.kind,
			Name:      name,
			Qualifier: qual,
			Line:      uint32(pos.Row) + 1,
			Col:       uint32(pos.Column),
		})
	}
}

func (x *extractor) buildTests(f *model.Facts, defs []defCand) {
	if len(x.tests) == 0 {
		return
	}
	bySpan := make(map[span2]int32, len(defs))
	for i := range defs {
		k := span2{defs[i].start, defs[i].end}
		if _, dup := bySpan[k]; !dup {
			bySpan[k] = defs[i].final
		}
	}
	namer, _ := x.spec.(testNamer)
	marked := map[int32]bool{}
	for _, t := range x.tests {
		di, ok := bySpan[t]
		if !ok || marked[di] {
			continue
		}
		marked[di] = true
		d := &f.Defs[di]
		d.Flags |= model.FlagTest
		if d.Kind == model.KindFunction {
			d.Kind = model.KindTest
		}
		target := defaultTestTarget(d.Name)
		if namer != nil {
			target = namer.testTarget(d.Name)
		}
		f.Tests = append(f.Tests, model.TestMark{Def: di, Target: target})
	}
	sort.Slice(f.Tests, func(i, j int) bool { return f.Tests[i].Def < f.Tests[j].Def })
}

func nodeSpan(n *sitter.Node) model.Span {
	sp, ep := n.StartPosition(), n.EndPosition()
	return model.Span{
		StartLine: uint32(sp.Row) + 1,
		StartCol:  uint32(sp.Column),
		EndLine:   uint32(ep.Row) + 1,
		EndCol:    uint32(ep.Column),
		StartByte: uint32(n.StartByte()),
		EndByte:   uint32(n.EndByte()),
	}
}
