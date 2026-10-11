package parse

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pragmataW/graphmaster/internal/model"
)

// ---------------------------------------------------------------- imports

// isFromImport reports whether raw is the text of a from-import statement
// (as opposed to a dotted module name such as "fromage.x").
func isFromImport(raw string) bool {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "from") || len(raw) == 4 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(raw[4:])
	return !isPyIdentRune(r)
}

// normalizeFromImport strips comments and line continuations and collapses
// whitespace, so the statement fits on one line.
func normalizeFromImport(raw string) string {
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSuffix(strings.TrimRight(line, " \t\r"), `\`)
		b.WriteString(line)
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// pyImportedName is one name of a from-import.
type pyImportedName struct{ name, alias string }

// parseFromImport splits a normalized from-import into its module ("..m")
// and names. wildcard is set for "import *".
func parseFromImport(s string) (mod string, names []pyImportedName, wildcard, ok bool) {
	rest, found := strings.CutPrefix(s, "from")
	if !found {
		return "", nil, false, false
	}
	var mb strings.Builder
	for i := 0; ; {
		if i >= len(rest) {
			return "", nil, false, false
		}
		c := rest[i]
		switch c {
		case '.':
			mb.WriteByte(c)
			i++
			continue
		case ' ', '\t':
			i++
			continue
		}
		r, _ := utf8.DecodeRuneInString(rest[i:])
		if !isPyIdentRune(r) || unicode.IsDigit(r) {
			return "", nil, false, false
		}
		j := i
		for j < len(rest) {
			r, size := utf8.DecodeRuneInString(rest[j:])
			if !isPyIdentRune(r) {
				break
			}
			j += size
		}
		tok := rest[i:j]
		if tok == "import" {
			rest = rest[j:]
			break
		}
		mb.WriteString(tok)
		i = j
	}
	mod = mb.String()
	if mod == "" || strings.Contains(strings.TrimLeft(mod, "."), "..") || strings.HasSuffix(mod, ".") && strings.Trim(mod, ".") != "" {
		return "", nil, false, false
	}
	rest = strings.TrimSpace(rest)
	rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rest, "("), ")"))
	if rest == "*" {
		return mod, nil, true, true
	}
	for _, part := range strings.Split(rest, ",") {
		fs := strings.Fields(part)
		switch {
		case len(fs) == 0:
			continue
		case len(fs) == 1 && isPyIdent(fs[0]):
			names = append(names, pyImportedName{name: fs[0]})
		case len(fs) == 3 && fs[1] == "as" && isPyIdent(fs[0]) && isPyIdent(fs[2]):
			names = append(names, pyImportedName{name: fs[0], alias: fs[2]})
		default:
			return "", nil, false, false
		}
	}
	return mod, names, false, len(names) > 0
}

// joinPyModule appends name to a (possibly purely relative) module.
func joinPyModule(mod, name string) string {
	if strings.Trim(mod, ".") == "" {
		return mod + name
	}
	return mod + "." + name
}

// expandPyImports turns captured from-import statements into one Import
// per name, drops __future__ imports and malformed statements, and keeps
// plain imports.
func expandPyImports(imps []model.Import) []model.Import {
	var out []model.Import
	for _, im := range imps {
		if !strings.HasPrefix(im.Spec, "from") || !isFromImport(im.Spec) {
			if isPyDotted(im.Spec) {
				out = append(out, im)
			}
			continue
		}
		mod, names, wildcard, ok := parseFromImport(im.Spec)
		if !ok || mod == "__future__" {
			continue
		}
		if wildcard {
			out = append(out, model.Import{Spec: mod, Names: []string{"*"}, Wildcard: true, Line: im.Line})
			continue
		}
		for _, n := range names {
			alias := n.alias
			if alias == n.name {
				alias = ""
			}
			out = append(out, model.Import{Spec: joinPyModule(mod, n.name), Alias: alias,
				Names: []string{n.name}, Line: im.Line})
		}
	}
	return out
}

// ---------------------------------------------------------------- defs

var pyEnumBases = map[string]bool{"Enum": true, "IntEnum": true, "StrEnum": true, "Flag": true, "IntFlag": true}

// pyClassifyDefs refines kinds and flags: enum and protocol classes,
// methods and constructors, module constants, async/static/abstract.
func pyClassifyDefs(f *model.Facts) {
	for _, in := range f.Inherits {
		d := &f.Defs[in.Child]
		if d.Kind != model.KindClass {
			continue
		}
		switch {
		case pyEnumBases[in.Name]:
			d.Kind = model.KindEnum
		case in.Name == "Protocol":
			d.Kind = model.KindInterface
		}
	}
	for i := range f.Defs {
		d := &f.Defs[i]
		var parent model.NodeKind
		if d.Parent != model.NoParent {
			parent = f.Defs[d.Parent].Kind
		}
		switch {
		case d.Kind == model.KindFunction && parent.IsType():
			d.Kind = model.KindMethod
			if d.Name == "__init__" || d.Name == "__new__" {
				d.Kind = model.KindConstructor
			}
		case d.Kind == model.KindField && parent == model.KindEnum:
			d.Kind = model.KindEnumMember
		case d.Kind == model.KindVariable && d.Parent == model.NoParent && pyIsTypeVar(d.Sig):
			d.Kind = model.KindType
		case d.Kind == model.KindVariable && d.Parent == model.NoParent && isAllCaps(d.Name):
			d.Kind = model.KindConstant
		}
		if strings.Contains(d.Sig, "async def ") {
			d.Flags |= model.FlagAsync
		}
		if pyHasDecorator(d.Sig, "staticmethod", "classmethod") {
			d.Flags |= model.FlagStatic
		}
		if pyHasDecorator(d.Sig, "abstractmethod", "abc.abstractmethod") {
			d.Flags |= model.FlagAbstract
		}
	}
}

// pyHasDecorator reports whether sig starts with one of the decorators.
func pyHasDecorator(sig string, names ...string) bool {
	for _, tok := range strings.Fields(sig) {
		dec, ok := strings.CutPrefix(tok, "@")
		if !ok {
			return false // decorators precede def/class
		}
		for _, n := range names {
			if dec == n {
				return true
			}
		}
	}
	return false
}

// pyIsTypeVar reports whether an assignment sig defines a type variable
// (T = TypeVar("T"), P = ParamSpec("P"), ...).
func pyIsTypeVar(sig string) bool {
	_, rhs, ok := strings.Cut(sig, "=")
	if !ok {
		return false
	}
	rhs = strings.TrimSpace(rhs)
	for _, p := range []string{"TypeVar(", "ParamSpec(", "TypeVarTuple(", "NewType(", "typing.TypeVar(", "typing.NewType("} {
		if strings.HasPrefix(rhs, p) {
			return true
		}
	}
	return false
}

func isAllCaps(s string) bool {
	letter := false
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			return false
		case unicode.IsUpper(r):
			letter = true
		}
	}
	return letter
}

// ---------------------------------------------------------------- refs

// pyScope summarizes the names a file binds at module level.
type pyScope struct {
	aliases map[string]string // from-import alias -> imported name
	bound   map[string]bool   // names bound by imports (module roots, from-imported names)
	classes map[string]bool   // top-level types of this file
	defined map[string]bool   // every def name and import binding
}

func newPyScope(f *model.Facts) *pyScope {
	sc := &pyScope{aliases: map[string]string{}, bound: map[string]bool{},
		classes: map[string]bool{}, defined: map[string]bool{}}
	for _, im := range f.Imports {
		switch {
		case im.Wildcard:
		case len(im.Names) > 0:
			local := im.Names[0]
			if im.Alias != "" {
				local = im.Alias
				sc.aliases[im.Alias] = im.Names[0]
			}
			sc.bound[local] = true
			sc.defined[local] = true
		case im.Alias != "":
			sc.bound[im.Alias] = true
			sc.defined[im.Alias] = true
		default:
			root, _, _ := strings.Cut(im.Spec, ".")
			sc.bound[root] = true
			sc.defined[root] = true
		}
	}
	for _, d := range f.Defs {
		sc.defined[d.Name] = true
		if d.Parent == model.NoParent && d.Kind.IsType() {
			sc.classes[d.Name] = true
		}
	}
	return sc
}

// dealias rewrites the root of a dotted name bound by a from-import alias
// to the imported name.
func (sc *pyScope) dealias(dotted string) string {
	root, rest, dot := strings.Cut(dotted, ".")
	if orig, ok := sc.aliases[root]; ok {
		if dot {
			return orig + "." + rest
		}
		return orig
	}
	return dotted
}

// pyFixRefs turns receivers rooted at a module-level binding into
// qualifiers, resolves from-import aliases to imported names, and drops
// references to builtins the file does not shadow.
func pyFixRefs(f *model.Facts) {
	sc := newPyScope(f)
	refs := f.Refs[:0]
	for _, r := range f.Refs {
		if r.Qualifier == "" && r.Receiver != "" && isPyDotted(r.Receiver) {
			root, _, _ := strings.Cut(r.Receiver, ".")
			if sc.bound[root] || sc.classes[root] {
				r.Qualifier, r.Receiver = r.Receiver, ""
			}
		}
		if r.Qualifier != "" {
			r.Qualifier = sc.dealias(r.Qualifier)
		}
		if r.Qualifier == "" && r.Receiver == "" {
			r.Name = sc.dealias(r.Name)
			if PythonBuiltin(r.Name) && !sc.defined[r.Name] {
				continue
			}
		}
		refs = append(refs, r)
	}
	f.Refs = refs
}

// pyFixInherits drops "object" bases and resolves from-import aliases.
func pyFixInherits(f *model.Facts) {
	sc := newPyScope(f)
	out := f.Inherits[:0]
	for _, in := range f.Inherits {
		if in.Qualifier == "" {
			in.Name = sc.dealias(in.Name)
			if in.Name == "object" {
				continue
			}
		} else {
			in.Qualifier = sc.dealias(in.Qualifier)
		}
		out = append(out, in)
	}
	f.Inherits = out
	if len(f.Inherits) == 0 {
		f.Inherits = nil
	}
}

// ---------------------------------------------------------------- names

func isPyIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isPyIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !isPyIdentRune(r) || (i == 0 && unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// isPyDotted reports whether s is a dotted identifier chain ("a.b.c").
func isPyDotted(s string) bool {
	if s == "" {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if !isPyIdent(seg) {
			return false
		}
	}
	return true
}

// PythonBuiltin reports whether name is a Python builtin (function, type,
// exception or constant); unshadowed references to it never resolve to
// repository code.
func PythonBuiltin(name string) bool { return pyBuiltins[name] }

var pyBuiltins = func() map[string]bool {
	m := map[string]bool{}
	for _, n := range strings.Fields(`
		abs aiter all anext any ascii bin bool breakpoint bytearray bytes callable chr
		classmethod compile complex copyright credits delattr dict dir divmod enumerate
		eval exec exit filter float format frozenset getattr globals hasattr hash help
		hex id input int isinstance issubclass iter len license list locals map max
		memoryview min next object oct open ord pow print property quit range repr
		reversed round set setattr slice sorted staticmethod str sum super tuple type
		vars zip __import__ __build_class__ __debug__ __name__ __file__ __doc__
		None True False NotImplemented Ellipsis
		BaseException BaseExceptionGroup Exception ExceptionGroup ArithmeticError
		AssertionError AttributeError BlockingIOError BrokenPipeError BufferError
		BytesWarning ChildProcessError ConnectionAbortedError ConnectionError
		ConnectionRefusedError ConnectionResetError DeprecationWarning EOFError
		EncodingWarning EnvironmentError FileExistsError FileNotFoundError
		FloatingPointError FutureWarning GeneratorExit IOError ImportError
		ImportWarning IndentationError IndexError InterruptedError IsADirectoryError
		KeyError KeyboardInterrupt LookupError MemoryError ModuleNotFoundError
		NameError NotADirectoryError NotImplementedError OSError OverflowError
		PendingDeprecationWarning PermissionError ProcessLookupError RecursionError
		ReferenceError ResourceWarning RuntimeError RuntimeWarning StopAsyncIteration
		StopIteration SyntaxError SyntaxWarning SystemError SystemExit TabError
		TimeoutError TypeError UnboundLocalError UnicodeDecodeError UnicodeEncodeError
		UnicodeError UnicodeTranslateError UnicodeWarning UserWarning ValueError
		Warning ZeroDivisionError`) {
		m[n] = true
	}
	return m
}()
