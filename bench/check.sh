#!/usr/bin/env bash
# Check benchmark results against bench/thresholds.json.
#
#   bench/check.sh -profile linux-ci -k8s k8s.jsonl bench-out/bench-*.txt
#
# All arguments go to the perfgate command (internal/perfgate); run with
# -h for its flags. Exit status: 0 all checks passed, 1 a threshold was
# exceeded or a result is missing, 2 usage or input error.
#
# Environment:
#   PERFGATE_BIN  prebuilt perfgate binary (default: build it, pure Go, no cgo)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

args=("$@")
has_thresholds=0
for a in "$@"; do
	case "$a" in -thresholds | --thresholds | -thresholds=* | --thresholds=*) has_thresholds=1 ;; esac
done
if ((!has_thresholds)); then
	args=(-thresholds "$ROOT/bench/thresholds.json" "${args[@]}")
fi

if [[ -z "${PERFGATE_BIN:-}" ]]; then
	# Build instead of `go run`, which reports every failure as exit 1.
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	(cd "$ROOT" && CGO_ENABLED=0 go build -o "$tmp/perfgate" ./internal/perfgate/cmd/perfgate)
	PERFGATE_BIN="$tmp/perfgate"
fi
rc=0
"$PERFGATE_BIN" "${args[@]}" || rc=$?
exit "$rc"
