#!/usr/bin/env bash
# Refreshes static/themes/ and static/assets/fonts/ from
# github.com/DrEVILish/ftl-themes's compiled dist/ + assets/ - run this
# before `go build` to pick up new/updated themes (see CLAUDE.md's Building
# section). This app never edits those files directly: any change to a
# theme's look goes through a pull request against that repo instead (its
# own contract - CONTRACT.md - requires the theme owner's approval), and
# the next run of this script is how the change actually reaches this app.
set -euo pipefail

repo="DrEVILish/ftl-themes"
theme_dir="$(dirname "$0")/../static/themes"
icon_dir="$theme_dir/icons"
font_dir="$(dirname "$0")/../static/assets/fonts"
mkdir -p "$theme_dir" "$icon_dir" "$font_dir"

# themes.json is the authoritative theme list - upstream has both added and
# *removed* themes between runs of this script (a real reorg, not just
# additions), so slugs are derived from it rather than from globbing
# dist/*.css. A glob also used to sweep up non-theme bundle variants
# (*.layered.css, *-tokens.css) that don't correspond to a themes.json slug
# at all.
gh api "repos/$repo/contents/dist/themes.json" --jq '.content' | base64 -d > "$theme_dir/themes.json"
slugs=$(jq -r '.[].slug' "$theme_dir/themes.json")

# Clean stale files before writing fresh ones, or a theme upstream removed
# (or a bundle-variant file an older version of this script fetched) lingers
# on disk forever, still embedded into the binary via go:embed, even though
# it's unreachable through themes.json and this app's theme picker.
find "$theme_dir" -maxdepth 1 -name '*.css' -delete
find "$icon_dir" -maxdepth 1 -name '*.svg' -delete

for slug in $slugs; do
  gh api "repos/$repo/contents/dist/$slug.css" --jq '.content' | base64 -d > "$theme_dir/$slug.css"
done

# Per-theme icon sprites (CONTRACT.md "Per-theme icon overrides and the
# generic fallback") - a full generic-icon sprite per theme with any icon
# id that theme overrides swapped in, plus dist/icons/generic.svg as the
# no-theme-applied fallback. Optional for this app to ever reference, but
# part of the same dist/ output everything else here comes from.
icons=$(gh api "repos/$repo/contents/dist/icons" --jq '.[].name')
for icon in $icons; do
  gh api "repos/$repo/contents/dist/icons/$icon" --jq '.content' | base64 -d > "$icon_dir/$icon"
done

fonts=$(gh api "repos/$repo/contents/assets/fonts" --jq '.[].name')
find "$font_dir" -maxdepth 1 -type f -delete
for font in $fonts; do
  gh api "repos/$repo/contents/assets/fonts/$font" --jq '.content' | base64 -d > "$font_dir/$font"
done

echo "Fetched $(echo "$slugs" | wc -l | tr -d ' ') theme(s), $(echo "$icons" | wc -l | tr -d ' ') icon sprite(s) into $theme_dir"
