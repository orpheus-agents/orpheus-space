#!/bin/sh
set -eu
version=${VERSION:-dev}
case "$version" in *[!A-Za-z0-9._-]*|'') echo 'Invalid VERSION' >&2; exit 1;; esac
mkdir -p bin/release
for arch in amd64 arm64; do
  destination="bin/release/cli-linux-$arch"
  mkdir -p "$destination"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$destination/orpheus-space" ./cmd/orpheus-space
  tar -czf "bin/release/orpheus-space_${version}_linux_${arch}.tar.gz" -C "$destination" orpheus-space
done
tar -czf "bin/release/orpheus-space_${version}_skill.tar.gz" -C skills orpheus-space
(cd bin/release && sha256sum "orpheus-space_${version}_linux_amd64.tar.gz" "orpheus-space_${version}_linux_arm64.tar.gz" "orpheus-space_${version}_skill.tar.gz" > checksums.txt)
