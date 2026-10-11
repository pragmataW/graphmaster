package index

import (
	"bytes"
	"slices"
	"strconv"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// Resolution queues kept in meta besides pending_names (see
// internal/resolve): metaPendingFile is resolve.PendingFilePrefix, keys
// "pending_file:<files.id>" queue one file's own references;
// metaPendingDependents is resolve.PendingDependentsPrefix, keys
// "pending_dependents:<names.id>" queue the receiver-typed references that
// depend on a name's definitions; metaPendingRemoved is
// resolve.PendingRemovedPrefix, keys "pending_removed:<names.id>" queue a
// name that only lost definitions.
const (
	metaPendingFile       = "pending_file:"
	metaPendingDependents = "pending_dependents:"
	metaPendingRemoved    = "pending_removed:"
)

// storedNode is the part of a nodes row an in-place rewrite compares or
// updates.
type storedNode struct {
	id, parent                                 int64
	kind                                       model.NodeKind
	nameID                                     int64
	name, qname, sig                           string
	flags                                      model.DefFlags
	startLine, startCol, endLine, startB, endB uint32
}

// rewriteInPlace stores the new facts of an already indexed file whose
// definitions are mostly unchanged: same module, same imports, and the
// definitions it keeps (matchDefs: equal apart from their positions, under
// the same parent) keep their inheritance clauses, with ids still
// increasing in definition order among definitions sharing a name
// (sameNameOrder). That is the common case of a body edit, of a
// file reprocessed only because a dependency of its module identity or
// import targets changed, and of an edit adding, removing or changing a
// few definitions.
//
// Kept definitions keep their node ids, so references from other files
// stay resolved and their names need not be queued: what other files can
// see of them did not change. The file's own edges and imports are
// replaced and the file goes on the per-file resolution queue; names are
// queued only where something visible may have changed:
//   - the name of every added definition (all its references re-resolved);
//   - the name of every removed definition, as removed (see deleteFile),
//     and the names of other files' references into it;
//   - the dependents of every kept definition name (receiver typing
//     elsewhere reading the definitions) when the content is unchanged,
//     since the import targets the definitions' types resolve through may
//     have changed, or when the resolver reads definition bodies of other
//     files for this language (IncrementalHints.ReadsForeignSource); else
//     the dependents of kept definitions whose signature names an added or
//     removed definition, since their types may resolve to it;
//   - a reference name whose reference count crossed the common-name limit.
//
// It reports false, having written nothing, when the file does not qualify.
func (g *graphWriter) rewriteInPlace(fileID int64, row *store.File, old *store.File, module string, facts *model.Facts) (bool, error) {
	if g.hints == nil || row.PkgID != old.PkgID {
		return false, nil
	}
	nodes, err := loadStoredNodes(g.w, fileID)
	if err != nil {
		return false, err
	}
	match := matchDefs(nodes, row.Lang, module, facts)
	if !sameNameOrder(nodes, match, facts) {
		return false, nil
	}
	if same, err := sameInherits(g.w, fileID, nodes, match, facts); err != nil || !same {
		return false, err
	}
	oldImports, err := importSig(g.w, fileID)
	if err != nil || oldImports != factsImportSig(facts) {
		return false, err
	}

	before, err := fileRefCounts(g.w, fileID)
	if err != nil {
		return false, err
	}
	kept := make([]bool, len(nodes))
	for _, k := range match {
		if k >= 0 {
			kept[k] = true
		}
	}
	added := map[int64]struct{}{}   // name ids of added definitions
	changed := map[string]bool{}    // names of added and removed definitions
	removed := map[int64]struct{}{} // name ids of removed definitions
	for i, n := range nodes {
		if !kept[i] {
			removed[n.nameID] = struct{}{}
			changed[n.name] = true
		}
	}
	// Removed names not pending yet are queued as removed (see deleteFile).
	var newlyRemoved []int64
	for _, id := range sortedIDs(removed) {
		var one int
		err := g.w.QueryRow(`SELECT count(*) FROM pending_names WHERE name_id = ?`, id).Scan(&one)
		if err != nil {
			return false, err
		}
		if one == 0 {
			newlyRemoved = append(newlyRemoved, id)
		}
	}
	for i, n := range nodes {
		if !kept[i] {
			if err := g.w.DeleteNode(fileID, n.id); err != nil {
				return false, err
			}
		}
	}

	ids := make([]int64, len(facts.Defs))
	for j := range facts.Defs {
		d := &facts.Defs[j]
		if k := match[j]; k >= 0 {
			n := &nodes[k]
			ids[j] = n.id
			if n.startLine == d.StartLine && n.startCol == d.StartCol && n.endLine == d.EndLine &&
				n.startB == d.StartByte && n.endB == d.EndByte {
				continue
			}
			if _, err := g.w.Exec(`UPDATE nodes SET start_line = ?, start_col = ?, end_line = ?, start_byte = ?, end_byte = ?
				WHERE id = ?`, d.StartLine, d.StartCol, d.EndLine, d.StartByte, d.EndByte, n.id); err != nil {
				return false, err
			}
			continue
		}
		id, nameID, err := g.insertNode(fileID, row.Path, row.Lang, module, facts, j, ids)
		if err != nil {
			return false, err
		}
		ids[j] = id
		added[nameID] = struct{}{}
		changed[d.Name] = true
	}
	for _, q := range []string{
		`DELETE FROM edges WHERE file_id = ?`,
		`DELETE FROM imports WHERE file_id = ?`,
		// Re-recorded as the file's module, imports and references are
		// derived again (see DeleteFileGraph).
		`DELETE FROM dep_owners WHERE file_id = ?`,
	} {
		if _, err := g.w.Exec(q, fileID); err != nil {
			return false, err
		}
	}
	if err := g.insertOwnEdges(fileID, ids, facts, nil); err != nil {
		return false, err
	}
	if err := g.insertImports(fileID, facts); err != nil {
		return false, err
	}
	if len(facts.Imports) > 0 {
		g.link.relinkFile(fileID)
	}

	allKept := bytes.Equal(row.Blob, old.Blob) || g.hints.ReadsForeignSource(row.Lang)
	deps := map[int64]struct{}{}
	for i, n := range nodes {
		if !kept[i] {
			continue
		}
		if allKept || len(changed) > 0 && slices.ContainsFunc(identTokens(n.sig), func(s string) bool { return changed[s] }) {
			deps[n.nameID] = struct{}{}
		}
	}
	for _, id := range sortedIDs(deps) {
		if err := g.w.SetMeta(metaPendingDependents+strconv.FormatInt(id, 10), "1"); err != nil {
			return false, err
		}
	}
	for _, id := range newlyRemoved {
		if _, ok := added[id]; ok {
			continue
		}
		if _, err := g.w.Exec(`DELETE FROM pending_names WHERE name_id = ?`, id); err != nil {
			return false, err
		}
		if err := g.w.SetMeta(metaPendingRemoved+strconv.FormatInt(id, 10), "1"); err != nil {
			return false, err
		}
	}
	queue := added
	after, err := fileRefCounts(g.w, fileID)
	if err != nil {
		return false, err
	}
	flips, err := commonFlips(g.w, before, after, g.hints.CommonNameLimit())
	if err != nil {
		return false, err
	}
	for _, id := range flips {
		queue[id] = struct{}{}
	}
	if err := g.w.EnqueuePending(sortedIDs(queue)...); err != nil {
		return false, err
	}
	return true, queueFile(g.w, fileID)
}

// queueFile puts fileID on the per-file resolution queue.
func queueFile(w *store.Writer, fileID int64) error {
	return w.SetMeta(metaPendingFile+strconv.FormatInt(fileID, 10), "1")
}

func loadStoredNodes(w *store.Writer, fileID int64) ([]storedNode, error) {
	rows, err := w.Query(`SELECT n.id, coalesce(n.parent_id, 0), n.kind, n.name_id, m.name, n.qname,
		coalesce(n.sig, ''), n.flags, n.start_line, n.start_col, n.end_line, n.start_byte, n.end_byte
		FROM nodes n JOIN names m ON m.id = n.name_id WHERE n.file_id = ? ORDER BY n.id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedNode
	for rows.Next() {
		var (
			n           storedNode
			kind, flags int
		)
		if err := rows.Scan(&n.id, &n.parent, &kind, &n.nameID, &n.name, &n.qname, &n.sig, &flags,
			&n.startLine, &n.startCol, &n.endLine, &n.startB, &n.endB); err != nil {
			return nil, err
		}
		n.kind = model.NodeKind(uint8(kind))
		n.flags = model.DefFlags(uint16(flags))
		out = append(out, n)
	}
	return out, rows.Err()
}

// sameInherits compares the stored inheritance edges of the kept nodes
// with the inheritance clauses (child, kind, name, qualifier) of the
// definitions keeping them (match, see matchDefs).
func sameInherits(w *store.Writer, fileID int64, nodes []storedNode, match []int, f *model.Facts) (bool, error) {
	index := make(map[int64]int, len(nodes)) // kept node id -> new def index
	for j, k := range match {
		if k >= 0 {
			index[nodes[k].id] = j
		}
	}
	rows, err := w.Query(`SELECT e.src, e.kind, m.name, coalesce(e.qual, '') FROM edges e JOIN names m ON m.id = e.name_id
		WHERE e.file_id = ? AND e.kind IN (?, ?, ?)`,
		fileID, int(model.EdgeExtends), int(model.EdgeImplements), int(model.EdgeEmbeds))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var stored []string
	for rows.Next() {
		var (
			src        int64
			kind       int
			name, qual string
		)
		if err := rows.Scan(&src, &kind, &name, &qual); err != nil {
			return false, err
		}
		if j, ok := index[src]; ok {
			stored = append(stored, inheritKey(j, model.EdgeKind(uint8(kind)), name, qual))
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	var want []string
	for _, in := range f.Inherits {
		if in.Child < 0 || int(in.Child) >= len(match) || match[in.Child] < 0 {
			continue
		}
		want = append(want, inheritKey(int(in.Child), in.Kind, in.Name, in.Qualifier))
	}
	slices.Sort(stored)
	slices.Sort(want)
	return slices.Equal(stored, want), nil
}

func inheritKey(child int, kind model.EdgeKind, name, qual string) string {
	return strconv.Itoa(child) + "\x00" + strconv.Itoa(int(kind)) + "\x00" + name + "\x00" + qual
}

// factsImportSig renders facts' imports like importSig renders stored ones.
func factsImportSig(f *model.Facts) string {
	lines := make([]string, 0, len(f.Imports))
	for i := range f.Imports {
		im := &f.Imports[i]
		lines = append(lines, im.Spec+"\x00"+im.Alias+"\x00"+strings.Join(importNames(im), ",")+"\n")
	}
	slices.Sort(lines)
	return strings.Join(lines, "")
}

// fileRefCounts counts the file's references per name the way the
// resolver's common-name cutoff does: one per edge group, whatever its
// resolution.
func fileRefCounts(w *store.Writer, fileID int64) (map[int64]int, error) {
	rows, err := w.Query(`SELECT name_id, count(*) FROM (SELECT DISTINCT src, kind, name_id, line, col FROM edges
		WHERE file_id = ? AND name_id IS NOT NULL) GROUP BY name_id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// commonFlips returns the names whose repository-wide reference count,
// changed by after-before, crossed limit.
func commonFlips(w *store.Writer, before, after map[int64]int, limit int) ([]int64, error) {
	delta := map[int64]int{}
	for id, n := range after {
		delta[id] += n
	}
	for id, n := range before {
		delta[id] -= n
	}
	var out []int64
	for id, d := range delta {
		if d == 0 {
			continue
		}
		now, err := refCount(w, id)
		if err != nil {
			return nil, err
		}
		if (now > limit) != (now-d > limit) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// refCount mirrors the resolver's count of distinct references named id.
func refCount(w *store.Writer, id int64) (int, error) {
	var low, high int
	if err := w.QueryRow(`SELECT count(*) FROM (SELECT DISTINCT file_id, src, kind, line, col FROM edges
		WHERE name_id = ?1 AND (dst IS NULL OR conf < 80))`, id).Scan(&low); err != nil {
		return 0, err
	}
	if err := w.QueryRow(`SELECT count(*) FROM nodes n JOIN edges e ON e.dst = n.id
		WHERE n.name_id = ?1 AND e.name_id = ?1 AND e.conf >= 80`, id).Scan(&high); err != nil {
		return 0, err
	}
	return low + high, nil
}
