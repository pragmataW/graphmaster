# Benchmarks

## Kubernetes (`k8s.sh`)

`k8s.sh` measures graphmaster on a real, large Go codebase: Kubernetes
pinned to **v1.34.0**, shallow-cloned once into a cache directory.

```sh
./bench/k8s.sh                                  # builds ./graphmaster via make
GRAPHMASTER_BIN=/path/to/graphmaster ./bench/k8s.sh
RESULTS=bench-results.jsonl ./bench/k8s.sh      # also append a JSON line
```

Steps, each timed as wall-clock seconds for one `graphmaster index` run:

| Step | What changes | What it shows |
|---|---|---|
| full index | `.graphmaster/` removed | cold build (parse, write, resolve) |
| no-change re-index | nothing | dirty-check cost, should be well under a second |
| touch N files | mtimes only | stat-only update, no reparse |
| edit N Go files | appended function | incremental reparse + resolution |
| restore edits | `git checkout` | incremental again, mostly parse-cache hits |

It also prints the size of `graph.db`. Edited files are always restored
(also on failure), and `.graphmaster/` stays in the cached checkout, so a
second run starts with an existing index. Delete the cache dir to start
over.

| Variable | Default |
|---|---|
| `GRAPHMASTER_BIN` | build `./graphmaster` with `make build` |
| `GRAPHMASTER_BENCH_CACHE` | `~/.cache/graphmaster-bench` |
| `KUBE_VERSION` | `v1.34.0` |
| `TOUCH_COUNT` | `500` |
| `EDIT_COUNT` | `50` |
| `RESULTS` | unset (no JSON output) |

Requirements: `git`, `perl` (millisecond timer), `awk`, network access for
the first clone (around 300 MB).

## Synthetic repositories

Go benchmarks generate repositories with
`internal/testutil.Synth(seed, files, locPerFile, langs)` or the
`MaterializeSynth` helper, which also commits the result to git. The output
is deterministic for a given seed. Each function calls another file in its
package, a lower-numbered package, and a sibling in its own file, using each
language's real import mechanism (Go packages, Python packages, ES modules,
Rust `mod` trees, Java packages, C/C++ headers, C# namespaces, PHP PSR-4).

```go
root := testutil.MaterializeSynth(b, 1, 10_000, 100, model.AllLangs()) // ~1M LOC
```
