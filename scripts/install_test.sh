#!/bin/sh
# Tests for install.sh against a fake release directory served over file://.
#
#   sh scripts/install_test.sh
#
# Every case runs install.sh in a throwaway HOME with INSTALL_BASE_URL pointing
# at a locally built release layout, so no network access is needed.
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
INSTALLER="$ROOT/install.sh"
# Shell that runs install.sh; set INSTALL_TEST_SHELL=dash to check strict POSIX.
SH=${INSTALL_TEST_SHELL:-sh}

WORK=$(mktemp -d "${TMPDIR:-/tmp}/gm-install-test.XXXXXX")
trap 'rm -rf "$WORK"' EXIT INT TERM

PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); printf 'ok   %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$1"; [ -f "$WORK/out" ] && sed 's/^/     | /' "$WORK/out"; }

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# make_release DIR OS ARCH: writes a fake archive plus checksums.txt into DIR.
# The fake binary is a shell script that prints a version line.
make_release() {
	dir=$1 os=$2 arch=$3
	mkdir -p "$dir/stage"
	cat >"$dir/stage/graphmaster" <<'EOF'
#!/bin/sh
echo "graphmaster 9.9.9 (commit fake, built test)"
EOF
	chmod +x "$dir/stage/graphmaster"
	cp "$ROOT/LICENSE" "$dir/stage/LICENSE"
	archive="graphmaster_${os}_${arch}.tar.gz"
	(cd "$dir/stage" && tar -czf "../$archive" graphmaster LICENSE)
	rm -rf "$dir/stage"
	printf '%s  %s\n' "$(sha256 "$dir/$archive")" "$archive" >"$dir/checksums.txt"
	# An unrelated entry must not confuse the lookup.
	printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" \
		"graphmaster_${os}_${arch}.tar.gz.sbom.json" >>"$dir/checksums.txt"
}

# run_installer NAME [ENV=VALUE...]: runs install.sh in a fresh
# HOME, capturing output in $WORK/out and the exit status in $status.
run_installer() {
	case_home="$WORK/home-$1"
	shift
	mkdir -p "$case_home"
	set +e
	env HOME="$case_home" PATH="/usr/bin:/bin:/usr/sbin:/sbin" "$@" "$SH" "$INSTALLER" >"$WORK/out" 2>&1
	status=$?
	set -e
}

host_os() {
	case "$(uname -s)" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	*) echo unsupported ;;
	esac
}

OS=$(host_os)
ARCH=amd64
REL="$WORK/release"
make_release "$REL" "$OS" "$ARCH"

# 1. Happy path: installs into an explicit dir and the binary runs.
run_installer ok INSTALL_BASE_URL="file://$REL" GRAPHMASTER_OS="$OS" GRAPHMASTER_ARCH="$ARCH" \
	GRAPHMASTER_INSTALL_DIR="$WORK/bin-ok"
if [ "$status" -eq 0 ] && [ -x "$WORK/bin-ok/graphmaster" ] &&
	"$WORK/bin-ok/graphmaster" | grep -q 'graphmaster 9.9.9' &&
	grep -q 'graphmaster install' "$WORK/out"; then
	pass "installs verified archive and prints next steps"
else
	fail "installs verified archive and prints next steps"
fi

# 2. Default dir for a non-root user is ~/.local/bin, created on demand.
if [ "$(id -u)" -ne 0 ]; then
	run_installer default INSTALL_BASE_URL="file://$REL" GRAPHMASTER_OS="$OS" GRAPHMASTER_ARCH="$ARCH"
	if [ "$status" -eq 0 ] && [ -x "$WORK/home-default/.local/bin/graphmaster" ] &&
		grep -q 'not on your PATH' "$WORK/out"; then
		pass "defaults to ~/.local/bin and warns when it is not on PATH"
	else
		fail "defaults to ~/.local/bin and warns when it is not on PATH"
	fi
fi

# 3. -b flag overrides the install dir.
mkdir -p "$WORK/home-flag"
set +e
env HOME="$WORK/home-flag" PATH="/usr/bin:/bin:/usr/sbin:/sbin" INSTALL_BASE_URL="file://$REL" \
	GRAPHMASTER_OS="$OS" GRAPHMASTER_ARCH="$ARCH" "$SH" "$INSTALLER" -b "$WORK/bin-flag" >"$WORK/out" 2>&1
status=$?
set -e
if [ "$status" -eq 0 ] && [ -x "$WORK/bin-flag/graphmaster" ]; then
	pass "-b selects the install dir"
else
	fail "-b selects the install dir"
fi

# 4. Checksum mismatch: archive tampered after checksums were published.
BAD="$WORK/release-bad"
make_release "$BAD" "$OS" "$ARCH"
printf 'tampered' >>"$BAD/graphmaster_${OS}_${ARCH}.tar.gz"
run_installer bad INSTALL_BASE_URL="file://$BAD" GRAPHMASTER_OS="$OS" GRAPHMASTER_ARCH="$ARCH" \
	GRAPHMASTER_INSTALL_DIR="$WORK/bin-bad"
if [ "$status" -ne 0 ] && grep -qi 'checksum mismatch' "$WORK/out" && [ ! -e "$WORK/bin-bad/graphmaster" ]; then
	pass "rejects an archive whose sha256 does not match"
else
	fail "rejects an archive whose sha256 does not match"
fi

# 5. Archive missing from checksums.txt is rejected, not installed unverified.
MISS="$WORK/release-miss"
make_release "$MISS" "$OS" "$ARCH"
grep -v "graphmaster_${OS}_${ARCH}.tar.gz\$" "$MISS/checksums.txt" >"$MISS/c" && mv "$MISS/c" "$MISS/checksums.txt"
run_installer miss INSTALL_BASE_URL="file://$MISS" GRAPHMASTER_OS="$OS" GRAPHMASTER_ARCH="$ARCH" \
	GRAPHMASTER_INSTALL_DIR="$WORK/bin-miss"
if [ "$status" -ne 0 ] && grep -qi 'no checksum' "$WORK/out" && [ ! -e "$WORK/bin-miss/graphmaster" ]; then
	pass "rejects an archive with no checksum entry"
else
	fail "rejects an archive with no checksum entry"
fi

# 6. Missing archive (unpublished platform) fails cleanly.
run_installer noarch INSTALL_BASE_URL="file://$REL" GRAPHMASTER_OS="$OS" GRAPHMASTER_ARCH=arm64 \
	GRAPHMASTER_INSTALL_DIR="$WORK/bin-noarch"
if [ "$status" -ne 0 ] && grep -qi 'download failed' "$WORK/out"; then
	pass "fails when the platform archive is not published"
else
	fail "fails when the platform archive is not published"
fi

# 7. Unsupported OS / arch are rejected before any download.
run_installer badarch INSTALL_BASE_URL="file://$REL" GRAPHMASTER_OS="$OS" GRAPHMASTER_ARCH=mips
if [ "$status" -ne 0 ] && grep -qi 'unsupported architecture' "$WORK/out"; then
	pass "rejects unsupported architectures"
else
	fail "rejects unsupported architectures"
fi
run_installer bados INSTALL_BASE_URL="file://$REL" GRAPHMASTER_OS=plan9 GRAPHMASTER_ARCH="$ARCH"
if [ "$status" -ne 0 ] && grep -qi 'unsupported operating system' "$WORK/out"; then
	pass "rejects unsupported operating systems"
else
	fail "rejects unsupported operating systems"
fi

# 8. Version pinning builds the tagged GitHub URL (checked via dry run).
run_installer pin GRAPHMASTER_VERSION=0.2.0 GRAPHMASTER_OS=linux GRAPHMASTER_ARCH=arm64 GRAPHMASTER_DRY_RUN=1
if [ "$status" -eq 0 ] &&
	grep -q 'https://github.com/pragmataW/graphmaster/releases/download/v0.2.0/graphmaster_linux_arm64.tar.gz' "$WORK/out"; then
	pass "pins a version to its release download URL"
else
	fail "pins a version to its release download URL"
fi
run_installer latest GRAPHMASTER_OS=darwin GRAPHMASTER_ARCH=amd64 GRAPHMASTER_DRY_RUN=1
if [ "$status" -eq 0 ] &&
	grep -q 'https://github.com/pragmataW/graphmaster/releases/latest/download/graphmaster_darwin_amd64.tar.gz' "$WORK/out"; then
	pass "defaults to the latest release URL"
else
	fail "defaults to the latest release URL"
fi

# 9. Optional: a real goreleaser snapshot in dist/ installs and runs.
if [ -f "$ROOT/dist/checksums.txt" ] && [ "$OS" = darwin ]; then
	real_arch=$(uname -m)
	[ "$real_arch" = x86_64 ] && real_arch=amd64
	run_installer dist INSTALL_BASE_URL="file://$ROOT/dist" GRAPHMASTER_INSTALL_DIR="$WORK/bin-dist" \
		GRAPHMASTER_ARCH="$real_arch"
	if [ "$status" -eq 0 ] && "$WORK/bin-dist/graphmaster" --version | grep -q '^graphmaster '; then
		pass "installs the goreleaser snapshot from dist/"
	else
		fail "installs the goreleaser snapshot from dist/"
	fi
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
