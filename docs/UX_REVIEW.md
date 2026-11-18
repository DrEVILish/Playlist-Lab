# Playlist Lab: UX and system review (2026-10-08)

Scope: Go + htmx 4 + ftl-themes + deemix stay. The look comes from
ftl-themes (default `blue-future`, now v5.3), so this pass doesn't invent a
palette or typefaces. It covers information architecture, flows, copy, data
handling, and the bugs found along the way.

Verified in headless Chromium against a scratch database and a fake Plex
server (24 playlists, missing tracks, schedules), at 390px, 1000px and
1280px. Spot checks covered `lcars`, `windows95` and `winxp-luna`.

---

## 1. What each part of the UI does

### App shell (every page)

| Element | Function |
|---|---|
| Brand | Home link to Playlists. |
| Main nav | Six destinations, each a real page: **Playlists**, **Missing** (with a count badge), **Import**, **Mixes**, **Collections** (Plex server owners only), **Export** (to YouTube). Below 720px it collapses behind a ☰ toggle (`.nav-collapse`). |
| Bell | Background-job feed over SSE: imports, mixes, deemix, refreshes. Running jobs show a spinner; finished ones toast once while the page is open. |
| Account menu | Settings; **Shared with me**, **Status and reports**, **Backup and restore** (these three open as modals and are also real pages); Log out. |
| Status bar | The Plex server and library every music feature works against, with a link to change them. |
| Toasts | Short confirmations, plus **every failed request** (new): the server's message shown as an error toast. |
| Confirm dialog | Themed replacement for `window.confirm` on every `hx-confirm`. |

### Playlists (`/`)

| Element | Function |
|---|---|
| Heading | Counts: on Plex, on a schedule, tracks still missing (links to Missing). Primary actions **Import playlist** and **Generate mix**. The ⋯ menu has: refresh all scheduled now, save this list as CSV, shared with me. |
| Quick filters | All / Needs attention (has missing tracks or a failed refresh) / Scheduled. |
| Search | Name contains; keeps every other filter. |
| Active filter tags | One removable tag per filter, "Showing N of M", Clear all. |
| Column header | Click to sort; the funnel opens that column's filter (a popover). |
| Row | Click to expand a read-only track preview. Right-click opens the row menu. The pencil opens the editor. |
| Missing badge | Expands that playlist's missing tracks inline. |
| Schedule cell | Interval badge (opens the schedule modal), next run, last run (red if it failed), or **Set schedule**. A "—" means the playlist was made outside Playlist Lab, so there's no source to refresh from. |
| Added | Import date, else Plex's own added date (new). |
| Bulk bar | Appears when rows are checked, with a count. Actions: save backup, save CSV, merge into a named new playlist, delete. |

### Editor (`/playlists/{id}`)

| Element | Function |
|---|---|
| Heading | Cover, name, tracks, duration, source, added date, missing badge (jumps to the list), schedule badge. |
| Sort… | Popover form: field and direction. |
| Shuffle | Now asks first; the old order isn't kept. |
| Save as file… | One click per format (M3U8, M3U, PLS, XSPF, CSV, text). |
| More (⋯) | Rename, upload cover, share, remove duplicates, delete. |
| Missing tracks | Accordion with a count; same list as the Missing page, scoped to this playlist. |
| Add a track | Search-as-you-type over the Plex library. |
| Track table | Drag handle and ▲/▼ to reorder, checkbox, preview, inline metadata, delete. Columns can be hidden and reordered. Checked rows can be copied into a new playlist. |

### Missing (`/missing`)

| Element | Function |
|---|---|
| Heading | What "missing" means and how to resolve it. **Retry all**, **Download all** (only once deemix is set up), **Save list as CSV**. |
| Filter | Client-side filter by track, artist or playlist. |
| Per-playlist group | Retry this playlist, clear list. |
| Row | **Match…** (search Plex, remembered for future runs) and **Download** (deemix, when set up) shown directly. The ⋯ menu has: retry matching, use a similar track, search with Lidarr (when set up), remove from list. |
| Bulk bar | Retry selected, remove from list. |

### Import (`/import`)

| Element | Function |
|---|---|
| Paste a link | One box. The server now works out the source from the link's host. **Review matches** (recommended: see every match before anything is created), **Import without review**, **Import a file**. |
| Browse | Source picker in text (Spotify, Deezer, YouTube, Billboard, ARIA, ListenBrainz, Last.fm), search by name, country charts, saved Spotify users, Billboard chart weeks. |
| From another Plex Home user | Copy a Home user's playlist. |

### Mixes (`/mixes`, `/mix-templates`)

The two pages are now one section with a **Generate / Saved mixes**
switch. Generate shows six listening-history mixes (with **Generate all
six**, which asks first) and nine library mixes, including Custom and AI.
Each opens a quick-settings or advanced form.

### Collections (`/collections`, `/dynamic-collections`)

One nav entry, with a **Collections / Dynamic collections** switch. Each
has Browse presets and New. Table, schedules and missing films and shows
are unchanged.

### Export (`/cross-import`)

Steps: connect, choose, match, review and create. A list of past exports.

### Settings

Vertical tabs. Per-user: Plex servers, matching, mixes, AI, connected
services, library scan, appearance (theme, palette variant, tint, text
size), sessions, server info. Admin: statistics, users, missing tracks
across all users, schedules, Deemix, Lidarr, YouTube, TMDB, TVDB, Trakt,
logs, reverse proxy.

---

## 2. Bugs found and fixed

| # | Bug | Effect | Fix |
|---|---|---|---|
| B1 | Column filter forms were nested inside the bulk-delete `<form>`. The HTML parser drops a nested form tag, so the Search filter's input belonged to `POST /playlists/bulk-delete`. | Searching from the Playlist column POSTed to bulk-delete with no ids. | Filters are top-layer popovers outside the bulk form, and a visible search box was added. |
| B2 | Bulk **Delete** and **Merge** were native form submits to handlers that only answer with `HX-Redirect`. | After deleting or merging, the browser showed a blank page at `/playlists/bulk-delete`. | Both use `hx-post` with `hx-include="closest form"`; delete uses the themed confirm. |
| B3 | The layout listened for htmx 2 events (`htmx:beforeRequest`, `htmx:sendError`, `htmx:responseError`); the bundled library is htmx 4. | The offline banner never showed. `htmx:confirm` read `e.detail.question` (undefined in v4), so every confirm fell back to the browser's native `confirm()`. | Rewired to `htmx:error`, `htmx:after:request`, `htmx:response:error` and `ctx.confirm`. |
| B4 | 4xx/5xx responses are deliberately not swapped (`noSwap`), and nothing else surfaced them. | Every failed action (no deemix match, bad merge name, Plex down) failed silently. | A global error toast shows the server's message. |
| B5 | Pasted links used the selected source tab, Spotify by default. | A Deezer or Apple Music link failed unless you first picked the right tab. Apple, Tidal, Amazon and Qobuz were unreachable from the page even though their adapters are registered. | `ImportHandler.importSource` detects the source from the link's host (test: `import_source_test.go`). |
| B6 | Admin panels rendered stored secrets back into the page (`value="{{.DeemixArl}}"`, plus the TMDB, TVDB, Trakt, Lidarr API keys and the YouTube client secret). | Secrets reached every admin's browser HTML and cache, against DESIGN.md §11.4 ("masked, no reveal"). | Never rendered; a blank field keeps the saved value; an explicit **Remove** (confirmed first) clears it (test: `TestSubmittedSecretKeepsSavedWhenBlank`). |
| B7 | The deemix ARL check ran at every start even when no ARL was ever set. | Every admin got a red "Deemix login failed" notice on each restart. | Skipped when the ARL is unset; the notice now points to Settings › Deemix. |
| B8 | A saved theme slug that upstream later removes was used as-is. | It linked a missing stylesheet, so the whole app rendered unstyled. | Falls back to `blue-future` (test: `TestRemovedThemeFallsBackToDefault`). |
| B9 | The header always rendered `<img src="">` when the user had an empty thumbnail. | A broken image next to the username. | Initials avatar unless there's a real URL. |
| B10 | Status counts were sorted by count only, from a map. | Tied rows reordered on every reload. | Stable tie-break by label. |
| B11 | Date added was only known for playlists imported through the app. | 18 of 24 playlists showed "—" and sorted last. | Falls back to Plex's `addedAt`. |
| B12 | htmx 4 ignores a button's `formaction`. | Settings › Deemix "Test ARL" posted to the *save* route, re-saving instead of testing. | The button has its own `hx-post` to `/admin/deemix/arl/check`. |

---

## 3. UX changes

**Navigation (biggest issue).** Eleven top-level items lived in one bar.
At 1280px, Dynamic Collections, Export to YouTube and Mix Templates sat
off-screen with no scroll cue. On a phone only "Playlists" was visible, and
Missing wasn't in the nav at all. Now there are six destinations; sibling
pages share one entry and switch inside it; account items moved to an
account menu; the duplicated ⚡ menu (deemix all, retry all, refresh) was
removed, since its actions live on Missing and Playlists. Nav items now
navigate, so URL, Back and bookmarks work. Import and Generate are now
full pages everywhere: the Playlists buttons link to them. This departs from
DESIGN.md §11.3 ("Generate Mix stays a modal"): a fifteen-card picker and
its forms read better with room and a URL. Both still render as modals for
any htmx caller (`RenderModal`).

**Playlists.**
- Visible search, quick filters, and removable filter tags.
- 11 columns down to 9; Next run and Last run fold into the Schedule cell.
- Source shown as readable text: brand wordmarks vanished on dark themes
  (Billboard, ARIA, Last.fm and Apple were black on navy).
- Phone layout scrolls sideways with the checkbox and name frozen.
- A real empty state with Import and Generate.
- The bulk bar shows itself from CSS and keeps a live count.

**Editor.** Eleven loose toolbar controls became Sort…, Shuffle, Save as file…
and More. Destructive or irreversible actions (Shuffle, Remove duplicates,
Delete) confirm first. "Split checked tracks into new playlist" is now
"Copy checked to new playlist", which says what happens: the source isn't
changed.

**Missing.**
- The page explains itself.
- The two resolving actions (Match, Download) sit in each row instead of a
  menu, per DESIGN.md §11.6.
- Deemix and Lidarr actions only appear when those services are set up.

**Import.** One link box (source auto-detected) above a separate Browse
panel. The recommended path is named **Review matches**; "Preview" didn't
say what it did. Copy for empty states and Plex Home errors now says what
to do next.

**Copy.** Sentence case, actions named for what they do, one word per
action: **Download** only ever means fetching music with deemix, **Save…**
means getting a file, and **Export** means sending a playlist to YouTube. "Export to
YouTube" no longer says "No import history"; "Mix Templates" and "Saved
mixes" are one name; modals no longer repeat their title twice.

**ftl-themes v4 → v5.3.** This fixed six of the issues we filed
(`[hidden]`, nav-collapse, modal width, modal header, bar contrast, sticky
headers); their workarounds are removed. Every menu is now a `[popover]`
(top layer, no clipping), and toasts sit above modals. See
`FTL_ISSUES.md` for the rest, including new #14 (links vanish in a
selected row).

---

## 4. Gaps still open

Ranked by impact. Each says when it is worth doing.

1. **Untracked `internal/services/browser/zzz_*_test.go` drive the live
   :3001 server, and `zzz_all_themes_shot_test.go` writes `theme` for
   user 1 in the production DB.** A plain `go test ./...` runs them, and
   this review's first test run did. Delete them, or put them behind a
   build tag, now.
2. **Error messages are developer-worded.** About 33 handlers return text
   like "invalid track id" or "no server selected", which users now see as
   toasts. Rewrite them in the interface's voice when touching each
   handler.
3. **Missing groups use the app's stored playlist name.** A rename made in
   Plex isn't reflected on Missing (Playlists uses the live Plex title).
   Refresh the stored name when the Playlists page loads, if users rename
   in Plex.
4. **No bulk "download selected" for missing tracks.** It's only one track
   or all of them. Add it when deemix users manage large backlogs.
5. **Apple Music, Tidal, Amazon and Qobuz sources** are reachable now via
   pasted links but are untested here (the fake Plex harness doesn't cover
   the scrapers). Check each with a real link before advertising them.
6. **The confirm dialog's button always says "OK".** Let `hx-confirm`
   carry an action name (Delete, Shuffle) when it's next touched.
7. **Status › "View logs"** links to an admin-only tab for every user.
   Gate it on admin.
8. **Settings opens on Matching**, not Plex servers. Change it if new
   users struggle to find server setup.
9. **DESIGN.md items not built:** tiered automatic backups (§11.8), the
   first-run tour (§12), and WebSocket live updates (§6; SSE covers the
   bell). Each is backend work; scope it separately.
10. **Editor rows carry both a drag handle and ▲/▼ buttons.** That is
    noisy at 36+ rows. Collapse ▲/▼ into keyboard-only (shown on focus)
    if the table feels crowded.
