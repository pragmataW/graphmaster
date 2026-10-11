#!/usr/bin/env bash
# Benchmark graphmaster on a pinned Kubernetes checkout.
#
# Measures, in order:
#   1. full index from scratch
#   2. no-change re-index (dirty check only)
#   3. touch-only re-index (mtime changes, identical content: no reparse)
#   4. incremental re-index after editing EDIT_COUNT Go files
# and reports the resulting graph.db size.
#
# Environment:
#   GRAPHMASTER_BIN         graphmaster binary (default: build ./graphmaster via make)
#   GRAPHMASTER_BENCH_CACHE clone cache dir (default: ~/.cache/graphmaster-bench)
#   KUBE_VERSION            git tag to benchmark (default: v1.34.0)
#   TOUCH_COUNT             files to touch in step 3 (default: 500)
#   EDIT_COUNT              Go files to edit in step 4 (default: 50)
#   RESULTS                 optional file to append a JSON result line to
set -euo pipefail

KUBE_VERSION="${KUBE_VERSION:-v1.34.0}"
CACHE_DIR="${GRAPHMASTER_BENCH_CACHE:-$HOME/.cache/graphmaster-bench}"
TOUCH_COUNT="${TOUCH_COUNT:-500}"
EDIT_COUNT="${EDIT_COUNT:-50}"
REPO_URL="https://github.com/kubernetes/kubernetes.git"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$CACHE_DIR/kubernetes-$KUBE_VERSION"

log() { printf '==> %s\n' "$*" >&2; }

# now prints a wall-clock timestamp in seconds with millisecond precision
# (portable: macOS date has no %N).
now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }

# timed runs "$@" and prints the elapsed seconds.
timed() {
	local start end
	start="$(now)"
	"$@" >&2
	end="$(now)"
	awk -v s="$start" -v e="$end" 'BEGIN { printf "%.3f\n", e - s }'
}

# ---------------------------------------------------------------- binary
if [[ -z "${GRAPHMASTER_BIN:-}" ]]; then
	log "building graphmaster"
	make -C "$ROOT" build >&2
	GRAPHMASTER_BIN="$ROOT/graphmaster"
fi
[[ -x "$GRAPHMASTER_BIN" ]] || { echo "graphmaster binary not found: $GRAPHMASTER_BIN" >&2; exit 1; }
GRAPHMASTER_BIN="$(cd "$(dirname "$GRAPHMASTER_BIN")" && pwd)/$(basename "$GRAPHMASTER_BIN")"

# ---------------------------------------------------------------- checkout
if [[ ! -d "$SRC/.git" ]]; then
	log "cloning kubernetes $KUBE_VERSION into $SRC (shallow)"
	mkdir -p "$CACHE_DIR"
	git clone --quiet --depth 1 --branch "$KUBE_VERSION" "$REPO_URL" "$SRC"
fi
cd "$SRC"
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
	log "restoring modified files from a previous run"
	git checkout --quiet -- .
fi

FILES="$(git ls-files | wc -l | tr -d ' ')"
GO_FILES="$(git ls-files '*.go' | wc -l | tr -d ' ')"
log "kubernetes $KUBE_VERSION: $FILES tracked files, $GO_FILES Go files"

gm() { "$GRAPHMASTER_BIN" index; }

# Edited files are restored even if a step fails.
trap 'git -C "$SRC" checkout --quiet -- . 2>/dev/null || true' EXIT

# ---------------------------------------------------------------- 1. full
rm -rf .graphmaster
log "full index"
T_FULL="$(timed gm)"

# ---------------------------------------------------------------- 2. no-op
log "no-change re-index"
T_NOOP="$(timed gm)"

# ---------------------------------------------------------------- 3. touch
# Spread the sample over the tree deterministically.
pick() { # pick <count> <pathspec...>
	local count="$1"
	shift
	local total
	total="$(git ls-files -- "$@" | wc -l | tr -d ' ')"
	local step=$((total / count))
	((step < 1)) && step=1
	git ls-files -- "$@" | awk -v s="$step" -v c="$count" 'NR % s == 0 && n < c { print; n++ }'
}

log "touch $TOUCH_COUNT files (content unchanged)"
pick "$TOUCH_COUNT" '*.go' | xargs touch
T_TOUCH="$(timed gm)"

# ---------------------------------------------------------------- 4. edit
log "edit $EDIT_COUNT Go files"
pick "$EDIT_COUNT" '*.go' ':(exclude)vendor/*' ':(exclude)*_test.go' | while IFS= read -r f; do
	printf '\nfunc graphmasterBenchProbe%s() {}\n' "$(printf '%s' "$f" | cksum | cut -d' ' -f1)" >>"$f"
done
T_EDIT="$(timed gm)"

git checkout --quiet -- .
log "restore edits"
T_RESTORE="$(timed gm)"

# ---------------------------------------------------------------- report
DB_BYTES=0
if [[ -f .graphmaster/graph.db ]]; then
	DB_BYTES="$(wc -c <.graphmaster/graph.db | tr -d ' ')"
fi

printf '\n%-28s %10s\n' "step" "seconds"
printf '%-28s %10s\n' "full index" "$T_FULL"
printf '%-28s %10s\n' "no-change re-index" "$T_NOOP"
printf '%-28s %10s\n' "touch $TOUCH_COUNT files" "$T_TOUCH"
printf '%-28s %10s\n' "edit $EDIT_COUNT Go files" "$T_EDIT"
printf '%-28s %10s\n' "restore edits" "$T_RESTORE"
printf '%-28s %10s MiB\n' "graph.db size" "$(awk -v b="$DB_BYTES" 'BEGIN { printf "%.1f", b / 1048576 }')"

if [[ -n "${RESULTS:-}" ]]; then
	printf '{"version":"%s","files":%s,"go_files":%s,"full_s":%s,"noop_s":%s,"touch_s":%s,"edit_s":%s,"restore_s":%s,"db_bytes":%s}\n' \
		"$KUBE_VERSION" "$FILES" "$GO_FILES" "$T_FULL" "$T_NOOP" "$T_TOUCH" "$T_EDIT" "$T_RESTORE" "$DB_BYTES" >>"$RESULTS"
	log "appended result to $RESULTS"
fi
