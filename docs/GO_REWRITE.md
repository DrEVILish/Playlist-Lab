# Playlist Lab: Go + HTMX Rewrite — Design Record

This document tracks the design and status of the in-progress rewrite of Playlist Lab from
Node/Express/TypeScript + React to Go + HTMX, being built on branch `3.0`. It exists alongside
`docs/SPEC.md` (which describes the *current, live* Node app) so the rewrite's decisions survive
across sessions rather than only living in an ephemeral planning doc.

The live production service (`playlist-lab-server.service`, port 3001) continues to run the Node
app unchanged throughout this effort — see "Rollout" below.

## Scope decisions (already made, not open for re-litigation)

- **Big-bang rollout.** The full Go+HTMX replacement is built on branch `3.0` and cut over once at
  the end. No strangler-fig, no dual-stack deployment during development.
- **Full feature parity from day one.** Every current route/service/adapter must work before
  cutover.
- **`apps/desktop` (Electron) is out of scope.** Untouched by this effort.
- **Browser scraping**: Puppeteer+stealth is ported to `chromedp` in Go, accepting some reliability
  risk on sites that relied on stealth-plugin evasion (no chromedp/Chromium binary exists in the
  dev sandbox this was built in, so these paths are logic-verified against source only, not
  live-tested, until run somewhere with a real browser).
- **UI design source of truth is `apps/web/src` (the current React app), not a hand-rolled
  layout.** Early phases (0-6) shipped functional-but-bare `html/template` pages — plain tables, no
  nav chrome, a stub CSS file — to validate the backend port first. This was corrected: the HTMX
  templates must be a like-for-like structural and visual port of the current React pages/components
  (`PlaylistsPage`, `GenerateMixesPage`, `ImportPage`, `CrossImportPage`, `SettingsPage`,
  `AdminPage`, `BackupRestorePage`, `LoginPage`, plus shared chrome — `Header`, `Layout`, `Footer`,
  `MobileNav`, `NotificationCenter`), including porting `apps/web/src`'s CSS (~8200 lines across all
  stylesheets) into `static/css/` rather than reducing styling to a minimal stub. "Feature parity"
  explicitly includes **UI parity**: every page keeps its current layout, styling, and interactive
  affordances (modals, dialogs, mobile nav), re-expressed as server-rendered HTML + HTMX attributes
  instead of React state/JSX — not simplified in the name of "it still technically works."

## Architecture

See the Go project structure, library choices, and per-layer migration notes in the original planning
doc this was derived from (kept for detailed rationale): the plan produced during initial scoping
covers `chi` for routing, `modernc.org/sqlite` (pure Go, no cgo), stdlib `html/template` +
`go:embed`, `robfig/cron/v3` for scheduling, and the HTMX/SSE translation approach (real SSE via
`htmx-ext-sse` for notifications/mix-progress/matching-progress, `hx-trigger="every Ns"` polling for
everything that was already dumb polling in the React app). Nothing in that architecture has changed
except the UI-fidelity decision above.

One implementation deviation worth recording: rather than building the planned `internal/sse/hub.go`
generic SSE broadcaster, every phase so far (mixes, cross-import, missing-tracks, import) has reused
the single existing mechanism — `internal/services/notifications` (per-user SSE feed) +
`internal/services/actionqueue` (bounded worker pool) — for both progress-reporting and long-running
work, rather than each feature inventing its own EventEmitter-equivalent. Cross-import's
review/override step (matches the user can inspect and adjust before executing) is the one place
that needed its own small addressable store (`internal/services/crossimport.Store`) since that state
doesn't fit in a one-line notification.

## Status by phase (as of the last update to this document)

- **Phase 0-1** (scaffolding, DB, auth, Plex core + playlist CRUD): done.
- **Phase 2** (matching engine): done, including Hepburn kana romanization; kanji/kuromoji tokenizing
  deliberately stubbed (documented gap, matches the original code's own "weaker signal" framing).
- **Phase 3** (adapters): all platforms have Go adapters — Plex, Spotify, Apple Music, Deezer, Tidal,
  Qobuz, Amazon Music, ListenBrainz, YouTube (three variants: OAuth/Data-API-v3, cookie-based Music,
  and InnerTube via the new standalone `github.com/drevilish/innertube-go` module), plus chart-only
  sources ARIA, Billboard, Last.fm.
- **Phase 4** (scheduling/jobs): `robfig/cron` scheduler, notification store + SSE handler, bounded
  action queue (verified with `go test -race`). `cache-cleanup`, `deemix-arl-check`, and
  `daily-scraper` jobs are all ported and wired; `schedule-checker` remains deferred (see below).
- **Phase 5** (browser scraping): `internal/services/browser` (chromedp) with partial stealth
  mitigation (UA override, `--disable-blink-features=AutomationControlled`, `navigator.webdriver`
  override) backs Apple Music, Tidal, Amazon Music, and Qobuz as full Source+Target adapters, plus
  ARIA chart scraping.
- **Phase 6** (AI/mixes/cross-import/missing-tracks/import): Gemini/Grok REST clients, Last.fm chart
  client, all 15 mix-generation algorithms, mix templates CRUD, cross-import (Plex-source +
  YouTube-target reachable so far — matches the current app's own hard filter on that route),
  deemix + Lidarr missing-track handling, and general playlist import (URL-based across
  Deezer/ListenBrainz/YouTube/ARIA/Billboard/Last.fm, plus file upload for M3U/M3U8/PLS/XSPF/CSV/TXT).
- **UI parity pass** (this document's trigger): in progress. Prior phases' templates need to be
  reworked to match `apps/web/src`'s actual layout/CSS instead of the placeholder markup shipped
  during Phase 0-6. Admin's Schedules/Deemix/Lidarr/Logs tabs (previously hidden entirely) now show
  real content or an honest "not yet ported" note per tab instead of being hidden: Deemix (Deezer
  ARL) and Lidarr (URL/API key) are real, `admin_config`-backed settings forms (same table/pattern as
  `internal/db/admin_config.go`, wired through `deemixService.SetARL`/`lidarrService.SetConfig`);
  Schedules (background-job status, all-users schedule list) and Logs (log viewer/level) remain
  stubbed - each is its own separate service integration, not a small extension of the config-form
  pattern the other two used. The Playlists page (home.html) had no mobile view at all until this
  session - only the desktop `<table>` was ever ported, even though PlaylistsPage.css's own
  `.playlists-cards`/`.playlist-card*` rules (the mobile card-list styling) had already been ported
  and were just sitting unused. Fixed: home.html now renders both the table and a card-list
  (identical data, `{{define "playlistRowActions"}}` shared between them so the two views can't
  drift), switched via `@media` at the same 768px breakpoint as the rest of the app; each card's
  expand/collapse is a checkbox-hack (`.playlist-card-toggle` + `:checked ~` sibling rules), the
  same no-JS pattern the admin tabs and mobile nav drawer already used, replacing what was
  `isCardExpanded` React state in the original. Verified visually (chromium installed into this
  sandbox specifically to check) at both 1400px and a 390px/iPhone-sized viewport, including the
  expand interaction and the mobile nav drawer. Separately, the sortable table headers (Source/
  Tracks/Duration/Missing Tracks/Date Added) were rendering in the browser's default link blue with
  an underline - the original `SortableHeader` component was a plain `<th onClick>`, never a real
  `<a>`, so nothing in `PlaylistsPage.css` ever needed to reset anchor styling; this port's simpler
  GET-link approach needed that reset added (`.playlists-table th a`), now fixed.

## Known gaps (honest, not silently dropped)

- ~~`schedule-checker` job still deferred~~ - stale, this bullet was never struck through after the
  job was actually finished in the same commit that wrote this doc. `internal/services/scheduler`
  (`RunDue`/`Run`/`IsDue`, 454 lines, unit-tested) fully implements it: uniform per-source dispatch
  through the same `importsvc.ImportPlaylist`/`adapters.Registry` every other import path uses (so
  Spotify/Apple/Tidal/Amazon/Qobuz as resync sources need no special-casing, matching the original's
  own `importPlaylist(playlist.source, ...)` design), plus real replace-vs-accumulate resync
  (`executePlaylistRefresh`'s `updateMode`, `replacePlaylistTracks` vs `accumulateIntoPlaylist`) and
  mix-generation schedules (`executeMixGeneration`, all 7 mix types). The per-playlist Schedule
  create/manage UI is also done (`templates/partials/schedule_form.html`, opened from each playlist
  row in `templates/home.html`). What's genuinely still missing, all narrower than this bullet
  implied: chart-import schedules (creating a schedule from a raw chart URL before any playlist
  exists) and mix-template/legacy-multi-mix-array schedule shapes have no creation path in the Go
  UI (both cut deliberately, documented in scheduler.go's package doc); manual Run Now/Run All
  (`handlers/schedules.go`) only logs on error instead of pushing live progress into the
  notification bell the way every other background job here does. The **admin-wide** Schedules tab
  (cross-user job status/list) is a separate, already-accurate gap - see the Admin bullet above.
- ~~Spotify and YouTube Music are not selectable in the general import dropdown~~ - fixed: both now
  have real `SourceAdapter`s (`internal/adapters/spotify/source.go`,
  `internal/adapters/youtubemusic/target.go`'s `FetchTracks`) registered in `main.go` and listed in
  `import.go`'s `importSources`. This turned out to be more than the "UI-wiring gap" this bullet
  originally described - neither had a `FetchTracks` implementation at all, only wiring for the
  dropdown/dispatch (already fully data-driven from the registry, needing zero changes) once one
  existed. YouTube Music's path (unauthenticated innertube `browse`, no cookie needed for a public
  playlist) was verified end-to-end against a real playlist import in this session. Spotify's path
  (per-user OAuth token falling back to the client-credentials grant `token.go` already had) reaches
  the real API and handles its error branches correctly, but wasn't confirmed against a real
  successful fetch - the only playlist IDs tried during this port hit Spotify's own restriction on
  reading full track lists of its own editorial/algorithmic playlists via the public API, and no
  Spotify app credentials were available in this sandbox to try a normal user-created playlist.
- **No Chrome/Chromium binary** in the sandbox this was built in — every chromedp-backed adapter
  (Apple Music, Tidal, Amazon, Qobuz, ARIA) is logic-verified against the original Puppeteer source
  only, never run against a real page.
- **Newly noticed while wiring the Spotify source above**: `spotify.Target`, `apple.Target`,
  `tidal.Target`, `amazon.Target`, and `qobuz.Target` are fully implemented (`OAuthCapable`,
  `SearchCatalog`/`MatchTracks`/`CreatePlaylist`) but none of them is ever constructed or
  `RegisterTarget`-ed anywhere in `main.go` - only `youtube.NewTarget` and (as of this session)
  `youtubemusic.NewTarget` (registered as a *source*, not target) are wired. Cross-import's own
  Plex-source/YouTube-target-only restriction doesn't need them, so this may be intentional
  (targets meant for a not-yet-built "export/connect" surface outside cross-import) rather than an
  oversight, but it wasn't previously documented here - flagging honestly rather than guessing at
  intent or wiring them speculatively.
- **Cutover (Phase 7)** has not started - by design, per the "big-bang rollout" decision above, this
  is deliberately not started incrementally. Progress on its stated prerequisite, a regression pass
  covering auth/playlist-CRUD/one-adapter-round-trip/matching golden cases, so far:
  - **auth**: `internal/handlers/auth_test.go` covers `handleLogin`/`reverifyMembership`'s branching
    (first-user auto-admin, Plex-Home/friends approval, revocation, and the "swallow lookup errors
    without disabling an already-approved user" contract) against a fake plex.tv, using a new
    `auth.SetPlexAPIBaseForTest` test-only seam (`internal/auth/plex.go`) rather than hitting the
    real network.
  - **playlist-CRUD**: `internal/handlers/playlists_test.go` covers delete/bulk-delete (including
    partial-failure tolerance)/move/remove/clone against a fake Plex server.
  - **one-adapter-round-trip**: `internal/adapters/deezer/roundtrip_test.go` chains a real
    `SourceAdapter.FetchTracks` → `matching.MatchPlaylist` → Plex playlist creation →
    `missing_tracks` persistence in one test - no test anywhere previously exercised that whole
    chain together.
  - **matching golden cases**: `internal/services/matching/matchplaylist_golden_test.go` is the
    first top-level test of `MatchPlaylist` itself (every other matching test covered one internal
    piece in isolation) - verified via deliberate mutation (temporarily breaking the scoring logic
    it depends on) that it actually fails on a real regression rather than passing vacuously.
  A new production systemd unit is still needed before any cutover, and the above is a start on the
  regression pass, not the full pass itself - most handlers/adapters/services are still far short of
  solid coverage (see any `go test ./... -cover` run for the current per-package numbers).
