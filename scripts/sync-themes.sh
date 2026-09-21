#!/usr/bin/env bash
# Copies the pinned ftl-themes bundles into static/ so they are embedded in
# the binary. go:embed cannot reach outside static/, hence the copy rather
# than embedding the submodule directly.
#
# Run after changing the submodule pin.
# TestEmbeddedThemesMatchSubmodule (internal/handlers/settings_theme_test.go)
# fails if the copies drift from the pin.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if [ ! -f third_party/ftl-themes/dist/themes.json ]; then
  echo "third_party/ftl-themes is empty - run: git submodule update --init" >&2
  exit 1
fi

mkdir -p static/themes static/assets/fonts
rm -f static/themes/*.css static/themes/themes.json static/assets/fonts/*.woff2
cp third_party/ftl-themes/dist/*.css third_party/ftl-themes/dist/themes.json static/themes/
cp third_party/ftl-themes/assets/fonts/*.woff2 static/assets/fonts/
echo "synced $(ls static/themes/*.css | wc -l) theme bundles + $(ls static/assets/fonts/*.woff2 | wc -l) fonts"
