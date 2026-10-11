package resolve

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"slices"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
)

// File is a files row as seen by the resolver.
type File struct {
	ID   int64
	Path string
	Lang model.Lang
	Size int64
	// pkgName is packages.name of files.pkg_id, if the indexer set one.
	pkgName string
}

// Def is a definition (nodes row).
type Def struct {
	ID        int64
	FileID    int64
	ParentID  int64 // 0 = top level
	NameID    int64
	Name      string
	Kind      model.NodeKind
	QName     string
	Sig       string
	Flags     model.DefFlags
	StartLine uint32
	StartByte uint32
	EndByte   uint32
}

// Import is an imports row of one file.
type Import struct {
	Spec  string
	Alias string
	Names []string // imported member names; "*" marks a wildcard import
	Line  uint32
}

// Wildcard reports whether the import brings all members of its target into
// scope (Java a.b.*, C# using, C++ using namespace, Python star import).
func (im Import) Wildcard() bool { return slices.Contains(im.Names, "*") }

// InheritRef is an inheritance edge (extends/implements/embeds) declared by
// a type, identified by its target name rather than its resolved dst so
// that resolution never depends on processing order.
type InheritRef struct {
	Kind model.EdgeKind
	Name string
	Qual string
	Line uint32
	Col  uint32
}

// Site is one reference being resolved: an edge group sharing
// (file, src, kind, name, qual, line, col).
type Site struct {
	File *File
	Src  *Def // the enclosing definition the edge starts from
	Kind model.EdgeKind
	Name string
	Qual string
	Line uint32 // 1-based line of the referenced name
	Col  uint32 // 0-based byte column of the referenced name
}

// lookupSpec finds the language spec providing ModuleOf/ResolveImport;
// tests replace it to stub languages owned by other units.
var lookupSpec = parse.Lookup

// querier reads through the resolver's connection.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// Closer is implemented by per-language state stored with SetState that
// holds native resources.
type Closer interface{ Close() }

// Session holds the caches of one resolution run. Definitions, files and
// imports do not change while resolving (only edges do), so they are cached
// for the whole run; parsed trees are dropped after each batch, sources once
// they exceed maxSourceBytes.
// A Session is not safe for concurrent use.
type Session struct {
	ctx  context.Context
	q    querier
	rc   model.RepoContext
	opts Options

	files    map[int64]*File
	modules  map[int64]string
	defs     map[int64]*Def
	byName   map[int64][]*Def
	byModule map[int64]map[string][]*Def // name id -> module -> defs
	nameIDs  map[string]int64            // 0 = name never interned
	names    map[int64]string
	children map[int64][]*Def
	inherits map[int64][]InheritRef
	imports  map[int64][]Import
	targets  map[[2]string]model.ImportTarget
	counts   map[int64]int // distinct references per name id

	sources     map[int64][]byte // nil value = unavailable
	sourceBytes int
	state       map[model.Lang]any
	batch       map[model.Lang]any // per-batch language state

	err error // first loader error, see fail
}

// maxSourceBytes bounds the source text kept across batches: files are
// re-read by every batch that resolves references in them otherwise.
const maxSourceBytes = 64 << 20

func newSession(ctx context.Context, q querier, rc model.RepoContext, opts Options) *Session {
	return &Session{
		ctx: ctx, q: q, rc: rc, opts: opts,
		files:    map[int64]*File{},
		modules:  map[int64]string{},
		defs:     map[int64]*Def{},
		byName:   map[int64][]*Def{},
		byModule: map[int64]map[string][]*Def{},
		nameIDs:  map[string]int64{},
		names:    map[int64]string{},
		children: map[int64][]*Def{},
		inherits: map[int64][]InheritRef{},
		imports:  map[int64][]Import{},
		targets:  map[[2]string]model.ImportTarget{},
		counts:   map[int64]int{},
		sources:  map[int64][]byte{},
		state:    map[model.Lang]any{},
		batch:    map[model.Lang]any{},
	}
}

// endBatch releases per-batch caches (sources and per-batch state).
func (s *Session) endBatch() {
	closeAll(s.batch)
	if s.sourceBytes > maxSourceBytes {
		clear(s.sources)
		s.sourceBytes = 0
	}
	// Bound memory on huge repositories: definition caches are rebuilt
	// lazily, so dropping them is always safe.
	if len(s.defs) > s.opts.cacheLimit() {
		clear(s.defs)
		clear(s.byName)
		clear(s.byModule)
		clear(s.children)
		clear(s.inherits)
		clear(s.counts)
	}
}

// close releases everything, including native per-language state.
func (s *Session) close() {
	closeAll(s.batch)
	closeAll(s.state)
}

func closeAll(m map[model.Lang]any) {
	for l, v := range m {
		if c, ok := v.(Closer); ok {
			c.Close()
		}
		delete(m, l)
	}
}

// State returns language state stored with SetState for the whole run.
func (s *Session) State(lang model.Lang) any { return s.state[lang] }

// SetState stores language state for the whole run; a Closer is closed at
// the end of the run.
func (s *Session) SetState(lang model.Lang, v any) { s.state[lang] = v }

// BatchState returns language state stored with SetBatchState.
func (s *Session) BatchState(lang model.Lang) any { return s.batch[lang] }

// SetBatchState stores language state (e.g. parsed trees) until the end of
// the current batch; a Closer is closed then.
func (s *Session) SetBatchState(lang model.Lang, v any) { s.batch[lang] = v }

// Context returns the run's context.
func (s *Session) Context() context.Context { return s.ctx }

// ScopedRepoContext is implemented by a model.RepoContext that attributes
// the answers it gives to a file (internal/index.RepoCtx): For(path)
// returns the view to use while resolving on behalf of that file, so a
// later change of an answer only invalidates the files that relied on it.
type ScopedRepoContext interface {
	For(path string) model.RepoContext
}

// rcFor returns the repository view for work done on behalf of path.
func (s *Session) rcFor(path string) model.RepoContext {
	if sc, ok := s.rc.(ScopedRepoContext); ok {
		return sc.For(path)
	}
	return s.rc
}

// RepoContext returns the repository view used for module identity and
// import resolution.
func (s *Session) RepoContext() model.RepoContext { return s.rc }

// rows runs a query and calls scan for every row, closing the cursor before
// returning so writes on the same connection never interleave with it.
func (s *Session) rows(q string, scan func(*sql.Rows) error, args ...any) error {
	rs, err := s.q.Query(q, args...)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		if err := scan(rs); err != nil {
			return err
		}
	}
	return rs.Err()
}

// fail records the first loader error; accessors return empty results and
// the run reports the error at the next batch boundary.
func (s *Session) fail(err error) {
	if err != nil && s.err == nil {
		s.err = err
	}
}

// File returns the files row id, or nil.
func (s *Session) File(id int64) *File {
	if f, ok := s.files[id]; ok {
		return f
	}
	var f *File
	err := s.rows(`SELECT f.path, f.lang, f.size, coalesce(p.name, '')
		FROM files f LEFT JOIN packages p ON p.id = f.pkg_id WHERE f.id = ?`, func(r *sql.Rows) error {
		var lang int
		f = &File{ID: id}
		if err := r.Scan(&f.Path, &lang, &f.Size, &f.pkgName); err != nil {
			return err
		}
		f.Lang = model.Lang(uint8(lang))
		return nil
	}, id)
	s.fail(err)
	s.files[id] = f
	return f
}

// Module returns the module identity of f (Go import path, Java package,
// file path for TS/JS, ...): the indexer's packages row when set, else the
// language spec's ModuleOf. "" means unknown.
func (s *Session) Module(f *File) string {
	if f == nil {
		return ""
	}
	if m, ok := s.modules[f.ID]; ok {
		return m
	}
	m := f.pkgName
	if m == "" {
		if spec, ok := lookupSpec(f.Lang); ok {
			m = spec.ModuleOf(f.Path, nil, s.rcFor(f.Path))
		}
	}
	s.modules[f.ID] = m
	return m
}

const defCols = `id, file_id, coalesce(parent_id, 0), kind, name_id, qname, coalesce(sig, ''), flags,
	start_line, start_byte, end_byte`

func (s *Session) scanDefs(where string, args ...any) []*Def {
	var out []*Def
	err := s.rows(`SELECT `+defCols+` FROM nodes WHERE `+where+` ORDER BY id`, func(r *sql.Rows) error {
		d := &Def{}
		var kind, flags int
		if err := r.Scan(&d.ID, &d.FileID, &d.ParentID, &kind, &d.NameID, &d.QName, &d.Sig, &flags,
			&d.StartLine, &d.StartByte, &d.EndByte); err != nil {
			return err
		}
		d.Kind = model.NodeKind(uint8(kind))
		d.Flags = model.DefFlags(uint16(flags))
		out = append(out, d)
		return nil
	}, args...)
	s.fail(err)
	for i, d := range out {
		if old, ok := s.defs[d.ID]; ok {
			out[i] = old
			continue
		}
		d.Name = s.name(d.NameID)
		s.defs[d.ID] = d
	}
	return out
}

// Def returns the definition id, or nil.
func (s *Session) Def(id int64) *Def {
	if id == 0 {
		return nil
	}
	if d, ok := s.defs[id]; ok {
		return d
	}
	if ds := s.scanDefs(`id = ?`, id); len(ds) == 1 {
		return ds[0]
	}
	return nil
}

// Parent returns d's lexical parent, or nil.
func (s *Session) Parent(d *Def) *Def {
	if d == nil {
		return nil
	}
	return s.Def(d.ParentID)
}

// name returns the interned string for id.
func (s *Session) name(id int64) string {
	if n, ok := s.names[id]; ok {
		return n
	}
	var n string
	s.fail(s.rows(`SELECT name FROM names WHERE id = ?`, func(r *sql.Rows) error { return r.Scan(&n) }, id))
	s.names[id] = n
	return n
}

// nameID returns the id of an interned name, or 0.
func (s *Session) nameID(name string) int64 {
	if id, ok := s.nameIDs[name]; ok {
		return id
	}
	var id int64
	err := s.rows(`SELECT id FROM names WHERE name = ?`, func(r *sql.Rows) error { return r.Scan(&id) }, name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.fail(err)
	}
	s.nameIDs[name] = id
	if id != 0 {
		s.names[id] = name
	}
	return id
}

// DefsNamed returns every definition called name, in id order.
func (s *Session) DefsNamed(name string) []*Def {
	id := s.nameID(name)
	if id == 0 {
		return nil
	}
	return s.defsByNameID(id)
}

func (s *Session) defsByNameID(id int64) []*Def {
	if ds, ok := s.byName[id]; ok {
		return ds
	}
	ds := s.scanDefs(`name_id = ?`, id)
	s.byName[id] = ds
	return ds
}

// defsInModule returns the definitions called name whose file belongs to
// module (see Module), in DefsNamed order.
func (s *Session) defsInModule(name, module string) []*Def {
	id := s.nameID(name)
	if id == 0 {
		return nil
	}
	byMod, ok := s.byModule[id]
	if !ok {
		byMod = map[string][]*Def{}
		for _, d := range s.defsByNameID(id) {
			m := s.Module(s.File(d.FileID))
			byMod[m] = append(byMod[m], d)
		}
		s.byModule[id] = byMod
	}
	return byMod[module]
}

// Children returns the definitions lexically nested directly in d.
func (s *Session) Children(d *Def) []*Def {
	if d == nil {
		return nil
	}
	if cs, ok := s.children[d.ID]; ok {
		return cs
	}
	cs := s.scanDefs(`parent_id = ?`, d.ID)
	s.children[d.ID] = cs
	return cs
}

// Inherits returns the inheritance references declared by type d, sorted
// by position.
func (s *Session) Inherits(d *Def) []InheritRef {
	if d == nil {
		return nil
	}
	if in, ok := s.inherits[d.ID]; ok {
		return in
	}
	var out []InheritRef
	err := s.rows(`SELECT DISTINCT e.kind, m.name, coalesce(e.qual, ''), e.line, e.col
		FROM edges e JOIN names m ON m.id = e.name_id
		WHERE e.src = ? AND e.kind IN (?, ?, ?)
		ORDER BY e.line, e.col, e.kind`, func(r *sql.Rows) error {
		var in InheritRef
		var kind int
		if err := r.Scan(&kind, &in.Name, &in.Qual, &in.Line, &in.Col); err != nil {
			return err
		}
		in.Kind = model.EdgeKind(uint8(kind))
		out = append(out, in)
		return nil
	}, d.ID, int(model.EdgeExtends), int(model.EdgeImplements), int(model.EdgeEmbeds))
	s.fail(err)
	s.inherits[d.ID] = out
	return out
}

// Imports returns the imports of f in source order.
func (s *Session) Imports(f *File) []Import {
	if f == nil {
		return nil
	}
	if ims, ok := s.imports[f.ID]; ok {
		return ims
	}
	var out []Import
	err := s.rows(`SELECT spec, coalesce(alias, ''), coalesce(names, ''), line FROM imports
		WHERE file_id = ? ORDER BY line, spec`, func(r *sql.Rows) error {
		var im Import
		var names string
		if err := r.Scan(&im.Spec, &im.Alias, &names, &im.Line); err != nil {
			return err
		}
		if names != "" {
			im.Names = strings.Split(names, ",")
		}
		out = append(out, im)
		return nil
	}, f.ID)
	s.fail(err)
	s.imports[f.ID] = out
	return out
}

// ImportTarget resolves an import spec written in f through the language
// spec's ResolveImport (cached).
func (s *Session) ImportTarget(f *File, spec string) model.ImportTarget {
	key := [2]string{f.Path, spec}
	if t, ok := s.targets[key]; ok {
		return t
	}
	t := model.ImportTarget{Kind: model.TargetUnresolved}
	if ls, ok := lookupSpec(f.Lang); ok {
		t = ls.ResolveImport(spec, f.Path, s.rcFor(f.Path))
	}
	s.targets[key] = t
	return t
}

// Binding finds the import of f that binds the local identifier root
// (through ImportBindings or an alias) and returns its target.
func (s *Session) Binding(f *File, root string) (model.ImportTarget, bool) {
	if f == nil || root == "" {
		return model.ImportTarget{}, false
	}
	lr := For(f.Lang)
	for _, im := range s.Imports(f) {
		if slices.Contains(lr.ImportBindings(im), root) {
			return s.ImportTarget(f, im.Spec), true
		}
	}
	return model.ImportTarget{}, false
}

// NamedImport finds an import of f that brings the member name into scope
// by name (Python from-import, JS named import, Java single-type import).
func (s *Session) NamedImport(f *File, name string) (model.ImportTarget, bool) {
	if f == nil {
		return model.ImportTarget{}, false
	}
	for _, im := range s.Imports(f) {
		if slices.Contains(im.Names, name) {
			return s.ImportTarget(f, im.Spec), true
		}
	}
	return model.ImportTarget{}, false
}

// InTarget reports whether d is defined inside an import target.
func (s *Session) InTarget(d *Def, t model.ImportTarget) bool {
	f := s.File(d.FileID)
	if f == nil {
		return false
	}
	switch t.Kind {
	case model.TargetFile:
		return f.Path == t.Path
	case model.TargetModule:
		if m := s.Module(f); m != "" && t.Module != "" {
			return m == t.Module
		}
		return t.Path != "" && model.CleanRel(path.Dir(f.Path)) == t.Path
	}
	return false
}

// SameModule reports whether two files belong to the same known module.
func (s *Session) SameModule(a, b *File) bool {
	if a == nil || b == nil {
		return false
	}
	if a.ID == b.ID {
		return true
	}
	ma := s.Module(a)
	return ma != "" && ma == s.Module(b)
}

// Source returns the current content of f, or nil when it cannot be read
// or no longer matches the indexed size (the file changed after indexing).
func (s *Session) Source(f *File) []byte {
	if f == nil {
		return nil
	}
	if src, ok := s.sources[f.ID]; ok {
		return src
	}
	var src []byte
	var err error
	switch {
	case s.opts.Source != nil:
		src, err = s.opts.Source(f.Path)
	case s.rc != nil:
		src, err = s.rc.ReadFile(f.Path)
	default:
		err = errors.New("no source")
	}
	if err != nil || (f.Size > 0 && int64(len(src)) != f.Size) {
		src = nil
	}
	s.sources[f.ID] = src
	s.sourceBytes += len(src)
	return src
}

// EnclosingType returns the innermost type definition containing d
// (d itself if it is a type), or nil.
func (s *Session) EnclosingType(d *Def) *Def {
	for i := 0; d != nil && i < maxDepth; i++ {
		if d.Kind.IsType() {
			return d
		}
		d = s.Parent(d)
	}
	return nil
}

// refCount returns the number of distinct references named id, counting a
// T5 fan-out once. Used for the common-name cutoff.
func (s *Session) refCount(id int64) int {
	if n, ok := s.counts[id]; ok {
		return n
	}
	var low, high int
	s.fail(s.rows(`SELECT count(*) FROM (SELECT DISTINCT file_id, src, kind, line, col FROM edges
		WHERE name_id = ?1 AND (dst IS NULL OR conf < 80))`, func(r *sql.Rows) error { return r.Scan(&low) }, id))
	s.fail(s.rows(`SELECT count(*) FROM nodes n JOIN edges e ON e.dst = n.id
		WHERE n.name_id = ?1 AND e.name_id = ?1 AND e.conf >= 80`, func(r *sql.Rows) error { return r.Scan(&high) }, id))
	s.counts[id] = low + high
	return low + high
}

// Common reports whether name is too common for global guessing (T4/T5):
// more than Options.CommonNameLimit definitions or references.
func (s *Session) Common(name string) bool {
	id := s.nameID(name)
	if id == 0 {
		return false
	}
	limit := s.opts.commonLimit()
	return len(s.defsByNameID(id)) > limit || s.refCount(id) > limit
}
