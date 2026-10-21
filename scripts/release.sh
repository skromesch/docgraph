#!/bin/sh
# Build release archives into dist/: darwin/arm64 natively, linux/amd64 and
# linux/arm64 in golang:<ver>-bookworm containers (glibc 2.36: runs on Debian 12,
# Ubuntu 22.04+, RHEL 9). cgo is required (SQLite, sqlite-vec, HF tokenizers), and
# the tokenizers static lib is C++ built against libstdc++, so Linux builds use a
# real Linux toolchain instead of zig.
#
#   scripts/release.sh v0.1.0          # all targets
#   TARGETS="linux/arm64" scripts/release.sh v0.1.0
#
# The ONNX Runtime and the model are not bundled: `docgraph model pull` fetches
# them (pinned + SHA-256 verified) on first use.
set -eu
cd "$(dirname "$0")/.."

version="${1:?usage: scripts/release.sh VERSION}"
targets="${TARGETS:-darwin/arm64 linux/amd64 linux/arm64}"
gover="$(go env GOVERSION | sed 's/^go//')"
tokver="$(go list -m -f '{{.Version}}' github.com/daulet/tokenizers)"
ldflags="-s -w -X main.version=$version"
mkdir -p dist lib/include

# sqlite-vec's cgo binding includes <sqlite3.h>; use the header of the SQLite that mattn bundles
cp "$(go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3)/sqlite3-binding.h" lib/include/sqlite3.h
chmod u+w lib/include/sqlite3.h

for t in $targets; do
  os="${t%/*}"; arch="${t#*/}"; name="docgraph_${version}_${os}_${arch}"
  out="dist/$name"; rm -rf "$out"; mkdir -p "$out"
  echo "== $t"
  case "$os" in
    darwin)
      [ "$(go env GOOS)/$(go env GOARCH)" = "$t" ] || { echo "darwin targets build natively only; skipping $t"; continue; }
      [ -f lib/libtokenizers.a ] || mise run tokenizers
      CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" CGO_LDFLAGS="-L$PWD/lib" \
        go build -trimpath -tags sqlite_fts5 -ldflags "$ldflags" -o "$out/docgraph" ./cmd/docgraph
      ;;
    linux)
      mkdir -p "lib/linux-$arch"
      [ -f "lib/linux-$arch/libtokenizers.a" ] || curl -fsSL \
        "https://github.com/daulet/tokenizers/releases/download/$tokver/libtokenizers.linux-$arch.tar.gz" | tar -xz -C "lib/linux-$arch"
      docker run --rm --platform "linux/$arch" -v "$PWD:/src" -v "$(go env GOMODCACHE):/go/pkg/mod" \
        -v "docgraph-gocache-$arch:/root/.cache/go-build" -w /src "golang:$gover-bookworm" \
        sh -c "CGO_ENABLED=1 CGO_CFLAGS='-I/src/lib/include' CGO_LDFLAGS='-L/src/lib/linux-$arch' \
          go build -buildvcs=false -trimpath -tags sqlite_fts5 -ldflags '$ldflags' -o '/src/$out/docgraph' ./cmd/docgraph"
      ;;
    *) echo "unsupported target $t"; exit 1 ;;
  esac
  cp README.md "$out/"
  tar -C "$out" -czf "dist/docgraph_${os}_${arch}.tar.gz" docgraph README.md
done
(cd dist && shasum -a 256 docgraph_*.tar.gz > checksums.txt && cat checksums.txt)
