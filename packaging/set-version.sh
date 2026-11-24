#!/bin/sh
# set-version.sh X.Y.Z: stamp the release version into app/package.json (the MCP server reports it, see
# core-go/build.sh and build.ps1).
set -e
v=$1
# Canonical x.y.z: the Windows version resource needs numeric parts without leading zeros.
echo "$v" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' || { echo "set-version.sh: '$v' is not plain x.y.z" >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
# perl, not sed: GNU and BSD (macOS) sed differ.
perl -pi -e "s/^(  \"version\": )\"[^\"]*\"/\\1\"$v\"/" "$root/app/package.json"
grep -q "^  \"version\": \"$v\"" "$root/app/package.json" || { echo "set-version.sh: app/package.json not updated" >&2; exit 1; }
echo "version $v"
