#!/bin/sh
# Install todobem into ~/.todobem/bin — no Go, no sudo, no shell-profile edits.
#
#   curl -fsSL https://github.com/extractumio/todobem/releases/latest/download/install.sh | sh
#   curl -fsSL https://github.com/extractumio/todobem/releases/download/v0.1.0/install.sh | TODOBEM_VERSION=v0.1.0 sh
#
# It downloads the release archive for this OS and architecture with the release's SHA256SUMS,
# checks the checksum, and puts the binary at ~/.todobem/bin/todobem. An existing installation
# is left alone: `todobem upgrade` replaces it. Every later version is installed by
# `todobem upgrade`, which checks what it downloads the same way.
set -eu

repo="${TODOBEM_REPO:-extractumio/todobem}"
base="https://github.com/$repo/releases"

fail() {
  echo "todobem install: $*" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported OS $(uname -s) (macOS and Linux only)" ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "sha256sum or shasum is required"
fi

[ -n "${HOME:-}" ] || fail "HOME is not set"
dir="$HOME/.todobem/bin"
if [ -e "$dir/todobem" ]; then
  fail "todobem is already installed in $dir — run \`todobem upgrade\` instead"
fi

version="${TODOBEM_VERSION:-}"
if [ -z "$version" ]; then
  # /releases/latest redirects to /releases/tag/<version> of the latest release.
  url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$base/latest")" || fail "cannot reach $base"
  version="${url##*/tag/}"
  [ "$version" != "$url" ] || fail "no release published in $repo yet"
fi
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) fail "not a release version: $version" ;;
esac

archive="todobem_${version}_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading todobem $version for $os/$arch…"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/download/$version/SHA256SUMS" || fail "no SHA256SUMS in release $version"
curl -fsSL -o "$tmp/$archive" "$base/download/$version/$archive" || fail "release $version has no build for $os/$arch"

want="$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || fail "SHA256SUMS does not list $archive"
got="$(sha256 "$tmp/$archive")"
[ "$got" = "$want" ] || fail "checksum mismatch for $archive — nothing installed"

mkdir -p "$tmp/x"
tar -xzf "$tmp/$archive" -C "$tmp/x" todobem || fail "$archive holds no todobem binary"
[ -f "$tmp/x/todobem" ] && [ ! -L "$tmp/x/todobem" ] || fail "$archive holds no todobem binary"

mkdir -p "$dir"
chmod 700 "$HOME/.todobem" "$dir"
chmod 755 "$tmp/x/todobem"
mv "$tmp/x/todobem" "$dir/todobem"

echo "Installed $("$dir/todobem" -version)"
echo "  binary: $dir/todobem"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "  add it to your PATH:  export PATH=\"$dir:\$PATH\"" ;;
esac
echo "  start:  todobem          (the UI on http://127.0.0.1:7788)"
echo "  later:  todobem upgrade  ·  todobem upgrade rollback"
