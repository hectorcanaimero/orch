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
  # Neither /releases/latest nor the newest v* tag is safe: the archived
  # Python line published v0.5.1…v0.10.1 (no -py suffix, wheels only) and
  # v0.11.0-py, so picking by tag name 404'd on v0.10.1 (#234). Pick the
  # newest release that actually carries this platform's Go archive — the
  # API lists releases newest first.
  TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" \
    | grep -o "releases/download/v[0-9][^/\"]*/orch_v[^/\"]*_${goos}_${goarch}\.tar\.gz" \
    | head -n 1 \
    | sed -E 's#releases/download/([^/]+)/.*#\1#')
  [ -n "$TAG" ] || die "no release has an orch binary for ${goos}/${goarch} yet — see https://github.com/${REPO}/releases, or build from source (docs/RELEASING.md)"
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
  || die "download failed: $URL — ${TAG} has no orch binary for ${goos}/${goarch} (releases before v0.12.0 are the Python line). Omit the version to get the newest Go release."

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
