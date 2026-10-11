package testutil

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/pragmataW/graphmaster/internal/model"
)

// synthLang renders one language's synthetic sources and support files.
type synthLang interface {
	sourcePath(pkg, file int) string
	// baseHeader and fnLines estimate line counts so Synth can pick a
	// function count that lands close to the requested LOC. fnLines
	// includes the worst-case import lines a function can add.
	baseHeader() int
	fnLines() int
	comment() string
	render(b *bytes.Buffer, p *synthPlan)
	// support returns manifests, headers and package-init files for a
	// language whose packages hold sizes[q] files with nf functions each.
	support(sizes []int, nf int) []SynthFile
}

var synthLangs = map[model.Lang]synthLang{
	model.LangGo:         synthGo{},
	model.LangPython:     synthPython{},
	model.LangTypeScript: synthTS{dir: "ts", ext: ".ts", typed: true},
	model.LangTSX:        synthTS{dir: "tsx", ext: ".tsx", typed: true},
	model.LangJavaScript: synthTS{dir: "js", ext: ".js", importExt: ".js"},
	model.LangRust:       synthRust{},
	model.LangJava:       synthJava{},
	model.LangC:          synthC{},
	model.LangCPP:        synthCPP{},
	model.LangCSharp:     synthCSharp{},
	model.LangPHP:        synthPHP{},
}

// printer is a tiny line writer.
type printer struct{ b *bytes.Buffer }

func (w printer) f(format string, args ...any) {
	fmt.Fprintf(w.b, format, args...)
	w.b.WriteByte('\n')
}

func pkgName(q int) string  { return fmt.Sprintf("p%03d", q) }
func fileName(j int) string { return fmt.Sprintf("m%04d", j) }
func className(j int) string {
	return fmt.Sprintf("C%04d", j)
}

// pkgFns lists every C/C++ function name defined in package q.
func pkgFns(q, size, nf int) []string {
	var out []string
	for j := q * synthPkgSize; j < q*synthPkgSize+size; j++ {
		for k := range nf {
			out = append(out, pyFn(j, k))
		}
	}
	return out
}

func synthFile(path, content string) SynthFile {
	return SynthFile{Path: path, Content: []byte(content)}
}

// ---------------------------------------------------------------- Go

type synthGo struct{}

func goFn(file, fn int) string { return fmt.Sprintf("F%d_%d", file, fn) }

func (synthGo) sourcePath(pkg, j int) string {
	return fmt.Sprintf("go/%s/%s.go", pkgName(pkg), fileName(j))
}
func (synthGo) baseHeader() int { return 6 }
func (synthGo) fnLines() int    { return 9 }
func (synthGo) comment() string { return "//" }

func (synthGo) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f("// Synthetic file %s for graphmaster benchmarks.", fileName(p.file))
	w.f("package %s", pkgName(p.pkg))
	w.f("")
	if deps := p.depPkgs(); len(deps) > 0 {
		w.f("import (")
		for _, q := range deps {
			w.f("\t%q", "synth/"+pkgName(q))
		}
		w.f(")")
		w.f("")
	}
	for _, fn := range p.fns {
		w.f("// %s is synthetic.", goFn(p.file, fn.idx))
		w.f("func %s(n int) int {", goFn(p.file, fn.idx))
		w.f("\tx := n * 3")
		for _, c := range fn.calls {
			qual := ""
			if c.pkg != p.pkg {
				qual = pkgName(c.pkg) + "."
			}
			w.f("\tx += %s%s(n - 1)", qual, goFn(c.file, c.fn))
		}
		w.f("\treturn x")
		w.f("}")
		w.f("")
	}
}

func (synthGo) support([]int, int) []SynthFile {
	return []SynthFile{synthFile("go/go.mod", "module synth\n\ngo 1.25\n")}
}

// ---------------------------------------------------------------- Python

type synthPython struct{}

func pyFn(file, fn int) string { return fmt.Sprintf("f%d_%d", file, fn) }

func (synthPython) sourcePath(pkg, j int) string {
	return fmt.Sprintf("py/synth/%s/%s.py", pkgName(pkg), fileName(j))
}
func (synthPython) baseHeader() int { return 3 }
func (synthPython) fnLines() int    { return 9 }
func (synthPython) comment() string { return "#" }

func (synthPython) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f(`"""Synthetic module %s."""`, fileName(p.file))
	w.f("")
	for _, d := range p.deps() {
		if d.pkg == p.pkg {
			w.f("from . import %s", fileName(d.file))
		} else {
			w.f("from synth.%s import %s", pkgName(d.pkg), fileName(d.file))
		}
	}
	for _, fn := range p.fns {
		w.f("")
		w.f("")
		w.f("def %s(n):", pyFn(p.file, fn.idx))
		w.f("    x = n * 3")
		for _, c := range fn.calls {
			if p.local(c) {
				w.f("    x += %s(n - 1)", pyFn(c.file, c.fn))
			} else {
				w.f("    x += %s.%s(n - 1)", fileName(c.file), pyFn(c.file, c.fn))
			}
		}
		w.f("    return x")
	}
}

func (synthPython) support(sizes []int, _ int) []SynthFile {
	out := []SynthFile{
		synthFile("py/pyproject.toml", "[project]\nname = \"synth\"\nversion = \"0.0.0\"\n"),
		synthFile("py/synth/__init__.py", ""),
	}
	for q := range sizes {
		out = append(out, synthFile(fmt.Sprintf("py/synth/%s/__init__.py", pkgName(q)), ""))
	}
	return out
}

// ---------------------------------------------------------------- TS / TSX / JS

// synthTS covers TypeScript, TSX and ESM JavaScript, which differ only in
// extensions, type annotations and import specifiers.
type synthTS struct {
	dir, ext  string
	importExt string // appended to import specifiers (".js" for node ESM)
	typed     bool
}

func (s synthTS) sourcePath(pkg, j int) string {
	return fmt.Sprintf("%s/src/%s/%s%s", s.dir, pkgName(pkg), fileName(j), s.ext)
}
func (synthTS) baseHeader() int { return 2 }
func (synthTS) fnLines() int    { return 9 }
func (synthTS) comment() string { return "//" }

func (s synthTS) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f("// Synthetic module %s.", fileName(p.file))
	// Collect the named imports per dependency file.
	names := map[synthRef][]string{}
	for _, fn := range p.fns {
		for _, c := range fn.calls {
			if !p.local(c) {
				key := synthRef{pkg: c.pkg, file: c.file, fn: -1}
				if n := pyFn(c.file, c.fn); !slices.Contains(names[key], n) {
					names[key] = append(names[key], n)
				}
			}
		}
	}
	for _, d := range p.deps() {
		spec := "./" + fileName(d.file)
		if d.pkg != p.pkg {
			spec = "../" + pkgName(d.pkg) + "/" + fileName(d.file)
		}
		list := names[d]
		slices.Sort(list)
		w.f("import { %s } from %q;", strings.Join(list, ", "), spec+s.importExt)
	}
	sig := "(n)"
	if s.typed {
		sig = "(n: number): number"
	}
	for _, fn := range p.fns {
		w.f("")
		w.f("export function %s%s {", pyFn(p.file, fn.idx), sig)
		w.f("  let x = n * 3;")
		for _, c := range fn.calls {
			w.f("  x += %s(n - 1);", pyFn(c.file, c.fn))
		}
		w.f("  return x;")
		w.f("}")
	}
}

func (s synthTS) support([]int, int) []SynthFile {
	if !s.typed {
		return []SynthFile{synthFile(s.dir+"/package.json", "{\n  \"name\": \"synth-js\",\n  \"type\": \"module\"\n}\n")}
	}
	return []SynthFile{synthFile(s.dir+"/tsconfig.json",
		"{\n  \"compilerOptions\": { \"strict\": true, \"jsx\": \"react-jsx\", \"module\": \"ESNext\" },\n  \"include\": [\"src\"]\n}\n")}
}

// ---------------------------------------------------------------- Rust

type synthRust struct{}

func (synthRust) sourcePath(pkg, j int) string {
	return fmt.Sprintf("rust/src/%s/%s.rs", pkgName(pkg), fileName(j))
}
func (synthRust) baseHeader() int { return 3 }
func (synthRust) fnLines() int    { return 9 }
func (synthRust) comment() string { return "//" }

func (synthRust) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f("//! Synthetic module %s.", fileName(p.file))
	w.f("")
	for _, d := range p.deps() {
		if d.pkg == p.pkg {
			w.f("use super::%s;", fileName(d.file))
		} else {
			w.f("use crate::%s::%s;", pkgName(d.pkg), fileName(d.file))
		}
	}
	for _, fn := range p.fns {
		w.f("")
		w.f("pub fn %s(n: i64) -> i64 {", pyFn(p.file, fn.idx))
		w.f("    let mut x = n * 3;")
		for _, c := range fn.calls {
			if p.local(c) {
				w.f("    x += %s(n - 1);", pyFn(c.file, c.fn))
			} else {
				w.f("    x += %s::%s(n - 1);", fileName(c.file), pyFn(c.file, c.fn))
			}
		}
		w.f("    x")
		w.f("}")
	}
}

func (synthRust) support(sizes []int, _ int) []SynthFile {
	var lib strings.Builder
	lib.WriteString("//! Synthetic crate root.\n\n")
	out := []SynthFile{synthFile("rust/Cargo.toml", "[package]\nname = \"synth\"\nversion = \"0.0.0\"\nedition = \"2021\"\n")}
	for q, size := range sizes {
		fmt.Fprintf(&lib, "pub mod %s;\n", pkgName(q))
		var mod strings.Builder
		for j := q * synthPkgSize; j < q*synthPkgSize+size; j++ {
			fmt.Fprintf(&mod, "pub mod %s;\n", fileName(j))
		}
		out = append(out, synthFile(fmt.Sprintf("rust/src/%s/mod.rs", pkgName(q)), mod.String()))
	}
	return append(out, synthFile("rust/src/lib.rs", lib.String()))
}

// ---------------------------------------------------------------- Java

type synthJava struct{}

func (synthJava) sourcePath(pkg, j int) string {
	return fmt.Sprintf("java/src/synth/%s/%s.java", pkgName(pkg), className(j))
}
func (synthJava) baseHeader() int { return 7 }
func (synthJava) fnLines() int    { return 9 }
func (synthJava) comment() string { return "//" }

func (synthJava) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f("package synth.%s;", pkgName(p.pkg))
	w.f("")
	imports := false
	for _, d := range p.deps() {
		if d.pkg != p.pkg {
			w.f("import synth.%s.%s;", pkgName(d.pkg), className(d.file))
			imports = true
		}
	}
	if imports {
		w.f("")
	}
	w.f("/** Synthetic class %d. */", p.file)
	w.f("public final class %s {", className(p.file))
	w.f("    private %s() {}", className(p.file))
	for _, fn := range p.fns {
		w.f("")
		w.f("    public static int m%d(int n) {", fn.idx)
		w.f("        int x = n * 3;")
		for _, c := range fn.calls {
			if p.local(c) {
				w.f("        x += m%d(n - 1);", c.fn)
			} else {
				w.f("        x += %s.m%d(n - 1);", className(c.file), c.fn)
			}
		}
		w.f("        return x;")
		w.f("    }")
	}
	w.f("}")
}

func (synthJava) support([]int, int) []SynthFile { return nil }

// ---------------------------------------------------------------- C

type synthC struct{}

func (synthC) sourcePath(pkg, j int) string {
	return fmt.Sprintf("c/%s/%s.c", pkgName(pkg), fileName(j))
}
func (synthC) baseHeader() int { return 3 }
func (synthC) fnLines() int    { return 10 }
func (synthC) comment() string { return "//" }

func (synthC) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f("/* Synthetic translation unit %s. */", fileName(p.file))
	w.f("#include %q", pkgName(p.pkg)+".h")
	for _, q := range p.depPkgs() {
		w.f("#include %q", "../"+pkgName(q)+"/"+pkgName(q)+".h")
	}
	for _, fn := range p.fns {
		w.f("")
		w.f("int %s(int n) {", pyFn(p.file, fn.idx))
		w.f("    if (n <= 0) return 0;")
		w.f("    int x = n * 3;")
		for _, c := range fn.calls {
			w.f("    x += %s(n - 1);", pyFn(c.file, c.fn))
		}
		w.f("    return x;")
		w.f("}")
	}
}

func (synthC) support(sizes []int, nf int) []SynthFile {
	var out []SynthFile
	for q, size := range sizes {
		guard := strings.ToUpper("SYNTH_" + pkgName(q) + "_H")
		var h strings.Builder
		fmt.Fprintf(&h, "#ifndef %s\n#define %s\n\n", guard, guard)
		for _, fn := range pkgFns(q, size, nf) {
			fmt.Fprintf(&h, "int %s(int n);\n", fn)
		}
		fmt.Fprintf(&h, "\n#endif /* %s */\n", guard)
		out = append(out, synthFile(fmt.Sprintf("c/%s/%s.h", pkgName(q), pkgName(q)), h.String()))
	}
	return out
}

// ---------------------------------------------------------------- C++

type synthCPP struct{}

func (synthCPP) sourcePath(pkg, j int) string {
	return fmt.Sprintf("cpp/%s/%s.cpp", pkgName(pkg), fileName(j))
}
func (synthCPP) baseHeader() int { return 7 }
func (synthCPP) fnLines() int    { return 10 }
func (synthCPP) comment() string { return "//" }

func (synthCPP) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f("// Synthetic translation unit %s.", fileName(p.file))
	w.f("#include %q", pkgName(p.pkg)+".hpp")
	for _, q := range p.depPkgs() {
		w.f("#include %q", "../"+pkgName(q)+"/"+pkgName(q)+".hpp")
	}
	w.f("")
	w.f("namespace %s {", pkgName(p.pkg))
	for _, fn := range p.fns {
		w.f("")
		w.f("int %s(int n) {", pyFn(p.file, fn.idx))
		w.f("    if (n <= 0) return 0;")
		w.f("    int x = n * 3;")
		for _, c := range fn.calls {
			qual := ""
			if c.pkg != p.pkg {
				qual = pkgName(c.pkg) + "::"
			}
			w.f("    x += %s%s(n - 1);", qual, pyFn(c.file, c.fn))
		}
		w.f("    return x;")
		w.f("}")
	}
	w.f("")
	w.f("}  // namespace %s", pkgName(p.pkg))
}

func (synthCPP) support(sizes []int, nf int) []SynthFile {
	var out []SynthFile
	for q, size := range sizes {
		var h strings.Builder
		fmt.Fprintf(&h, "#pragma once\n\nnamespace %s {\n\n", pkgName(q))
		for _, fn := range pkgFns(q, size, nf) {
			fmt.Fprintf(&h, "int %s(int n);\n", fn)
		}
		fmt.Fprintf(&h, "\n}  // namespace %s\n", pkgName(q))
		out = append(out, synthFile(fmt.Sprintf("cpp/%s/%s.hpp", pkgName(q), pkgName(q)), h.String()))
	}
	return out
}

// ---------------------------------------------------------------- C#

type synthCSharp struct{}

func csNS(q int) string { return fmt.Sprintf("Synth.P%03d", q) }

func (synthCSharp) sourcePath(pkg, j int) string {
	return fmt.Sprintf("cs/P%03d/%s.cs", pkg, className(j))
}
func (synthCSharp) baseHeader() int { return 6 }
func (synthCSharp) fnLines() int    { return 9 }
func (synthCSharp) comment() string { return "//" }

func (synthCSharp) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	deps := p.depPkgs()
	for _, q := range deps {
		w.f("using %s;", csNS(q))
	}
	if len(deps) > 0 {
		w.f("")
	}
	w.f("namespace %s", csNS(p.pkg))
	w.f("{")
	w.f("    public static class %s", className(p.file))
	w.f("    {")
	for i, fn := range p.fns {
		if i > 0 {
			w.f("")
		}
		w.f("        public static int M%d(int n)", fn.idx)
		w.f("        {")
		w.f("            int x = n * 3;")
		for _, c := range fn.calls {
			if p.local(c) {
				w.f("            x += M%d(n - 1);", c.fn)
			} else {
				w.f("            x += %s.M%d(n - 1);", className(c.file), c.fn)
			}
		}
		w.f("            return x;")
		w.f("        }")
	}
	w.f("    }")
	w.f("}")
}

func (synthCSharp) support([]int, int) []SynthFile {
	return []SynthFile{synthFile("cs/Synth.csproj",
		"<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n    <TargetFramework>net8.0</TargetFramework>\n    <RootNamespace>Synth</RootNamespace>\n  </PropertyGroup>\n</Project>\n")}
}

// ---------------------------------------------------------------- PHP

type synthPHP struct{}

func (synthPHP) sourcePath(pkg, j int) string {
	return fmt.Sprintf("php/src/P%03d/%s.php", pkg, className(j))
}
func (synthPHP) baseHeader() int { return 9 }
func (synthPHP) fnLines() int    { return 10 }
func (synthPHP) comment() string { return "//" }

func (synthPHP) render(b *bytes.Buffer, p *synthPlan) {
	w := printer{b}
	w.f("<?php")
	w.f("")
	w.f("declare(strict_types=1);")
	w.f("")
	w.f(`namespace Synth\P%03d;`, p.pkg)
	w.f("")
	uses := false
	for _, d := range p.deps() {
		if d.pkg != p.pkg {
			w.f(`use Synth\P%03d\%s;`, d.pkg, className(d.file))
			uses = true
		}
	}
	if uses {
		w.f("")
	}
	w.f("final class %s", className(p.file))
	w.f("{")
	for i, fn := range p.fns {
		if i > 0 {
			w.f("")
		}
		w.f("    public static function m%d(int $n): int", fn.idx)
		w.f("    {")
		w.f("        $x = $n * 3;")
		for _, c := range fn.calls {
			if p.local(c) {
				w.f("        $x += self::m%d($n - 1);", c.fn)
			} else {
				w.f("        $x += %s::m%d($n - 1);", className(c.file), c.fn)
			}
		}
		w.f("        return $x;")
		w.f("    }")
	}
	w.f("}")
}

func (synthPHP) support([]int, int) []SynthFile {
	return []SynthFile{synthFile("php/composer.json",
		"{\n    \"name\": \"synth/synth\",\n    \"autoload\": { \"psr-4\": { \"Synth\\\\\": \"src/\" } }\n}\n")}
}
