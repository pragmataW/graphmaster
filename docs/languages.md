# Languages

graphmaster parses ten languages (eleven tree-sitter grammars, since TSX has
its own) and resolves references across files with per-language rules.

| Language | Extensions | Module identity | Resolver |
|---|---|---|---|
| Go | `.go` | import path from the nearest `go.mod`; package scope is the directory | `resolve/go.go` |
| Python | `.py` `.pyi` | dotted module from the `__init__.py` package chain, else from the source root (`pyproject.toml` / `setup.py` / `setup.cfg` directory or its `src/` layout) | `resolve/python.go` |
| TypeScript | `.ts` `.mts` `.cts` | the file | `resolve/typescript.go` |
| TSX | `.tsx` | the file | `resolve/typescript.go` |
| JavaScript | `.js` `.mjs` `.cjs` `.jsx` | the file | `resolve/typescript.go` |
| Rust | `.rs` | crate from `Cargo.toml` plus the `mod` tree | `resolve/rust.go` |
| Java | `.java` | package + class | `resolve/java.go` |
| C | `.c` `.h` | the file | `resolve/c.go` |
| C++ | `.cpp` `.cc` `.cxx` `.c++` `.hpp` `.hh` `.hxx` `.h++` `.ipp` `.tpp` `.C` `.H` | the file; namespaces/classes in the qualified name | `resolve/cpp.go` |
| C# | `.cs` | namespace; `.csproj` directory is the project | `resolve/csharp.go` |
| PHP | `.php` | `namespace`, mapped to directories through `composer.json` PSR-4 | `resolve/php.go` |

`.h` files are parsed as C. Files with an upper-case `.C`/`.H` extension are
C++.

Restrict indexing to some languages with `languages = ["go", "typescript",
"tsx"]` (aliases such as `golang`, `py`, `ts`, `js`, `rs`, `c++`, `cs`, `c#`
are accepted). See [configuration.md](configuration.md).

## What is extracted

For every file: definitions (`package`, `module`, `namespace`, `class`,
`struct`, `interface`, `trait`, `enum`, `type`, `function`, `method`,
`constructor`, `field`, `property`, `variable`, `constant`, `macro`,
`enum_member`, `test`) with qualified names, line ranges and a signature of at
most 200 characters; references (calls, type references, member accesses)
with their qualifier or receiver expression; imports; inheritance
(`extends`, `implements`, `embeds`); and test markers.

Edges in the graph:

| Kind | Meaning |
|---|---|
| `contains` | a definition nested in another |
| `calls` | a call site |
| `references` | a non-call use of a symbol (type, field, variable) |
| `imports` | a file importing a file or package |
| `extends` / `implements` / `embeds` | type hierarchy (Go struct embedding is `embeds`) |
| `tests` | a test exercising a symbol |
| `includes` | C/C++ `#include` |
| `declares` | C/C++ header declaration to its definition |

Files are skipped from parsing, but still listed in `status`, when they are
larger than `max_file_size` (default 1 MiB), binary (a NUL byte in the first
8 KiB), minified (average line longer than 300 bytes in a file of at least
1 KiB, or any line longer than 10,000 bytes) or generated (a Go-style
`Code generated ... DO NOT EDIT` marker within the first 8 KiB, or
`@generated` / `auto-generated` in the first 10 lines).

## How resolution works

Extraction stores every reference by name, with its qualifier
(`pkg.Func`, `Foo::bar`) or receiver (`x.Method()`), and without a target.
The resolver (`internal/resolve`) then picks targets with five confidence
tiers. The first tier that matches wins:

| Tier | Confidence | Rule |
|---|---|---|
| T1 import-qualified | 100 | The qualifier or alias is bound by an import, or the bare name was imported by name, and the target module has exactly one matching definition. |
| T2 lexical | 90 | Same file (inner scope first), then the same package or module. |
| T3 receiver-typed | 80 | `x.M()` where the type of `x` is known locally (Go receivers and parameters, Java/C# locals and fields, TypeScript annotations and `new T()`, Python `self.`, C++ `this->`, PHP `$this->`), walking up the type's ancestors. |
| T4 unique global | 50 | Exactly one candidate with that name in the language across the repository. |
| T5 ambiguous | 30 / n | 2 to 5 candidates: one edge to each. More than 5: left unresolved. |

Names with more than 2000 definitions or references skip T4 and T5 (they are
too common to guess). References to the standard library, third-party
packages, `node_modules`, system headers and builtins stay unresolved and are
shown by `callees` as library calls.

The query tools filter by confidence: `callers`, `callees` and `traverse`
default to `min_confidence=50`, `impact` to 30. Edges below 80 are marked
with `~`:

```text
$ graphmaster query callers geo::Shape::area --min-confidence 10
graphmaster callers · fresh
method geo::Shape::area  cpp/include/geo/shape.hpp:10-10  virtual double area() const = 0
  ~method geo::Canvas::totalArea  cpp/src/canvas.cpp:9-15  @12
```

Resolution is incremental. When a file changes, its definitions and outgoing
edges are replaced, edges pointing into it become unresolved again, and the
names it defined before and after the change are queued in `pending_names`
in the same transaction. The resolver then re-resolves only the queued names
(and receiver-typed references in files that use them). Tests check that an
incremental update produces exactly the graph a from-scratch build produces.

### Per-language rules

- **Go:** an import binds its package name (alias or the last path element);
  blank and dot imports bind nothing. Methods and fields are members and
  never visible as bare names; top-level methods belong to the receiver type
  of their signature. Unqualified type names resolve within the package.
  Receiver typing reads the enclosing function: receiver and parameters,
  `var` and `:=` declarations, composite literals, `new`/`make`, conversions,
  function and method results, struct fields (through embedded types) and
  package-level variables. `TestServer_Start` tests
  `Server.Start`. Go types implement interfaces implicitly, so after
  resolution a pass derives `implements` edges from method sets (value and
  pointer receivers, promoted methods of embedded types, embedded
  interfaces, signatures compared without package qualifiers) at confidence
  70, or 85 when a compile-time assertion such as `var _ I = (*T)(nil)`
  names the pair. Empty interfaces, interfaces embedding types outside the
  repository, ubiquitous single-method shapes (`String`, `Error`) and
  interfaces with too many implementers are only linked by assertions.
- **Python:** absolute and relative imports (`from .m import x`, `from m
  import *`) against the source root; `self.` and `cls.` receivers.
- **TypeScript / TSX / JavaScript:** one family; modules are files. Relative
  imports with extension and `index.*` probing, `tsconfig.json` `baseUrl`
  and `paths` (following `extends`), `package.json` workspaces, barrel
  re-exports (`export * from`, `export { X } from`), `require()` and
  `import x = require()`. Bare npm specifiers are external. Receiver typing
  uses `this`/`super`, annotations, `new T()`, `x as T`, declared return
  types and field types.
- **Rust:** `Cargo.toml` crates, the `mod` tree, `use` with `crate::`,
  `super::` and `self::`; trait `impl` blocks link to their trait.
- **Java:** package + class qualified names; single-type and on-demand
  (`a.b.*`) imports; platform packages (`java.*`, `javax.*`, `jdk.*`,
  `jakarta.*`, ...) are external; implicit `this` makes
  members visible inside their class.
- **C:** `#include "x.h"` resolves to a file (searched in the including
  file's directory, `include/`, and `-I` directories from
  `compile_commands.json` when present); `<...>` includes are external.
  Header declarations are linked to their definitions (`declares`). Macro
  calls are best effort.
- **C++:** the C rules plus `ns::Cls::m` qualified names, `using namespace`,
  and out-of-line definitions (`Cls::m` in a `.cpp`) attached to their class.
- **C#:** namespaces, `using` (also `global using` and aliases), partial
  classes merged across files, `.csproj` roots.
- **PHP:** `namespace` and `use`, `composer.json` PSR-4 autoload mapping to
  directories, literal `require`/`include` paths, `$this->` receivers.

Dynamic dispatch, reflection, macros that generate definitions and
dependency injection are not followed. That is what the confidence tiers and
the `~` marker are for.

## Adding a language

A language is a self-registering set of files; no shared code changes
besides the language enum.

1. **Enum and extensions.** Append a `Lang` constant and its extensions in
   `internal/model/lang.go` (append only: values are stored in `graph.db`).
2. **Grammar.** Add the tree-sitter Go binding to `go.mod` (it must be ABI
   compatible with `github.com/tree-sitter/go-tree-sitter` v0.25.0) and a
   blank import to `internal/deps/deps.go` so the ABI smoke test loads it.
3. **Extraction query: `internal/parse/queries/<x>.scm`.** Capture
   convention (documented at the top of every `.scm` file):

   ```scheme
   ; @def.<kind> + @name [+ @scope]     definition; <kind> is a model.NodeKind name
   ; @ref.call|@ref.type|@ref.member    reference + @name [+ @receiver|@qualifier]
   ; @import.path [+ @import.alias|@import.name|@import.wildcard]
   ; @inherit.extends|implements|embeds target type of the enclosing type def
   ; @test                              marks the def whose node is captured
   ; @package                           package / namespace clause
   ; @local                             name scoped to the enclosing def
   ; captures starting with "_" are predicate helpers and ignored
   ```

4. **Spec: `internal/parse/lang_<x>.go`.** Implement `model.LanguageSpec`
   (`Lang`, `Extensions`, `Grammar`, `QuerySource`, `ExtractorVersion`,
   `ModuleOf`, `ResolveImport`) and call `Register` from `init()`. Optional
   hooks in `internal/parse/hooks.go` refine the generic extractor: export
   rules, signature formatting, test-name mapping, import cleanup,
   post-processing. Bump `ExtractorVersion` whenever the output changes; it
   invalidates cached facts for that language only.
5. **Golden facts: `testdata/golden/<x>/`.** Add small source files that
   cover every definition kind, a call, a member call with a receiver, an
   import, inheritance and a test, then generate the expected output:

   ```sh
   go test -tags "sqlite_fts5 sqlite_omit_load_extension" ./internal/parse -run TestGolden -update
   ```

   Review the generated `.golden` files by hand before committing.
6. **Resolver: `internal/resolve/<x>.go`** (optional). Languages without one
   use `Generic`. Embed `Generic` and override what differs (`ImportBindings`,
   `IsMember`, `OwnerName`, `MembersInScope`, `ResolveType`, `Receiver`),
   then `Register(model.Lang<X>, xResolver{})` from `init()`. Hooks must be
   deterministic so incremental and full resolution agree.
7. **Fixture.** Add a small program under `testdata/fixture-repo/<x>/` and
   its expected edges to `testdata/fixture-repo/EXPECTED.md` and
   `expected_edges.txt`.

Run `make test` and check `graphmaster status` on a real project in the new
language.
