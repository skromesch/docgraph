#!/bin/sh
# Install the docgraph release binary (Linux amd64/arm64, macOS arm64).
#
#   curl -fsSL https://raw.githubusercontent.com/skromesch/docgraph/main/install.sh | sh
#
# Options (flags or environment variables):
#   -v, --version VERSION   release to install, e.g. v0.1.0        (DOCGRAPH_VERSION, default: latest)
#   -d, --dir DIR           where to put the binary                (DOCGRAPH_INSTALL_DIR)
#                           default: /usr/local/bin when run as root, else ~/.local/bin
#   --no-model              do not download the model + ONNX Runtime afterwards
#   -h, --help
# Piped into sh, pass flags with:  curl -fsSL .../install.sh | sh -s -- --version v0.1.0
#
# The archive's SHA-256 is checked against the release's checksums.txt before anything is
# installed; `docgraph model pull` then fetches the pinned, SHA-256-verified model (~570 MB)
# and ONNX Runtime into ~/.cache/docgraph.
set -eu

REPO="${DOCGRAPH_REPO:-skromesch/docgraph}"
BIN="docgraph"
BASE_URL="${DOCGRAPH_BASE_URL:-https://github.com/$REPO/releases}" # overridable for testing
version="${DOCGRAPH_VERSION:-latest}"
dir="${DOCGRAPH_INSTALL_DIR:-}"
pull_model=1

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    -v|--version) [ $# -ge 2 ] || die "$1 needs a value"; version="$2"; shift 2 ;;
    -d|--dir)     [ $# -ge 2 ] || die "$1 needs a value"; dir="$2"; shift 2 ;;
    --no-model)   pull_model=0; shift ;;
    -h|--help)    sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac
[ "$os/$arch" != darwin/amd64 ] || die "no Intel macOS build (ONNX Runtime ships no prebuilt osx-x86_64 library)"

if [ -z "$dir" ]; then
  if [ "$(id -u)" = 0 ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  die "need curl or wget"
fi
command -v tar >/dev/null 2>&1 || die "need tar"
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "need sha256sum or shasum to verify the download"
fi

if [ "$version" = latest ]; then
  url="$BASE_URL/latest/download"
else
  case "$version" in v*) ;; *) version="v$version" ;; esac
  url="$BASE_URL/download/$version"
fi

archive="${BIN}_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $archive ($version)"
fetch "$url/$archive" "$tmp/$archive" || die "download failed: $url/$archive"
fetch "$url/checksums.txt" "$tmp/checksums.txt" || die "download failed: $url/checksums.txt"

want="$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "$archive is not listed in checksums.txt"
got="$(sha256 "$tmp/$archive")"
[ "$want" = "$got" ] || die "checksum mismatch for $archive (expected $want, got $got)"

tar -xzf "$tmp/$archive" -C "$tmp" "$BIN" || die "could not extract $BIN from $archive"
mkdir -p "$dir" || die "cannot create $dir (use --dir, or run with sudo)"
[ -w "$dir" ] || die "$dir is not writable (use --dir, or run with sudo)"
# Write next to the target, then rename: replacing a running binary in place is safe.
cp "$tmp/$BIN" "$dir/.$BIN.new" && chmod 755 "$dir/.$BIN.new" && mv -f "$dir/.$BIN.new" "$dir/$BIN"
if [ "$os" = darwin ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$dir/$BIN" 2>/dev/null || true
fi

say "Installed $dir/$BIN"
"$dir/$BIN" --version 2>/dev/null || true
if [ "$pull_model" = 1 ]; then
  say ""
  say "Downloading the embedding model and ONNX Runtime (once, ~600 MB) ..."
  "$dir/$BIN" model pull || say "model download failed; retry later with: $BIN model pull"
fi
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say ""; say "$dir is not on your PATH. Add it, e.g.:"; say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
say ""
say "Next: cd into your project and run '$BIN' for the interactive menu (init, index, ignore rules, MCP registration)."
