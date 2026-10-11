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

## Performance gates

`.github/workflows/nightly.yml` runs every night (and on demand via
*workflow_dispatch*, optionally with a subset of gates and without the
Kubernetes step) on `macos-15` and `ubuntu-24.04`. Each runner executes the
synthetic gates and the Kubernetes benchmark, checks the results against
`thresholds.json` and uploads everything in `bench-out/` (raw `go test`
output, `k8s.jsonl`, `k8s.log`, `perfgate.txt`/`perfgate.json`,
`machine.txt`) as the `perf-<os>` artifact. The job summary shows the
checks as a table; a failed gate fails the job after the upload.

Run the same gates locally:

```sh
bench/synth.sh                         # dirty full incr multi query -> bench-out/
RESULTS=bench-out/k8s.jsonl bench/k8s.sh
bench/check.sh -profile plan -k8s bench-out/k8s.jsonl bench-out/bench-*.txt
```

`synth.sh` gates (`bench/synth.sh dirty query` runs a subset):

| Gate | Test | Size | PLAN target |
|---|---|---|---|
| `dirty` | `BenchmarkFreshenNoChange` warm and cold, `-benchtime 20x -count 3` | 100k Go files | no-change dirty check < 150ms |
| `full` | `BenchmarkFullBuildSynth`, one cold build | 10k files x 100 LOC, all languages | 1M LOC < 60s |
| `incr` | `BenchmarkIncrementalSynth`, edit/delete/add per language | same 1M LOC repo | (regression gate) |
| `multi` | `BenchmarkMultiFileEdit`, `-benchtime 5x -count 3` | 50-file edit + restore | (regression gate) |
| `query` | `TestQueryLatency` with `GRAPHMASTER_QUERY_PERF_STRICT=1` | `GRAPHMASTER_QUERY_PERF_FILES` (default 3000) | per-tool p95 < 50ms |

The `query` gate enforces its limit inside the test (50ms in every
environment, no per-runner headroom); the others are checked by `check.sh`.
The nightly keeps the test default of 3000 files: `repo_map` and `imports`
grow with the file count (M4 p95 at 3k / 20k files: 9.5ms / 26.6ms and
1.7ms / 11.2ms), so the PLAN size of 100k files would not fit 50ms on the
slower runners. Raise `GRAPHMASTER_QUERY_PERF_FILES` locally to probe it.

### Threshold checker

`check.sh` builds and runs `internal/perfgate` (or runs `PERFGATE_BIN`). It
reads the last JSON line of a `RESULTS` file (`-k8s`) and any number of
`go test -bench` outputs (`-bench FILE`, repeatable, or as trailing
arguments; repeated runs of a benchmark are reduced to their median), and compares them with one profile of
`thresholds.json`. Every check prints one line; failures are listed again at
the end with the measured value and the limit, for example

```
perfgate: profile linux-ci: 1 of 42 checks FAILED:
  FAIL k8s k8s noop_s: 0.71 > max 0.6
```

Exit status: 0 pass, 1 a limit was exceeded or a configured result is
missing (a skipped or crashed benchmark cannot pass), 2 usage or input
error. `-summary FILE` appends a markdown table (the workflow passes
`$GITHUB_STEP_SUMMARY`), `-json FILE` writes the report, and `-partial`
ignores limits of benchmarks that did not run at all (subset runs).

### Thresholds

Limits are inclusive maxima (or minima, such as `LOC/s`). Values are numbers
in the metric's unit or Go durations (`"150ms"`), and bench metric keys may
be patterns (`"*-edit-ms"`).

| Metric | `plan` (M4 dev machine) | `macos-ci`, `linux-ci` | Reference M4 result |
|---|---|---|---|
| k8s full index | 60s | 180s | 31.1s |
| k8s no-change re-index | 0.3s | 0.6s | 0.056s |
| k8s touch 500 files | 1s | 2s | 0.086s |
| k8s edit 50 Go files | 2s | 4s | 0.415s |
| k8s restore edits | 2s | 4s | 0.398s |
| k8s `graph.db` size | 320 MiB | 320 MiB | 183 MiB |
| synth no-change dirty check (warm, cold) | 150ms | 450ms | 88ms, 117ms |
| synth 1M LOC full index | 60s (>= 16.7k LOC/s) | 120s | 14.5s (69k LOC/s) |
| incremental edit / delete / add (per language) | 0.5s / 1s / 2.5s | 1.5s / 3s / 7.5s | max 151ms / 272ms / 908ms |
| 50-file edit + restore | 500ms | 1500ms | 94ms |
| resolver refs per 50-file restore | 800 | 800 | 400 |

`plan` holds the PLAN targets and is the profile to use on a developer
machine. The CI profiles allow for slower hardware: the `macos-15` runner is
a 3-vCPU M1 VM and the `ubuntu-24.04` runner has 4 x86-64 vCPUs, so parallel
work (full index, initial builds) runs about 3x slower than on a 10-core M4,
and single-threaded steps (dirty check, small incremental updates) about
1.5-2x slower. The CI limits add roughly 2x headroom on top of that so that
runner noise does not fail the nightly. They are initial estimates: tighten
them from the uploaded results once a few nightly runs are in. The
deterministic counters (`graph.db` size, resolver refs) use the same limits
everywhere.

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
