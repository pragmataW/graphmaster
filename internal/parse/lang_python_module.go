package parse

import (
	"path"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// Python module identity and import resolution.
//
// A file inside a regular package (directories with __init__.py) is named
// by its package chain: python/shop/services/pricing.py with __init__.py in
// shop/ and services/ is "shop.services.pricing"; a package's __init__.py
// is the package itself ("shop.services"). A file outside any package is
// named relative to its source root: the nearest directory holding
// pyproject.toml, setup.py or setup.cfg (its src/ subdirectory for the src
// layout), else the repository root.
//
// Absolute imports are searched in these roots, in order: the directory
// above the importing file's top-level package (or the file's own
// directory), the project root's src/, the project root, and the repository
// root. Relative imports start at the importing file's package.

var pyProjectMarkers = []string{"pyproject.toml", "setup.py", "setup.cfg"}

// ModuleOf implements model.LanguageSpec.
func (pythonSpec) ModuleOf(p string, _ *model.Facts, ctx model.RepoContext) string {
	p = model.CleanRel(p)
	dir, stem := pyDirStem(p)
	var parts []string
	if stem != "__init__" {
		parts = append(parts, stem)
	}
	if ctx == nil {
		return pyJoinModule(pySegments(dir), parts)
	}
	var pkgs []string
	d := dir
	for d != "" && pyIsPackage(ctx, d) {
		pkgs = append([]string{path.Base(d)}, pkgs...)
		d = pyParent(d)
	}
	if len(pkgs) > 0 {
		return pyJoinModule(pkgs, parts)
	}
	root := ""
	if proj, ok := pyProjectRoot(ctx, dir); ok {
		root = proj
		if src := path.Join(proj, "src"); pyUnder(dir, src) {
			root = src
		}
	}
	return pyJoinModule(pySegments(relDir(root, dir)), parts)
}

// ResolveImport implements model.LanguageSpec. A spec names a module
// (import a.b) or a module member (from a import b -> "a.b"): the longest
// existing module among spec and its parent wins.
func (s pythonSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	rel := strings.TrimLeft(spec, ".")
	dots := len(spec) - len(rel)
	var segs []string
	if rel != "" {
		segs = strings.Split(rel, ".")
		for _, seg := range segs {
			if seg == "" {
				return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
			}
		}
	}
	if ctx == nil {
		if dots > 0 {
			return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
		}
		return model.ImportTarget{Kind: model.TargetExternal, Module: spec}
	}
	dir := model.CleanRel(path.Dir(model.CleanRel(fromFile)))

	if dots > 0 {
		// Relative imports cannot leave the top-level package (a file
		// outside any package may still import its siblings).
		depth := 0
		for d := dir; d != "" && pyIsPackage(ctx, d); d = pyParent(d) {
			depth++
		}
		if dots-1 >= max(depth, 1) {
			return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
		}
		base := dir
		for i := 1; i < dots; i++ {
			base = pyParent(base)
		}
		if t, ok := s.lookupMember(ctx, base, segs, true); ok {
			return t
		}
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	if len(segs) == 0 {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	roots := pyRoots(ctx, dir)
	for _, root := range roots {
		if t, ok := s.lookupMember(ctx, root, segs, false); ok {
			return t
		}
	}
	// A missing submodule of an in-repo top-level package is unresolved
	// rather than external.
	for _, root := range roots {
		top := path.Join(root, segs[0])
		if ctx.FileExists(top+".py") || pyIsPackage(ctx, top) {
			return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
		}
	}
	return model.ImportTarget{Kind: model.TargetExternal, Module: spec}
}

// lookupMember finds segs under base as a module, or else its parent
// module (segs naming a member of it). The parent may be base itself only
// for relative imports.
func (s pythonSpec) lookupMember(ctx model.RepoContext, base string, segs []string, relative bool) (model.ImportTarget, bool) {
	if t, ok := s.lookupModule(ctx, base, segs); ok {
		return t, true
	}
	if len(segs) > 1 || len(segs) == 1 && relative {
		return s.lookupModule(ctx, base, segs[:len(segs)-1])
	}
	return model.ImportTarget{}, false
}

// lookupModule finds the module segs under base: a .py/.pyi file, a
// regular package (__init__), or a namespace package directory. Empty segs
// name base itself (relative "from . import x").
func (s pythonSpec) lookupModule(ctx model.RepoContext, base string, segs []string) (model.ImportTarget, bool) {
	p := path.Join(append([]string{base}, segs...)...)
	if len(segs) > 0 {
		for _, ext := range []string{".py", ".pyi"} {
			if ctx.FileExists(p + ext) {
				return s.fileTarget(ctx, p+ext), true
			}
		}
	}
	for _, init := range []string{"__init__.py", "__init__.pyi"} {
		if f := path.Join(p, init); ctx.FileExists(f) {
			return s.fileTarget(ctx, f), true
		}
	}
	if ctx.DirExists(p) {
		return model.ImportTarget{Kind: model.TargetModule, Path: p,
			Module: s.ModuleOf(path.Join(p, "__init__.py"), nil, ctx)}, true
	}
	return model.ImportTarget{}, false
}

func (s pythonSpec) fileTarget(ctx model.RepoContext, f string) model.ImportTarget {
	return model.ImportTarget{Kind: model.TargetFile, Path: f, Module: s.ModuleOf(f, nil, ctx)}
}

// pyRoots lists the absolute-import search roots for a file in dir.
func pyRoots(ctx model.RepoContext, dir string) []string {
	var roots []string
	add := func(r string) {
		for _, x := range roots {
			if x == r {
				return
			}
		}
		roots = append(roots, r)
	}
	d := dir
	for d != "" && pyIsPackage(ctx, d) {
		d = pyParent(d)
	}
	add(d)
	if proj, ok := pyProjectRoot(ctx, dir); ok {
		if src := path.Join(proj, "src"); ctx.DirExists(src) {
			add(src)
		}
		add(proj)
	}
	add("")
	return roots
}

// pyProjectRoot is the nearest directory at or above dir with a project
// marker file.
func pyProjectRoot(ctx model.RepoContext, dir string) (string, bool) {
	for d := dir; ; d = pyParent(d) {
		for _, m := range pyProjectMarkers {
			if ctx.FileExists(path.Join(d, m)) {
				return d, true
			}
		}
		if d == "" {
			return "", false
		}
	}
}

func pyIsPackage(ctx model.RepoContext, dir string) bool {
	return ctx.FileExists(path.Join(dir, "__init__.py")) || ctx.FileExists(path.Join(dir, "__init__.pyi"))
}

// pyDirStem splits a clean path into its directory and module stem.
func pyDirStem(p string) (dir, stem string) {
	dir = model.CleanRel(path.Dir(p))
	stem = path.Base(p)
	if i := strings.LastIndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	return dir, stem
}

func pyParent(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

// pyUnder reports whether dir is base or inside it.
func pyUnder(dir, base string) bool {
	return dir == base || strings.HasPrefix(dir, base+"/")
}

func pySegments(rel string) []string {
	if rel == "" {
		return nil
	}
	return strings.Split(rel, "/")
}

func pyJoinModule(pkgs, parts []string) string {
	m := strings.Join(append(append([]string(nil), pkgs...), parts...), ".")
	if m == "" {
		return "."
	}
	return m
}
