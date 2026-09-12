#!/usr/bin/env bash
# scripts/install.sh — download and install the latest orch Go binary
# release. See docs/RELEASING.md for the tag scheme and what
# .github/workflows/release-go.yml builds; this script only consumes an
# already-published release, it doesn't build anything.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/hectorcanaimero/orch/main/scripts/install.sh | sh
#   ./scripts/install.sh                 # latest release
#   ./scripts/install.sh v0.12.0         # a specific tag
#
# Env overrides:
#   INSTALL_DIR   Where the binary lands. Default: $HOME/.local/bin.
#   ORCH_VERSION  Same as passing a version argument.
set -euo pipefail

REPO="hectorcanaimero/orch"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${ORCH_VERSION:-${1:-latest}}"

die()  { echo "error: $*" >&2; exit 1; }
info() { echo "==> $*"; }

# ---- resolve OS/arch, matching .goreleaser.yaml's build matrix exactly -----

os="$(uname -s)"
case "$os" in
  Linux)  goos=linux ;;
  Darwin) goos=darwin ;;
  *) die "unsupported OS: $os (orch ships linux and darwin only)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  goarch=amd64 ;;
  arm64|aarch64) goarch=arm64 ;;
  *) die "unsupported architecture: $arch (orch ships amd64 and arm64 only)" ;;
esac

# ---- resolve the tag --------------------------------------------------------

if [ "$VERSION" = "latest" ]; then
  # The GitHub "latest" release redirect follows Python's own v*-py tags
  # too if one is ever newer — ask the API for the newest v* (no -py)
  # release specifically instead of trusting /releases/latest.
  TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" \
    | grep -o '"tag_name": *"v[0-9][^"]*"' \
    | grep -v -- '-py"' \
    | head -n 1 \
    | sed -E 's/.*"(v[0-9][^"]*)".*/\1/')
  [ -n "$TAG" ] || die "could not find a Go release (a tag matching v[0-9]* without -py) — see https://github.com/${REPO}/releases"
else
  TAG="$VERSION"
fi

ASSET="orch_${TAG}_${goos}_${goarch}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/checksums.txt"

info "Installing orch ${TAG} (${goos}/${goarch}) to ${INSTALL_DIR}"

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

info "Downloading ${ASSET}..."
curl -fsSL -o "${TMP_DIR}/${ASSET}" "$URL" \
  || die "download failed: $URL (does that release/asset exist?)"

if curl -fsSL -o "${TMP_DIR}/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null; then
  info "Verifying checksum..."
  (cd "$TMP_DIR" && grep " ${ASSET}\$" checksums.txt | sha256sum -c -) \
    || die "checksum verification failed for ${ASSET}"
else
  echo "warning: could not fetch checksums.txt — skipping verification" >&2
fi

tar -xzf "${TMP_DIR}/${ASSET}" -C "$TMP_DIR" orch

mkdir -p "$INSTALL_DIR"
install -m 0755 "${TMP_DIR}/orch" "${INSTALL_DIR}/orch"

info "Installed: $("${INSTALL_DIR}/orch" --version)"

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *) echo "warning: ${INSTALL_DIR} is not on your PATH — add it, e.g.:" >&2
     echo "    export PATH=\"${INSTALL_DIR}:\$PATH\"" >&2 ;;
esac
