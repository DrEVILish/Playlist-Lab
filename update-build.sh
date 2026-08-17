#!/usr/bin/env bash
#
# update-build.sh — Playlist Lab dev helper
#
# Pulls latest main and builds every component in the correct order:
#   packages/shared -> apps/server -> apps/web
#
# Mirrors the actual CI pipeline (.github/workflows/ci.yml), which installs
# every workspace with --legacy-peer-deps. The README's plain `npm install`
# steps do NOT match CI and will fail with ERESOLVE peer-dependency errors.
#
# Usage:
#   ./update-build.sh                # clone-or-pull + build, using defaults below
#   INSTALL_DIR=/opt/foo ./update-build.sh
#   ./update-build.sh --skip-pull    # build only, don't touch git
#   ./update-build.sh --clean        # run each package's `clean` script first
#
# NOTE on paths: the .deb/.rpm installer unpacks a flattened production
# build (dist/ + node_modules/, no .git, no source) to
# /opt/playlist-lab-server. That is NOT a git checkout and this script will
# refuse to touch it. Point INSTALL_DIR at a location that either doesn't
# exist yet or is already a git clone of this repo.

set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/DrEVILish/Playlist-Lab.git}"
INSTALL_DIR="${INSTALL_DIR:-/opt/playlist-lab-server}"
SKIP_PULL=false
CLEAN=false

for arg in "$@"; do
  case "$arg" in
    --skip-pull) SKIP_PULL=true ;;
    --clean) CLEAN=true ;;
    *) echo "Unknown option: $arg" >&2; exit 1 ;;
  esac
done

log()  { printf '\n=== %s ===\n' "$1"; }
run()  { printf '\n$ %s  (in %s)\n' "$*" "$(pwd)"; "$@"; }

# --- resolve INSTALL_DIR: clone if missing, refuse if it's not a git repo ---
if [ ! -e "$INSTALL_DIR" ]; then
  log "Cloning into $INSTALL_DIR"
  mkdir -p "$(dirname "$INSTALL_DIR")"
  run git clone "$REPO_URL" "$INSTALL_DIR"
elif [ -d "$INSTALL_DIR/.git" ]; then
  : # existing git checkout, fine
else
  echo "ERROR: $INSTALL_DIR exists but is not a git repository." >&2
  echo "This is likely a flattened release install (from the .deb/.rpm" >&2
  echo "installer), which has no .git and can't be 'pulled'. Point" >&2
  echo "INSTALL_DIR at a different path for your dev checkout, e.g.:" >&2
  echo "  INSTALL_DIR=/opt/playlist-lab-dev $0" >&2
  exit 1
fi

cd "$INSTALL_DIR"

if [ "$SKIP_PULL" = false ]; then
  log "git pull"
  run git pull
else
  echo "(skipping git pull: --skip-pull)"
fi

for component in packages/shared apps/server apps/web; do
  [ -d "$component" ] || { echo "ERROR: expected directory not found: $component" >&2; exit 1; }

  if [ "$CLEAN" = true ] && node -e "process.exit(require('$INSTALL_DIR/$component/package.json').scripts?.clean ? 0 : 1)" 2>/dev/null; then
    log "$component: clean"
    (cd "$component" && run npm run clean)
  fi

  log "$component: install"
  (cd "$component" && run npm install --legacy-peer-deps)

  log "$component: build"
  (cd "$component" && run npm run build)
done

echo
echo "✅ All components updated and built successfully in $INSTALL_DIR"
