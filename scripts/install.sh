#!/bin/sh
# Install FailTrace from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/Ri4dH/failtrace/main/scripts/install.sh | sh
#
# Environment:
#   FAILTRACE_VERSION      release to install, e.g. v0.1.0 (default: the latest release)
#   FAILTRACE_INSTALL_DIR  where to put the binary (default: /usr/local/bin if writable, else ~/.local/bin)
#   FAILTRACE_BASE_URL     download location (default: GitHub releases; used for testing)
#
# The archive's SHA-256 checksum is verified against the release's checksums.txt
# before anything is installed.

set -eu

REPO="Ri4dH/failtrace"

say() { printf 'failtrace-install: %s\n' "$*" >&2; }
fail() { say "error: $*"; exit 1; }

# fetch URL DEST downloads URL to DEST (DEST "-" prints to stdout).
fetch() {
	if command -v curl >/dev/null 2>&1; then
		if [ "$2" = "-" ]; then curl -fsSL "$1"; else curl -fsSL -o "$2" "$1"; fi
	elif command -v wget >/dev/null 2>&1; then
		if [ "$2" = "-" ]; then wget -qO- "$1"; else wget -qO "$2" "$1"; fi
	else
		fail "curl or wget is required"
	fi
}

latest_version() {
	# github.com/<repo>/releases/latest redirects to .../releases/tag/<version>.
	if command -v curl >/dev/null 2>&1; then
		url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") || return 1
	else
		url=$(wget -S --spider "https://github.com/$REPO/releases/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1) || return 1
	fi
	tag=${url##*/}
	case "$tag" in
		v[0-9]*) printf '%s\n' "$tag" ;;
		*) return 1 ;;
	esac
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		fail "sha256sum or shasum is required to verify the download"
	fi
}

main() {
	case "$(uname -s)" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) fail "unsupported OS $(uname -s); download a release from https://github.com/$REPO/releases" ;;
	esac
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		arm64 | aarch64) arch=arm64 ;;
		*) fail "unsupported architecture $(uname -m)" ;;
	esac

	version=${FAILTRACE_VERSION:-}
	if [ -z "$version" ]; then
		version=$(latest_version) || fail "could not determine the latest release; set FAILTRACE_VERSION"
	fi
	case "$version" in
		v*) ;;
		*) version="v$version" ;;
	esac
	case "$version" in
		*[!A-Za-z0-9.+_-]*) fail "invalid version $version" ;;
	esac

	base=${FAILTRACE_BASE_URL:-https://github.com/$REPO/releases/download}
	archive="failtrace_${version#v}_${os}_${arch}.tar.gz"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "downloading $archive ($version)"
	fetch "$base/$version/$archive" "$tmp/$archive" || fail "download failed: $base/$version/$archive"
	fetch "$base/$version/checksums.txt" "$tmp/checksums.txt" || fail "could not download checksums.txt"

	expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
	actual=$(sha256_of "$tmp/$archive")
	[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive (expected $expected, got $actual)"

	tar -xzf "$tmp/$archive" -C "$tmp" failtrace || fail "could not extract $archive"

	dir=${FAILTRACE_INSTALL_DIR:-}
	if [ -z "$dir" ]; then
		if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
	fi
	mkdir -p "$dir" || fail "cannot create $dir"
	[ -w "$dir" ] || fail "$dir is not writable; set FAILTRACE_INSTALL_DIR"
	install -m 0755 "$tmp/failtrace" "$dir/failtrace" 2>/dev/null || {
		cp "$tmp/failtrace" "$dir/failtrace" && chmod 0755 "$dir/failtrace"
	} || fail "could not install to $dir"

	say "installed $("$dir/failtrace" version 2>/dev/null || echo "failtrace $version") to $dir/failtrace"
	case ":$PATH:" in
		*":$dir:"*) ;;
		*) say "note: $dir is not on your PATH; add it, e.g.: export PATH=\"$dir:\$PATH\"" ;;
	esac
}

main "$@"
