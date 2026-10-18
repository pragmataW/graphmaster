package parse

import (
	_ "embed"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/tailscale/hujson"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/pragmataW/graphmaster/internal/model"
)

//go:embed queries/typescript.scm
var typescriptQuery string

// typescriptSpec is the TypeScript language spec (.ts/.mts/.cts). Module
// identity is the file; imports resolve through relative paths, the
// nearest tsconfig.json (baseUrl/paths, following extends) and package.json
// workspaces. Bare npm specs are external.
type typescriptSpec struct{ jsFamily }

func init() { Register(typescriptSpec{}) }

func (typescriptSpec) Lang() model.Lang        { return model.LangTypeScript }
func (typescriptSpec) Extensions() []string    { return model.LangTypeScript.Extensions() }
func (typescriptSpec) Grammar() unsafe.Pointer { return tsts.LanguageTypescript() }
func (typescriptSpec) QuerySource() string     { return typescriptQuery }
func (typescriptSpec) ExtractorVersion() int   { return 2 }

// ResolveImport implements model.LanguageSpec (see jsResolveImport).
func (typescriptSpec) ResolveImport(spec, fromFile string, ctx model.RepoContext) model.ImportTarget {
	return jsResolveImport(spec, fromFile, ctx, true)
}

// Extension probe orders: TypeScript sources prefer .ts over compiled .js
// siblings, JavaScript sources the reverse.
var (
	tsProbeExts = []string{".ts", ".tsx", ".d.ts", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".json"}
	jsProbeExts = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".d.ts", ".mts", ".cts", ".json"}
	// jsToTSExt maps an ESM-style ".js" spec to the TS source it compiles from.
	jsToTSExt = map[string][]string{
		".js": {".ts", ".tsx"}, ".jsx": {".tsx"}, ".mjs": {".mts"}, ".cjs": {".cts"},
	}
)

// jsResolveImport resolves a JS/TS module specifier written in fromFile:
//
//   - relative ("./x", "../y") and absolute-from-root specs probe the file,
//     its extensions, a directory's package.json entry and index.*;
//   - other specs try the nearest tsconfig.json / jsconfig.json (paths, then
//     baseUrl) and package.json workspaces;
//   - anything else is an external package (Module = package name).
//
// A spec matched by a tsconfig paths pattern that names no file is
// unresolved rather than external.
func jsResolveImport(spec, fromFile string, ctx model.RepoContext, preferTS bool) model.ImportTarget {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexAny(spec, "?#"); i > 0 && jsIsRelative(spec) {
		spec = spec[:i]
	}
	if spec == "" {
		return model.ImportTarget{Kind: model.TargetUnresolved}
	}
	r := jsResolver{ctx: ctx, exts: jsProbeExts}
	if preferTS {
		r.exts = tsProbeExts
	}
	dir := model.CleanRel(path.Dir(model.CleanRel(fromFile)))
	if jsIsRelative(spec) {
		if ctx != nil {
			if base, ok := jsJoin(dir, spec); ok {
				if p, ok := r.probe(base, true); ok {
					return jsFileTarget(p)
				}
			}
		}
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	if strings.HasPrefix(spec, "/") || strings.Contains(spec, `\`) {
		return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
	}
	if ctx != nil {
		if p, matched := r.viaTSConfig(dir, spec); p != "" {
			return jsFileTarget(p)
		} else if matched {
			return model.ImportTarget{Kind: model.TargetUnresolved, Module: spec}
		}
		if p := r.viaWorkspaces(dir, spec); p != "" {
			return jsFileTarget(p)
		}
	}
	name, _ := jsSplitPackage(spec)
	return model.ImportTarget{Kind: model.TargetExternal, Module: name}
}

func jsFileTarget(p string) model.ImportTarget {
	return model.ImportTarget{Kind: model.TargetFile, Path: p, Module: p}
}

func jsIsRelative(spec string) bool {
	return spec == "." || spec == ".." || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../")
}

// jsJoin joins a repo-relative dir and a relative spec, rejecting paths
// that escape the repository.
func jsJoin(dir, rel string) (string, bool) {
	j := path.Join(dir, rel)
	if j == ".." || strings.HasPrefix(j, "../") || strings.HasPrefix(j, "/") {
		return "", false
	}
	return model.CleanRel(j), true
}

// jsSplitPackage splits a bare spec into package name and subpath:
// "@scope/pkg/a/b" -> ("@scope/pkg", "a/b"), "lodash/fp" -> ("lodash", "fp").
func jsSplitPackage(spec string) (name, sub string) {
	parts := strings.SplitN(spec, "/", 3)
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		name = parts[0] + "/" + parts[1]
		if len(parts) == 3 {
			sub = parts[2]
		}
		return name, sub
	}
	name, sub, _ = strings.Cut(spec, "/")
	return name, sub
}

// jsResolver probes candidate files through a RepoContext.
type jsResolver struct {
	ctx  model.RepoContext
	exts []string
}

// probe finds the file a module path denotes: the path itself, with an
// extension, a directory's package.json entry (when pkgJSON) or its index.
func (r jsResolver) probe(base string, pkgJSON bool) (string, bool) {
	if base != "" && path.Ext(base) != "" {
		ext := path.Ext(base)
		for _, alt := range jsToTSExt[ext] { // ESM "./x.js" written for x.ts
			if p := strings.TrimSuffix(base, ext) + alt; r.ctx.FileExists(p) {
				return p, true
			}
		}
		if r.ctx.FileExists(base) {
			return base, true
		}
	}
	for _, ext := range r.exts {
		if p := base + ext; base != "" && r.ctx.FileExists(p) {
			return p, true
		}
	}
	if !r.ctx.DirExists(base) {
		return "", false
	}
	if pkgJSON {
		if p, ok := r.packageEntry(base); ok {
			return p, true
		}
	}
	for _, ext := range r.exts {
		if p := path.Join(base, "index"+ext); r.ctx.FileExists(p) {
			return p, true
		}
	}
	return "", false
}

// packageEntry resolves a package directory through its package.json
// "types", "typings", "source", "module" or "main" field.
func (r jsResolver) packageEntry(dir string) (string, bool) {
	pj, ok := jsReadPackageJSON(r.ctx, path.Join(dir, "package.json"))
	if !ok {
		return "", false
	}
	for _, field := range []string{pj.Types, pj.Typings, pj.Source, pj.Module, pj.Main} {
		if field == "" {
			continue
		}
		if p, ok := jsJoin(dir, field); ok && p != dir {
			if p, ok := r.probe(p, false); ok {
				return p, true
			}
		}
	}
	return "", false
}

// ---------------------------------------------------------------- tsconfig

// tsConfig is the merged compilerOptions subset graphmaster uses.
type tsConfig struct {
	baseURL  string // repo-relative directory; valid when hasBase
	hasBase  bool
	paths    map[string][]string
	pathsDir string // directory paths entries are relative to
}

type tsConfigFile struct {
	Extends         json.RawMessage `json:"extends"`
	CompilerOptions struct {
		BaseURL *string             `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

// viaTSConfig resolves spec through the nearest tsconfig.json or
// jsconfig.json. matched reports that a paths pattern matched.
func (r jsResolver) viaTSConfig(dir, spec string) (file string, matched bool) {
	cfgPath, ok := jsFindConfig(r.ctx, dir)
	if !ok {
		return "", false
	}
	cfg := loadTSConfig(r.ctx, cfgPath)
	if len(cfg.paths) > 0 {
		if subs, ok := tsMatchPaths(cfg.paths, spec); ok {
			for _, s := range subs {
				if p, ok := jsJoin(cfg.pathsDir, s); ok {
					if p, ok := r.probe(p, true); ok {
						return p, true
					}
				}
			}
			return "", true
		}
	}
	if cfg.hasBase {
		if p, ok := jsJoin(cfg.baseURL, spec); ok {
			if p, ok := r.probe(p, true); ok {
				return p, false
			}
		}
	}
	return "", false
}

// jsFindConfig returns the nearest tsconfig.json or jsconfig.json at or
// above dir.
func jsFindConfig(ctx model.RepoContext, dir string) (string, bool) {
	for {
		for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
			if p := path.Join(dir, name); ctx.FileExists(p) {
				return p, true
			}
		}
		if dir == "" {
			return "", false
		}
		dir = model.CleanRel(path.Dir(dir))
	}
}

// tsMatchPaths returns the substituted targets of the best matching paths
// pattern: an exact key, else the wildcard pattern with the longest prefix.
func tsMatchPaths(paths map[string][]string, spec string) ([]string, bool) {
	if t, ok := paths[spec]; ok && !strings.Contains(spec, "*") {
		return t, true
	}
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic tie-breaking
	best, bestLen, star := "", -1, ""
	for _, k := range keys {
		prefix, suffix, ok := strings.Cut(k, "*")
		if !ok || !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) ||
			len(spec) < len(prefix)+len(suffix) {
			continue
		}
		if len(prefix) > bestLen {
			best, bestLen, star = k, len(prefix), spec[len(prefix):len(spec)-len(suffix)]
		}
	}
	if bestLen < 0 {
		return nil, false
	}
	out := make([]string, 0, len(paths[best]))
	for _, t := range paths[best] {
		out = append(out, strings.Replace(t, "*", star, 1))
	}
	return out, true
}

// tsConfigCache memoizes parsed config files by path and content, so edits
// are always observed while repeated lookups stay cheap.
var tsConfigCache = struct {
	sync.Mutex
	m map[string]*tsConfigFile
}{m: map[string]*tsConfigFile{}}

const tsConfigCacheMax = 512

func parseTSConfigFile(ctx model.RepoContext, p string) (*tsConfigFile, bool) {
	b, err := ctx.ReadFile(p)
	if err != nil {
		return nil, false
	}
	key := p + "\x00" + string(b)
	tsConfigCache.Lock()
	c, ok := tsConfigCache.m[key]
	tsConfigCache.Unlock()
	if ok {
		return c, c != nil
	}
	c = &tsConfigFile{}
	std, err := hujson.Standardize(b)
	if err != nil || json.Unmarshal(std, c) != nil {
		c = nil
	}
	tsConfigCache.Lock()
	if len(tsConfigCache.m) >= tsConfigCacheMax {
		clear(tsConfigCache.m)
	}
	tsConfigCache.m[key] = c
	tsConfigCache.Unlock()
	return c, c != nil
}

// loadTSConfig reads cfgPath and its in-repo "extends" chain; options of a
// config override those it extends.
func loadTSConfig(ctx model.RepoContext, cfgPath string) tsConfig {
	var chain []string // most derived first
	seen := map[string]bool{}
	files := map[string]*tsConfigFile{}
	for p := cfgPath; p != "" && !seen[p] && len(chain) < 16; {
		seen[p] = true
		c, ok := parseTSConfigFile(ctx, p)
		if !ok {
			break
		}
		chain = append(chain, p)
		files[p] = c
		p = tsExtendsPath(ctx, p, c.Extends)
	}
	var cfg tsConfig
	for i := len(chain) - 1; i >= 0; i-- { // base first, derived last
		p := chain[i]
		c := files[p]
		dir := model.CleanRel(path.Dir(p))
		if b := c.CompilerOptions.BaseURL; b != nil {
			if d, ok := jsJoin(dir, *b); ok {
				cfg.baseURL, cfg.hasBase = d, true
			}
		}
		if c.CompilerOptions.Paths != nil {
			cfg.paths, cfg.pathsDir = c.CompilerOptions.Paths, dir
		}
	}
	if cfg.hasBase && cfg.paths != nil {
		cfg.pathsDir = cfg.baseURL // paths are relative to baseUrl when set
	}
	return cfg
}

// tsExtendsPath resolves the first in-repo config named by "extends" (a
// string or, since TS 5.0, an array). Package configs are not followed.
func tsExtendsPath(ctx model.RepoContext, from string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var specs []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		specs = []string{one}
	} else if json.Unmarshal(raw, &specs) != nil {
		return ""
	}
	dir := model.CleanRel(path.Dir(from))
	for i := len(specs) - 1; i >= 0; i-- { // later entries take precedence
		s := specs[i]
		if !jsIsRelative(s) {
			continue
		}
		p, ok := jsJoin(dir, s)
		if !ok {
			continue
		}
		for _, cand := range []string{p, p + ".json", path.Join(p, "tsconfig.json")} {
			if ctx.FileExists(cand) {
				return cand
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------- workspaces

type jsPackageJSON struct {
	Name       string          `json:"name"`
	Main       string          `json:"main"`
	Module     string          `json:"module"`
	Types      string          `json:"types"`
	Typings    string          `json:"typings"`
	Source     string          `json:"source"`
	Workspaces json.RawMessage `json:"workspaces"`
}

func jsReadPackageJSON(ctx model.RepoContext, p string) (jsPackageJSON, bool) {
	var pj jsPackageJSON
	b, err := ctx.ReadFile(p)
	if err != nil {
		return pj, false
	}
	if json.Unmarshal(b, &pj) != nil {
		return pj, false
	}
	return pj, true
}

// workspacePatterns decodes "workspaces": [..] or {"packages": [..]}.
func workspacePatterns(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Packages
	}
	return nil
}

// viaWorkspaces resolves a bare spec naming a package of the nearest
// enclosing npm/yarn/pnpm-style workspace (package.json "workspaces").
// Wildcard patterns ("packages/*") are matched by the conventional
// directory name of the package.
func (r jsResolver) viaWorkspaces(dir, spec string) string {
	name, sub := jsSplitPackage(spec)
	for d := dir; ; d = model.CleanRel(path.Dir(d)) {
		if pj, ok := jsReadPackageJSON(r.ctx, path.Join(d, "package.json")); ok {
			if pats := workspacePatterns(pj.Workspaces); len(pats) > 0 {
				if pkgDir := r.findWorkspacePackage(d, pats, name); pkgDir != "" {
					return r.packageFile(pkgDir, sub)
				}
				return ""
			}
		}
		if d == "" {
			return ""
		}
	}
}

func (r jsResolver) findWorkspacePackage(root string, patterns []string, name string) string {
	short := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		short = name[i+1:]
	}
	flat := strings.ReplaceAll(strings.TrimPrefix(name, "@"), "/", "-")
	for _, pat := range patterns {
		pat = strings.TrimSuffix(strings.TrimPrefix(pat, "./"), "/")
		if strings.HasPrefix(pat, "!") {
			continue
		}
		var cands []string
		if i := strings.IndexByte(pat, '*'); i >= 0 {
			parent := strings.TrimSuffix(pat[:i], "/")
			cands = []string{path.Join(parent, short), path.Join(parent, flat), path.Join(parent, name)}
		} else {
			cands = []string{pat}
		}
		for _, c := range cands {
			d, ok := jsJoin(root, c)
			if !ok {
				continue
			}
			if pj, ok := jsReadPackageJSON(r.ctx, path.Join(d, "package.json")); ok && pj.Name == name {
				return d
			}
		}
	}
	return ""
}

// packageFile resolves a subpath (or the entry point) of a workspace
// package directory.
func (r jsResolver) packageFile(pkgDir, sub string) string {
	if sub != "" {
		for _, base := range []string{pkgDir, path.Join(pkgDir, "src")} {
			if p, ok := jsJoin(base, sub); ok {
				if p, ok := r.probe(p, true); ok {
					return p
				}
			}
		}
		return ""
	}
	if p, ok := r.probe(pkgDir, true); ok {
		return p
	}
	if p, ok := r.probe(path.Join(pkgDir, "src", "index"), false); ok {
		return p
	}
	return ""
}
