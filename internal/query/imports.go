package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pragmataW/graphmaster/internal/store"
)

// pkgRef is a packages row.
type pkgRef struct {
	ID        int64
	Name, Dir string
}

// importTarget renders where an import resolved to.
func importTarget(targetPath, pkgName, pkgDir string) string {
	switch {
	case targetPath != "":
		return "→ " + targetPath
	case pkgDir != "":
		return "→ " + pkgDir + "/"
	case pkgName != "":
		return "→ " + pkgName
	}
	return "(external)"
}

// Imports lists what a file or package imports (direction=out) or who
// imports it (direction=in).
func (e *Engine) Imports(ctx context.Context, in ImportsInput) (string, error) {
	return e.run(ctx, "imports", in.MaxTokens, func(ctx context.Context, q *querier, o *output) error {
		dir := strings.ToLower(strings.TrimSpace(in.Direction))
		switch dir {
		case "":
			dir = "out"
		case "out", "in":
		default:
			return invalidf("direction must be out or in, got %q", in.Direction)
		}
		path, module := normPathPrefix(in.Path), strings.TrimSpace(in.Module)
		if (path == "") == (module == "") {
			return invalidf("pass exactly one of path or module")
		}
		if path != "" {
			f, err := q.st.FileByPath(ctx, path)
			if err == nil {
				if dir == "out" {
					return q.fileImports(ctx, o, f)
				}
				return q.fileImporters(ctx, o, f)
			}
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			pkgs, err := q.packagesWhere(ctx, `dir = ?`, strings.TrimSuffix(path, "/"))
			if err != nil {
				return err
			}
			if len(pkgs) == 0 {
				return q.fileNotFound(ctx, o, path)
			}
			module = pkgs[0].Name
		}
		pkgs, err := q.packagesWhere(ctx, `name = ? OR dir = ?`, module, module)
		if err != nil {
			return err
		}
		if dir == "in" {
			return q.moduleImporters(ctx, o, module, pkgs)
		}
		if len(pkgs) == 0 {
			o.linef("%s is not an indexed package; use direction=in to list its importers", module)
			return nil
		}
		return q.packageImports(ctx, o, pkgs[0])
	})
}

func (q *querier) packagesWhere(ctx context.Context, cond string, args ...any) ([]pkgRef, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, name, coalesce(dir, '') FROM packages WHERE `+cond+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pkgRef
	for rows.Next() {
		var p pkgRef
		if err := rows.Scan(&p.ID, &p.Name, &p.Dir); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// collectRows runs a query and renders each row with render.
func collectRows(rows *sql.Rows, err error, render func(scan func(...any) error) (string, error)) ([]string, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		s, err := render(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (q *querier) fileImports(ctx context.Context, o *output, f *store.File) error {
	rows, err := q.db.QueryContext(ctx, `SELECT i.line, i.spec, coalesce(i.alias, ''), coalesce(i.names, ''),
			coalesce(tf.path, ''), coalesce(p.name, ''), coalesce(p.dir, '')
		FROM imports i LEFT JOIN files tf ON tf.id = i.target_file LEFT JOIN packages p ON p.id = i.target_pkg
		WHERE i.file_id = ? ORDER BY i.line, i.spec`, f.ID)
	lines, err := collectRows(rows, err, func(scan func(...any) error) (string, error) {
		var (
			line                                   int
			spec, alias, names, tpath, pname, pdir string
		)
		if err := scan(&line, &spec, &alias, &names, &tpath, &pname, &pdir); err != nil {
			return "", err
		}
		r := fmt.Sprintf("%d  %s", line, spec)
		if alias != "" {
			r += " as " + alias
		}
		if names != "" {
			r += " {" + strings.ReplaceAll(names, ",", ", ") + "}"
		}
		return r + "  " + importTarget(tpath, pname, pdir), nil
	})
	if err != nil {
		return err
	}
	o.linef("imports of %s (%d):", f.Path, len(lines))
	o.list(len(lines), func(i int) string { return "  " + lines[i] }, 0)
	return nil
}

func (q *querier) fileImporters(ctx context.Context, o *output, f *store.File) error {
	rows, err := q.db.QueryContext(ctx, `SELECT f.path, i.line, i.spec FROM imports i JOIN files f ON f.id = i.file_id
		WHERE i.target_file = ?1 OR (?2 <> 0 AND i.target_pkg = ?2) ORDER BY f.path, i.line`, f.ID, f.PkgID)
	lines, err := collectRows(rows, err, importerRow)
	if err != nil {
		return err
	}
	o.linef("importers of %s (%d):", f.Path, len(lines))
	o.list(len(lines), func(i int) string { return "  " + lines[i] }, 0)
	return nil
}

func importerRow(scan func(...any) error) (string, error) {
	var (
		path, spec string
		line       int
	)
	if err := scan(&path, &line, &spec); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d  %s", path, line, spec), nil
}

func (q *querier) moduleImporters(ctx context.Context, o *output, module string, pkgs []pkgRef) error {
	ids := make([]int64, len(pkgs))
	for i, p := range pkgs {
		ids[i] = p.ID
	}
	args := append([]any{module}, int64Args(ids)...)
	rows, err := q.db.QueryContext(ctx, `SELECT f.path, i.line, i.spec FROM imports i JOIN files f ON f.id = i.file_id
		WHERE i.spec = ? OR i.target_pkg IN (`+placeholders(len(ids))+`) ORDER BY f.path, i.line`, args...)
	lines, err := collectRows(rows, err, importerRow)
	if err != nil {
		return err
	}
	o.linef("importers of %s (%d):", module, len(lines))
	o.list(len(lines), func(i int) string { return "  " + lines[i] }, 0)
	return nil
}

func (q *querier) packageImports(ctx context.Context, o *output, p pkgRef) error {
	rows, err := q.db.QueryContext(ctx, `SELECT i.spec, count(DISTINCT i.file_id),
			coalesce(max(tf.path), ''), coalesce(max(tp.name), ''), coalesce(max(tp.dir), '')
		FROM files f JOIN imports i ON i.file_id = f.id
		LEFT JOIN files tf ON tf.id = i.target_file LEFT JOIN packages tp ON tp.id = i.target_pkg
		WHERE f.pkg_id = ? GROUP BY i.spec ORDER BY i.spec`, p.ID)
	lines, err := collectRows(rows, err, func(scan func(...any) error) (string, error) {
		var (
			spec, tpath, pname, pdir string
			n                        int
		)
		if err := scan(&spec, &n, &tpath, &pname, &pdir); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s  (%s)  %s", spec, plural(n, "file"), importTarget(tpath, pname, pdir)), nil
	})
	if err != nil {
		return err
	}
	label := p.Name
	if p.Dir != "" && p.Dir != p.Name {
		label += " (" + p.Dir + "/)"
	}
	o.linef("imports of package %s (%d):", label, len(lines))
	o.list(len(lines), func(i int) string { return "  " + lines[i] }, 0)
	return nil
}
