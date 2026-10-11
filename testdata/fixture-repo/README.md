# fixture-repo

A small, multi-language project used by graphmaster's tests. Every directory
holds a tiny but idiomatic program in one of the ten supported languages, with
cross-file and cross-package calls, inheritance and tests.

| Dir       | Language            | Highlights                                      |
|-----------|---------------------|-------------------------------------------------|
| `go/`     | Go                  | go.mod, packages, embedding, generated file     |
| `python/` | Python              | package, `__init__`, relative imports           |
| `ts/`     | TypeScript / TSX    | tsconfig `paths` alias, React component         |
| `js/`     | JavaScript          | CommonJS + ESM, minified bundle                 |
| `rust/`   | Rust                | crate, mod tree, trait impl                     |
| `java/`   | Java                | packages, extends / implements                  |
| `c/`      | C                   | header / implementation pairs                   |
| `cpp/`    | C++                 | namespaces, classes, out-of-line methods        |
| `csharp/` | C#                  | namespaces, partial class, interface            |
| `php/`    | PHP                 | composer PSR-4 autoload, namespaces             |

Expected symbols and edges are listed in `EXPECTED.md`. Some files (vendored
code, a >1 MiB table, untracked and ignored files) are created by
`internal/testutil.Materialize` and are not committed here.
