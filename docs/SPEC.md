# Playlist Lab — Specification Brief

Playlist Lab is a self-hosted web app that manages Plex playlists: it imports playlists/charts from
streaming services into a Plex library (matching each track to the user's Plex catalog), generates
algorithmic "mixes" from the library itself, keeps playlists in sync on a schedule, and can push Plex
playlists back out to other services. It is a monorepo with three packages:

- `apps/server` — Node/Express/TypeScript API + background job runner + SQLite (better-sqlite3) storage.
- `apps/web` — React 18 + TypeScript SPA (Vite, react-router).
- `packages/shared` — TypeScript types and a typed `ApiClient` used by the web app for every server call.

In production, the server (`apps/server/dist/index.js`) also serves the built web app as static files, so
one process (`playlist-lab-server.service`, port 3001) is the whole deployment.

## Authentication & authorization

Login is Plex's PIN-based OAuth flow (`POST /api/auth/start` creates a PIN, the client polls
`POST /api/auth/poll`, `POST /api/auth/token` finalizes it). A user's Plex token is stored server-side;
sessions are cookie-based (`express-session`, SQLite-backed store). The **first user ever to log in is
auto-promoted to admin**; every other user is checked against the admin's Plex Home membership on each
login, and Plex Home members no longer on the admin's Home are disabled automatically. Route-level
`requireAuth` middleware gates almost everything under `/api/*`; unauthenticated requests get a uniform
`401 { error: { code: 'AUTH_REQUIRED' } }`.

## Data model (SQLite, `apps/server/src/database/schema.sql`)

Key tables: `users` (Plex identity + token + AI/Spotify credentials), `user_servers` (which Plex
server/library a user targets), `user_settings` (matching + mix config JSON blobs), `playlists` (Plex
playlists Playlist Lab created/tracks), `missing_tracks` (tracks that failed to match during an
import, kept per playlist for later retry), `schedules` + `schedule_executions` (recurring
refresh/mix-generation jobs and their run history), `cached_playlists` (scraped source playlists, to
avoid re-scraping), `manual_matches` (a user's explicit track-level match override, reused on future
imports), `cross_import_jobs` (service-to-service playlist copy jobs), `oauth_connections` (tokens for
non-Spotify external services), `mix_templates` (saved mix configs), `import_queue` (async import job
state), `favorite_playlists`, `playlist_shares`, `admin_users`.

## Backend architecture

- **Adapters** (`apps/server/src/adapters/`): a source/target registry per external service (Spotify,
  Deezer, Apple Music, Tidal, Amazon, Qobuz, YouTube [3 variants], ListenBrainz, Plex itself). A
  "source" fetches track lists from a service; a "target" pushes a Plex playlist's tracks out to a
  service. This is what powers the generic Cross-Import feature.
- **Scrapers** (`services/scrapers.ts`): unauthenticated scraping fallbacks (Deezer public API, Spotify
  embed-page scraping, chart scraping for Billboard/ARIA/Last.fm/etc.) used when a full OAuth
  integration isn't configured.
- **Matching** (`services/matching.ts`): fuzzy-matches an external track (title/artist/album) against
  the user's Plex library — artist-first search, title-only fallback, compilation/various-artists
  handling, configurable `minMatchScore`/`stripParentheses`/`useFirstArtistOnly` via user settings.
  Extensively logged (`[Matching] ...`) for diagnosing bad matches.
- **Import** (`services/import.ts`): orchestrates scrape → match → create/update Plex playlist, for
  both synchronous ("preview then confirm") and queued ("fire and forget") import flows.
- **Import queue** (`services/import-queue.ts` + `index.ts` job handler): imports triggered from charts,
  favorited playlists, or "paste a URL" enqueue a job; a single handler processes them serially,
  auto-finalizes (always creates a new Plex playlist, no overwrite-existing option yet), and reports
  progress through the notification system below.
- **Job notifications** (`services/job-notifications.ts`): a small in-memory (non-persisted, per-user,
  capped at 50) feed of background-job progress — used by deemix downloads, missing-track retry
  batches, scheduled refreshes, queued imports, and AI-generated playlists. Read by the header's
  notification bell (`GET/POST /api/notifications/*`).
- **Scheduler** (`services/jobs.ts`, `services/scheduler.ts`, `services/schedule-checker-job.ts`):
  cron-based. `schedule-checker` runs every 10 minutes and executes any due `schedules` row
  (playlist refresh or mix regeneration); `daily-scraper` refreshes chart caches nightly;
  `cache-cleanup` prunes `cached_playlists` weekly.
- **Deemix** (`services/deemix.ts`, new/uncommitted): queues downloads for missing tracks via a
  companion deemix-server, tracked through the notification feed.
- **Config service** (`config/index.ts`): server-side settings (public URL for OAuth redirects, Deezer
  ARL token) persisted to `server.conf.json` next to the SQLite DB; env vars always take priority.

## API surface (all under `/api`, Express routers in `apps/server/src/routes/`)

| Area | Router | Representative endpoints |
|---|---|---|
| Auth | `auth.ts` | PIN login (`/start`,`/poll`,`/token`), `/logout`, `/me` |
| Servers | `servers.ts` | list/select Plex servers, libraries, library scan |
| Settings | `settings.ts` | matching/mix config, AI provider + keys |
| Config | `config.ts` | public URL for OAuth redirects |
| Playlists | `playlists.ts` | CRUD, track add/remove/reorder, share, cover upload, reimport |
| Export | `export.ts` | `/api/playlists/export` (file / YouTube) |
| Missing tracks | `missing.ts` | list, retry (single/batch, queued), rematch, deemix download, delete |
| Notifications | `notifications.ts` | list / dismiss / clear the job-status feed |
| Admin | `admin.ts` | user management, stats, job/log inspection, deemix ARL & settings, shutdown |
| Import | `import.ts` | per-service quick import, file import, search, preview/match/confirm, queue status (see note below) |
| AI | `ai.ts` | prompt → AI-generated playlist |
| Mixes | `mixes.ts` | ~15 mix generators (weekly, daily, sonic, mood, era, workout, genre-blend, ...) |
| Mix templates | `mix-templates.ts` | save/list/run saved mix configs |
| Schedules | `schedules.ts` | CRUD, run now/run-all, execution history |
| Cross-import | `cross-import.ts` | generic service→service playlist copy: sources/targets, search, match (SSE progress), OAuth per service |
| Plex Home | `plex-home.ts` | list/switch Home users, copy playlists between them |
| Plex sharing | `plex-sharing.ts` | share with Plex friends / server users |
| Spotify auth | `spotify-auth.ts` | Spotify OAuth, credential test/save, search, playlists |
| YouTube config | `youtube-config.ts` | YouTube API credentials |
| Charts | `charts.ts` | per-source/country chart listings |
| Search | `search.ts` | Plex library search |
| Proxy | `proxy.ts` | authenticated image/audio proxy (hides Plex tokens from the client) |
| Saved Spotify users / Favorites | `saved-spotify-users.ts`, `favorite-playlists.ts` | quick-access lists |
| Migrate | `migrate.ts` | `/api/migrate/desktop` — legacy desktop-app data import |

**Note:** `import.ts` still exposes session-based progress/status/cancel endpoints
(`GET /progress/:sessionId`, `GET /status/:sessionId`, `POST /cancel/:sessionId`) and a manual queue
inspector (`GET /queue`, `GET /queue/completed`, `DELETE /queue/:jobId`, etc.) — these were built for the
now-deleted `QueuePage.tsx` and are no longer called from the web app (queued imports now report through
the notification bell instead). They still work, just orphaned. `POST /api/import/preview` is similarly
unreachable from the current UI. See QA findings below.

## Frontend (apps/web)

Single-page app behind `ProtectedRoute` (redirects to `/login` if not authenticated). Layout: a header
(logo, Import/Generate/Shared-With-Me/Backup-Restore/Status buttons that open modals, a notification
bell, settings gear, logout) over one real page route, `/` → `PlaylistsPage`. Everything else
(`ImportPage`, `GenerateMixesPage`, `SettingsPage`, `BackupRestorePage`) is either a lazy-loaded route
(bookmarkable) or, more commonly, opened as a modal from the header without navigating away from the
playlist table. Mobile gets a dedicated `MobileNav` since it can't rely on header-lifted modal state.

- **PlaylistsPage** (`/`) — the main table: every Plex playlist Playlist Lab manages, with per-column
  filters (source, missing-tracks status, schedule status, smart-playlist flag, track-count range,
  duration range, free-text search), a "playlists needing attention" toggle, bulk select
  (backup/delete), and per-row actions: inline rename, edit tracks (`PlaylistEditor`), share
  (`ShareModal`), export (`ExportModal`, file or YouTube), reimport now, quick JSON backup, delete.
  Expandable missing-tracks panel per playlist (`MissingTracksPanel`) with per-track retry/rematch/
  deemix-download/delete and a "retry all" batch action.
- **ImportPage** — the largest page (~3400 lines): per-service import forms (Spotify, Deezer, Apple,
  Tidal, YouTube, Amazon, Qobuz, ListenBrainz, ARIA/Billboard charts, Last.fm, file upload), a
  preview/match/confirm review flow, and search-based import.
- **GenerateMixesPage** — UI for the ~15 mix-generation algorithms, each with its own parameter form;
  SSE-driven progress bar during generation.
- **SettingsPage** — tabbed: Plex, Server, AI, Matching, Mixes, Services (connected OAuth services),
  and (admin-only) Admin — which embeds the full `AdminPage` component (user management, job/log
  viewers, deemix settings) as a tab rather than a separate route.
- **CrossImportPage** / `components/cross-import/*` — step-driven wizard (source → target → OAuth →
  playlist picker → matching (SSE) → review → confirm) for copying a playlist from one service to
  another (including Plex→other-service, via the target adapters).
- **BackupRestorePage** — export/import a JSON snapshot of settings + playlists via
  `apiClient.confirmImport`.
- **NotificationCenter** (new/uncommitted) — header bell, polls `GET /api/notifications` every 4s,
  renders in-progress/success/error rows for deemix downloads, missing-track retry batches, scheduled
  refreshes, queued imports, and AI playlist generation; triggers a playlist-table refresh the first
  poll after an import/schedule notification flips to `success`. Replaces the deleted
  `HeaderActivity` component and `QueuePage`.

## Background jobs

Three cron jobs run once `NODE_ENV=production` (or `ENABLE_JOBS=true`): `daily-scraper` (2am, refreshes
chart caches), `schedule-checker` (every 10 minutes, executes due playlist-refresh/mix-generation
schedules), `cache-cleanup` (Sunday 3am, prunes stale `cached_playlists` rows).

---

# QA Report

## Method

No headless browser was available in this environment, so I could not click through the UI directly.
QA instead combined: full TypeScript build of both apps, the existing automated test suites, live
smoke-testing of the running production service (`playlist-lab-server.service`, restarted with current
source per the standing dev-server workflow), and targeted code review of the newest/uncommitted code
(the notification-center refactor is the largest unreviewed surface right now) plus the endpoints most
recently touched by the "2.0" commit. Direct inspection of the live SQLite database (which holds Plex
tokens) was blocked by the environment's permission classifier and not attempted further — reasonably,
since it wasn't necessary for what follows.

## Results

**Build:** `apps/server` (`tsc --build`) and `apps/web` (`tsc && vite build`) both compile clean, no
errors or warnings.

**Tests:** `apps/web` — 89/89 pass. `apps/server` — 855/856 pass; one property test fails
(`tests/property/migration.property.test.ts`, "Desktop Data Import Round-Trip"). See finding #1.

**Live smoke test** (restarted service, current build): health check, SPA root, SPA fallback routing,
static asset serving, and unauthenticated `401`/unknown-route `404` handling all behave correctly.
Production log review turned up finding #3 below — a real, currently-occurring failure.

## Findings

1. **Low severity, not user-facing today.** `POST /api/migrate/desktop` (legacy desktop-app import)
   passes `matchingSettings` straight through to storage with no validation. The property test found
   that a `minMatchScore` of `NaN` comes back as `null` after a round trip (an artifact of
   `JSON.stringify(NaN) → null`), which is arguably correct JSON behavior rather than a real defect.
   More relevant: `apiClient.migrateDesktopData()` exists in `packages/shared/src/api/index.ts` but is
   **not called from any page** — this endpoint has no UI entry point at all in the current app, so
   nothing a user does can trigger it. Leave as-is unless the desktop-migration flow is coming back.

2. **Dead backend surface, not a bug.** `routes/import.ts`'s session-based progress/status/cancel
   endpoints and the manual queue-inspector endpoints (`GET /queue`, `GET /queue/completed`,
   `DELETE /queue/:jobId`, etc.) and `POST /api/import/preview` have no caller anywhere in
   `apps/web` — confirmed by grepping the shared `ApiClient` and both `EventSource` usages in the web
   app. They were built for the now-deleted `QueuePage.tsx`; queued imports now report through the new
   notification bell instead, and that path is correctly wired (verified `addNotification`/
   `updateNotification` call sites line up with the bell's `TYPE_LABEL` map for every emitter: deemix,
   retry-match, schedule, import). Worth deleting alongside the rest of the Queue-page cleanup, but not
   urgent since it doesn't affect users.

3. **Real, currently-occurring failure — worth your attention.** Live production logs
   (`/opt/playlist-lab-server/logs/error.log`) show two active schedules (playlist refreshes sourced
   from Deezer, names logged as "george day" and "now that's what I call running") failing on **every**
   10-minute `schedule-checker` tick with `Failed to scrape Deezer playlist: Request failed with status
   code 403`, going back at least since 17:32 today. I confirmed this isn't a general network/IP block —
   ad-hoc requests from this same server to `api.deezer.com` for other playlist IDs succeed fine (`200`).
   That points to those two specific Deezer playlists having become inaccessible from the public API
   (deleted, made private, or region-restricted) rather than a Playlist Lab code defect. Recommend
   checking those two playlists directly on Deezer, or reconfiguring/removing the schedules — no code
   change indicated unless you want the scraper to surface a clearer "playlist unavailable" error
   instead of a generic 403 message (currently: `services/scrapers.ts` `scrapeDeezerPlaylist()` doesn't
   distinguish a dead playlist from a transient failure).

## Not covered

Authenticated, click-through UI verification (login, import wizard, mix generation, drag-drop track
editing, etc.) requires either a browser automation tool (not installed in this environment) or a live
user session — neither was available/appropriate here. If you want that level of verification, the
options are: install a headless browser (e.g. `playwright install`) so I can drive the real UI next
time, or you spot-check the flows above yourself against `http://localhost:3001` and report back
anything that looks off.
