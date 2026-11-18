#!/usr/bin/env bash
# Vendor one reviewed ftl-themes revision into the assets embedded by Go.
set -euo pipefail

revision=35aaf838db666a7745595fbdacec5721683bd2cb
root="$(cd "$(dirname "$0")/.." && pwd)"
stage="$(mktemp -d "$root/static/.ftl-themes.XXXXXX")"
trap 'rm -rf -- "$stage"' EXIT
source_dir="$stage/upstream"
mkdir "$source_dir"

# An existing checkout can supply the pinned Git tree without using its
# working files or requiring network access.
if [ "$#" -eq 1 ]; then
  git -C "$1" archive "$revision" dist assets/fonts | tar -x -C "$source_dir"
elif [ "$#" -eq 0 ]; then
  git -C "$source_dir" init --quiet
  git -C "$source_dir" remote add origin https://github.com/DrEVILish/ftl-themes.git
  git -C "$source_dir" config remote.origin.promisor true
  git -C "$source_dir" config remote.origin.partialclonefilter blob:none
  git -C "$source_dir" fetch --quiet --depth=1 --filter=blob:none origin "$revision"
  git -C "$source_dir" sparse-checkout set --no-cone '/dist/*.css' '/dist/themes.json' '/dist/icons/' '/assets/fonts/'
  git -C "$source_dir" -c advice.detachedHead=false checkout --quiet --detach FETCH_HEAD
else
  echo "Usage: $0 [path-to-ftl-themes-git-checkout]" >&2
  exit 2
fi

python3 - "$source_dir" "$stage" "$revision" <<'PY'
import json
import re
import shutil
import sys
from pathlib import Path

source, stage = map(Path, sys.argv[1:3])
dist = source / "dist"
catalogue = json.loads((dist / "themes.json").read_text())
slugs = [theme["slug"] for theme in catalogue]
versions = {theme["version"] for theme in catalogue}
if (
    "blue-future" not in slugs
    or len(slugs) != len(set(slugs))
    or len(versions) != 1
    or any(theme["contract"] != 4 or theme["dataTheme"] != theme["slug"] for theme in catalogue)
):
    raise SystemExit("Inconsistent or incompatible ftl-themes manifest")

themes = stage / "themes"
themes.mkdir()
for slug in slugs:
    if not re.fullmatch(r"[a-z0-9-]+", slug):
        raise SystemExit(f"Invalid theme slug: {slug}")
    bundle = dist / f"{slug}.css"
    css = bundle.read_text()
    if not css or not re.search(r"\[data-theme=[\"']?" + re.escape(slug) + r"[\"']?\]", css):
        raise SystemExit(f"Unusable theme bundle: {bundle.name}")
    for asset in re.findall(r"url\([\"']?\.\./(assets/[^)'\"]+)", css):
        if not (source / asset).is_file():
            raise SystemExit(f"{bundle.name} references missing {asset}")
    shutil.copyfile(bundle, themes / bundle.name)

shutil.copyfile(dist / "themes.json", themes / "themes.json")
shutil.copytree(dist / "icons", themes / "icons")
shutil.copytree(source / "assets/fonts", stage / "fonts")
(themes / "upstream-revision.txt").write_text(sys.argv[3] + "\n")
print(f"Validated {len(slugs)} themes from ftl-themes {next(iter(versions))}")
PY

mkdir -p "$root/static/assets"
replace_directory() {
  local incoming="$1" destination="$2" previous="$3"
  if [ -d "$destination" ]; then mv "$destination" "$previous"; fi
  if ! mv "$incoming" "$destination"; then
    if [ -d "$previous" ]; then mv "$previous" "$destination"; fi
    return 1
  fi
}
replace_directory "$stage/fonts" "$root/static/assets/fonts" "$stage/previous-fonts"
if ! replace_directory "$stage/themes" "$root/static/themes" "$stage/previous-themes"; then
  mv "$root/static/assets/fonts" "$stage/failed-fonts"
  if [ -d "$stage/previous-fonts" ]; then mv "$stage/previous-fonts" "$root/static/assets/fonts"; fi
  exit 1
fi
