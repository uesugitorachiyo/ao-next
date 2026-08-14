#!/bin/sh
set -eu

candidate_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
metadata=$(cargo metadata --manifest-path "$candidate_root/Cargo.toml" --no-deps --format-version 1)

for public_file in LICENSE SECURITY.md .github/workflows/ci.yml; do
  test -f "$candidate_root/$public_file"
done

grep -F 'git clone https://github.com/uesugitorachiyo/ao-next.git' \
  "$candidate_root/README.md" >/dev/null

for package_name in ao-next-core ao-next-cli ao-next-eval; do
  printf '%s' "$metadata" | jq -e --arg name "$package_name" \
    '.packages | any(.name == $name)' >/dev/null
done

cli_help=$(cargo run --quiet --manifest-path "$candidate_root/Cargo.toml" \
  -p ao-next-cli -- --version 2>/dev/null)
printf '%s' "$cli_help" | jq -e \
  '.schema_version == "ao.next.cli-help.v1" and .command == "ao-next"' >/dev/null
