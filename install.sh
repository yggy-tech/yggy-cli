#!/bin/sh
# Installs the yggy CLI from a GitHub release.
#
#   curl -fsSL https://github.com/yggy-tech/yggy-cli/releases/latest/download/install.sh | sh
#
# Environment:
#   YGGY_VERSION        release to install, for example 0.2.0 (default: the latest)
#   YGGY_INSTALL_DIR    where to put the binary (default: $HOME/.local/bin)
#   YGGY_DOWNLOAD_BASE  repository URL to download from (default: https://github.com/yggy-tech/yggy-cli)
#
# The archive is checked against the SHA-256 checksum published with the release.
set -eu

base="${YGGY_DOWNLOAD_BASE:-https://github.com/yggy-tech/yggy-cli}"
base="${base%/}"
dir="${YGGY_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
fail() { printf 'yggy install: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

if have curl; then
	fetch() { curl -fsSL -o "$2" "$1"; }
	final_url() { curl -fsSL -o /dev/null -w '%{url_effective}' "$1"; }
elif have wget; then
	fetch() { wget -q -O "$2" "$1"; }
	final_url() { wget -q -S --spider "$1" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
	fail "curl or wget is required"
fi

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported system $(uname -s). On Windows, download the zip from $base/releases and put yggy.exe on your PATH; an installer for Windows is coming soon" ;;
esac

case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) fail "unsupported architecture $(uname -m)" ;;
esac

version="${YGGY_VERSION:-}"
version="${version#v}"
if [ -z "$version" ]; then
	tag_url="$(final_url "$base/releases/latest")" || fail "could not look up the latest release at $base"
	version="${tag_url##*/v}"
	case "$version" in
		"" | *[!0-9.]*) fail "could not read the latest version from $tag_url (no release published yet?)" ;;
	esac
fi

archive="yggy_${version}_${os}_${arch}.tar.gz"
release="$base/releases/download/v${version}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading yggy $version for $os/$arch"
fetch "$release/$archive" "$tmp/$archive" || fail "could not download $release/$archive"
fetch "$release/checksums.txt" "$tmp/checksums.txt" || fail "could not download $release/checksums.txt"

want="$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || fail "no checksum published for $archive"
if have sha256sum; then
	got="$(sha256sum "$tmp/$archive" | awk '{ print $1 }')"
elif have shasum; then
	got="$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')"
else
	fail "sha256sum or shasum is required to verify the download"
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $archive: the download is corrupted or was changed"

tar -xzf "$tmp/$archive" -C "$tmp" yggy || fail "could not unpack $archive"

mkdir -p "$dir" || fail "could not create $dir"
cp "$tmp/yggy" "$dir/.yggy-install.$$" || fail "cannot write to $dir. Set YGGY_INSTALL_DIR to a folder you can write to"
chmod 0755 "$dir/.yggy-install.$$"
mv -f "$dir/.yggy-install.$$" "$dir/yggy"

say "Installed yggy $version to $dir/yggy"
case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "Add $dir to your PATH to run it, for example:"
	   say "  export PATH=\"$dir:\$PATH\"" ;;
esac
say "Next: yggy auth login. Update later with: yggy update"
