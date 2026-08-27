#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
go_bin=$(command -v go)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/ao-mission-soak-canary-offline.XXXXXX")
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

mkdir -p \
  "$scratch/home" \
  "$scratch/tmp" \
  "$scratch/gotmp" \
  "$scratch/gocache" \
  "$scratch/gomodcache"

cd "$root"
env -i \
  HOME="$scratch/home" \
  TMPDIR="$scratch/tmp" \
  GOTMPDIR="$scratch/gotmp" \
  PATH="$(dirname "$go_bin"):/usr/bin:/bin" \
  GOENV=off \
  GOWORK=off \
  GOFLAGS=-mod=readonly \
  GOTOOLCHAIN=local \
  GOPROXY=off \
  GOSUMDB=off \
  GONOSUMDB='*' \
  GOVCS='*:off' \
  GOMODCACHE="$scratch/gomodcache" \
  GOCACHE="$scratch/gocache" \
  CGO_ENABLED=0 \
  LC_ALL=C \
  TZ=UTC \
  "$go_bin" test ./internal/mission \
    -run '^TestSoakCanaryGitVerifierSupportsGitFile$' \
    -count=1

if find "$scratch/gomodcache" -mindepth 1 -print -quit | grep -q .; then
  echo "offline soak-canary test populated GOMODCACHE" >&2
  exit 1
fi
