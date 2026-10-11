#!/bin/sh
# Sanity checks for a goreleaser dist/ directory (snapshot or release):
#   - every expected archive exists and matches checksums.txt
#   - linux binaries are statically linked ELF executables
#   - the darwin binary for this host runs and prints its version
#
#   sh scripts/verify-dist.sh [DIST_DIR]
set -eu

DIST=${1:-dist}
[ -d "$DIST" ] || {
	echo "verify-dist: $DIST does not exist; run make release-snapshot first" >&2
	exit 1
}

failed=0
ok() { printf 'ok   %s\n' "$1"; }
bad() {
	printf 'FAIL %s\n' "$1"
	failed=1
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

for a in graphmaster_darwin_arm64.tar.gz graphmaster_darwin_amd64.tar.gz \
	graphmaster_linux_amd64.tar.gz graphmaster_linux_arm64.tar.gz; do
	if [ ! -f "$DIST/$a" ]; then
		bad "$a missing"
		continue
	fi
	want=$(awk -v f="$a" '$2 == f { print $1 }' "$DIST/checksums.txt")
	if [ -n "$want" ] && [ "$want" = "$(sha256_of "$DIST/$a")" ]; then
		ok "$a checksum"
	else
		bad "$a checksum"
	fi
done

for bin in "$DIST"/linux_linux_*/graphmaster; do
	[ -f "$bin" ] || {
		bad "no linux binaries in $DIST"
		break
	}
	desc=$(file -b "$bin")
	case $desc in
	ELF*"statically linked"*) ok "$bin: static ELF" ;;
	*) bad "$bin: $desc" ;;
	esac
done

case $(uname -s)/$(uname -m) in
Darwin/arm64) host=darwin_darwin_arm64 ;;
Darwin/x86_64) host=darwin_darwin_amd64 ;;
*) host="" ;;
esac
if [ -n "$host" ]; then
	for bin in "$DIST"/"$host"*/graphmaster; do
		if [ -x "$bin" ] && out=$("$bin" --version) && [ "${out#graphmaster }" != "$out" ]; then
			ok "$bin --version: $out"
		else
			bad "$bin --version"
		fi
	done
fi

[ "$failed" -eq 0 ]
