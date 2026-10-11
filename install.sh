#!/bin/sh
# graphmaster installer.
#
#   curl -fsSL https://raw.githubusercontent.com/pragmataW/graphmaster/main/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- -v 0.1.0 -b /usr/local/bin
#
# Downloads the release archive for this OS/architecture from GitHub
# releases, verifies its SHA-256 against the release's checksums.txt and
# installs the graphmaster binary. Nothing else is downloaded, nothing is
# reported anywhere (no telemetry).
#
# Options (each has an environment variable equivalent):
#   -b DIR       install directory      GRAPHMASTER_INSTALL_DIR
#                (default: /usr/local/bin as root, else ~/.local/bin)
#   -v VERSION   release to install     GRAPHMASTER_VERSION (default: latest)
#   -n           dry run: print what would be downloaded and exit
#                                       GRAPHMASTER_DRY_RUN=1
#
# Overrides for mirrors and testing:
#   INSTALL_BASE_URL   directory holding the archives and checksums.txt
#                      (https://, http:// or file://); replaces the GitHub URL
#   GRAPHMASTER_OS     darwin | linux (default: uname -s)
#   GRAPHMASTER_ARCH   amd64 | arm64 (default: uname -m)
set -eu

REPO="pragmataW/graphmaster"
BINARY="graphmaster"

say() { printf '%s\n' "$*"; }
warn() { printf 'graphmaster-install: warning: %s\n' "$*" >&2; }
die() {
	printf 'graphmaster-install: error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
Usage: install.sh [-b DIR] [-v VERSION] [-n]

  -b DIR      install directory (default: /usr/local/bin as root, else ~/.local/bin)
  -v VERSION  release to install, e.g. 0.1.0 (default: latest)
  -n          dry run: print what would be downloaded and exit

Environment: GRAPHMASTER_INSTALL_DIR, GRAPHMASTER_VERSION, GRAPHMASTER_DRY_RUN,
INSTALL_BASE_URL (mirror or file:// directory), GRAPHMASTER_OS, GRAPHMASTER_ARCH.
EOF
}

install_dir=${GRAPHMASTER_INSTALL_DIR:-}
version=${GRAPHMASTER_VERSION:-latest}
dry_run=${GRAPHMASTER_DRY_RUN:-0}

while [ $# -gt 0 ]; do
	case $1 in
	-b)
		[ $# -ge 2 ] || die "-b needs a directory"
		install_dir=$2
		shift 2
		;;
	-v)
		[ $# -ge 2 ] || die "-v needs a version"
		version=$2
		shift 2
		;;
	-n) dry_run=1 && shift ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option: $1 (try -h)" ;;
	esac
done

has() { command -v "$1" >/dev/null 2>&1; }

detect_os() {
	if [ -n "${GRAPHMASTER_OS:-}" ]; then
		os=$GRAPHMASTER_OS
	else
		case $(uname -s) in
		Darwin) os=darwin ;;
		Linux) os=linux ;;
		MINGW* | MSYS* | CYGWIN* | Windows_NT) os=windows ;;
		*) os=$(uname -s) ;;
		esac
	fi
	case $os in
	darwin | linux) ;;
	windows) die "Windows is not supported by this script; download the .zip from https://github.com/$REPO/releases (experimental)" ;;
	*) die "unsupported operating system: $os (graphmaster ships darwin and linux builds)" ;;
	esac
}

detect_arch() {
	if [ -n "${GRAPHMASTER_ARCH:-}" ]; then
		arch=$GRAPHMASTER_ARCH
	else
		arch=$(uname -m)
		# A shell running under Rosetta 2 reports x86_64 on Apple silicon;
		# prefer the native build.
		if [ "$os" = darwin ] && [ "$arch" = x86_64 ] &&
			[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
			arch=arm64
		fi
	fi
	case $arch in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture: $arch (graphmaster ships amd64 and arm64 builds)" ;;
	esac
}

# fetch URL DEST downloads URL to DEST. file:// URLs are copied directly so
# a local release directory works without curl.
fetch() {
	case $1 in
	file://*)
		[ -f "${1#file://}" ] || return 1
		cp "${1#file://}" "$2"
		;;
	https://*)
		if has curl; then
			curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 -o "$2" "$1"
		elif has wget; then
			wget -q -O "$2" "$1"
		else
			die "curl or wget is required"
		fi
		;;
	*)
		if has curl; then
			curl -fsSL --retry 3 -o "$2" "$1"
		elif has wget; then
			wget -q -O "$2" "$1"
		else
			die "curl or wget is required"
		fi
		;;
	esac
}

sha256_of() {
	if has sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif has shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif has openssl; then
		openssl dgst -sha256 -r "$1" | awk '{print $1}'
	else
		die "sha256sum, shasum or openssl is required to verify the download"
	fi
}

default_install_dir() {
	if [ "$(id -u)" -eq 0 ]; then
		echo /usr/local/bin
	else
		[ -n "${HOME:-}" ] || die "HOME is not set; pass -b DIR"
		echo "$HOME/.local/bin"
	fi
}

detect_os
detect_arch

case $version in
latest) ;;
v*) ;;
*) version=v$version ;;
esac

if [ -n "${INSTALL_BASE_URL:-}" ]; then
	base_url=${INSTALL_BASE_URL%/}
elif [ "$version" = latest ]; then
	base_url="https://github.com/$REPO/releases/latest/download"
else
	base_url="https://github.com/$REPO/releases/download/$version"
fi

archive="${BINARY}_${os}_${arch}.tar.gz"
[ -n "$install_dir" ] || install_dir=$(default_install_dir)

if [ "$dry_run" = 1 ]; then
	say "would download $base_url/$archive"
	say "would verify it against $base_url/checksums.txt"
	say "would install $BINARY to $install_dir"
	exit 0
fi

has tar || die "tar is required"

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t graphmaster)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' INT TERM

say "Downloading $archive ($version) ..."
fetch "$base_url/$archive" "$tmp/$archive" || die "download failed: $base_url/$archive"
fetch "$base_url/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base_url/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt")
[ -n "$expected" ] || die "no checksum for $archive in checksums.txt; refusing to install"
actual=$(sha256_of "$tmp/$archive")
[ "$expected" = "$actual" ] ||
	die "checksum mismatch for $archive: expected $expected, got $actual; refusing to install"
say "Verified SHA-256 $actual"

mkdir "$tmp/x"
tar -xzf "$tmp/$archive" -C "$tmp/x" || die "could not extract $archive"
[ -f "$tmp/x/$BINARY" ] || die "$archive does not contain $BINARY"

# Copy next to the target, then rename, so a running graphmaster (an agent's
# MCP server, for example) is replaced atomically instead of overwritten.
sudo=""
if ! mkdir -p "$install_dir" 2>/dev/null || [ ! -w "$install_dir" ]; then
	if [ "$(id -u)" -ne 0 ] && has sudo; then
		say "$install_dir is not writable; using sudo"
		sudo="sudo"
		$sudo mkdir -p "$install_dir"
	else
		die "$install_dir is not writable; pass -b DIR"
	fi
fi
$sudo cp "$tmp/x/$BINARY" "$install_dir/.$BINARY.new.$$"
$sudo chmod 755 "$install_dir/.$BINARY.new.$$"
$sudo mv -f "$install_dir/.$BINARY.new.$$" "$install_dir/$BINARY"

say "Installed $install_dir/$BINARY"
"$install_dir/$BINARY" --version 2>/dev/null || true

case ":${PATH:-}:" in
*":$install_dir:"*) ;;
*)
	warn "$install_dir is not on your PATH"
	say "  Add it to your shell profile, for example:"
	say "    export PATH=\"$install_dir:\$PATH\""
	;;
esac

say ""
say "Next steps:"
say "  graphmaster install   # register graphmaster with Claude Code, Codex, Cursor and OpenCode"
say "  graphmaster doctor    # check the setup"
say "Then restart your agent and open any git repository."
