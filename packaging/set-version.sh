#!/bin/sh
# set-version.sh X.Y.Z: stamp the release version into app/package.json and core/Cargo.toml (the MCP server
# reports CARGO_PKG_VERSION). release.yml runs it in its own checkout before building; nothing is committed.
set -e
v=$1
case $v in
  *[!0-9.]*|.*|*.|*..*|'') echo "set-version.sh: '$v' is not plain x.y.z" >&2; exit 1 ;;
esac
[ "$(echo "$v" | tr -cd . | wc -c)" -eq 2 ] || { echo "set-version.sh: '$v' is not plain x.y.z" >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
# The first `version = ...` in Cargo.toml is the [package] one. perl, not sed: GNU and BSD (macOS) sed differ.
perl -0pi -e "s/^version = \"[^\"]*\"/version = \"$v\"/m" "$root/core/Cargo.toml"
perl -pi -e "s/^(  \"version\": )\"[^\"]*\"/\\1\"$v\"/" "$root/app/package.json"
grep -q "^version = \"$v\"" "$root/core/Cargo.toml" || { echo "set-version.sh: core/Cargo.toml not updated" >&2; exit 1; }
grep -q "^  \"version\": \"$v\"" "$root/app/package.json" || { echo "set-version.sh: app/package.json not updated" >&2; exit 1; }
echo "version $v"
