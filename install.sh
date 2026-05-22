#!/usr/bin/env bash
#
# mus CLI installer
#
# Usage:
#   curl -sSL https://codeberg.org/atrxia/mus/raw/branch/main/install.sh | bash
#
# Or to inspect first (recommended):
#   curl -sSL https://codeberg.org/atrxia/mus/raw/branch/main/install.sh -o install.sh
#   less install.sh
#   bash install.sh
#
# Optional environment variables:
#   MUS_VERSION       Specific tag to install (default: latest)
#   MUS_INSTALL_DIR   Force install location (default: auto-detect)

set -euo pipefail

REPO="atrxia/mus"
BIN_NAME="mus"
TAG="${MUS_VERSION:-}"

# --- helpers -----------------------------------------------------------------

if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    BOLD=$(tput bold); RED=$(tput setaf 1); GREEN=$(tput setaf 2)
    YELLOW=$(tput setaf 3); CYAN=$(tput setaf 6); RESET=$(tput sgr0)
else
    BOLD=""; RED=""; GREEN=""; YELLOW=""; CYAN=""; RESET=""
fi

info()  { printf "%s%s%s\n" "$CYAN"   "$*" "$RESET"; }
ok()    { printf "%s%s%s\n" "$GREEN"  "✓ $*" "$RESET"; }
warn()  { printf "%s%s%s\n" "$YELLOW" "⚠ $*" "$RESET" >&2; }
die()   { printf "%s%s%s\n" "$RED"    "✗ $*" "$RESET" >&2; exit 1; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"
}

# --- platform detection ------------------------------------------------------

# Asset names must match what `make build-all` emits in dist/ and what
# upgradeAssetName() in internal/cli/upgrade.go returns.
detect_asset() {
    local os arch
    os=$(uname -s)
    arch=$(uname -m)

    case "$os/$arch" in
        Linux/x86_64|Linux/amd64)
            echo "mus-linux-amd64"
            ;;
        Linux/aarch64|Linux/arm64)
            echo "mus-linux-arm64"
            ;;
        Darwin/arm64|Darwin/aarch64)
            echo "mus-darwin-arm64"
            ;;
        Darwin/x86_64)
            die "macOS on Intel (x86_64) is not currently published. Build from source: see https://codeberg.org/$REPO"
            ;;
        *)
            die "Unsupported platform: $os/$arch"
            ;;
    esac
}

# --- install location --------------------------------------------------------

# Determine where to install. Priority:
#   0. If $MUS_INSTALL_DIR is set, use it.
#   1. If mus is already in PATH:
#        - target dir writable -> reuse (in-place upgrade)
#        - target dir not writable -> abort with instructions
#   2. First writable, in-PATH directory among: ~/bin, ~/.local/bin, /usr/local/bin
#   3. ~/.local/bin (creating it; warn that PATH needs updating)
find_install_dir() {
    if [ -n "${MUS_INSTALL_DIR:-}" ]; then
        mkdir -p "$MUS_INSTALL_DIR" 2>/dev/null || true
        [ -w "$MUS_INSTALL_DIR" ] || die "MUS_INSTALL_DIR=$MUS_INSTALL_DIR is not writable"
        echo "$MUS_INSTALL_DIR"
        return
    fi

    # 1. Existing mus in PATH
    local existing existing_dir
    if existing=$(command -v "$BIN_NAME" 2>/dev/null); then
        existing_dir=$(dirname "$existing")
        if [ -w "$existing" ] || { [ ! -e "$existing" ] && [ -w "$existing_dir" ]; }; then
            warn "Existing mus found at $existing — will overwrite"
            echo "$existing_dir"
            return
        else
            die "Existing mus at $existing is not writable.
   Remove it manually (sudo rm $existing) and rerun this script,
   or set MUS_INSTALL_DIR=/some/writable/dir and rerun."
        fi
    fi

    # 2. Writable, in-PATH user directory
    local candidate
    for candidate in "$HOME/bin" "$HOME/.local/bin" "/usr/local/bin"; do
        case ":$PATH:" in
            *":$candidate:"*)
                if [ -d "$candidate" ] && [ -w "$candidate" ]; then
                    echo "$candidate"
                    return
                fi
                ;;
        esac
    done

    # 3. Fallback: create ~/.local/bin
    mkdir -p "$HOME/.local/bin"
    echo "$HOME/.local/bin"
}

# --- release lookup ----------------------------------------------------------

get_latest_tag() {
    local url body tag
    url="https://codeberg.org/api/v1/repos/$REPO/releases/latest"
    body=$(curl -fsSL "$url") || die "Could not fetch latest release info from $url"
    # Parse "tag_name":"vX.Y.Z" without depending on python/jq
    tag=$(printf "%s" "$body" | grep -o '"tag_name":"[^"]*"' | head -n1 | cut -d'"' -f4)
    [ -n "$tag" ] || die "Could not parse tag_name from release API response"
    echo "$tag"
}

# --- main --------------------------------------------------------------------

main() {
    info "${BOLD}mus CLI installer${RESET}"
    require_cmd curl
    require_cmd uname

    local asset target_dir target tmpfile url
    asset=$(detect_asset)
    target_dir=$(find_install_dir)
    target="$target_dir/$BIN_NAME"

    if [ -z "$TAG" ]; then
        info "Looking up latest release..."
        TAG=$(get_latest_tag)
    fi
    info "Version:  $TAG"
    info "Platform: $asset"
    info "Target:   $target"

    url="https://codeberg.org/$REPO/releases/download/$TAG/$asset"

    tmpfile=$(mktemp "${TMPDIR:-/tmp}/mus-install.XXXXXX")
    trap 'rm -f "$tmpfile" "$tmpfile.sig"' EXIT

    info "Downloading $url"
    curl -fSL --progress-bar -o "$tmpfile" "$url" \
        || die "Download failed"

    # Sanity: must be a non-empty executable, not an HTML 404 page.
    [ -s "$tmpfile" ] || die "Downloaded file is empty"
    if file "$tmpfile" 2>/dev/null | grep -qi 'html\|text'; then
        die "Downloaded file looks like HTML/text, not a binary. Check $url"
    fi

    chmod +x "$tmpfile"

    # Optional integrity check: try to fetch the matching .sig and ask the
    # just-downloaded binary to self-verify. This catches HTTPS MITM and
    # release-server compromise. We treat a missing .sig as a warning (older
    # releases may predate signing); a present-but-bad .sig aborts.
    info "Fetching signature for verification..."
    if curl -fsSL -o "$tmpfile.sig" "$url.sig"; then
        if "$tmpfile" _verify "$tmpfile" "$tmpfile.sig" >/dev/null 2>&1; then
            ok "Signature verified"
        elif "$tmpfile" _verify --help >/dev/null 2>&1; then
            die "Signature verification FAILED. Refusing to install.
   Downloaded binary may have been tampered with."
        else
            warn "Downloaded mus has no _verify subcommand; skipping check (older release)"
        fi
    else
        warn "No .sig published for $TAG; skipping signature check"
    fi

    mv "$tmpfile" "$target"
    rm -f "$tmpfile.sig"
    trap - EXIT

    ok "Installed to $target"

    # Verify it runs
    if "$target" version >/dev/null 2>&1; then
        ok "$("$target" version 2>&1 | head -n1)"
    else
        warn "Binary installed but '$BIN_NAME version' did not exit cleanly"
    fi

    # PATH advice
    case ":$PATH:" in
        *":$target_dir:"*)
            ok "$target_dir is in your PATH"
            ;;
        *)
            warn "$target_dir is NOT in your PATH"
            printf "  Add to your shell rc (~/.bashrc, ~/.zshrc, etc.):\n"
            printf "    %sexport PATH=\"%s:\$PATH\"%s\n" "$BOLD" "$target_dir" "$RESET"
            ;;
    esac

    cat <<EOF

Next steps:
  1. Store your ELN credentials:
       mus secret set eln_url    https://your-eln-server/api/v1
       mus secret set eln_apikey <your-key>

  2. Configure iRODS home + web base (writes to .mus in the current dir):
       mus config set irods_home /zone/home/your_lab
       mus config set irods_web  https://mango.kuleuven.be/data-object/view

  3. Link a working directory to an ELN experiment:
       mus eln tag-folder -x <experimentID>

  4. Try it:
       mus tag data.csv -m "raw data"
       mus check

  Docs: https://codeberg.org/$REPO
EOF
}

main "$@"
