# Contributing to graphmaster

Thanks for helping. Bug reports with a reproduction, new language support and
fixes to agent integrations are especially welcome.

## Requirements

- Go 1.25 or newer (`go.mod` pins the language version).
- A C toolchain: graphmaster uses cgo for the tree-sitter grammars and SQLite
  (`mattn/go-sqlite3`). On macOS install the Xcode Command Line Tools; on
  Linux `gcc` or `clang`. `CGO_ENABLED=1` must be set (the Makefile exports
  it).
- git.
- Optional: [golangci-lint](https://golangci-lint.run) v2 for `make lint`.

## Build tags

Every `go build`, `go test`, `go vet` and `golangci-lint` invocation needs
these tags:

```sh
-tags "sqlite_fts5 sqlite_omit_load_extension"
```

`sqlite_fts5` compiles SQLite's full-text search (symbol search depends on
it); `sqlite_omit_load_extension` removes the runtime extension loader. A
build without the tags compiles but fails at runtime when the schema creates
its FTS5 tables. The Makefile passes them for you.

The first build compiles eleven tree-sitter grammars and SQLite and takes a
few minutes; later builds are cached.

## Make targets

| Target | Does |
|---|---|
| `make build` | builds `./graphmaster` with version metadata from `git describe` |
| `make test` | `go test -race` over all packages |
| `make test-short` | `go test -short` (skips end-to-end and performance tests) |
| `make vet` | `go vet` |
| `make lint` | `golangci-lint run` with the build tags (config in `.golangci.yml`) |
| `make tidy` | `go mod tidy` and `go mod verify` |
| `make clean` | removes the binary and `dist/` |
| `make bench` | synthetic performance gates (`bench/synth.sh`) into `bench-out/`; `BENCH_GATES="dirty multi"` picks a subset |
| `make bench-k8s` | the Kubernetes benchmark (`bench/k8s.sh`) into `bench-out/k8s.jsonl` |
| `make perf-gates` | `make bench` plus the threshold check (`bench/check.sh`, profile `PERF_PROFILE`, default `plan`) |
| `make release-check`, `make release-snapshot` | `goreleaser check` and a local snapshot build into `dist/` |
| `make test-install` | `install.sh` tests against fake releases (and shellcheck) |

CI (`.github/workflows/ci.yml`) runs build, vet, `go test -race` and a
`--version` smoke test on macOS 15 and Ubuntu 24.04, plus golangci-lint. A
nightly workflow runs the performance gates described in
[bench/README.md](bench/README.md).

## Working on the code

Start with [docs/architecture.md](docs/architecture.md) for the package map
and the freshness algorithm. A few rules keep the codebase consistent:

- **Test first.** Write or extend the test that shows the bug or the new
  behaviour, watch it fail, then change the code. Most packages use
  table-driven tests.
- **Logic in the core, translation in adapters.** Anything that decides
  something (steering, freshness, resolution) belongs in `internal/steer`,
  `internal/index`, `internal/resolve` or `internal/query`. Agent adapters
  only parse payloads, render decisions and describe install plans.
- **Fail open.** Hooks must never block an agent because graphmaster broke:
  return the "no opinion" answer and log.
- **Incremental equals full.** Any change to indexing or resolution must keep
  an incremental update producing exactly the graph of a from-scratch build.
  The oracle tests in `internal/index` check this; run them.
- **Bounded output.** Query tools respect `max_tokens`; UI endpoints respect
  their element cap.
- Idiomatic Go, `gofmt`, English identifiers and comments. Exported and
  non-obvious identifiers get doc comments.
- Keep dependencies minimal. Adding a module to `go.mod` needs a reason in
  the pull request; tree-sitter grammars must be ABI compatible with
  `github.com/tree-sitter/go-tree-sitter` v0.25.0 (`internal/deps` has a
  smoke test that loads every grammar).

## Tests

```sh
make test                                   # everything, with -race
go test -tags "sqlite_fts5 sqlite_omit_load_extension" ./internal/steer/...   # one package
go test -tags "sqlite_fts5 sqlite_omit_load_extension" -short ./...           # skip slow tests
```

### Golden files

Extraction and query output are checked against golden files. After an
intended output change, regenerate them with `-update` and review the diff
line by line before committing:

```sh
# extracted facts per language: testdata/golden/<lang>/*.golden
go test -tags "sqlite_fts5 sqlite_omit_load_extension" ./internal/parse -run TestGolden -update

# tool output on the fixture repository: internal/query/testdata/golden/
go test -tags "sqlite_fts5 sqlite_omit_load_extension" ./internal/query -update
```

When extraction output changes, bump the language's `ExtractorVersion` so
cached facts are invalidated.

### Fixtures

- `testdata/fixture-repo/` is a small program in each of the ten languages
  with cross-file calls, inheritance and tests. `internal/testutil.Materialize`
  copies it into a temporary git repository and adds the files that cannot
  be committed as-is (vendored code, a file over 1 MiB, untracked and ignored
  files). Expected symbols and edges are listed in `EXPECTED.md` and checked
  from `expected_edges.txt`.
- `internal/testutil.Synth` / `MaterializeSynth` generate deterministic
  synthetic repositories of any size for benchmarks and performance tests.
- `testdata/hooks/<agent>/` holds hook payloads captured from real agents and
  synthetic ones; `internal/replay` replays them through the dispatcher.
  See [testdata/hooks/README.md](testdata/hooks/README.md) for how to record
  new payloads with `GRAPHMASTER_RECORD` and scrub them.
- `testdata/configs/<agent>/` holds realistic existing agent configs
  (comments, other MCP servers and hooks) for install round-trip tests.

### End-to-end tests

`internal/e2e` builds the binary and drives it like an agent: install,
doctor and uninstall against a temporary `HOME`, the MCP server over stdio,
hook invocations and the UI server. They are skipped with `-short`. Tests
never touch your real `HOME` or agent configs.

### Benchmarks

```sh
go test -tags "sqlite_fts5 sqlite_omit_load_extension" -run '^$' -bench . -benchtime=1x ./internal/index/
./bench/k8s.sh      # Kubernetes v1.34.0, needs network access for the first clone
```

## Adding a language

See [docs/languages.md](docs/languages.md#adding-a-language): a language is
`internal/parse/lang_<x>.go` + `internal/parse/queries/<x>.scm` + golden
files, and optionally `internal/resolve/<x>.go`.

## Adding or changing an agent integration

Each agent lives in `internal/adapters/<agent>/` and implements
`adapters.Agent` (detection, payload parsing, decision rendering, install
plan, post-install notes). Base payload parsing on captured payloads in
`testdata/hooks/<agent>/`, not on documentation alone: agents change their
hook schemas without notice. Install plans must be reversible by
`graphmaster uninstall`; the install tests check byte-exact restores.

## Pull requests

- One topic per pull request, with tests.
- `make test` and `make lint` pass locally.
- Update the documentation (`README.md`, `docs/`) and `CHANGELOG.md` when
  user-visible behaviour changes.
- Describe how you verified the change, especially for agent integrations
  (which agent version, which events).

## Reporting bugs

Open an issue with the graphmaster version (`graphmaster --version`), OS,
agent and version, the output of `graphmaster doctor`, and the relevant lines
from `<repo>/.graphmaster/log`. For security issues see
[SECURITY.md](SECURITY.md) instead.

By contributing you agree that your contributions are licensed under the MIT
license of this project.
