package index

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
	"github.com/pragmataW/graphmaster/internal/store"
)

// QNamer may be implemented by a model.LanguageSpec to control how a
// definition's qualified name is built from its module identity
// (LanguageSpec.ModuleOf) and container chain (Facts.QNameParts).
type QNamer interface {
	QName(module string, parts []string) string
}

// QName returns the qualified name stored in nodes.qname. Without a QNamer
// the module and the container chain are joined with the language's member
// separator: "::" for C++ and Rust, "." otherwise, e.g.
// "example.com/app/store.Memory.Get" or "shop.models.Item.total".
func QName(lang model.Lang, module string, parts []string) string {
	if spec, ok := parse.Lookup(lang); ok {
		if q, ok := spec.(QNamer); ok {
			return q.QName(module, parts)
		}
	}
	sep := "."
	if lang == model.LangCPP || lang == model.LangRust {
		sep = "::"
	}
	name := strings.Join(parts, sep)
	switch {
	case module == "":
		return name
	case name == "":
		return module
	}
	return module + sep + name
}

// graphWriter maps processed files onto graph rows through one Writer.
//
// Row conventions (shared with internal/resolve):
//   - nodes: one per Def; qname from QName; skey from store.SymbolKey.
//   - contains edges parent -> child, resolved, conf 100.
//   - one unresolved edge (dst NULL, conf 0) per Ref with an enclosing def,
//     kind calls/references, name_id = Ref.Name, qual = Ref.Qualifier.
//     Receivers are not stored: the resolver reads them from the source
//     text at (line, col), and treats an empty qual as receiver-shaped.
//     File-scope references (no enclosing def) are not stored.
//   - one unresolved extends/implements/embeds edge per Inherit (src = the
//     child type), one unresolved tests edge per TestMark with a target and
//     one unresolved declares edge per FlagDeclaration def (src = the
//     declaration, name = its own name).
//   - imports rows without targets; link.go fills target_file/target_pkg.
//     Wildcard imports carry "*" among their names.
//   - files.pkg_id -> packages(lang, ModuleOf(...)).
//   - pending_names gets every def name and every unresolved edge name of
//     the file (old names are queued by DeleteFileGraph); when a rewritten
//     file's imports change, the reference names of its importers too
//     (requeueImporters).
//   - with IncrementalHints, a rewrite is done in place when it keeps the
//     module and imports (rewriteInPlace: unchanged definitions keep their
//     node ids, only added and removed definition names are queued, the
//     file is queued for resolution on its own), and the names of a deleted file's
//     definitions are queued as removed (deleteFile); see the meta queues
//     in rewrite.go.
type graphWriter struct {
	w    *store.Writer
	rc   *RepoCtx
	link *linker
	// hints enables in-place rewrites (rewriteInPlace); nil disables them.
	hints IncrementalHints
}

// writeFile stores one processed file. old is its previous row, if any.
func (g *graphWriter) writeFile(row store.File, old *store.File, facts *model.Facts) error {
	if old != nil && old.PkgID != 0 {
		g.link.touch(old.PkgID)
	}
	var module string
	if facts != nil {
		if spec, ok := parse.Lookup(row.Lang); ok {
			module = spec.ModuleOf(row.Path, facts, g.rc.For(row.Path))
		}
		if module != "" {
			id, err := g.link.pkg(row.Lang, module, path.Dir(row.Path))
			if err != nil {
				return err
			}
			row.PkgID = id
		}
	}
	id, err := g.w.UpsertFile(&row)
	if err != nil {
		return err
	}
	if old != nil && old.PkgID != row.PkgID {
		// Imports targeting this file take their package from it.
		if err := g.link.relinkImporters(id); err != nil {
			return err
		}
	}
	if old != nil && facts != nil {
		if done, err := g.rewriteInPlace(id, &row, old, module, facts); err != nil || done {
			return err
		}
	}
	var oldImports string
	if old != nil {
		if oldImports, err = importSig(g.w, id); err != nil {
			return err
		}
		if err := g.w.DeleteFileGraph(id); err != nil {
			return err
		}
	}
	if facts != nil {
		if err := g.insertFacts(id, row.Path, row.Lang, module, facts); err != nil {
			return err
		}
		if len(facts.Imports) > 0 {
			g.link.relinkFile(id)
		}
	}
	if old != nil {
		newImports, err := importSig(g.w, id)
		if err != nil {
			return err
		}
		if newImports != oldImports {
			return requeueImporters(g.w, id)
		}
	}
	return nil
}

// deleteFile removes a file from the graph. With hints, the names
// DeleteFileGraph queues because the file's definitions disappear are
// queued as removed instead (unless already pending), so the resolver
// skips their references that are resolved with high confidence to other
// definitions; and names whose reference count crosses the common-name
// limit through the removal of the file's references are queued.
func (g *graphWriter) deleteFile(old *store.File) error {
	if err := g.link.fileRemoved(old); err != nil {
		return err
	}
	if g.hints == nil {
		return g.w.DeleteFile(old.ID)
	}
	before, err := fileRefCounts(g.w, old.ID)
	if err != nil {
		return err
	}
	removed, err := newlyQueuedDefNames(g.w, old.ID)
	if err != nil {
		return err
	}
	if err := g.w.DeleteFile(old.ID); err != nil {
		return err
	}
	for _, id := range removed {
		if _, err := g.w.Exec(`DELETE FROM pending_names WHERE name_id = ?`, id); err != nil {
			return err
		}
		if err := g.w.SetMeta(metaPendingRemoved+strconv.FormatInt(id, 10), "1"); err != nil {
			return err
		}
	}
	flips, err := commonFlips(g.w, before, nil, g.hints.CommonNameLimit())
	if err != nil {
		return err
	}
	return g.w.EnqueuePending(flips...)
}

// newlyQueuedDefNames returns the definition names of file id that are not
// pending yet. Edges of other files into the file carry these names too.
func newlyQueuedDefNames(w *store.Writer, fileID int64) ([]int64, error) {
	rows, err := w.Query(`SELECT DISTINCT n.name_id FROM nodes n
		WHERE n.file_id = ? AND NOT EXISTS (SELECT 1 FROM pending_names p WHERE p.name_id = n.name_id)
		ORDER BY 1`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// importSig is a canonical rendering of a file's stored imports.
func importSig(w *store.Writer, fileID int64) (string, error) {
	rows, err := w.Query(`SELECT spec, coalesce(alias, ''), coalesce(names, '') FROM imports
		WHERE file_id = ? ORDER BY 1, 2, 3`, fileID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var spec, alias, names string
		if err := rows.Scan(&spec, &alias, &names); err != nil {
			return "", err
		}
		b.WriteString(spec + "\x00" + alias + "\x00" + names + "\n")
	}
	return b.String(), rows.Err()
}

// Bounds for requeueImporters.
const (
	maxReexportHops  = 4
	maxImporterFiles = 512
)

// requeueImporters queues the reference names of the files importing
// fileID, transitively up to maxReexportHops through importers that are
// themselves imported. A file's imports can re-export definitions of other
// files (TS/JS barrels, Python __init__.py, Rust pub use), so when they
// change, references resolved through them must be re-resolved even though
// no definition changed. At most maxImporterFiles files are requeued.
func requeueImporters(w *store.Writer, fileID int64) error {
	seen := map[int64]bool{fileID: true}
	frontier := []int64{fileID}
	for hop := 0; hop < maxReexportHops && len(frontier) > 0; hop++ {
		var next []int64
		for _, f := range frontier {
			rows, err := w.Query(`SELECT DISTINCT file_id FROM imports WHERE target_file = ?`, f)
			if err != nil {
				return err
			}
			for rows.Next() {
				var imp int64
				if err := rows.Scan(&imp); err != nil {
					rows.Close()
					return err
				}
				if !seen[imp] && len(seen) < maxImporterFiles {
					seen[imp] = true
					next = append(next, imp)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		slices.Sort(next)
		for _, f := range next {
			if _, err := w.Exec(`INSERT OR IGNORE INTO pending_names(name_id)
				SELECT DISTINCT name_id FROM edges WHERE file_id = ? AND name_id IS NOT NULL AND kind <> ?`,
				f, int(model.EdgeContains)); err != nil {
				return err
			}
		}
		frontier = next
	}
	return nil
}

func (g *graphWriter) insertFacts(fileID int64, filePath string, lang model.Lang, module string, f *model.Facts) error {
	ids, names, err := g.insertNodes(fileID, filePath, lang, module, f)
	if err != nil {
		return err
	}
	if err := g.insertOwnEdges(fileID, ids, f, names); err != nil {
		return err
	}
	if err := g.insertImports(fileID, f); err != nil {
		return err
	}
	return g.w.EnqueuePending(sortedIDs(names)...)
}

// insertNodes stores f's definitions and returns their ids (by Defs index)
// and name ids.
func (g *graphWriter) insertNodes(fileID int64, filePath string, lang model.Lang, module string, f *model.Facts) ([]int64, map[int64]struct{}, error) {
	ids := make([]int64, len(f.Defs))
	names := map[int64]struct{}{}
	for i := range f.Defs {
		id, nameID, err := g.insertNode(fileID, filePath, lang, module, f, i, ids)
		if err != nil {
			return nil, nil, err
		}
		ids[i] = id
		names[nameID] = struct{}{}
	}
	return ids, names, nil
}

// insertNode stores definition i of f; ids holds the node ids of the
// definitions before it (its parent's among them). It returns the node id
// and name id.
func (g *graphWriter) insertNode(fileID int64, filePath string, lang model.Lang, module string, f *model.Facts, i int, ids []int64) (int64, int64, error) {
	d := &f.Defs[i]
	qname := QName(lang, module, f.QNameParts(int32(i)))
	n := &store.Node{
		FileID: fileID, Kind: d.Kind, Name: d.Name, QName: qname,
		StartLine: d.StartLine, StartCol: d.StartCol, EndLine: d.EndLine,
		StartByte: d.StartByte, EndByte: d.EndByte, Sig: d.Sig, Flags: d.Flags,
		SKey: store.SymbolKey(filePath, qname, d.Kind),
	}
	if d.Parent >= 0 && int(d.Parent) < i {
		n.ParentID = ids[d.Parent]
	}
	id, err := g.w.InsertNode(n)
	if err != nil {
		return 0, 0, err
	}
	return id, n.NameID, nil
}

// insertOwnEdges stores the edges f's definitions (ids, by Defs index)
// start from: contains and declares edges, references, inheritance and
// test edges. The name ids of the named edges are added to names.
func (g *graphWriter) insertOwnEdges(fileID int64, ids []int64, f *model.Facts, names map[int64]struct{}) error {
	def := func(i int32) (int64, bool) {
		if i < 0 || int(i) >= len(ids) {
			return 0, false
		}
		return ids[i], true
	}
	addEdge := func(e *store.Edge) error {
		if _, err := g.w.InsertEdge(e); err != nil {
			return err
		}
		if e.NameID != 0 && names != nil {
			names[e.NameID] = struct{}{}
		}
		return nil
	}
	for i := range f.Defs {
		d := &f.Defs[i]
		if d.Parent >= 0 && int(d.Parent) < i {
			if err := addEdge(&store.Edge{FileID: fileID, Src: ids[d.Parent], Dst: ids[i],
				Kind: model.EdgeContains, Conf: 100, Line: d.StartLine, Col: d.StartCol}); err != nil {
				return err
			}
		}
		if d.Flags.Has(model.FlagDeclaration) {
			if err := addEdge(&store.Edge{FileID: fileID, Src: ids[i], Kind: model.EdgeDeclares,
				Name: d.Name, Line: d.StartLine, Col: d.StartCol}); err != nil {
				return err
			}
		}
	}
	for i := range f.Refs {
		r := &f.Refs[i]
		src, ok := def(r.Enclosing)
		if !ok {
			continue
		}
		if err := addEdge(&store.Edge{FileID: fileID, Src: src, Kind: r.Kind.EdgeKind(),
			Name: r.Name, Qual: r.Qualifier, Line: r.Line, Col: r.Col}); err != nil {
			return err
		}
	}
	for i := range f.Inherits {
		in := &f.Inherits[i]
		src, ok := def(in.Child)
		if !ok {
			continue
		}
		if err := addEdge(&store.Edge{FileID: fileID, Src: src, Kind: in.Kind,
			Name: in.Name, Qual: in.Qualifier, Line: in.Line, Col: in.Col}); err != nil {
			return err
		}
	}
	for _, tm := range f.Tests {
		src, ok := def(tm.Def)
		if !ok || tm.Target == "" {
			continue
		}
		line := f.Defs[tm.Def].StartLine
		if err := addEdge(&store.Edge{FileID: fileID, Src: src, Kind: model.EdgeTests,
			Name: tm.Target, Line: line}); err != nil {
			return err
		}
	}
	return nil
}

// insertImports stores f's imports without targets (see linker).
func (g *graphWriter) insertImports(fileID int64, f *model.Facts) error {
	for i := range f.Imports {
		im := &f.Imports[i]
		if err := g.w.InsertImport(&store.Import{FileID: fileID, Spec: im.Spec, Alias: im.Alias,
			Names: importNames(im), Line: im.Line}); err != nil {
			return err
		}
	}
	return nil
}

// importNames returns the stored member names of im: wildcard imports
// carry "*" among them.
func importNames(im *model.Import) []string {
	names := im.Names
	if im.Wildcard && !slices.Contains(names, "*") {
		names = append(slices.Clip(names), "*")
	}
	return names
}

func sortedIDs(set map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
