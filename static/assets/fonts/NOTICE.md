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

All files vendored here except Bedstead (CC0) and Px437 IBM VGA 9x16 (CC BY-SA 4.0), both below, turned out to be **SIL Open Font License,
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

## Bedstead

- **Family:** Bedstead (Regular, subset to Latin, arrows, box drawing and
  the teletext sextant mosaics U+1FB00–1FBAF — `Bedstead.woff2`)
- **Designer:** Ben Harris — an outline recreation of the Mullard SAA5050
  teletext character generator
- **License:** CC0 1.0 (public domain dedication), per the author's page
  and the font's `SPDX-License-Identifier: CC0-1.0`. Not OFL — more
  permissive: no conditions at all.
- **Source:** https://bjh21.me.uk/bedstead/ (`bedstead.otf` 3.261,
  converted to WOFF2 with fontTools)
- **Used by:** `teletext` (every glyph on the page)

## Baloo 2

- **Family:** Baloo 2 (weight vendored: Bold 700 — `Baloo2-Bold.woff2`;
  Google serves the same static instance for 700 and 800, so only one
  file is vendored and used at 700 for both headings and buttons)
- **Designer:** Ek Type
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Baloo+2
- **Used by:** `barbie` (rounded-display voice for headings and buttons)

## IM Fell English SC

- **Family:** IM Fell English SC (weight vendored: Regular 400 —
  `IMFellEnglishSC-Regular.woff2`)
- **Designer:** Igino Marini (digitisation of the 17th-century Fell types)
- **License:** SIL Open Font License, version 1.1 — confirmed by its
  placement at `google/fonts` `ofl/imfellenglishsc/` (with `OFL.txt`)
  and `license: "OFL"` in that directory's `METADATA.pb`
- **Source:** https://fonts.google.com/specimen/IM+Fell+English+SC
- **Used by:** `steampunk` (display face: headings, panel nameplates,
  buttons, the app bar)

## Px437 IBM VGA 9x16

- **Family:** Px437 IBM VGA 9x16 (web cut `Web437_IBM_VGA_9x16`, converted
  from the pack's WOFF to WOFF2 with fontTools, glyphs unmodified —
  `Web437_IBM_VGA_9x16.woff2`)
- **Designer:** VileR (int10h.org), reproducing the IBM VGA ROM font
- **License:** Creative Commons Attribution-ShareAlike 4.0 International
  (CC BY-SA 4.0), per the pack's own `LICENSE.TXT` —
  https://creativecommons.org/licenses/by-sa/4.0/. Attribution: "Ultimate
  Oldschool PC Font Pack" by VileR, https://int10h.org/oldschool-pc-fonts/.
  ShareAlike applies to the font file itself (any modified version must
  carry the same license), not to the CSS or apps that load it.
- **Source:** https://int10h.org/oldschool-pc-fonts/ (pack v2.2, web
  edition)
- **Used by:** `msdos` (every glyph on the page)

## Michroma

- **Family:** Michroma (weight vendored: Regular 400 —
  `Michroma-Regular.woff2`)
- **Designer:** Vernon Adams
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Michroma
- **Used by:** `prometheus` (wide squared label and heading face)

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

## Share Tech and Share Tech Mono

- **Family:** Share Tech (Regular 400 — `ShareTech-Regular.woff2`) and
  Share Tech Mono (Regular 400 — `ShareTechMono-Regular.woff2`)
- **Designer:** Carrois Type Design
- **License:** SIL Open Font License, version 1.1
- **Source:** https://fonts.google.com/specimen/Share+Tech and
  https://fonts.google.com/specimen/Share+Tech+Mono
- **Used by:** `prometheus` (body text and data readouts), `cyberpunk-2077` (Share Tech Mono: code and logs)

## DSEG7 Classic

- **Family:** DSEG7 Classic (Bold Italic — `DSEG7Classic-BoldItalic.woff2`)
- **Designer:** keshikan
- **License:** SIL Open Font License, version 1.1 (full text in `DSEG-LICENSE.txt`)
- **Source:** https://github.com/keshikan/DSEG
- **Used by:** `cassette-futurism` (seven-segment readout digits)

## Titillium Web

- **Family:** Titillium Web (weights vendored: Regular 400, Bold 700, Bold
  Italic 700 — `TitilliumWeb-Regular.woff2`, `TitilliumWeb-Bold.woff2`,
  `TitilliumWeb-BoldItalic.woff2`; Google Fonts' Latin subset, unmodified)
- **Designer:** Accademia di Belle Arti di Urbino
- **License:** SIL Open Font License, version 1.1 (verified in
  `google/fonts` `ofl/titilliumweb/OFL.txt` and its `METADATA.pb`,
  `license: "OFL"`)
- **Source:** https://fonts.google.com/specimen/Titillium+Web
- **Used by:** `motorsport-telemetry` (UI, timing figures and readouts; its
  digits are tabular by default, all 560 units wide)

## Rajdhani

- **Family:** Rajdhani (weights vendored: Medium 500, Bold 700 —
  `Rajdhani-Medium.woff2`, `Rajdhani-Bold.woff2`; Latin subset as served
  by Google Fonts)
- **Designer:** Indian Type Foundry
- **License:** SIL Open Font License, version 1.1 (verified in the
  `google/fonts` repo, `ofl/rajdhani/OFL.txt` and `METADATA.pb`
  `license: "OFL"`)
- **Source:** https://fonts.google.com/specimen/Rajdhani
- **Used by:** `cyberpunk-2077` (all UI text; stand-in for the game's
  commercial condensed face)

## Bottom line

Eighteen files, fourteen families: twelve SIL OFL 1.1, Bedstead CC0 and Px437
CC BY-SA 4.0, all verified per family rather than assumed from "it's on
Google Fonts." Attribution is optional under
OFL (unlike, say, CC BY), but is included here anyway for the same
diligence reasons `docs/icon-license-research.md` and
`assets/logos/README.md` document their sources: so "we checked" isn't
just asserted from memory, and so a future addition to this directory has
a template to follow instead of reinventing the check.
