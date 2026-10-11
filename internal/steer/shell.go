package steer

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// SearchCall is one recognised search command inside a shell command line.
type SearchCall struct {
	// Tool is the canonical tool name: grep, egrep, fgrep, rg, ag, ack,
	// "git grep", find, fd, ls, tree or locate.
	Tool string
	// Patterns are the literal search patterns (content regexes, or file-name
	// patterns for NameSearch tools). Dynamic ($VAR) patterns are omitted.
	Patterns []string
	// Paths are the explicit or implied (".") targets. Nil when the input is
	// stdin or an unknown file list (xargs, find -exec).
	Paths []string
	// Globs are file filters: --include, -g, -t TYPE, -name, -e EXT, ...
	Globs []string
	// Recursive is true for tree-wide searches (not a single file / stdin).
	Recursive bool
	// ReadsStdin is true when the tool searches its standard input.
	ReadsStdin bool
	// Fixed is true for fixed-string matching (-F, -Q, fgrep).
	Fixed bool
	// NameSearch is true when file names, not contents, are searched.
	NameSearch bool
}

// ShellAnalysis is the result of AnalyzeShell.
type ShellAnalysis struct {
	Searches []SearchCall
	// AllReadOnly is true when every command is a known side-effect-free
	// command (search tools, cat, head, sort, sed -n, ...) and no output is
	// redirected to a file.
	AllReadOnly bool
	// UnknownDir is true when a cd/pushd/popd or "git -C" moved a command to
	// a directory that cannot be resolved statically (no argument, ~, -,
	// $VAR). Search scopes are then unknown, so the line is never steered.
	UnknownDir bool
}

// Steerable reports whether the command line should be steered: everything
// is read-only and at least one search walks a tree reading files.
func (a ShellAnalysis) Steerable() bool {
	if !a.AllReadOnly || a.UnknownDir {
		return false
	}
	for _, s := range a.Searches {
		if s.Recursive && !s.ReadsStdin {
			return true
		}
	}
	return false
}

// maxShellDepth bounds nested "bash -c" re-parsing.
const maxShellDepth = 4

// AnalyzeShell parses a shell command line and reports the searches it runs.
// A parse error means the command could not be understood; callers must
// allow it.
func AnalyzeShell(cmd string) (ShellAnalysis, error) {
	a := &analyzer{res: ShellAnalysis{AllReadOnly: true}}
	if err := a.parse(cmd, ctx{}); err != nil {
		return ShellAnalysis{}, err
	}
	if a.err != nil {
		return ShellAnalysis{}, a.err
	}
	return a.res, nil
}

// ctx is the execution context of a command.
type ctx struct {
	stdin bool // stdin is a pipe, redirect or here-doc
	fed   bool // operands come from xargs / find -exec / fd -x
	depth int  // nesting level of "sh -c" re-parsing
}

type analyzer struct {
	res ShellAnalysis
	err error // first nested parse error
	// dir is the working directory set by earlier cd/pushd commands,
	// relative to the caller's cwd (or absolute); "" means unchanged.
	// Relative search paths are rebased onto it.
	dir string
}

func (a *analyzer) notRO() { a.res.AllReadOnly = false }

func (a *analyzer) parse(src string, c ctx) error {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return err
	}
	a.stmts(f.Stmts, c)
	return nil
}

// reparse analyses the command string of "sh -c" / "env -S".
func (a *analyzer) reparse(src arg, c ctx) {
	c.depth++
	if src.dyn || c.depth > maxShellDepth {
		a.notRO()
		return
	}
	c.stdin, c.fed = false, false
	saved := a.dir // the child shell's cd does not leak out
	if err := a.parse(src.s, c); err != nil && a.err == nil {
		a.err = err
	}
	a.dir = saved
}

func (a *analyzer) stmts(ss []*syntax.Stmt, c ctx) {
	for _, s := range ss {
		a.stmt(s, c)
	}
}

func (a *analyzer) stmt(s *syntax.Stmt, c ctx) {
	if s == nil {
		return
	}
	if s.Coprocess {
		a.notRO()
	}
	for _, r := range s.Redirs {
		a.scanWord(r.Word, c)
		a.scanWord(r.Hdoc, c)
		switch r.Op {
		case syntax.RdrIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
			c.stdin = true
		case syntax.DplIn:
			// <&N duplicates an fd: harmless.
		case syntax.DplOut:
			if w := wordArg(r.Word); !isFdWord(w) && !isNullSink(w) {
				a.notRO()
			}
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.AppClob,
			syntax.RdrAll, syntax.RdrAllClob, syntax.AppAll, syntax.AppAllClob:
			if !isNullSink(wordArg(r.Word)) {
				a.notRO()
			}
		default: // <> and anything new
			a.notRO()
		}
	}
	a.cmd(s.Cmd, c)
}

func (a *analyzer) cmd(cmd syntax.Command, c ctx) {
	switch x := cmd.(type) {
	case nil:
	case *syntax.CallExpr:
		for _, as := range x.Assigns {
			a.scan(as, c)
		}
		args := make([]arg, len(x.Args))
		for i, w := range x.Args {
			a.scanWord(w, c)
			args[i] = wordArg(w)
		}
		a.call(args, c)
	case *syntax.BinaryCmd:
		switch x.Op {
		case syntax.Pipe, syntax.PipeAll:
			a.stmt(x.X, c)
			piped := c
			piped.stdin = true
			a.stmt(x.Y, piped)
		default: // && ||
			a.stmt(x.X, c)
			a.stmt(x.Y, c)
		}
	case *syntax.Subshell:
		saved := a.dir // cd inside ( ... ) does not leak out
		a.stmts(x.Stmts, c)
		a.dir = saved
	case *syntax.Block:
		a.stmts(x.Stmts, c)
	case *syntax.IfClause:
		for ic := x; ic != nil; ic = ic.Else {
			a.stmts(ic.Cond, c)
			a.stmts(ic.Then, c)
		}
	case *syntax.WhileClause:
		a.stmts(x.Cond, c)
		a.stmts(x.Do, c)
	case *syntax.ForClause:
		a.scan(x.Loop, c)
		a.stmts(x.Do, c)
	case *syntax.CaseClause:
		a.scanWord(x.Word, c)
		for _, it := range x.Items {
			for _, p := range it.Patterns {
				a.scanWord(p, c)
			}
			a.stmts(it.Stmts, c)
		}
	case *syntax.TimeClause:
		a.stmt(x.Stmt, c)
	case *syntax.DeclClause:
		for _, as := range x.Args {
			a.scan(as, c)
		}
	case *syntax.TestClause, *syntax.ArithmCmd, *syntax.LetClause:
		a.scan(x, c)
	default: // FuncDecl, CoprocClause, TestDecl, ...
		a.notRO()
	}
}

// scan finds command and process substitutions inside n and analyses them.
func (a *analyzer) scan(n syntax.Node, c ctx) {
	syntax.Walk(n, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.CmdSubst:
			a.stmts(x.Stmts, ctx{depth: c.depth})
			return false
		case *syntax.ProcSubst:
			a.stmts(x.Stmts, ctx{depth: c.depth})
			return false
		}
		return true
	})
}

func (a *analyzer) scanWord(w *syntax.Word, c ctx) {
	if w != nil {
		a.scan(w, c)
	}
}

// arg is a shell word after quote removal; dyn marks words containing
// expansions whose value is unknown statically.
type arg struct {
	s   string
	dyn bool
}

func wordArg(w *syntax.Word) arg {
	if w == nil {
		return arg{dyn: true}
	}
	var b strings.Builder
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescapeUnquoted(x.Value))
		case *syntax.SglQuoted:
			if x.Dollar {
				b.WriteString(unescapeANSIC(x.Value))
			} else {
				b.WriteString(x.Value)
			}
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok {
					return arg{dyn: true}
				}
				b.WriteString(unescapeDouble(lit.Value))
			}
		default:
			return arg{dyn: true}
		}
	}
	return arg{s: b.String()}
}

func unescapeUnquoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unescapeDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("$`\"\\\n", s[i+1]) >= 0 {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var ansiEscapes = map[byte]byte{
	'n': '\n', 't': '\t', 'r': '\r', 'a': '\a', 'b': '\b', 'f': '\f',
	'v': '\v', 'e': 0x1b, 'E': 0x1b, '\\': '\\', '\'': '\'', '"': '"', '?': '?',
}

// unescapeANSIC decodes the common escapes of $'...' strings.
func unescapeANSIC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if r, ok := ansiEscapes[s[i+1]]; ok {
				b.WriteByte(r)
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isFdWord(w arg) bool {
	if w.dyn || w.s == "" {
		return false
	}
	if w.s == "-" {
		return true
	}
	_, err := strconv.Atoi(w.s)
	return err == nil
}

func isNullSink(w arg) bool {
	switch w.s {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return !w.dyn
	}
	return false
}

// commandAliases maps alternative binary names to the canonical command.
var commandAliases = map[string]string{
	"ggrep": "grep", "gegrep": "egrep", "gfgrep": "fgrep", "gsed": "sed",
	"gawk": "awk", "mawk": "awk", "nawk": "awk", "gfind": "find",
	"fdfind": "fd", "ack-grep": "ack", "gsort": "sort", "guniq": "uniq",
	"mlocate": "locate", "plocate": "locate", "slocate": "locate",
	"gxargs": "xargs", "genv": "env", "gnice": "nice",
}

// plainReadOnly commands never write files regardless of arguments.
var plainReadOnly = map[string]bool{
	"cat": true, "head": true, "tail": true, "wc": true, "cut": true,
	"echo": true, "printf": true, "cd": true, "pwd": true, "true": true,
	"false": true, "tr": true, "nl": true, "basename": true, "dirname": true,
	"realpath": true, "which": true, "test": true, "[": true, ":": true,
	"type": true, "ps": true, "date": true, "whoami": true, "uname": true,
	"id": true, "hostname": true, "du": true, "df": true, "file": true,
	"stat": true, "readlink": true,
}

// call dispatches one simple command.
func (a *analyzer) call(args []arg, c ctx) {
	if len(args) == 0 {
		return
	}
	if args[0].dyn {
		a.notRO()
		return
	}
	name := baseName(args[0].s)
	if alias, ok := commandAliases[name]; ok {
		name = alias
	}
	rest := args[1:]
	switch name {
	case "grep", "egrep", "fgrep", "rgrep":
		a.grep(name, rest, c)
	case "rg":
		a.rg(rest, c)
	case "ag":
		a.ag(rest, c)
	case "ack":
		a.ack(rest, c)
	case "git":
		a.git(rest, c)
	case "find":
		a.find(rest, c)
	case "fd":
		a.fd(rest, c)
	case "ls":
		a.ls(rest, c)
	case "tree":
		a.tree(rest, c)
	case "locate":
		a.locate(rest)
	case "xargs":
		a.xargs(rest, c)
	case "env":
		a.env(rest, c)
	case "time":
		a.time(rest, c)
	case "command":
		a.command(rest, c)
	case "builtin":
		a.call(rest, c)
	case "nice":
		a.nice(rest, c)
	case "bash", "sh", "zsh", "dash", "ksh":
		a.shell(rest, c)
	case "cd", "pushd":
		a.cd(rest)
	case "popd":
		a.res.UnknownDir = true
	case "sed":
		a.sed(rest)
	case "awk":
		a.awk(rest)
	case "sort":
		a.sort(rest)
	case "uniq":
		a.uniq(rest)
	default:
		if !plainReadOnly[name] {
			a.notRO()
		}
	}
}

// ---- option parsing --------------------------------------------------------

// optSpec describes a tool's value-taking flags.
type optSpec struct {
	short  string          // short flags that take a value
	long   map[string]bool // long flags (without "--") that take a value
	digits bool            // -NUM shorthand (grep -5)
	// stopAtOperand ends option parsing at the first operand (POSIX style,
	// used by wrappers whose operands are a command line).
	stopAtOperand bool
}

type option struct {
	name   string // "-r" or "--recursive"
	val    string
	hasVal bool
	dynVal bool
}

func longSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// parseOptions splits args into options and operands (GNU permutation
// unless stopAtOperand). dd is the operand index where "--" appeared, or -1.
func parseOptions(args []arg, spec optSpec) (opts []option, ops []arg, dd int) {
	dd = -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.dyn || len(a.s) < 2 || a.s[0] != '-' {
			ops = append(ops, a)
			if spec.stopAtOperand {
				return opts, append(ops, args[i+1:]...), dd
			}
			continue
		}
		if a.s == "--" {
			dd = len(ops)
			return opts, append(ops, args[i+1:]...), dd
		}
		if strings.HasPrefix(a.s, "--") {
			name, val, eq := strings.Cut(a.s[2:], "=")
			o := option{name: "--" + name}
			if eq {
				o.val, o.hasVal = val, true
			} else if spec.long[name] && i+1 < len(args) {
				i++
				o.val, o.hasVal, o.dynVal = args[i].s, true, args[i].dyn
			}
			opts = append(opts, o)
			continue
		}
		cl := a.s[1:]
		for j := 0; j < len(cl); j++ {
			ch := cl[j]
			if spec.digits && ch >= '0' && ch <= '9' {
				for j+1 < len(cl) && cl[j+1] >= '0' && cl[j+1] <= '9' {
					j++
				}
				opts = append(opts, option{name: "-NUM"})
				continue
			}
			o := option{name: "-" + string(ch)}
			if strings.IndexByte(spec.short, ch) >= 0 {
				if j+1 < len(cl) {
					o.val, o.hasVal = cl[j+1:], true
				} else if i+1 < len(args) {
					i++
					o.val, o.hasVal, o.dynVal = args[i].s, true, args[i].dyn
				}
				opts = append(opts, o)
				break
			}
			opts = append(opts, o)
		}
	}
	return opts, ops, dd
}

// ---- search tools ----------------------------------------------------------

// operandMode says what a search reads when it has no operands.
type operandMode int

const (
	noneStdin     operandMode = iota // grep: stdin unless recursive
	noneCwd                          // git grep, find: current directory
	noneStdinPipe                    // rg/ag/ack: stdin if piped, else cwd
)

// finish fills Paths/Recursive/ReadsStdin from the operands and records sc.
func (a *analyzer) finish(sc SearchCall, ops []arg, rec bool, mode operandMode, c ctx, fileLike func(string) bool) {
	var known []string
	dyn, dash := c.fed, false
	for _, o := range ops {
		switch {
		case o.dyn || o.s == "{}":
			dyn = true
		case o.s == "-":
			dash = true
		default:
			known = append(known, o.s)
		}
	}
	switch {
	case len(known) > 0 || dyn:
		sc.Paths = known
		sc.Recursive = rec && (dyn || !allMatch(known, fileLike)) || c.fed && len(known) == 0
	case dash:
		sc.ReadsStdin = true
	case mode == noneStdin && !rec, mode == noneStdinPipe && c.stdin:
		sc.ReadsStdin = true
	default:
		sc.Paths = []string{"."}
		sc.Recursive = rec
	}
	for i, p := range sc.Paths {
		sc.Paths[i] = a.rebase(p)
	}
	a.res.Searches = append(a.res.Searches, sc)
}

// rebase resolves a search path against the directory set by cd.
func (a *analyzer) rebase(p string) string {
	if a.dir == "" || path.IsAbs(p) {
		return p
	}
	return path.Join(a.dir, p)
}

// chdir applies a directory change to target. Targets that depend on the
// environment (none, ~, -, $VAR) make the scope unknown.
func (a *analyzer) chdir(target arg, ok bool) {
	if !ok || target.dyn || target.s == "" || target.s == "-" || strings.HasPrefix(target.s, "~") {
		a.res.UnknownDir = true
		return
	}
	a.dir = a.rebase(target.s)
	if a.dir == "." {
		a.dir = ""
	}
}

// cd handles cd/pushd: options (-L, -P, -e, -@, +N/-N for pushd) are
// skipped and the first operand is the target.
func (a *analyzer) cd(args []arg) {
	for i, x := range args {
		if x.dyn {
			a.chdir(x, true)
			return
		}
		if x.s == "--" {
			if i+1 < len(args) {
				a.chdir(args[i+1], true)
			} else {
				a.chdir(arg{}, false)
			}
			return
		}
		if len(x.s) > 1 && (x.s[0] == '-' || x.s[0] == '+') {
			if x.s[0] == '+' || x.s[1] >= '0' && x.s[1] <= '9' {
				a.chdir(arg{}, false) // pushd +N / -N rotates the stack
				return
			}
			continue
		}
		a.chdir(x, true)
		return
	}
	a.chdir(arg{}, false)
}

func allMatch(ss []string, f func(string) bool) bool {
	for _, s := range ss {
		if !f(s) {
			return false
		}
	}
	return true
}

// gitPathspecFile treats pathspecs with wildcards as tree-wide.
func gitPathspecFile(p string) bool {
	return !strings.ContainsAny(p, globMeta) && looksLikeFile(p)
}

func shallow(val string) bool {
	n, err := strconv.Atoi(val)
	return err == nil && n <= 1
}

func isHelp(opts []option) bool {
	for _, o := range opts {
		switch o.name {
		case "--help", "--version":
			return true
		}
	}
	return false
}

var grepSpec = optSpec{
	short:  "efABCmdD",
	digits: true,
	long: longSet("regexp", "file", "after-context", "before-context", "context",
		"max-count", "include", "exclude", "exclude-dir", "exclude-from",
		"directories", "devices", "label", "binary-files", "group-separator"),
}

func (a *analyzer) grep(name string, args []arg, c ctx) {
	opts, ops, _ := parseOptions(args, grepSpec)
	if isHelp(opts) {
		return
	}
	sc := SearchCall{Tool: name, Fixed: name == "fgrep"}
	if name == "rgrep" {
		sc.Tool = "grep"
	}
	rec := name == "rgrep"
	patSet := false
	for _, o := range opts {
		switch o.name {
		case "-e", "--regexp":
			patSet = true
			if o.hasVal && !o.dynVal {
				sc.Patterns = append(sc.Patterns, o.val)
			}
		case "-f", "--file":
			patSet = true
		case "-r", "-R", "--recursive", "--dereference-recursive":
			rec = true
		case "-d", "--directories":
			if o.val == "recurse" {
				rec = true
			}
		case "-F", "--fixed-strings":
			sc.Fixed = true
		case "-E", "-G", "-P", "--extended-regexp", "--basic-regexp", "--perl-regexp":
			sc.Fixed = false
		case "--include":
			if o.hasVal && !o.dynVal {
				sc.Globs = append(sc.Globs, o.val)
			}
		}
	}
	ops = takePattern(&sc, ops, patSet)
	a.finish(sc, ops, rec, noneStdin, c, looksLikeFile)
}

// takePattern moves the first operand into Patterns unless -e/-f was used.
func takePattern(sc *SearchCall, ops []arg, patSet bool) []arg {
	if patSet || len(ops) == 0 {
		return ops
	}
	if !ops[0].dyn {
		sc.Patterns = append(sc.Patterns, ops[0].s)
	}
	return ops[1:]
}

// typeGlobs maps rg/ag/ack file type names to globs.
var typeGlobs = map[string][]string{
	"go":         {"*.go"},
	"py":         {"*.py", "*.pyi"},
	"python":     {"*.py", "*.pyi"},
	"js":         {"*.js", "*.jsx", "*.mjs", "*.cjs"},
	"javascript": {"*.js", "*.jsx", "*.mjs", "*.cjs"},
	"ts":         {"*.ts", "*.tsx", "*.mts", "*.cts"},
	"typescript": {"*.ts", "*.tsx", "*.mts", "*.cts"},
	"rust":       {"*.rs"},
	"java":       {"*.java"},
	"c":          {"*.c", "*.h"},
	"cc":         {"*.c", "*.h"},
	"cpp":        {"*.cpp", "*.cc", "*.cxx", "*.hpp", "*.hh", "*.hxx", "*.h"},
	"cs":         {"*.cs"},
	"csharp":     {"*.cs"},
	"php":        {"*.php"},
	"md":         {"*.md", "*.markdown"},
	"markdown":   {"*.md", "*.markdown"},
	"json":       {"*.json"},
	"yaml":       {"*.yaml", "*.yml"},
	"toml":       {"*.toml"},
	"txt":        {"*.txt"},
	"html":       {"*.html", "*.htm"},
	"css":        {"*.css"},
	"sh":         {"*.sh", "*.bash"},
	"shell":      {"*.sh", "*.bash"},
	"ruby":       {"*.rb"},
	"kotlin":     {"*.kt", "*.kts"},
	"swift":      {"*.swift"},
	"sql":        {"*.sql"},
	"xml":        {"*.xml"},
	"proto":      {"*.proto"},
	"protobuf":   {"*.proto"},
}

var rgSpec = optSpec{
	short: "efgtTABCmMjEdr",
	long: longSet("regexp", "file", "glob", "iglob", "type", "type-not", "type-add",
		"type-clear", "after-context", "before-context", "context", "max-count",
		"max-columns", "threads", "encoding", "max-depth", "replace", "color",
		"colors", "context-separator", "field-context-separator",
		"field-match-separator", "path-separator", "pre", "pre-glob", "sort",
		"sortr", "ignore-file", "max-filesize", "engine", "dfa-size-limit",
		"regex-size-limit", "hyperlink-format", "generate"),
}

func (a *analyzer) rg(args []arg, c ctx) {
	opts, ops, _ := parseOptions(args, rgSpec)
	if isHelp(opts) {
		return
	}
	sc := SearchCall{Tool: "rg"}
	rec, patSet := true, false
	for _, o := range opts {
		switch o.name {
		case "-e", "--regexp":
			patSet = true
			if o.hasVal && !o.dynVal {
				sc.Patterns = append(sc.Patterns, o.val)
			}
		case "-f", "--file":
			patSet = true
		case "-g", "--glob", "--iglob":
			if o.hasVal && !o.dynVal && !strings.HasPrefix(o.val, "!") {
				sc.Globs = append(sc.Globs, o.val)
			}
		case "-t", "--type":
			sc.Globs = append(sc.Globs, typeGlobs[o.val]...)
		case "-F", "--fixed-strings":
			sc.Fixed = true
		case "-d", "--max-depth":
			if shallow(o.val) {
				rec = false
			}
		case "--files":
			sc.NameSearch, patSet = true, true
		case "--type-list":
			return
		}
	}
	ops = takePattern(&sc, ops, patSet)
	a.finish(sc, ops, rec, noneStdinPipe, c, looksLikeFile)
}

var agSpec = optSpec{
	short: "GgmpW",
	long: longSet("file-search-regex", "max-count", "depth", "path-to-ignore",
		"ignore", "ignore-dir", "pager", "width", "workers"),
}

func (a *analyzer) ag(args []arg, c ctx) {
	opts, ops, _ := parseOptions(args, agSpec)
	if isHelp(opts) {
		return
	}
	sc := SearchCall{Tool: "ag"}
	rec, patSet := true, false
	for _, o := range opts {
		switch o.name {
		case "-Q", "--literal", "-F", "--fixed-strings":
			sc.Fixed = true
		case "-g":
			sc.NameSearch, patSet = true, true
			if o.hasVal && !o.dynVal {
				sc.Patterns = append(sc.Patterns, o.val)
			}
		case "--depth":
			if shallow(o.val) && o.val != "-1" {
				rec = false
			}
		default:
			sc.Globs = append(sc.Globs, typeFlagGlobs(o)...)
		}
	}
	ops = takePattern(&sc, ops, patSet)
	a.finish(sc, ops, rec, noneStdinPipe, c, looksLikeFile)
}

// typeFlagGlobs handles ag/ack style "--go" type selectors.
func typeFlagGlobs(o option) []string {
	if !strings.HasPrefix(o.name, "--") || o.hasVal {
		return nil
	}
	return typeGlobs[o.name[2:]]
}

var ackSpec = optSpec{
	short: "ABmgt",
	long: longSet("type", "ignore-dir", "noignore-dir", "ignore-file", "match",
		"output", "pager", "max-count", "after-context", "before-context",
		"range-start", "range-end"),
}

func (a *analyzer) ack(args []arg, c ctx) {
	opts, ops, _ := parseOptions(args, ackSpec)
	if isHelp(opts) {
		return
	}
	sc := SearchCall{Tool: "ack"}
	patSet := false
	for _, o := range opts {
		switch o.name {
		case "-Q", "--literal":
			sc.Fixed = true
		case "-g":
			sc.NameSearch, patSet = true, true
			if o.hasVal && !o.dynVal {
				sc.Patterns = append(sc.Patterns, o.val)
			}
		case "-f":
			sc.NameSearch, patSet = true, true
		case "-t", "--type":
			sc.Globs = append(sc.Globs, typeGlobs[o.val]...)
		default:
			sc.Globs = append(sc.Globs, typeFlagGlobs(o)...)
		}
	}
	ops = takePattern(&sc, ops, patSet)
	a.finish(sc, ops, true, noneStdinPipe, c, looksLikeFile)
}

// gitReadOnly are git subcommands without side effects (besides grep).
var gitReadOnly = map[string]bool{
	"status": true, "log": true, "show": true, "diff": true, "ls-files": true,
	"ls-tree": true, "blame": true, "rev-parse": true, "describe": true,
	"shortlog": true,
}

var gitGlobalWithValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--super-prefix": true, "--config-env": true,
}

func (a *analyzer) git(args []arg, c ctx) {
	// "git -C dir" changes directory for this invocation only.
	saved := a.dir
	defer func() { a.dir = saved }()
	i := 0
	for i < len(args) && !args[i].dyn && strings.HasPrefix(args[i].s, "-") {
		if gitGlobalWithValue[args[i].s] {
			if args[i].s == "-C" {
				if i+1 < len(args) {
					a.chdir(args[i+1], true)
				} else {
					a.chdir(arg{}, false)
				}
			}
			i++
		}
		i++
	}
	if i >= len(args) || args[i].dyn {
		if i >= len(args) {
			return // bare "git" prints help
		}
		a.notRO()
		return
	}
	switch sub := args[i].s; {
	case sub == "grep":
		a.gitGrep(args[i+1:], c)
	case gitReadOnly[sub]:
	default:
		a.notRO()
	}
}

var gitGrepSpec = optSpec{
	short: "efABCm",
	long: longSet("file", "after-context", "before-context", "context",
		"max-count", "max-depth", "threads"),
}

func (a *analyzer) gitGrep(args []arg, c ctx) {
	opts, ops, dd := parseOptions(args, gitGrepSpec)
	sc := SearchCall{Tool: "git grep"}
	rec, patSet := true, false
	for _, o := range opts {
		switch o.name {
		case "-e":
			patSet = true
			if o.hasVal && !o.dynVal {
				sc.Patterns = append(sc.Patterns, o.val)
			}
		case "-f", "--file":
			patSet = true
		case "-F", "--fixed-strings":
			sc.Fixed = true
		case "--max-depth":
			if n, err := strconv.Atoi(o.val); err == nil && n == 0 {
				rec = false
			}
		}
	}
	if !patSet && len(ops) > 0 && dd != 0 {
		if !ops[0].dyn {
			sc.Patterns = append(sc.Patterns, ops[0].s)
		}
		ops = ops[1:]
		if dd > 0 {
			dd--
		}
	}
	if dd >= 0 {
		ops = ops[dd:] // operands before "--" are revisions
	}
	// Git grep ignores stdin.
	a.finish(sc, ops, rec, noneCwd, ctx{depth: c.depth, fed: c.fed}, gitPathspecFile)
}

// findValuePrimaries are find primaries that consume one argument.
var findValuePrimaries = map[string]bool{
	"-type": true, "-xtype": true, "-mindepth": true, "-newer": true,
	"-anewer": true, "-cnewer": true, "-perm": true, "-size": true,
	"-user": true, "-group": true, "-uid": true, "-gid": true, "-mtime": true,
	"-atime": true, "-ctime": true, "-mmin": true, "-amin": true, "-cmin": true,
	"-links": true, "-inum": true, "-samefile": true, "-fstype": true,
	"-used": true, "-printf": true, "-context": true, "-regextype": true,
	"-newermt": true, "-newerct": true, "-newerat": true, "-files0-from": true,
}

func isFindExpr(a arg) bool {
	if a.dyn {
		return false
	}
	return a.s == "(" || a.s == ")" || a.s == "!" || a.s == "," ||
		len(a.s) > 1 && a.s[0] == '-'
}

func (a *analyzer) find(args []arg, c ctx) {
	i := 0
	for i < len(args) && !args[i].dyn {
		s := args[i].s
		if s == "-D" { // debug options take a value
			i += 2
			continue
		}
		if s == "-H" || s == "-L" || s == "-P" || s == "-E" || s == "-X" ||
			s == "-s" || s == "-x" || strings.HasPrefix(s, "-O") {
			i++
			continue
		}
		break
	}
	var paths []arg
	for i < len(args) && !isFindExpr(args[i]) {
		paths = append(paths, args[i])
		i++
	}
	sc := SearchCall{Tool: "find", NameSearch: true}
	rec := true
	var execs [][]arg
	value := func() (arg, bool) {
		if i >= len(args) {
			return arg{}, false
		}
		v := args[i]
		i++
		return v, !v.dyn
	}
	for i < len(args) {
		p := args[i]
		i++
		if p.dyn {
			continue
		}
		switch p.s {
		case "-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename", "-lname", "-ilname":
			if v, ok := value(); ok {
				sc.Globs = append(sc.Globs, v.s)
			}
		case "-regex", "-iregex":
			if v, ok := value(); ok {
				sc.Patterns = append(sc.Patterns, v.s)
			}
		case "-maxdepth":
			if v, ok := value(); ok && shallow(v.s) {
				rec = false
			}
		case "-exec", "-execdir", "-ok", "-okdir":
			start := i
			for i < len(args) && (args[i].dyn || args[i].s != ";" && args[i].s != "+") {
				i++
			}
			execs = append(execs, args[start:i])
			i++ // skip terminator
		case "-delete":
			a.notRO()
		case "-fprint", "-fprint0", "-fls":
			a.notRO()
			value()
		case "-fprintf":
			a.notRO()
			value()
			value()
		default:
			if findValuePrimaries[p.s] {
				value()
			}
		}
	}
	a.finish(sc, paths, rec, noneCwd, ctx{depth: c.depth}, looksLikeFile)
	for _, e := range execs {
		a.call(e, ctx{fed: true, depth: c.depth})
	}
}

var fdSpec = optSpec{
	short: "etEdcjSo",
	long: longSet("extension", "type", "exclude", "max-depth", "min-depth",
		"exact-depth", "color", "threads", "size", "changed-within",
		"changed-before", "change-newer-than", "change-older-than", "newer",
		"older", "owner", "base-directory", "search-path", "path-separator",
		"max-results", "ignore-file", "batch-size", "format", "and",
		"max-buffer-time"),
}

func (a *analyzer) fd(args []arg, c ctx) {
	var exec []arg
	hasExec := false
	for i, x := range args {
		if !x.dyn && (x.s == "-x" || x.s == "--exec" || x.s == "-X" || x.s == "--exec-batch") {
			exec, args, hasExec = args[i+1:], args[:i], true
			if n := len(exec); n > 0 && !exec[n-1].dyn && exec[n-1].s == ";" {
				exec = exec[:n-1]
			}
			break
		}
	}
	opts, ops, _ := parseOptions(args, fdSpec)
	if isHelp(opts) {
		return
	}
	sc := SearchCall{Tool: "fd", NameSearch: true}
	rec, globMode := true, false
	var extra []arg
	for _, o := range opts {
		switch o.name {
		case "-e", "--extension":
			if o.hasVal && !o.dynVal {
				sc.Globs = append(sc.Globs, "*."+strings.TrimPrefix(o.val, "."))
			}
		case "-g", "--glob":
			globMode = true
		case "-F", "--fixed-strings":
			sc.Fixed = true
		case "-d", "--max-depth", "--exact-depth":
			if shallow(o.val) {
				rec = false
			}
		case "--search-path":
			extra = append(extra, arg{s: o.val, dyn: o.dynVal})
		}
	}
	if len(ops) > 0 {
		if p := ops[0]; !p.dyn {
			if globMode {
				sc.Globs = append(sc.Globs, p.s)
			} else {
				sc.Patterns = append(sc.Patterns, p.s)
			}
		}
		ops = ops[1:]
	}
	a.finish(sc, append(ops, extra...), rec, noneCwd, ctx{depth: c.depth}, looksLikeFile)
	if hasExec {
		a.call(exec, ctx{fed: true, depth: c.depth})
	}
}

var lsSpec = optSpec{
	short: "ITw",
	long: longSet("block-size", "format", "sort", "time", "hide", "ignore",
		"indicator-style", "quoting-style", "tabsize", "time-style", "width"),
}

func (a *analyzer) ls(args []arg, c ctx) {
	opts, ops, _ := parseOptions(args, lsSpec)
	for _, o := range opts {
		if o.name == "-R" || o.name == "--recursive" {
			a.finish(SearchCall{Tool: "ls", NameSearch: true}, ops, true, noneCwd, ctx{depth: c.depth}, looksLikeFile)
			return
		}
	}
}

var treeSpec = optSpec{
	short: "LPIoHT",
	long:  longSet("charset", "filelimit", "timefmt", "sort", "hintro", "houtro"),
}

func (a *analyzer) tree(args []arg, c ctx) {
	opts, ops, _ := parseOptions(args, treeSpec)
	if isHelp(opts) {
		return
	}
	sc := SearchCall{Tool: "tree", NameSearch: true}
	rec := true
	for _, o := range opts {
		switch o.name {
		case "-L":
			if shallow(o.val) {
				rec = false
			}
		case "-P":
			if o.hasVal && !o.dynVal {
				sc.Globs = append(sc.Globs, o.val)
			}
		case "-o":
			a.notRO()
		}
	}
	a.finish(sc, ops, rec, noneCwd, ctx{depth: c.depth}, looksLikeFile)
}

var locateSpec = optSpec{short: "dlnr", long: longSet("database", "limit", "regexp")}

func (a *analyzer) locate(args []arg) {
	opts, ops, _ := parseOptions(args, locateSpec)
	sc := SearchCall{Tool: "locate", Recursive: true, NameSearch: true}
	for _, o := range opts {
		if (o.name == "-r" || o.name == "--regexp") && o.hasVal && !o.dynVal {
			sc.Patterns = append(sc.Patterns, o.val)
		}
	}
	for _, o := range ops {
		if !o.dyn {
			sc.Patterns = append(sc.Patterns, o.s)
		}
	}
	a.res.Searches = append(a.res.Searches, sc)
}

// ---- wrappers --------------------------------------------------------------

var xargsSpec = optSpec{
	short:         "aEdILnPs",
	long:          longSet("arg-file", "delimiter", "max-args", "max-procs", "max-chars", "process-slot-var"),
	stopAtOperand: true,
}

func (a *analyzer) xargs(args []arg, c ctx) {
	_, ops, _ := parseOptions(args, xargsSpec)
	a.call(ops, ctx{fed: true, depth: c.depth}) // no command: echo
}

func (a *analyzer) env(args []arg, c ctx) {
	i := 0
	for ; i < len(args); i++ {
		x := args[i]
		if x.dyn {
			break
		}
		switch {
		case x.s == "-u" || x.s == "--unset" || x.s == "-C" || x.s == "--chdir":
			i++
		case x.s == "-S" || x.s == "--split-string":
			if i+1 < len(args) {
				a.reparse(args[i+1], c)
			}
			return
		case strings.HasPrefix(x.s, "-"):
			// -i, -0, -v, -uNAME, --ignore-environment, ...
		case strings.Contains(x.s, "="):
			// NAME=value
		default:
			a.call(args[i:], c)
			return
		}
	}
	if i < len(args) {
		a.call(args[i:], c)
	}
}

func (a *analyzer) time(args []arg, c ctx) {
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x.dyn:
			a.call(args[i:], c)
			return
		case x.s == "-o" || x.s == "--output" || strings.HasPrefix(x.s, "--output="):
			a.notRO()
			i++
		case x.s == "-f" || x.s == "--format":
			i++
		case strings.HasPrefix(x.s, "-"):
		default:
			a.call(args[i:], c)
			return
		}
	}
}

func (a *analyzer) command(args []arg, c ctx) {
	for i, x := range args {
		switch {
		case x.dyn:
			a.call(args[i:], c)
			return
		case x.s == "-v" || x.s == "-V":
			return // lookup only
		case strings.HasPrefix(x.s, "-"):
		default:
			a.call(args[i:], c)
			return
		}
	}
}

func (a *analyzer) nice(args []arg, c ctx) {
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x.dyn:
			a.call(args[i:], c)
			return
		case x.s == "-n" || x.s == "--adjustment":
			i++
		case strings.HasPrefix(x.s, "-"):
		default:
			a.call(args[i:], c)
			return
		}
	}
}

func (a *analyzer) shell(args []arg, c ctx) {
	cmdFlag := false
	for i := 0; i < len(args); i++ {
		x := args[i]
		switch {
		case x.dyn:
			if cmdFlag {
				a.reparse(x, c)
			} else {
				a.notRO()
			}
			return
		case x.s == "-o" || x.s == "+o" || x.s == "-O" || x.s == "+O" ||
			x.s == "--rcfile" || x.s == "--init-file":
			i++
		case strings.HasPrefix(x.s, "--"):
		case len(x.s) > 1 && (x.s[0] == '-' || x.s[0] == '+'):
			if x.s[0] == '-' && strings.IndexByte(x.s, 'c') > 0 {
				cmdFlag = true
			}
		default:
			if cmdFlag {
				a.reparse(x, c)
			} else {
				a.notRO() // script file
			}
			return
		}
	}
	a.notRO() // interactive or script from stdin
}

// ---- read-only checks for text tools ----------------------------------------

var (
	sedSpec = optSpec{
		short: "efli",
		long:  longSet("expression", "file", "line-length"),
	}
	// sedWriteRe spots w/W (write file) and e (execute) commands or flags.
	sedWriteRe = regexp.MustCompile(`(^|[/;{}\s\d])[wWe](\s|$)`)
)

func (a *analyzer) sed(args []arg) {
	opts, ops, _ := parseOptions(args, sedSpec)
	quiet, scriptSet := false, false
	var scripts []arg
	for _, o := range opts {
		switch o.name {
		case "-n", "--quiet", "--silent":
			quiet = true
		case "-i", "--in-place", "-f", "--file":
			a.notRO()
			return
		case "-e", "--expression":
			scriptSet = true
			scripts = append(scripts, arg{s: o.val, dyn: o.dynVal})
		}
	}
	if !scriptSet && len(ops) > 0 {
		scripts = append(scripts, ops[0])
	}
	if !quiet {
		a.notRO()
		return
	}
	for _, s := range scripts {
		if s.dyn || sedWriteRe.MatchString(s.s) {
			a.notRO()
			return
		}
	}
}

var awkUnsafeRe = regexp.MustCompile(`system\s*\(|\|\s*getline|\|&|>\s*"|\|\s*"`)

func (a *analyzer) awk(args []arg) {
	for i, x := range args {
		if x.dyn || awkUnsafeRe.MatchString(x.s) || x.s == "-f" && i+1 < len(args) {
			a.notRO()
			return
		}
	}
}

var sortSpec = optSpec{
	short: "kotST",
	long: longSet("key", "field-separator", "output", "buffer-size",
		"temporary-directory", "parallel", "batch-size", "compress-program",
		"files0-from", "random-source", "sort"),
}

func (a *analyzer) sort(args []arg) {
	opts, _, _ := parseOptions(args, sortSpec)
	for _, o := range opts {
		switch o.name {
		case "-o", "--output", "--compress-program":
			a.notRO()
			return
		}
	}
}

var uniqSpec = optSpec{short: "fsw", long: longSet("skip-fields", "skip-chars", "check-chars")}

func (a *analyzer) uniq(args []arg) {
	if _, ops, _ := parseOptions(args, uniqSpec); len(ops) >= 2 {
		a.notRO() // second operand is an output file
	}
}
