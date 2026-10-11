#!/usr/bin/env bash
# Run the synthetic-repository performance gates and save their output.
#
#   bench/synth.sh                  # every gate
#   bench/synth.sh dirty query      # a subset
#
# Gates (PLAN targets in parentheses; thresholds in bench/thresholds.json):
#   dirty  BenchmarkFreshenNoChange, 100k files no-change dirty check (< 150ms)
#   full   BenchmarkFullBuildSynth, 1M LOC cold full index            (< 60s)
#   incr   BenchmarkIncrementalSynth, per-language edit/delete/add on 1M LOC
#   multi  BenchmarkMultiFileEdit, 50-file edit + restore
#   query  TestQueryLatency with GRAPHMASTER_QUERY_PERF_STRICT=1      (p95 < 50ms)
#
# Benchmark output goes to $OUT/bench-<gate>.txt for bench/check.sh; the
# query gate passes or fails by itself (its log is $OUT/query.txt). Every
# requested gate runs even if an earlier one fails; the exit status is
# non-zero if any go test run failed.
#
# Environment:
#   OUT                    output directory (default: bench-out)
#   GO                     go command (default: go)
#   DIRTY_COUNT            -count for the dirty gate (default: 3; median is gated)
#   GRAPHMASTER_BENCH_FILES, GRAPHMASTER_BENCH_FULL_FILES, GRAPHMASTER_BENCH_LOC,
#   GRAPHMASTER_QUERY_PERF_FILES   size overrides, passed through to the tests
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${OUT:-bench-out}"
GO="${GO:-go}"
DIRTY_COUNT="${DIRTY_COUNT:-3}"
TAGS="sqlite_fts5 sqlite_omit_load_extension"
export CGO_ENABLED=1

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
cd "$ROOT"

log() { printf '==> %s\n' "$*" >&2; }

# gotest <output file> <go test args...>
gotest() {
	local out="$1"
	shift
	log "go test $* > $out"
	local start=$SECONDS
	"$GO" test -tags "$TAGS" -timeout 90m "$@" 2>&1 | tee "$out"
	local rc=${PIPESTATUS[0]}
	log "done in $((SECONDS - start))s (exit $rc)"
	return "$rc"
}

bench() { # bench <gate> <benchmark regexp> <benchtime> [extra args...]
	local gate="$1" re="$2" bt="$3"
	shift 3
	gotest "$OUT/bench-$gate.txt" -run '^$' -bench "$re" -benchtime "$bt" "$@" ./internal/index
}

gates=("$@")
((${#gates[@]})) || gates=(dirty full incr multi query)

failed=()
for g in "${gates[@]}"; do
	case "$g" in
	dirty) bench dirty '^BenchmarkFreshenNoChange$' 20x -count "$DIRTY_COUNT" ;;
	full) bench full '^BenchmarkFullBuildSynth$' 1x ;;
	incr) bench incr '^BenchmarkIncrementalSynth$' 1x ;;
	multi) bench multi '^BenchmarkMultiFileEdit$' 5x -count 3 ;;
	query) GRAPHMASTER_QUERY_PERF_STRICT=1 gotest "$OUT/query.txt" -run '^TestQueryLatency$' -count 1 -v ./internal/query ;;
	*)
		echo "unknown gate: $g (want dirty, full, incr, multi, query)" >&2
		exit 2
		;;
	esac || failed+=("$g")
done

if ((${#failed[@]})); then
	echo "synth gates failed to run or failed: ${failed[*]}" >&2
	exit 1
fi
