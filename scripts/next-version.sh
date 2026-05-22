#!/usr/bin/env bash
#
# Print the next semantic version given a current version and a bump level.
#
# Usage:  scripts/next-version.sh CURRENT LEVEL
#   CURRENT  = X.Y.Z (no leading v, no -dev suffix)
#   LEVEL    = major | minor | patch
#
# Examples:
#   scripts/next-version.sh 0.1.0 patch   -> 0.1.1
#   scripts/next-version.sh 0.1.5 minor   -> 0.2.0
#   scripts/next-version.sh 0.9.9 major   -> 1.0.0

set -euo pipefail

current="${1:-}"
level="${2:-patch}"

if [ -z "$current" ]; then
    echo "usage: $0 CURRENT LEVEL" >&2
    exit 2
fi

# Accept (and strip) a leading "v" or trailing "-dev" so we're tolerant of
# whatever callers throw at us.
current="${current#v}"
current="${current%-dev}"

# Split X.Y.Z. Refuse anything that isn't three dot-separated integers.
if ! [[ "$current" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    echo "not a valid semver: $current" >&2
    exit 1
fi
major="${BASH_REMATCH[1]}"
minor="${BASH_REMATCH[2]}"
patch="${BASH_REMATCH[3]}"

case "$level" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
    *) echo "unknown level: $level (use major|minor|patch)" >&2; exit 1 ;;
esac

echo "$major.$minor.$patch"
