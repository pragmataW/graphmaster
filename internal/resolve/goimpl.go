package resolve

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Go implicit interface satisfaction.
//
// Go types implement interfaces without declaring it, so extraction yields
// no implements edges for Go. After every resolution run the Go pass
// derives them from method sets and stores two kinds of rows, both with
// kind implements and no name (name_id NULL, so the resolver never treats
// them as references to re-resolve):
//
//   - type-level  T -> I, qual NULL: concrete type T (struct or named
//     non-interface type) satisfies interface I;
//   - method-level M -> I.M, qual DispatchQual: the method providing M in
//     T's method set (T's own, or promoted from an embedded type) may be the
//     target of a call through I.M (virtual dispatch). Queries follow these
//     rows from a concrete method to the interface methods it implements.
//
// Each row is stored with file_id = the file of its src, as every edge.
//
// Method sets follow the Go rules for *T (the pointer method set, so value
// and pointer receivers both count): declared methods (nested under the
// type, or top-level methods whose receiver names the type in the same
// package), then methods promoted through embedded fields breadth first,
// a shallower name shadowing deeper ones; an embedded interface promotes
// its methods. Interface method sets include embedded interfaces.
// Methods match by name and by a signature fingerprint: parameter and
// result types with qualifiers dropped (*v1.Pod and *Pod match), type
// parameters as wildcards; a truncated or unparsable signature matches by
// name only.
//
// Interfaces are linked only when the match is meaningful (see the
// constants below): empty interfaces, interfaces embedding a type outside
// the repository (their method set is partly unknown), single-method
// interfaces whose method is ubiquitous (String, Error) or that more than
// singleMethodLimit types satisfy, and interfaces with more than
// MaxImplementers implementers are skipped. A compile-time assertion
// (var _ I = (*T)(nil), var _ I = T{}, ...) always links its pair, at
// ConfAsserted, even when the interface is skipped or the method set is
// only partly known.
//
// The rows are a pure function of the nodes and resolved edges of the
// graph: the pass recomputes all of them in memory and writes only the
// difference to the stored rows, so an incremental run converges to the
// same graph as a full build no matter which definitions changed.
// Unresolved rows left behind when an interface was deleted (dst NULL)
// are removed by the same diff. The pass runs when a run had queued work
// (or a previous pass did not complete, see metaImplDirty).

// Confidence of derived implements rows.
const (
	ConfImplicit uint8 = 70 // method sets match
	ConfAsserted uint8 = 85 // a compile-time assertion names the pair
)

// DispatchQual marks method-level implements rows (see above).
const DispatchQual = "~dispatch"

// MaxImplementers bounds the implementers linked to one interface; above
// it the interface is skipped (only asserted pairs are linked).
const MaxImplementers = 5000

// singleMethodLimit: a single-method interface satisfied by more types
// than this is too generic to link implicitly (io.Closer-like shapes).
const singleMethodLimit = 100

// commonSingleMethods name single-method interfaces never linked
// implicitly: nearly every type has them.
var commonSingleMethods = map[string]bool{"String": true, "Error": true}

// metaImplDirty is set while queued changes have not been through the Go
// pass yet, so a run interrupted after resolving still runs it next time.
const metaImplDirty = "go_impl_dirty"

// giType is a Go type definition seen by the pass.
type giType struct {
	id, file int64
	line     uint32
	name     string
	iface    bool
	module   string
	tparams  map[string]bool
	direct   map[int64][]*giMethod // name id -> declared methods / method elems
	embeds   []int64               // embedded types, 0 = unresolved
}

// giMethod is a method declaration or interface method element of owner.
// Its signature fingerprint is parsed on first use: most methods are
// never compared.
type giMethod struct {
	id, file int64
	line     uint32
	sig      string
	owner    *giType
	shape    *goShape
}

func (m *giMethod) fingerprint() goShape {
	if m.shape == nil {
		sh := goMethodShape(m.sig, m.owner.iface, m.owner.tparams)
		m.shape = &sh
	}
	return *m.shape
}

func (m *giMethod) compatible(o *giMethod) bool { return m.fingerprint().compatible(o.fingerprint()) }

// goShape is a method signature fingerprint.
type goShape struct {
	ok              bool
	params, results []string
}

func (a goShape) compatible(b goShape) bool {
	if !a.ok || !b.ok {
		return true
	}
	if len(a.params) != len(b.params) || len(a.results) != len(b.results) {
		return false
	}
	eq := func(x, y []string) bool {
		for i := range x {
			if x[i] != y[i] && x[i] != "?" && y[i] != "?" {
				return false
			}
		}
		return true
	}
	return eq(a.params, b.params) && eq(a.results, b.results)
}

// implKey identifies a derived row.
type implKey struct {
	src, dst int64
	qual     string
}

type implVal struct {
	file int64
	line uint32
	conf uint8
}

// goImpl holds the in-memory model of one pass.
type goImpl struct {
	s       *Session
	types   map[int64]*giType
	sets    map[int64]map[int64][]*giMethod // concrete method sets
	isets   map[int64]map[int64][]*giMethod // interface method sets
	partial map[int64]bool                  // interface set partly unknown
	names   map[int64]string                // method name ids -> names
	byName  map[[2]string][]*giType         // concrete types by (package, name)
	module  func(file int64) string
	skipped int
}

// implQueued reports whether the run has queued work, i.e. whether the
// graph may change and the Go pass must run afterwards.
func implQueued(s *Session) (bool, error) {
	var n int
	err := s.rows(`SELECT (SELECT count(*) FROM (SELECT 1 FROM pending_names LIMIT 1))
		+ (SELECT count(*) FROM (SELECT 1 FROM meta WHERE key >= ?1 AND key < ?2 OR key >= ?3 AND key < ?4
			OR key >= ?5 AND key < ?6 OR key = ?7 LIMIT 1))`, func(r *sql.Rows) error { return r.Scan(&n) },
		PendingFilePrefix, PendingFilePrefix+"\xff", PendingRemovedPrefix, PendingRemovedPrefix+"\xff",
		PendingDependentsPrefix, PendingDependentsPrefix+"\xff", metaImplDirty)
	return n > 0, err
}

// goImplPass derives the Go implements rows and writes the difference to
// the stored ones in the writer's open transaction.
func goImplPass(s *Session, w *store.Writer, stats *Stats) error {
	g := &goImpl{s: s, types: map[int64]*giType{}, sets: map[int64]map[int64][]*giMethod{},
		isets: map[int64]map[int64][]*giMethod{}, partial: map[int64]bool{}, names: map[int64]string{},
		byName: map[[2]string][]*giType{}}
	want := map[implKey]implVal{}
	var goFiles bool
	if err := s.rows(`SELECT 1 FROM files WHERE lang = ? LIMIT 1`, func(*sql.Rows) error { goFiles = true; return nil },
		int(model.LangGo)); err != nil {
		return err
	}
	if goFiles {
		if err := g.load(); err != nil {
			return err
		}
		if err := g.derive(want); err != nil {
			return err
		}
	}
	n, err := writeImplRows(s, w, want, g.targets())
	stats.Implements += n
	stats.ImplementsSkipped += g.skipped
	return err
}

// load reads the Go types, methods and embeddings.
func (g *goImpl) load() error {
	s := g.s
	modules := map[int64]string{} // Go files -> package
	if err := s.rows(`SELECT f.id, coalesce(p.name, '') FROM files f LEFT JOIN packages p ON p.id = f.pkg_id
		WHERE f.lang = ?`, func(r *sql.Rows) error {
		var id int64
		var m string
		if err := r.Scan(&id, &m); err != nil {
			return err
		}
		modules[id] = m
		return nil
	}, int(model.LangGo)); err != nil {
		return err
	}
	g.module = func(file int64) string {
		m := modules[file]
		if m == "" {
			m = s.Module(s.File(file))
			modules[file] = m
		}
		return m
	}

	type raw struct {
		id, file, parent, nameID int64
		name, sig                string
		line                     uint32
	}
	var methods []raw
	if err := s.rows(`SELECT n.id, n.file_id, coalesce(n.parent_id, 0), n.kind, n.name_id, m.name, coalesce(n.sig, ''), n.start_line
		FROM nodes n JOIN names m ON m.id = n.name_id WHERE n.kind IN (?, ?, ?, ?)`, func(r *sql.Rows) error {
		var x raw
		var kind int
		if err := r.Scan(&x.id, &x.file, &x.parent, &kind, &x.nameID, &x.name, &x.sig, &x.line); err != nil {
			return err
		}
		if _, ok := modules[x.file]; !ok {
			return nil
		}
		switch k := model.NodeKind(uint8(kind)); k {
		case model.KindMethod:
			methods = append(methods, x)
		default:
			if k == model.KindType && goTypeAlias(x.sig, x.name) {
				return nil
			}
			g.types[x.id] = &giType{id: x.id, file: x.file, line: x.line, name: x.name,
				iface: k == model.KindInterface, tparams: goTypeParams(x.sig, x.name),
				direct: map[int64][]*giMethod{}}
		}
		return nil
	}, int(model.KindMethod), int(model.KindInterface), int(model.KindStruct), int(model.KindType)); err != nil {
		return err
	}

	// Concrete types by (package, name), for methods declared in another
	// file than their receiver type and for assertions.
	for _, t := range g.types {
		if !t.iface {
			t.module = g.module(t.file)
			k := [2]string{t.module, t.name}
			g.byName[k] = append(g.byName[k], t)
		}
	}
	for _, m := range methods {
		var owners []*giType
		switch {
		case m.parent != 0:
			if t := g.types[m.parent]; t != nil {
				owners = []*giType{t}
			}
		default:
			if recv := goReceiverTypeName(m.sig); recv != "" {
				owners = g.byName[[2]string{g.module(m.file), recv}]
			}
		}
		for _, t := range owners {
			gm := &giMethod{id: m.id, file: m.file, line: m.line, sig: m.sig, owner: t}
			t.direct[m.nameID] = append(t.direct[m.nameID], gm)
		}
		if len(owners) > 0 {
			g.names[m.nameID] = m.name
		}
	}

	ids := make([]int64, 0, len(g.types))
	for id := range g.types {
		ids = append(ids, id)
	}
	return s.rows(`SELECT e.src, coalesce(e.dst, 0) FROM json_each(?) j JOIN edges e ON e.src = j.value AND e.kind = ?`,
		func(r *sql.Rows) error {
			var src, dst int64
			if err := r.Scan(&src, &dst); err != nil {
				return err
			}
			if t := g.types[src]; t != nil {
				t.embeds = append(t.embeds, dst)
			}
			return nil
		}, jsonIDs(ids), int(model.EdgeEmbeds))
}

// jsonIDs renders ids as a JSON array for json_each.
func jsonIDs(ids []int64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	b.WriteByte(']')
	return b.String()
}

// targets returns the interfaces and interface method elements.
func (g *goImpl) targets() []int64 {
	var out []int64
	for _, t := range g.types {
		if !t.iface {
			continue
		}
		out = append(out, t.id)
		for _, ms := range t.direct {
			for _, m := range ms {
				out = append(out, m.id)
			}
		}
	}
	return out
}

// ifaceSet returns the method set of interface t (with embedded
// interfaces) and whether part of it is unknown.
func (g *goImpl) ifaceSet(t *giType) (map[int64][]*giMethod, bool) {
	if set, ok := g.isets[t.id]; ok {
		return set, g.partial[t.id]
	}
	g.isets[t.id] = nil // cycle guard
	set := map[int64][]*giMethod{}
	partial := false
	for name, ms := range t.direct {
		set[name] = append(set[name], ms...)
	}
	for _, d := range t.embeds {
		et := g.types[d]
		if et == nil || !et.iface {
			partial = true
			continue
		}
		es, p := g.ifaceSet(et)
		partial = partial || p
		for name, ms := range es {
			for _, m := range ms {
				if !slices.Contains(set[name], m) {
					set[name] = append(set[name], m)
				}
			}
		}
	}
	g.isets[t.id], g.partial[t.id] = set, partial
	return set, partial
}

// concreteSet returns the method set of *t: declared methods, then
// methods promoted through embedded fields, a name found at a shallower
// depth shadowing deeper ones. Providers of one name at the same depth
// are all kept (build-tag variants).
func (g *goImpl) concreteSet(t *giType) map[int64][]*giMethod {
	if set, ok := g.sets[t.id]; ok {
		return set
	}
	set := map[int64][]*giMethod{}
	seen := map[int64]bool{t.id: true}
	level := []*giType{t}
	for depth := 0; depth < maxDepth && len(level) > 0; depth++ {
		found := map[int64][]*giMethod{}
		var next []*giType
		for _, x := range level {
			ms := x.direct
			if x.iface {
				ms, _ = g.ifaceSet(x)
			}
			for name, list := range ms {
				if _, shadowed := set[name]; shadowed {
					continue
				}
				for _, m := range list {
					if !slices.Contains(found[name], m) {
						found[name] = append(found[name], m)
					}
				}
			}
			if x.iface {
				continue
			}
			for _, d := range x.embeds {
				if et := g.types[d]; et != nil && !seen[d] {
					seen[d] = true
					next = append(next, et)
				}
			}
		}
		for name, list := range found {
			set[name] = list
		}
		level = next
	}
	g.sets[t.id] = set
	return set
}

// satisfies reports whether method set set provides every method of iset.
func satisfies(set, iset map[int64][]*giMethod) bool {
	for name, ims := range iset {
		if !anyCompatible(set[name], ims) {
			return false
		}
	}
	return true
}

func anyCompatible(cms, ims []*giMethod) bool {
	for _, c := range cms {
		for _, i := range ims {
			if c.compatible(i) {
				return true
			}
		}
	}
	return false
}

// derive computes the wanted rows into want.
func (g *goImpl) derive(want map[implKey]implVal) error {
	var concrete, ifaces []*giType
	for _, t := range g.types {
		if t.iface {
			ifaces = append(ifaces, t)
		} else {
			concrete = append(concrete, t)
		}
	}
	// Concrete types by the method names their sets provide.
	providers := map[int64][]*giType{}
	for _, t := range concrete {
		for name := range g.concreteSet(t) {
			providers[name] = append(providers[name], t)
		}
	}
	link := func(t, i *giType, conf uint8) {
		addImplRow(want, implKey{t.id, i.id, ""}, implVal{t.file, t.line, conf})
		iset, _ := g.ifaceSet(i)
		set := g.concreteSet(t)
		for name, ims := range iset {
			for _, cm := range set[name] {
				for _, im := range ims {
					if cm.id != im.id && cm.compatible(im) {
						addImplRow(want, implKey{cm.id, im.id, DispatchQual}, implVal{cm.file, cm.line, conf})
					}
				}
			}
		}
	}
	for _, i := range ifaces {
		iset, partial := g.ifaceSet(i)
		if len(iset) == 0 || partial {
			continue
		}
		var rare []*giType
		first := true
		for name := range iset {
			if c := providers[name]; first || len(c) < len(rare) {
				rare, first = c, false
			}
		}
		var matched []*giType
		for _, t := range rare {
			if satisfies(g.concreteSet(t), iset) {
				matched = append(matched, t)
			}
		}
		if len(iset) == 1 {
			var only int64
			for name := range iset {
				only = name
			}
			if commonSingleMethods[g.names[only]] || len(matched) > singleMethodLimit {
				g.skipped++
				continue
			}
		}
		if len(matched) > MaxImplementers {
			g.skipped++
			continue
		}
		for _, t := range matched {
			link(t, i, ConfImplicit)
		}
	}
	pairs, err := g.assertions()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		link(p[0], p[1], ConfAsserted)
	}
	return nil
}

func addImplRow(want map[implKey]implVal, k implKey, v implVal) {
	if old, ok := want[k]; ok && old.conf >= v.conf {
		return
	}
	want[k] = v
}

// assertions returns the (concrete type, interface) pairs named by
// package-level blank variables: var _ I = (*T)(nil), &T{}, T{}, new(T).
// The variable's resolved type references give I and, for literals and
// qualified names, T; in (*T)(nil) an unqualified T is an expression
// operand that extraction does not record, so it is looked up by name in
// the variable's package (from its sig).
func (g *goImpl) assertions() ([][2]*giType, error) {
	blank := g.s.nameID("_")
	if blank == 0 {
		return nil, nil
	}
	type assertion struct {
		file int64
		sig  string
		refs []int64
	}
	byVar := map[int64]*assertion{}
	err := g.s.rows(`SELECT v.id, v.file_id, coalesce(v.sig, ''), e.dst FROM nodes v
		JOIN edges e ON e.src = v.id AND e.kind = ? JOIN nodes d ON d.id = e.dst AND d.kind IN (?, ?, ?)
		WHERE v.name_id = ? AND v.kind = ?`, func(r *sql.Rows) error {
		var v, file, dst int64
		var sig string
		if err := r.Scan(&v, &file, &sig, &dst); err != nil {
			return err
		}
		a := byVar[v]
		if a == nil {
			a = &assertion{file: file, sig: sig}
			byVar[v] = a
		}
		if dst != 0 {
			a.refs = append(a.refs, dst)
		}
		return nil
	}, int(model.EdgeReferences), int(model.KindInterface), int(model.KindStruct), int(model.KindType),
		blank, int(model.KindVariable))
	if err != nil {
		return nil, err
	}
	var out [][2]*giType
	base := func(t string) string { return t[strings.LastIndexByte(t, '.')+1:] }
	for _, a := range byVar {
		iface, typ, ok := goAssertion(a.sig)
		if !ok {
			continue
		}
		// Only the references naming the two types count: the expression
		// may mention others (&impl{opts: Options{}}).
		var ts, is []*giType
		for _, d := range a.refs {
			switch x := g.types[d]; {
			case x == nil:
			case x.iface && x.name == base(iface):
				is = append(is, x)
			case !x.iface && x.name == base(typ):
				ts = append(ts, x)
			}
		}
		if len(is) == 0 {
			continue
		}
		if len(ts) == 0 && !strings.Contains(typ, ".") {
			ts = g.byName[[2]string{g.module(a.file), typ}]
		}
		for _, t := range ts {
			for _, i := range is {
				out = append(out, [2]*giType{t, i})
			}
		}
	}
	return out, nil
}

// goAssertion parses an interface assertion sig "var _ I = <expr>" and
// returns I and the asserted type: (*T)(nil), &T{...}, T{...}, new(T)
// or T(nil); type arguments are dropped.
func goAssertion(sig string) (iface, typ string, ok bool) {
	if strings.Contains(sig, "…") {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(sig), "var _ ")
	if !ok {
		return "", "", false
	}
	iface, expr, ok := strings.Cut(rest, "=")
	if !ok {
		return "", "", false
	}
	iface, expr = strings.TrimSpace(iface), strings.TrimSpace(expr)
	if iface == "" {
		return "", "", false
	}
	expr = strings.TrimLeft(expr, "&* ")
	switch {
	case strings.HasPrefix(expr, "new("):
		end := matchClose(expr, 3)
		if end < 0 {
			return "", "", false
		}
		expr = expr[4:end]
	case strings.HasPrefix(expr, "("):
		end := matchClose(expr, 0)
		if end < 0 {
			return "", "", false
		}
		expr = expr[1:end]
	default:
		if i := strings.IndexAny(expr, "{("); i >= 0 {
			expr = expr[:i]
		}
	}
	strip := func(t string) string {
		t = strings.TrimLeft(strings.TrimSpace(t), "*")
		if i := strings.IndexByte(t, '['); i >= 0 {
			t = t[:i]
		}
		return strings.TrimSpace(t)
	}
	iface, typ = strip(iface), strip(expr)
	return iface, typ, iface != "" && typ != ""
}

// writeImplRows makes the stored derived rows equal to want and returns
// the number of rows changed. dsts are every node a derived row may point
// to (the interfaces and their method elements).
func writeImplRows(s *Session, w *store.Writer, want map[implKey]implVal, dsts []int64) (int, error) {
	type stored struct {
		id int64
		implVal
	}
	have := map[implKey]stored{}
	var drop []int64
	scan := func(r *sql.Rows) error {
		var (
			id, file, src, dst int64
			qual               string
			line               uint32
			conf, col          int
		)
		if err := r.Scan(&id, &file, &src, &dst, &qual, &line, &col, &conf); err != nil {
			return err
		}
		k := implKey{src, dst, qual}
		if _, dup := have[k]; dup || dst == 0 || col != 0 {
			drop = append(drop, id)
			return nil
		}
		have[k] = stored{id, implVal{file, line, uint8(conf)}}
		return nil
	}
	const cols = `e.id, e.file_id, e.src, coalesce(e.dst, 0), coalesce(e.qual, ''), e.line, e.col, e.conf`
	// Rows into interfaces and their methods (edges by dst), plus rows
	// whose interface was deleted (dst NULL, found through the partial
	// index on unresolved edges).
	if len(dsts) > 0 {
		if err := s.rows(`SELECT `+cols+` FROM json_each(?) j JOIN edges e ON e.dst = j.value AND e.kind = ?
			WHERE e.name_id IS NULL`, scan, jsonIDs(dsts), int(model.EdgeImplements)); err != nil {
			return 0, err
		}
	}
	if err := s.rows(`SELECT `+cols+` FROM edges e WHERE e.name_id IS NULL AND e.dst IS NULL AND e.kind = ?`, scan,
		int(model.EdgeImplements)); err != nil {
		return 0, err
	}
	changed := 0
	for _, id := range drop {
		if _, err := w.Exec(`DELETE FROM edges WHERE id = ?`, id); err != nil {
			return changed, err
		}
		changed++
	}
	for k, h := range have {
		if _, ok := want[k]; !ok {
			if _, err := w.Exec(`DELETE FROM edges WHERE id = ?`, h.id); err != nil {
				return changed, err
			}
			changed++
		}
	}
	keys := make([]implKey, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b implKey) int {
		if c := cmpInt64(a.src, b.src); c != 0 {
			return c
		}
		if c := cmpInt64(a.dst, b.dst); c != 0 {
			return c
		}
		return strings.Compare(a.qual, b.qual)
	})
	for _, k := range keys {
		v := want[k]
		if h, ok := have[k]; ok {
			if h.implVal == v {
				continue
			}
			if _, err := w.Exec(`UPDATE edges SET file_id = ?, line = ?, conf = ? WHERE id = ?`,
				v.file, v.line, int(v.conf), h.id); err != nil {
				return changed, err
			}
		} else if _, err := w.Exec(`INSERT INTO edges(file_id, src, dst, kind, name_id, qual, line, col, conf)
			VALUES(?, ?, ?, ?, NULL, ?, ?, 0, ?)`, v.file, k.src, k.dst, int(model.EdgeImplements),
			nullStr(k.qual), v.line, int(v.conf)); err != nil {
			return changed, fmt.Errorf("insert implements: %w", err)
		}
		changed++
	}
	return changed, nil
}

// goTypeAlias reports whether a type sig declares an alias (type A = B).
func goTypeAlias(sig, name string) bool {
	rest := goAfterTypeName(sig, name)
	if strings.HasPrefix(rest, "[") {
		if end := matchClose(rest, 0); end >= 0 {
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	return strings.HasPrefix(rest, "=")
}

// goAfterTypeName returns what follows the type name in "type Name...".
func goAfterTypeName(sig, name string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(sig), "type")
	if !ok {
		return ""
	}
	rest, ok = strings.CutPrefix(strings.TrimSpace(rest), name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(rest)
}

// goTypeParams returns the type parameter names of a type sig
// ("type Box[K comparable, V any] struct" -> K, V), or nil.
func goTypeParams(sig, name string) map[string]bool {
	rest := goAfterTypeName(sig, name)
	if !strings.HasPrefix(rest, "[") {
		return nil
	}
	end := matchClose(rest, 0)
	if end < 0 {
		return nil
	}
	return goParamNames(rest[1:end])
}

// goParamNames returns the first identifier of every comma-separated
// part of a type parameter list ("K, V any" -> K, V).
func goParamNames(list string) map[string]bool {
	out := map[string]bool{}
	for _, p := range splitTop(list, ',') {
		f := strings.Fields(p)
		if len(f) > 0 && isGoIdent(f[0]) {
			out[f[0]] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// goMethodShape fingerprints a method sig: "func (r *T) M(a int) (X, error)"
// or, for an interface method element, "M(a int) X". tparams are the type
// parameters of the owning type (wildcards); a generic receiver's own
// parameter names are added. A truncated sig, or one with comments, yields
// an unknown shape.
func goMethodShape(sig string, elem bool, tparams map[string]bool) goShape {
	if strings.Contains(sig, "…") || strings.Contains(sig, "//") || strings.Contains(sig, "/*") {
		return goShape{}
	}
	s := strings.Join(strings.Fields(sig), " ")
	if !elem {
		rest, ok := strings.CutPrefix(s, "func")
		if !ok {
			return goShape{}
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "(") {
			return goShape{}
		}
		end := matchClose(rest, 0)
		if end < 0 {
			return goShape{}
		}
		recv := rest[1:end]
		if i := strings.IndexByte(recv, '['); i >= 0 {
			if j := matchClose(recv, i); j > i {
				tp := goParamNames(recv[i+1 : j])
				if len(tp) > 0 {
					merged := map[string]bool{}
					for k := range tparams {
						merged[k] = true
					}
					for k := range tp {
						merged[k] = true
					}
					tparams = merged
				}
			}
		}
		s = strings.TrimSpace(rest[end+1:])
	}
	i := 0
	for i < len(s) && (s[i] == '_' || s[i] >= 0x80 || 'a' <= s[i] && s[i] <= 'z' || 'A' <= s[i] && s[i] <= 'Z' || '0' <= s[i] && s[i] <= '9') {
		i++
	}
	s = strings.TrimSpace(s[i:])
	if !strings.HasPrefix(s, "(") {
		return goShape{}
	}
	end := matchClose(s, 0)
	if end < 0 {
		return goShape{}
	}
	params, ok := goParamTypes(s[1:end], tparams)
	if !ok {
		return goShape{}
	}
	rest := strings.TrimSpace(s[end+1:])
	var results []string
	switch {
	case rest == "":
	case strings.HasPrefix(rest, "("):
		e := matchClose(rest, 0)
		if e != len(rest)-1 {
			return goShape{}
		}
		if results, ok = goParamTypes(rest[1:e], tparams); !ok {
			return goShape{}
		}
	default:
		results = []string{goNormType(rest, tparams)}
	}
	return goShape{ok: true, params: params, results: results}
}

// goTypeKeywords start unnamed parameter types that contain a space.
var goTypeKeywords = map[string]bool{"chan": true, "func": true, "map": true, "struct": true, "interface": true}

// goParamTypes returns the normalized types of a parameter or result list
// (named "a, b int, c string" or unnamed "int, string").
func goParamTypes(list string, tparams map[string]bool) ([]string, bool) {
	parts := splitTop(list, ',')
	named := func(p string) (string, bool) {
		i := strings.IndexByte(p, ' ')
		if i < 0 || !isGoIdent(p[:i]) || goTypeKeywords[p[:i]] {
			return "", false
		}
		return p[i+1:], true
	}
	anyNamed := false
	for _, p := range parts {
		if _, ok := named(p); ok {
			anyNamed = true
			break
		}
	}
	out := make([]string, 0, len(parts))
	pending := 0
	for _, p := range parts {
		if !anyNamed {
			out = append(out, goNormType(p, tparams))
			continue
		}
		typ, ok := named(p)
		if !ok {
			if !isGoIdent(p) {
				return nil, false
			}
			pending++
			continue
		}
		n := goNormType(typ, tparams)
		for ; pending >= 0; pending-- {
			out = append(out, n)
		}
		pending = 0
	}
	return out, pending == 0
}

// goNormType normalizes a type for fingerprints: qualifiers and type
// arguments dropped, composite types reduced to their kind, type
// parameters as the wildcard "?".
func goNormType(t string, tparams map[string]bool) string {
	var pre strings.Builder
	for {
		t = strings.TrimSpace(t)
		switch {
		case strings.HasPrefix(t, "*"):
			pre.WriteByte('*')
			t = t[1:]
			continue
		case strings.HasPrefix(t, "..."):
			pre.WriteString("...")
			t = t[3:]
			continue
		case strings.HasPrefix(t, "[]"):
			pre.WriteString("[]")
			t = t[2:]
			continue
		case strings.HasPrefix(t, "["):
			end := matchClose(t, 0)
			if end < 0 {
				return "?"
			}
			pre.WriteString("[]")
			t = t[end+1:]
			continue
		case strings.HasPrefix(t, "(") && matchClose(t, 0) == len(t)-1:
			t = t[1 : len(t)-1]
			continue
		}
		break
	}
	switch {
	case strings.HasPrefix(t, "<-chan"), strings.HasPrefix(t, "chan"):
		t = "chan"
	case strings.HasPrefix(t, "map["):
		t = "map"
	case strings.HasPrefix(t, "func"):
		t = "func"
	case strings.HasPrefix(t, "struct"):
		t = "struct"
	case strings.HasPrefix(t, "interface"):
		if strings.ReplaceAll(t, " ", "") == "interface{}" {
			t = "any"
		} else {
			t = "interface"
		}
	default:
		if i := strings.IndexByte(t, '['); i >= 0 {
			t = t[:i]
		}
		if i := strings.LastIndexByte(t, '.'); i >= 0 {
			t = t[i+1:]
		}
		if tparams[t] {
			return "?"
		}
	}
	return pre.String() + t
}

// splitTop splits s on sep outside brackets, trimming the parts and
// dropping empty ones (a trailing comma).
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case sep:
			if depth == 0 {
				add(s[start:i])
				start = i + 1
			}
		}
	}
	add(s[start:])
	return out
}
