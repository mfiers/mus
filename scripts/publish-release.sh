#!/usr/bin/env bash
#
# Publish a signed release to Codeberg. Driven by `make publish VERSION=x.y.z`.
#
# Preconditions (enforced):
#   - dist/ holds the build artifacts produced by `make release VERSION=$1`:
#     three binaries, each with a sibling .sig, plus SHA256SUMS + SHA256SUMS.sig.
#   - $CODEBERG_TOKEN is set with `write:repository` scope on $REPO.
#   - The git tag v$VERSION exists locally and on the remote.
#
# Side effects:
#   - Creates (idempotently) a Release at https://codeberg.org/$REPO/releases/tag/v$VERSION.
#   - Uploads every artifact in dist/ as a release asset.
#   - Re-running this script for an existing release re-uploads any missing
#     assets but will NOT overwrite already-present ones (Codeberg returns
#     409 on duplicate; we treat that as success).
#
# Does NOT push the git tag — `make release` already created it; assumes the
# caller has run `git push origin v$VERSION` (or `make release` is fresh and
# the tag is in place locally and remotely).

set -euo pipefail

VERSION="${1:-}"
REPO="${REPO:-atrxia/mus}"
API="https://codeberg.org/api/v1"
TOKEN="${CODEBERG_TOKEN:-${CODEBERG_GENERIC_TOKEN:-}}"

if [ -z "$VERSION" ]; then
    echo "usage: $0 VERSION (e.g. 0.1.1)" >&2
    exit 2
fi
if [ -z "$TOKEN" ]; then
    echo "CODEBERG_TOKEN (or CODEBERG_GENERIC_TOKEN) must be set" >&2
    exit 2
fi

TAG="v$VERSION"

# --- assertions --------------------------------------------------------------

[ -d dist ] || { echo "dist/ does not exist — run 'make release VERSION=$VERSION' first" >&2; exit 1; }

required=(
    "dist/mus-linux-amd64"
    "dist/mus-linux-arm64"
    "dist/mus-darwin-arm64"
    "dist/SHA256SUMS"
    "dist/SHA256SUMS.sig"
)
for f in "${required[@]}"; do
    [ -f "$f" ] || { echo "missing $f — run 'make release VERSION=$VERSION'" >&2; exit 1; }
done

# Detect whether the release is signed. Either every binary has a .sig or
# none does. A partial state is a setup bug we want to surface.
have_sig=0
missing_sig=0
for bin in dist/mus-linux-amd64 dist/mus-linux-arm64 dist/mus-darwin-arm64; do
    if [ -f "$bin.sig" ]; then have_sig=$((have_sig + 1)); else missing_sig=$((missing_sig + 1)); fi
done
if [ "$have_sig" -gt 0 ] && [ "$missing_sig" -gt 0 ]; then
    echo "partial signing state: $have_sig signed, $missing_sig unsigned" >&2
    echo "either run 'make sign' or remove dist/*.sig and re-run" >&2
    exit 1
fi
if [ "$have_sig" = 0 ]; then
    cat >&2 <<'WARN'
WARNING: no .sig files in dist/ — this release will be UNSIGNED.
  mus upgrade will refuse to install from it (embedded pubkey is set, so a
  missing .sig is treated as tampering). install.sh will warn and install.
  Intended only for fast debug iteration. Set SIGN=1 (default) for a real
  release.
WARN
fi

# Tag must exist locally and have been pushed.
git rev-parse --verify --quiet "$TAG" >/dev/null \
    || { echo "git tag $TAG does not exist locally" >&2; exit 1; }
git ls-remote --tags origin "$TAG" | grep -q "$TAG" \
    || { echo "tag $TAG not on origin — run 'git push origin $TAG'" >&2; exit 1; }

# --- create or look up release ----------------------------------------------

echo "==> creating release $TAG on $REPO"

api() {
    # api METHOD PATH [--data JSON] [--upload FILE --name NAME]
    local method="$1" path="$2"; shift 2
    local -a args=(
        -sS
        -w "\n__HTTP__:%{http_code}"
        -H "Authorization: token $TOKEN"
        -H "Accept: application/json"
        -X "$method"
    )
    while [ $# -gt 0 ]; do
        case "$1" in
            --data)
                args+=(-H "Content-Type: application/json" --data "$2"); shift 2 ;;
            --upload)
                args+=(-F "attachment=@$2"); shift 2 ;;
            *) echo "internal: unknown api() arg $1" >&2; exit 99 ;;
        esac
    done
    curl "${args[@]}" "$API$path"
}

http_status_of() {
    printf "%s" "$1" | tail -n1 | sed 's/__HTTP__://'
}
body_of() {
    printf "%s" "$1" | sed '$d'
}

# Read release notes from doc/release-notes/$TAG.md if present, otherwise
# fall back to git tag annotation.
notes=""
if [ -f "doc/release-notes/$TAG.md" ]; then
    notes=$(cat "doc/release-notes/$TAG.md")
elif tag_msg=$(git tag -l --format='%(contents:body)' "$TAG" 2>/dev/null) && [ -n "$tag_msg" ]; then
    notes="$tag_msg"
else
    notes="Signed release $TAG. See https://codeberg.org/$REPO for details."
fi

# JSON-escape the notes body.
notes_json=$(python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' <<< "$notes" 2>/dev/null \
    || printf "%s" "$notes" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e ':a;N;$!ba;s/\n/\\n/g' | awk '{print "\""$0"\""}')

payload=$(cat <<EOF
{
  "tag_name": "$TAG",
  "name": "mus $VERSION",
  "body": $notes_json,
  "draft": false,
  "prerelease": false
}
EOF
)

resp=$(api POST "/repos/$REPO/releases" --data "$payload")
status=$(http_status_of "$resp")
body=$(body_of "$resp")

case "$status" in
    201)
        release_id=$(printf "%s" "$body" | grep -o '"id":[0-9]*' | head -n1 | cut -d: -f2)
        echo "    created release id=$release_id"
        ;;
    409)
        # Release already exists — look it up so we can keep adding assets.
        resp=$(api GET "/repos/$REPO/releases/tags/$TAG")
        body=$(body_of "$resp")
        release_id=$(printf "%s" "$body" | grep -o '"id":[0-9]*' | head -n1 | cut -d: -f2)
        echo "    release already exists, id=$release_id — will sync assets"
        ;;
    *)
        echo "create release failed: HTTP $status" >&2
        echo "$body" >&2
        exit 1
        ;;
esac

[ -n "$release_id" ] || { echo "could not determine release id" >&2; exit 1; }

# --- upload assets ----------------------------------------------------------

echo "==> uploading assets"

upload_one() {
    local file="$1"
    local name
    name=$(basename "$file")
    local resp status
    resp=$(api POST "/repos/$REPO/releases/$release_id/assets?name=$name" --upload "$file")
    status=$(http_status_of "$resp")
    case "$status" in
        201) printf "    + %s\n" "$name" ;;
        409) printf "    = %s (already uploaded; skipping)\n" "$name" ;;
        *)
            echo "upload $name failed: HTTP $status" >&2
            body_of "$resp" >&2
            exit 1
            ;;
    esac
}

# Upload in a deterministic order: binaries first (most-clicked), then sigs,
# then checksums. Skip any file that doesn't exist — supports the SIGN=0 path
# where .sig files are absent.
for f in \
    dist/mus-linux-amd64 \
    dist/mus-linux-arm64 \
    dist/mus-darwin-arm64 \
    dist/mus-linux-amd64.sig \
    dist/mus-linux-arm64.sig \
    dist/mus-darwin-arm64.sig \
    dist/SHA256SUMS \
    dist/SHA256SUMS.sig
do
    [ -f "$f" ] || continue
    upload_one "$f"
done

echo
echo "Released:  https://codeberg.org/$REPO/releases/tag/$TAG"
echo "Install:   curl -sSL https://codeberg.org/$REPO/raw/branch/main/install.sh | bash"
