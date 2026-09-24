# Font notices (`assets/fonts/`)

Companion to `docs/icon-license-research.md` and `assets/logos/README.md` —
same idea, different asset class: this records what was actually checked
about each vendored font's license, rather than assuming "it's on Google
Fonts, so it must be SIL OFL" uniformly across every family. Each entry
below was verified against that family's own Google Fonts listing (and,
for the ones mirrored in the `google/fonts` GitHub repo, its `ofl/`
directory placement — Google Fonts sorts fonts into `ofl/`, `apache/` or
`ufl/` by license, so the directory itself is a second confirmation
alongside the specimen page's license line).

All six files vendored here turned out to be **SIL Open Font License,
version 1.1** — but that was verified per family below, not assumed.
OFL 1.1 permits embedding, bundling and redistributing the font (including
as a webfont, including verbatim, including at whatever scale this
library is redistributed to consuming apps) without requiring per-use or
per-page attribution — the one hard restriction is that the font may not
be **sold by itself**, on its own, separate from software; that doesn't
apply to embedding it in a theme's CSS bundle. Reserved font names must
not be reused by a modified version, which is moot here since these
files are vendored unmodified.

## Antonio

- **Family:** Antonio (weights vendored: Regular 400, Bold 700 —
  `Antonio-Regular.woff2`, `Antonio-Bold.woff2`)
- **Designer:** Vernon Adams
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Antonio
- **Used by:** `lcars` (display face for the sweep bar and numeric
  readouts)

## Audiowide

- **Family:** Audiowide (weight vendored: Regular 400 —
  `Audiowide-Regular.woff2`)
- **Designer:** Brian J. Bonislawsky, for Astigmatic (AOETI)
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Audiowide
- **Used by:** `vaporwave` (headings-only full-width display face)

## Baloo 2

- **Family:** Baloo 2 (weight vendored: Bold 700 — `Baloo2-Bold.woff2`;
  Google serves the same static instance for 700 and 800, so only one
  file is vendored and used at 700 for both headings and buttons)
- **Designer:** Ek Type
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Baloo+2
- **Used by:** `barbie` (rounded-display voice for headings and buttons)

## Orbitron

- **Family:** Orbitron (weight vendored: Bold 700/800 range —
  `Orbitron-Bold.woff2`)
- **Designer:** Matt McInerney (The League of Moveable Type)
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Orbitron
- **Used by:** `alienware` (AlienFX display/heading face)

## Pacifico

- **Family:** Pacifico (weight vendored: Regular 400 —
  `Pacifico-Regular.woff2`)
- **Designer:** Vernon Adams (commissioned by Google)
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Pacifico
- **Used by:** `barbie` (scoped narrowly to the brand mark and `h1` only,
  as the cursive script signature — never body/button text)

## Bottom line

Six files, five families, all SIL OFL 1.1, all verified per family rather
than assumed from "it's on Google Fonts." Attribution is optional under
OFL (unlike, say, CC BY), but is included here anyway for the same
diligence reasons `docs/icon-license-research.md` and
`assets/logos/README.md` document their sources: so "we checked" isn't
just asserted from memory, and so a future addition to this directory has
a template to follow instead of reinventing the check.
