#!/usr/bin/env bash
# scripts/release.sh — bump version, tag, push, watch CI.
#
# Usage:
#   ./scripts/release.sh 0.6.2          # explicit version
#   ./scripts/release.sh                # auto-increment patch
#   ./scripts/release.sh --watch        # also tail the CI run after push
#
# Run this AFTER the PR is merged to main.

set -euo pipefail

# ---------- helpers -----------------------------------------------------------

die()  { echo "error: $*" >&2; exit 1; }
info() { echo "▶ $*"; }

# ---------- args --------------------------------------------------------------

WATCH=0
NEW_VERSION=""

for arg in "$@"; do
  case "$arg" in
    --watch) WATCH=1 ;;
    --*)     die "unknown flag: $arg" ;;
    *)       NEW_VERSION="$arg" ;;
  esac
done

# ---------- sanity checks -----------------------------------------------------

command -v sd  >/dev/null || die "'sd' not found — brew install sd"
command -v gh  >/dev/null || die "'gh' not found — brew install gh"
command -v git >/dev/null || die "'git' not found"

# Must be run from repo root.
[[ -f pyproject.toml ]] || die "run this script from the repo root"

# ---------- resolve versions --------------------------------------------------

CURRENT=$(grep '^version = ' pyproject.toml | sed 's/version = "\(.*\)"/\1/')
[[ -n "$CURRENT" ]] || die "could not parse current version from pyproject.toml"

if [[ -z "$NEW_VERSION" ]]; then
  # Auto-increment patch: 0.6.1 → 0.6.2
  IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT"
  PATCH=$((PATCH + 1))
  NEW_VERSION="${MAJOR}.${MINOR}.${PATCH}"
fi

info "Current version : $CURRENT"
info "New version     : $NEW_VERSION"
echo

read -rp "Proceed? [y/N] " CONFIRM
[[ "$CONFIRM" =~ ^[Yy]$ ]] || { echo "Aborted."; exit 0; }

# ---------- sync main ---------------------------------------------------------

info "Syncing main..."
git checkout main
git pull --ff-only origin main

# Verify the branch is clean.
[[ -z "$(git status --porcelain)" ]] || die "working tree is not clean — commit or stash first"

# ---------- bump version ------------------------------------------------------

info "Bumping $CURRENT → $NEW_VERSION in pyproject.toml..."
sd "^version = \"${CURRENT}\"$" "version = \"${NEW_VERSION}\"" pyproject.toml

# Verify the replacement landed.
grep -q "^version = \"${NEW_VERSION}\"$" pyproject.toml \
  || die "sd replacement failed — check pyproject.toml manually"

git add pyproject.toml
git commit -m "chore: bump version to ${NEW_VERSION}"
git push origin main

# ---------- tag & trigger CI --------------------------------------------------

TAG="v${NEW_VERSION}"
info "Tagging $TAG and pushing (triggers release.yml)..."
git tag "$TAG"
git push origin "$TAG"

echo
echo "✓ Tag $TAG pushed. The release.yml workflow is now building the wheel."
echo "  Track progress:"
echo "    gh run watch"
echo "    gh release view $TAG"
echo

# ---------- optional watch ----------------------------------------------------

if [[ "$WATCH" -eq 1 ]]; then
  info "Watching CI run..."
  # Wait a couple seconds for GitHub to register the workflow run.
  sleep 5
  gh run watch
  echo
  gh release view "$TAG"
fi
