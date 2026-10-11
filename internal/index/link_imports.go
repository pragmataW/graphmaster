package index

import (
	"cmp"
	"database/sql"
	"errors"
	"slices"

	"github.com/pragmataW/graphmaster/internal/model"
	"github.com/pragmataW/graphmaster/internal/parse"
)

// relinkBulkThreshold switches finishImports from per-file queries to one
// pass over the files and imports tables.
const relinkBulkThreshold = 512

type importRow struct {
	rowid      int64
	file       fileRef
	spec       string
	targetFile int64
	targetPkg  int64
}

type fileInfo struct {
	id, pkg int64
}

// finishImports recomputes the import targets of the files to relink (all
// files with relinkAll) and records what each target depends on.
func (l *linker) finishImports() error {
	if !l.relinkAll && len(l.relink) == 0 {
		return nil
	}
	pkgs, err := l.loadPackages()
	if err != nil {
		return err
	}
	var (
		imps   []importRow
		byPath map[string]fileInfo
	)
	if l.relinkAll || len(l.relink) > relinkBulkThreshold {
		if imps, byPath, err = l.loadAllImports(); err != nil {
			return err
		}
	} else if imps, err = l.loadRelinkImports(); err != nil {
		return err
	}

	lookup := func(p string) (fileInfo, error) {
		if byPath != nil {
			return byPath[p], nil
		}
		var fi fileInfo
		err := l.w.QueryRow(`SELECT id, coalesce(pkg_id, 0) FROM files WHERE path = ?`, p).Scan(&fi.id, &fi.pkg)
		if errors.Is(err, sql.ErrNoRows) {
			return fileInfo{}, nil
		}
		return fi, err
	}
	type update struct{ rowid, tf, tp int64 }
	var updates []update
	type linkDep struct {
		key  string
		file int64
	}
	linkDeps := map[linkDep]bool{}
	for _, im := range imps {
		spec, ok := parse.Lookup(im.file.lang)
		if !ok {
			continue
		}
		// Answers are recorded for the importing file; what the row then
		// links to is recorded only while it is missing: a target that
		// exists is relinked when it goes away (relinkImporters,
		// finishPackages), and then records its key.
		t := spec.ResolveImport(im.spec, im.file.path, l.rc.For(im.file.path))
		var tf, tp int64
		switch t.Kind {
		case model.TargetFile:
			p := model.CleanRel(t.Path)
			fi, err := lookup(p)
			if err != nil {
				return err
			}
			tf, tp = fi.id, fi.pkg
			if tf == 0 && model.IsCodePath(p) {
				linkDeps[linkDep{depTarget + p, im.file.id}] = true
			}
			if tp == 0 && t.Module != "" {
				if tp = pkgs[pkgKey{im.file.lang, t.Module}]; tp == 0 {
					linkDeps[linkDep{moduleDepKey(im.file.lang, t.Module), im.file.id}] = true
				}
			}
		case model.TargetModule:
			if tp = pkgs[pkgKey{im.file.lang, t.Module}]; tp == 0 && t.Module != "" {
				linkDeps[linkDep{moduleDepKey(im.file.lang, t.Module), im.file.id}] = true
			}
		}
		if tf != im.targetFile || tp != im.targetPkg {
			updates = append(updates, update{im.rowid, tf, tp})
		}
	}
	for _, u := range updates {
		if _, err := l.w.Exec(`UPDATE imports SET target_file = ?, target_pkg = ? WHERE rowid = ?`,
			nullID(u.tf), nullID(u.tp), u.rowid); err != nil {
			return err
		}
	}
	keys := make([]linkDep, 0, len(linkDeps))
	for k := range linkDeps {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b linkDep) int {
		return cmp.Or(cmp.Compare(a.key, b.key), cmp.Compare(a.file, b.file))
	})
	for _, k := range keys {
		if _, err := l.w.Exec(`INSERT OR IGNORE INTO dep_owners(key, file_id) VALUES(?, ?)`, k.key, k.file); err != nil {
			return err
		}
	}
	if l.relinkAll {
		return l.w.SetMeta(metaLinkDeps, linkDepsVersion)
	}
	return nil
}

func (l *linker) loadPackages() (map[pkgKey]int64, error) {
	pkgs := map[pkgKey]int64{}
	rows, err := l.w.Query(`SELECT id, lang, name FROM packages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			lang int
			name string
		)
		if err := rows.Scan(&id, &lang, &name); err != nil {
			return nil, err
		}
		pkgs[pkgKey{model.Lang(uint8(lang)), name}] = id
	}
	return pkgs, rows.Err()
}

// loadAllImports loads every files row by path and the import rows to
// relink (all of them with relinkAll).
func (l *linker) loadAllImports() ([]importRow, map[string]fileInfo, error) {
	byPath := map[string]fileInfo{}
	byID := map[int64]fileRef{}
	rows, err := l.w.Query(`SELECT id, path, lang, coalesce(pkg_id, 0) FROM files`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var (
			fr   fileRef
			lang int
			pkg  int64
		)
		if err := rows.Scan(&fr.id, &fr.path, &lang, &pkg); err != nil {
			rows.Close()
			return nil, nil, err
		}
		fr.lang = model.Lang(uint8(lang))
		byPath[fr.path] = fileInfo{id: fr.id, pkg: pkg}
		byID[fr.id] = fr
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	imps, err := l.loadImports(`SELECT rowid, file_id, spec, coalesce(target_file, 0), coalesce(target_pkg, 0) FROM imports`,
		nil, func(id int64) fileRef { return byID[id] })
	if err != nil {
		return nil, nil, err
	}
	if !l.relinkAll {
		imps = slices.DeleteFunc(imps, func(im importRow) bool { return !l.relink[im.file.id] })
	}
	return imps, byPath, nil
}

// loadRelinkImports loads the import rows of the files to relink that
// still exist, in file id order.
func (l *linker) loadRelinkImports() ([]importRow, error) {
	ids := make([]int64, 0, len(l.relink))
	for id := range l.relink {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var imps []importRow
	for _, id := range ids {
		fr := fileRef{id: id}
		var lang int
		err := l.w.QueryRow(`SELECT path, lang FROM files WHERE id = ?`, id).Scan(&fr.path, &lang)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fr.lang = model.Lang(uint8(lang))
		got, err := l.loadImports(`SELECT rowid, file_id, spec, coalesce(target_file, 0), coalesce(target_pkg, 0) FROM imports WHERE file_id = ?`,
			[]any{id}, func(int64) fileRef { return fr })
		if err != nil {
			return nil, err
		}
		imps = append(imps, got...)
	}
	return imps, nil
}

func (l *linker) loadImports(q string, args []any, file func(int64) fileRef) ([]importRow, error) {
	rows, err := l.w.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []importRow
	for rows.Next() {
		var (
			im  importRow
			fid int64
		)
		if err := rows.Scan(&im.rowid, &fid, &im.spec, &im.targetFile, &im.targetPkg); err != nil {
			return nil, err
		}
		im.file = file(fid)
		out = append(out, im)
	}
	return out, rows.Err()
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
