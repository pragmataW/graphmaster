package parse

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/pragmataW/graphmaster/internal/model"
)

// The registry maps languages and file extensions to their specs. Language
// files (lang_<x>.go) call Register from init(), so adding a language never
// touches shared code.
var registry = struct {
	sync.RWMutex
	byLang map[model.Lang]model.LanguageSpec
	byExt  map[string]model.LanguageSpec
}{
	byLang: map[model.Lang]model.LanguageSpec{},
	byExt:  map[string]model.LanguageSpec{},
}

// Register adds spec to the registry. It panics on an invalid or duplicate
// language, a duplicate extension, or an extension without a leading dot,
// since all of these are programming errors caught at init time.
func Register(spec model.LanguageSpec) {
	l := spec.Lang()
	if !l.Valid() {
		panic(fmt.Sprintf("parse: register: invalid language %v", l))
	}
	registry.Lock()
	defer registry.Unlock()
	if _, dup := registry.byLang[l]; dup {
		panic(fmt.Sprintf("parse: register: duplicate language %v", l))
	}
	exts := spec.Extensions()
	for _, e := range exts {
		if !strings.HasPrefix(e, ".") || len(e) < 2 {
			panic(fmt.Sprintf("parse: register %v: bad extension %q", l, e))
		}
		if other, dup := registry.byExt[e]; dup {
			panic(fmt.Sprintf("parse: register %v: extension %q already claimed by %v", l, e, other.Lang()))
		}
	}
	registry.byLang[l] = spec
	for _, e := range exts {
		registry.byExt[e] = spec
	}
}

// unregister removes a language; tests only.
func unregister(l model.Lang) {
	registry.Lock()
	defer registry.Unlock()
	spec, ok := registry.byLang[l]
	if !ok {
		return
	}
	delete(registry.byLang, l)
	for _, e := range spec.Extensions() {
		delete(registry.byExt, e)
	}
}

// Lookup returns the spec registered for l.
func Lookup(l model.Lang) (model.LanguageSpec, bool) {
	registry.RLock()
	defer registry.RUnlock()
	s, ok := registry.byLang[l]
	return s, ok
}

// ForExt returns the spec claiming ext (with leading dot). Exact case is
// tried first, then lowercase, mirroring model.LangFromExt.
func ForExt(ext string) (model.LanguageSpec, bool) {
	registry.RLock()
	defer registry.RUnlock()
	if s, ok := registry.byExt[ext]; ok {
		return s, true
	}
	s, ok := registry.byExt[strings.ToLower(ext)]
	return s, ok
}

// ForPath returns the spec for a file path by its extension.
func ForPath(p string) (model.LanguageSpec, bool) {
	base := path.Base(strings.ReplaceAll(p, `\`, "/"))
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 {
		return nil, false
	}
	return ForExt(base[dot:])
}

// Langs returns the registered languages in ascending order.
func Langs() []model.Lang {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]model.Lang, 0, len(registry.byLang))
	for l := range registry.byLang {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// IsTestPath reports whether path is a test file by the naming convention of
// its language (see TestPathMatcher). Unknown languages return false.
func IsTestPath(p string) bool {
	spec, ok := ForPath(p)
	if !ok {
		return false
	}
	m, ok := spec.(TestPathMatcher)
	return ok && m.IsTestPath(p)
}
