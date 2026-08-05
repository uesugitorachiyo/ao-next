#!/bin/sh
set -eu

candidate_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
metadata=$(cargo metadata --manifest-path "$candidate_root/Cargo.toml" --no-deps --format-version 1)

for package_name in ao-next-core ao-next-cli ao-next-eval; do
  printf '%s' "$metadata" | jq -e --arg name "$package_name" \
    '.packages | any(.name == $name)' >/dev/null
done

schema_version=$(cargo run --quiet --manifest-path "$candidate_root/Cargo.toml" \
  -p ao-next-cli -- --schema-version)
test "$schema_version" = "ao.next.cli.v1"
