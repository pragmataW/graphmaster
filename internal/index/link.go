package index

import (
	"database/sql"
	"errors"
	"path"
	"strconv"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/store"
)

// linkAllThreshold switches package directory maintenance from per-package
// queries to one pass over the files table.
const linkAllThreshold = 64

type pkgKey struct {
	lang model.Lang
	name string
}

type fileRef struct {
	id   int64
	path string
	lang model.Lang
}

// linker maintains the rows whose content depends on other files and so
// must not depend on write order: packages (created on demand, directory =
// smallest directory of their files, dropped when empty) and import
// targets. It runs at the end of every write, inside the same batch.
//
// Import targets are recomputed only for the files that may need it
// (relink): files written in this run, importers of removed files or of
// files whose package changed, and files whose imports name a package
// created in this run. That is complete because linking records what each
// import's target depends on, attributed to the importing file: every
// RepoCtx answer ResolveImport consulted (a file whose answers change is
// reprocessed, and so relinked, by the dependency check of the next scan),
// and in dep_owners the target file ("p:" keys, consulted when a file is
// added) and the target module ("m:" keys, consulted when a package is
// created).
type linker struct {
	w  *store.Writer
	rc *RepoCtx

	known   map[pkgKey]int64
	touched map[int64]bool
	// relink holds the files whose import targets are recomputed.
	relink map[int64]bool
	// created lists the packages created in this run.
	created []pkgKey
	// relinkAll recomputes every import target, recording the link
	// dependencies of all of them (graphs written before they were
	// recorded, see metaLinkDeps).
	relinkAll bool
}

// metaLinkDeps marks a graph whose import rows all have their link
// dependencies recorded; its value is linkDepsVersion.
const (
	metaLinkDeps    = "link_deps"
	linkDepsVersion = "1"
)

// Link dependency keys in dep_owners. They have no meta "dep:" entry and
// are never checked by a scan; the linker consults them itself.
//
// depModule "m:<lang>:<module>": the attributed file has an import whose
// target names that module, which has no package row yet.
// depTarget "p:<path>": the attributed file has an import resolving to
// the code path, which has no files row yet.
const (
	depModule = "m:"
	depTarget = "p:"
)

func moduleDepKey(lang model.Lang, module string) string {
	return depModule + strconv.Itoa(int(lang)) + ":" + module
}

func newLinker(w *store.Writer, rc *RepoCtx) *linker {
	return &linker{w: w, rc: rc, known: map[pkgKey]int64{}, touched: map[int64]bool{}, relink: map[int64]bool{}}
}

func (l *linker) touch(pkgID int64) { l.touched[pkgID] = true }

// relinkFile recomputes the import targets of file id.
func (l *linker) relinkFile(id int64) { l.relink[id] = true }

// relinkImporters recomputes the import targets of the files importing
// file id.
func (l *linker) relinkImporters(id int64) error {
	return l.relinkQuery(`SELECT DISTINCT file_id FROM imports WHERE target_file = ?`, id)
}

// relinkQuery relinks the file ids returned by q.
func (l *linker) relinkQuery(q string, args ...any) error {
	rows, err := l.w.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f int64
		if err := rows.Scan(&f); err != nil {
			return err
		}
		l.relink[f] = true
	}
	return rows.Err()
}

// fileAdded relinks the files whose imports resolve to the new file path.
func (l *linker) fileAdded(path string) error { return l.relinkOwners(depTarget + path) }

// fileRemoved must be called before old is deleted: its importers are
// relinked and its package re-checked.
func (l *linker) fileRemoved(old *store.File) error {
	if old.PkgID != 0 {
		l.touch(old.PkgID)
	}
	return l.relinkImporters(old.ID)
}

// pkg returns the id of package (lang, name), creating it if needed.
func (l *linker) pkg(lang model.Lang, name, dir string) (int64, error) {
	k := pkgKey{lang, name}
	if id, ok := l.known[k]; ok {
		l.touch(id)
		return id, nil
	}
	var id int64
	err := l.w.QueryRow(`SELECT id FROM packages WHERE lang = ? AND name = ?`, int(lang), name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		p := &store.Package{Lang: lang, Name: name, Dir: dirOf(dir)}
		if id, err = l.w.UpsertPackage(p); err != nil {
			return 0, err
		}
		l.created = append(l.created, k)
	} else if err != nil {
		return 0, err
	}
	l.known[k] = id
	l.touch(id)
	return id, nil
}

func dirOf(d string) string {
	if d == "." {
		return ""
	}
	return d
}

// finish updates touched packages and import targets.
func (l *linker) finish() error {
	if err := l.finishPackages(); err != nil {
		return err
	}
	for _, k := range l.created {
		if err := l.relinkOwners(moduleDepKey(k.lang, k.name)); err != nil {
			return err
		}
	}
	return l.finishImports()
}

// relinkOwners relinks the files attributed to dependency key.
func (l *linker) relinkOwners(key string) error {
	return l.relinkQuery(`SELECT file_id FROM dep_owners WHERE key = ?`, key)
}

func (l *linker) finishPackages() error {
	if len(l.touched) == 0 {
		return nil
	}
	dirs := map[int64]string{} // pkg id -> smallest file directory
	seen := map[int64]bool{}
	note := func(pkg int64, p string) {
		d := dirOf(path.Dir(p))
		if cur, ok := dirs[pkg]; !ok || d < cur {
			dirs[pkg] = d
		}
		seen[pkg] = true
	}
	if len(l.touched) > linkAllThreshold {
		rows, err := l.w.Query(`SELECT pkg_id, path FROM files WHERE pkg_id IS NOT NULL`)
		if err != nil {
			return err
		}
		if err := scanPairs(rows, func(pkg int64, p string) {
			if l.touched[pkg] {
				note(pkg, p)
			}
		}); err != nil {
			return err
		}
	} else {
		for id := range l.touched {
			rows, err := l.w.Query(`SELECT pkg_id, path FROM files WHERE pkg_id = ?`, id)
			if err != nil {
				return err
			}
			if err := scanPairs(rows, note); err != nil {
				return err
			}
		}
	}
	for id := range l.touched {
		if !seen[id] {
			if _, err := l.w.Exec(`DELETE FROM packages WHERE id = ?`, id); err != nil {
				return err
			}
			// Imports linked to the package are relinked: they record
			// the module key, to link again when it reappears.
			if err := l.relinkQuery(`SELECT DISTINCT file_id FROM imports WHERE target_pkg = ?`, id); err != nil {
				return err
			}
			continue
		}
		if _, err := l.w.Exec(`UPDATE packages SET dir = ? WHERE id = ? AND dir IS NOT ?`,
			nullable(dirs[id]), id, nullable(dirs[id])); err != nil {
			return err
		}
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanPairs(rows *sql.Rows, fn func(int64, string)) error {
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			p  string
		)
		if err := rows.Scan(&id, &p); err != nil {
			return err
		}
		fn(id, p)
	}
	return rows.Err()
}
