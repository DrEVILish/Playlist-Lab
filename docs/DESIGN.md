# Playlist Lab — Design System

**Style direction:** SciFi Futuristic. The interface should read as an advanced
spacecraft computer, AI operating system, or high-end industrial control
system — not a cyberpunk website. Precise, uncluttered, dark. Glow is a
signal, not a decoration.

This is a **target spec**, built from the current codebase
(`static/css/base.css` and friends) plus two rounds of product decisions.
It keeps what already fits the brief and tightens the rest. Where an
existing token or pattern changes, the old value is noted so the diff is
traceable when implementation starts. Items that need real backend work
(not just templates/CSS) are called out inline and collected in §17.

---

## 1. Color

The dark-navy surface scale already matches the brief and is **kept
as-is**. The deliberate change is the primary/accent pair, which moves
from a muted slate-blue to a genuinely electric blue + cyan, per the
brief's explicit call for "electric blue and cyan as primary accent
colours."

### Backgrounds & surfaces (unchanged)

| Token | Value | Use |
|---|---|---|
| `--background` | `#0a1018` | App shell, deepest layer |
| `--background-elevated` | `#0e1520` | Raised sections within the shell |
| `--surface` | `#131d2a` | Cards, panels, inputs |
| `--surface-hover` | `#1a2736` | Hover state on surface elements |
| `--sidebar-bg` | `#0c1420` | Header / mobile nav |
| `--border` | `#1a2a3a` | Default hairline border |
| `--border-light` | `#243447` | Emphasized / hover border |

### Primary accent — electric blue + cyan (changed)

| Token | Old | New | Use |
|---|---|---|---|
| `--primary-color` | `#5b9bd5` | **`#2F8FFF`** | Primary actions, active nav/tab, selection, links |
| `--primary-hover` | `#4a8bc4` | **`#4DA1FF`** | Hover on primary elements |
| `--primary-dark` | `#3a7ab3` | **`#1D6FE0`** | Pressed / active-darker state |
| `--accent-cyan` | `#3d8f99` | **`#00E5FF`** | Secondary highlight: telemetry values, live/active system status, holographic accents |
| `--accent-teal` | `#4a9ea8` | **`#1FB8C4`** | Tertiary — data-viz series only (see §8.10), not on interactive controls |
| `--primary-glow` | `rgba(91,155,213,0.2)` | **`rgba(47,143,255,0.28)`** | Glow shadow base |

**Changed again:** `--gradient-primary` is banned outright, per later user
direction taken for ftl-themes compatibility — that theme system's token
contract (`--ctp-*`) has no gradient
concept at all, only flat accent colors plus glow, so a bespoke brand
gradient is one more thing an external theme can't override cleanly.
Every former `--gradient-primary` call site (primary buttons, active tab/
pill fills, progress bars, the logo wordmark) now uses a flat
`var(--primary-color)` or `var(--accent-cyan)` fill instead, with the glow
carrying the "energized" feel gradients used to. Don't introduce any new
gradient on an interactive surface.

Blue means "interactive / selected." Cyan means "live data / system
telemetry." If both appear on the same element, that's a bug, not a style
choice.

### Text (unchanged)

| Token | Value | Use |
|---|---|---|
| `--text-primary` | `#e8edf2` | Body text, headings |
| `--text-secondary` | `#7a8fa3` | Supporting text, labels |
| `--text-muted` | `#7488a0` | Disabled / tertiary |

Reserve pure white (`#ffffff`) for the specific things the brief calls
"important information" — a stat tile's primary number, a critical status
line — not `--text-primary` generally. It should stay rare enough that
seeing it means something.

### Semantic status colors (unchanged)

| Token | Value | Use |
|---|---|---|
| `--success` | `#66bb6a` | Completed / healthy / connected |
| `--warning` | `#ffa726` | Attention needed, non-blocking |
| `--error` | `#ef5350` | Failed / disconnected / destructive |

Standard green/amber/red, not folded into the blue/cyan system — a
control-room HUD needs status color that reads instantly without competing
with "is this selected" blue.

### Third-party brand marks — themed, not exempt (changed)

**Changed:** service logos (`static/service-logos/*.svg` — Plex, Spotify,
Deezer, Tidal, etc.) used to keep their original brand colors as the one
deliberate exception to the SciFi palette. Per later product direction,
that exception is dropped: every mark is now a flat white silhouette
(`filter: brightness(0) invert(1)` on the `<img>`, not a recolored source
asset) inside its neutral dark chip, brightening with a restrained
`--accent-cyan` `drop-shadow` glow on hover/selected state, matching every
other "live/active" affordance in §4. A Spotify integration no longer looks
like Spotify at a glance - it looks like the rest of the instrument panel,
which is the point: the container was already on-system, and the mark
inside now is too.

---

## 2. Typography

Kept as-is: `Inter` for UI text, `JetBrains Mono` for technical values.
**Extend** the monospace usage beyond `<code>` to any raw system value —
timestamps, durations, counts, IDs, file sizes, sync progress ("142 / 300
tracks"), version numbers. Add a `.mono-data` utility
(`font-family: var(--font-mono); font-variant-numeric: tabular-nums;`) and
apply it at those call sites. This is the single biggest lever for the
"spacecraft computer" feel and costs one class, not a redesign.

| Element | Size | Weight |
|---|---|---|
| h1 / page title | 1.75rem | 700 |
| h2 / section title | 1.25rem | 600 |
| h3 | 1rem | 600 |
| body | 0.9375rem | 400 |
| label / small UI text | 0.875rem | 500 |
| badge / micro text | 0.75rem | 500 |

Keep tight letter-spacing (`-0.5px`) reserved for the logo wordmark only.
Don't chase a "sci-fi" font effect with wide tracking app-wide — it costs
legibility, which the brief prioritizes explicitly.

**Text scale setting.** Settings gains a small/medium/large text-size
control (§14), implemented as a single root scale variable
(`--text-scale`, default `1`) multiplying the rem sizes above, so nothing
in this table needs per-component overrides.

---

## 3. Layout, spacing & navigation

- **Radius:** `--radius-sm: 6px`, `--radius-md: 8px`, `--radius-lg: 12px`,
  `--radius-xl: 16px` — geometric, not pill-shaped.
- **Breakpoints:** mobile `< 769px`, tablet `769–1024px`, desktop
  `≥ 1025px`, wide `≥ 1441px`.
- **App shell:** sticky header + slide-out mobile drawer — no persistent
  desktop sidebar for top-level nav. (Settings gets its own sidebar
  sub-nav internally — see §11.4 — that's local to Settings, not the app
  shell.)
- **Landing page:** the Playlists view (§11.1), not a stats dashboard.
- **Default density: compact** across every table/list in the app
  (Playlists, Editor, admin logs, Missing Tracks) — tighter row height and
  padding than the current `.card`/`.p-3` defaults.
- **No breadcrumbs.** Nested views (Editor, Admin sub-panels,
  Cross-Import steps) rely on tabs/step-indicators and back-navigation
  only — the app's nesting is shallow enough not to need a trail.
- **No public landing page.** This is a self-hosted tool; unauthenticated
  traffic goes straight to Login.

---

## 4. Elevation, borders & glow

**Changed:** the original brief asked for restrained glow ("sparingly,"
"avoid... bright glowing elements everywhere"), scoped to active/selected
state, live activity, and the primary CTA only. A later reference
stylesheet (an "instrument panel" hardware-control aesthetic - dark/
transparent controls that light up with a glowing cyan border on hover)
pushed the brief further: glow is now the app's general hover/focus
language for interactive controls, not a rare accent. Superseded rule:

**Glow now applies to:**
1. Hover/focus on any interactive control — buttons (`.btn-secondary`,
   `.icon-btn`, `.tab-button`, `.badge-button`), text inputs/selects/
   textareas, checkboxes when checked. A soft `rgba(0, 229, 255, …)`
   box-shadow (`--accent-cyan`) layered onto whatever border/background
   change already happens on hover — not a replacement for it.
2. `.card`'s thin HUD-reticle corner brackets (`::before`/`::after`,
   promoted from the Login/Setup hero cards' `.hero-backdrop-card` to every
   `.card` app-wide) - decorative, not a hover effect, but part of the same
   instrument-panel language.
3. Active/selected state, live system activity, and the primary CTA (the
   original three cases) still glow, unchanged - this rule only removes the
   *exclusion* on ordinary controls, it doesn't reduce anything that already
   had glow.

**Still not glowing:** plain static badges/chips with no interactive
affordance, and page/card backgrounds at rest (glow is a hover/focus/active
signal, not an ambient background effect). `--card-shadow-hover` (a plain
dark shadow, no color) is still correct for a `.card`'s hover *shadow* -
the corner brackets are the card's glow element, not its hover shadow.

**Borders over fills.** Panels distinguish themselves with a 1px
`--border`/`--border-light` line, not a lighter background fill (beyond
the existing one-level `--surface` vs `--background` step). This is what
makes the interface read as "panels," not "stacked cards."

**Transparency** stays scoped to overlays and sticky chrome (header
blur, modal/drawer overlay scrims) — not panel backgrounds, which stay
opaque for legibility against this app's table- and form-heavy layouts.

**No persistent system-health indicator.** A connectivity problem is
surfaced only when it actually happens (§10's offline banner), not via an
always-on status dot in the header — most of the time it would just be
green chrome.

---

## 5. Iconography

Inline SVG, 24×24 viewBox, `stroke="currentColor"`, `stroke-width="2"`,
`stroke-linecap/linejoin="round"` (Feather-style monoline). Sizes: 16 / 20
/ 24px only. Color follows text color via `currentColor` — no separate
icon-color tokens.

**Every icon-only control gets a tooltip** — a native `title` attribute is
sufficient, no tooltip library needed. This matters more than usual here:
the app leans on icon-only buttons throughout (header actions, row
actions, missing-track resolution actions).

Empty-state illustrations (§8.7) extend this same monoline language in the
primary/cyan palette — not full-color stock art — so they read as part of
this icon system rather than a bolted-on illustration pack.

---

## 6. Motion & live data

Durations: `0.15s` micro (hover color), `0.2s` buttons/inputs, `0.25s`
cards, `0.3s` nav slide-in. `ease` throughout — consistent with
"precise," not "bouncy." `prefers-reduced-motion` is **deliberately not
handled** — a conscious scope call for this app's audience, not an
oversight; revisit if that changes.

**Toast → bell animation.** A toast appears top-right as usual, and on
resolve/dismiss shrinks and animates toward the notification bell icon,
incrementing its unread badge and adding the item to notification history.
This is the one choreographed motion beyond the baseline above — keep it
to this single flow rather than reusing "fly to target" elsewhere.

**Live data updates.** Where server-side state changes while a page is
open (a background sync completes, another tab edits a playlist), the
page updates in place via a server push over htmx's WebSocket extension —
not polling, and not a manual "data changed, refresh?" prompt. *Backend-
scoped: needs the relevant handlers to broadcast on state change; see
§17.*

**Offline handling.** If server requests start failing outright (not a
single action's error, but the app losing reachability), show a slim
top banner ("Connection lost, retrying...") rather than leaving failures
silent — wire it into the same htmx `beforeRequest`/`afterRequest` hooks
already used for button loading states.

---

## 7. Data formatting conventions

| Data | Format | Notes |
|---|---|---|
| Track duration | `m:ss` (e.g. `3:42`) | Never hours for a single track |
| Playlist/sync total duration | `h:mm` (e.g. `2h 15m`) | |
| Timestamps (Last Run, log entries) | Relative (`2 days ago`) with the exact timestamp in a `title` tooltip on hover | Scannable by default, precise on demand — one attribute, not a second visible column |
| Date Added (Playlists table specifically) | Absolute date only (e.g. `Jan 2, 2026`), no relative/hover form | User feedback on the live table: a "when did this actually happen" column reads worse as a moving relative string than every other timestamp does — this is the one deliberate exception to the row above |
| Genre/mood tags | Chip/pill badges, reusing `.badge` (§8.2) | Not plain comma-separated text |

---

## 8. Core components

### 8.1 Buttons
- `.btn-primary`: transparent fill, `--accent-cyan` text/border — one per
  view/section; hover fills with a tinted `--accent-cyan` background and
  adds glow (§4, changed; superseded the gradient fill this bullet
  originally specified).
- `.btn-secondary`: `--surface` fill, `--border-light` outline — the
  default for everything else; hover brightens the border to
  `--accent-cyan` and adds the same glow (§4, changed).
- `.icon-btn`/`.tab-button`/`.badge-button` follow the same hover-glow
  treatment as `.btn-secondary`.
- Loading state (`.is-loading`, htmx `.htmx-request` dimming) unchanged.

### 8.2 Badges / chips
`.badge-primary/success/warning/error` — 15%-alpha tinted background,
solid-color text, no border. Update `.badge-primary`'s alpha color to the
new `--primary-color`. Same component doubles as the genre/mood tag chip
(§7).

### 8.3 Status dot
`.status-dot` (solid dot, color by state, pulse animation for "running")
is the canonical status primitive — reuse it anywhere status is currently
shown as a badge alone. Consolidate the several near-duplicate
`@keyframes pulse` definitions scattered across component CSS into one
shared `@keyframes status-pulse` in `base.css`.

### 8.4 Tooltips
Native `title` attribute on every icon-only control (§5). No tooltip
library.

### 8.5 Tabs
`.tab-buttons`/`.tab-button` stays the pattern for **in-page** sub-tabs
(e.g. the Generate Mix modal's Quick/Custom/Advanced switch). It is no
longer used for Settings' top-level navigation, which moves to a sidebar
(§11.4).

### 8.6 Data tables
The canonical table, used by Playlists, Editor, Missing Tracks, and admin
logs:
- **Compact density**, sticky header row while scrolling.
- **Full-row hover highlight** (`--surface-hover`).
- **Row actions always visible**, not hover-revealed — reversed from this
  doc's original hover-reveal call after using the live table: a dense
  table with several rows read as sparse/broken before a pointer ever
  touched a row, and touch devices have no hover state to reveal them
  with at all. The right-click/"..." context menu below stays as an
  additional way to reach the same actions, not the only way.
- **No pagination.** Render the full result set in one scrollable
  container — these datasets are well within what a modern browser
  handles natively. *Ceiling: if any table's row count grows into the
  tens of thousands, revisit with virtual scrolling — not before.*
- **Right-click context menu** on rows, as a themed alternative to the
  hover action icons, keyboard-accessible.
- **CSV export.** Missing Tracks and admin logs keep a standalone toolbar
  button (exporting whatever the current view/filter shows). Playlists'
  moved into the sticky bulk-actions bar as "Export Selected" instead —
  sitting next to a "Select" mode toggle in the main toolbar, it wasn't
  clear from the button alone what it would actually export; living in
  the bulk bar makes the scope unambiguous by context, the same way
  Backup/Merge/Delete Selected already are. Bulk-select checkboxes
  themselves are always visible now too (§8.6 above), which is what made
  the separate "Select" toggle redundant in the first place — a checkbox
  that behaves identically whether or not "Select mode" is on doesn't
  need a button to turn that mode on.
- Where a table's columns are user-configurable (currently: Editor, see
  §11.2), users can both show/hide and drag-reorder columns. *Backend-
  scoped: needs a persisted per-user column-preference store; see §17.*
- Sort/filter controls live in the table's own header bar (inline
  chip/dropdown toolbar), not a separate sidebar facet panel, and support
  multiple simultaneous filter criteria.
- Bulk selection: checkboxes hidden until hover or an explicit
  select-mode toggle. Once anything is selected, a **sticky bottom
  action bar** appears with the available bulk actions (delete, backup,
  schedule, resolve, etc.) — the same bar pattern reused everywhere bulk
  selection exists (Playlists, Missing Tracks).

### 8.7 Empty states
Custom line-art illustrations (not icon+text), in the primary/cyan
palette, same stroke weight as the icon system (§5) — one per empty
state (no playlists, no missing tracks, no schedules, etc.).

### 8.8 Skeleton loading
Skeleton screens (gray placeholder shapes matching the real layout) for
data-heavy view loads (Playlists table, Editor table), replacing the
centered spinner for those cases specifically. The spinner and
`.is-loading` button treatment stay for discrete actions.

### 8.9 Progress
Determinate progress bar with a percentage when the total is known (a
full sync, an import batch), with an expandable live log stream
underneath for detail. Multi-file uploads show **one progress bar per
file**, not a single combined bar.

### 8.10 Charts
Admin stat tiles each get a small inline sparkline for at-a-glance trend;
one larger line chart lower on the dashboard for whichever metric an
admin wants to inspect more closely. `--accent-teal` is reserved for
these data-viz series (§1) — never reused on interactive controls, so a
chart line is never confusable with a clickable element.

**Charting library: [TanStack Charts](https://tanstack.com/charts/latest).**
Style charts to match this system rather than its defaults: dark
`--surface` background, `--border` gridlines, `--primary-color`/
`--accent-cyan`/`--accent-teal` for series color, `--font-mono` for
axis/tick labels (§2, §7). *Framework note: TanStack Charts ships
framework adapters (React/Solid/Vue) — this app's frontend is server-
rendered Go templates + htmx with no JS framework in place, so adopting
it means introducing one of those adapters (or its framework-agnostic
core, if usable standalone) scoped to just the chart-bearing admin
views, not an app-wide framework migration. Confirm that scope before
implementation; see §17.*

### 8.11 Modals
One modal system for everything, including confirmations: the existing
`.modal-overlay`/`.modal-content` shell. **Confirmation dialogs move off
the native `<dialog>` element onto this same custom modal** — treat
"confirm" as a modal variant, not a second dialog mechanism. Consolidate
the duplicated modal CSS in page-specific files (`EditPlaylistsPage.css`,
`SharePlaylistsPage.css`) onto the shared one. Overlay stays
`rgba(0,0,0,0.5)`, no blur (blur is reserved for sticky chrome, §4). Esc
closes the modal and the overlay is click-to-dismiss, same as today.

**Layered submodals and width.** Every modal (regardless of which of the
app's several overlay/box CSS families it uses - `.modal-content`,
`.advanced-mix-modal`, `.quick-mix-settings-modal`, etc.) defaults to
`max-width: 90vw`; no modal gets its own narrower fixed-pixel override.
An action inside an already-open modal can open another one on top of it
instead of replacing it (`layout.html`'s `hx-swap="beforeend"` into
`#modal-root` + `closeTopModal()`, which closes only the topmost layer) -
each stacked layer is 10vw narrower than the one behind it (90vw, 80vw,
70vw, 60vw, 50vw, floor `max(40vw, 400px)`) so the layer(s) behind stay
partially visible. The generic, family-agnostic CSS for this lives in
`Modal.css` (`#modal-root > div:nth-of-type(N) > [class*="modal"]`) rather
than per-family, and applies desktop-only (`min-width: 769px`) - mobile
stays full-width via each family's own existing `@media (max-width: 768px)`
rule. Height follows the same existing `max-height: 90vh` convention
(shrinking to fit shorter content) at every layer, not just the first.
Not every modal-opens-a-modal path uses `beforeend` yet (e.g. Generate's
quick-mix/advanced-form cards, and Browse Presets' "Use This"/"All
Years..." still replace rather than stack) - extend that swap pattern to a
flow before assuming it visibly layers.

### 8.12 Forms & validation
- Inline validation: error message directly under the offending field,
  field border turns `--error`. No top-of-form summary banner.
- Required fields marked with an asterisk on the label (`Name *`).
- Character counters on length-limited fields (playlist name,
  description) appear only once the input is within ~20% of the limit —
  not shown from the first keystroke.
- Artist/track search fields (e.g. building a Custom Mix) use a live,
  debounced autocomplete dropdown, extending the existing
  `track_search_results.html` partial pattern.
- File upload zones (Backup restore, library import) are a bordered
  drag-and-drop drop zone that also contains the existing
  `.file-input-label` button as a fallback — covers both interaction
  styles.
- Copy-to-clipboard actions (share link, API key) are an icon button that
  copies and fires a brief "Copied" toast — not click-to-select text.
- **Toggle switches** (`.settings-toggle input[type="checkbox"]`, changed):
  a CSS-only sliding track+thumb via `appearance: none` plus a `::before`
  thumb on the native checkbox itself - no wrapper markup - glowing cyan
  when checked, matching the instrument-panel reference's switch look.
  Table row-select/bulk-select checkboxes are unaffected (different
  selector scope; those stay plain square checkboxes, appropriate for a
  compact table cell rather than a settings on/off control).
- **Range sliders** (`.settings-range`, changed): a styled gradient
  track + glowing square thumb (`::-webkit-slider-thumb`/
  `::-moz-range-thumb`) replacing the bare `accent-color` native look,
  same reference.

---

## 9. Optional decorative detail (restrained, opt-in)

The brief's "fine grid patterns... restrained holographic effects" are
**not** part of any core component in §8 and must not be added globally.
Specific surfaces may opt in:

- **Grid texture:** a single low-contrast background —
  `background-image: linear-gradient(rgba(47,143,255,0.04) 1px, transparent 1px), linear-gradient(90deg, rgba(47,143,255,0.04) 1px, transparent 1px); background-size: 24px 24px;`
  — on `--background`/`--background-elevated` only, never on `--surface`
  panels (would fight table/form content).
- **Corner accents (changed - no longer opt-in):** a thin cyan bracket
  (border on two sides, offset via `::before`/`::after`) evoking a HUD
  reticle. Originally scoped to `.hero-backdrop-card` (Login/Setup only);
  §4's glow-language update promoted it to a `.card` base-component default
  app-wide, since the instrument-panel reference this app now follows
  treats corner brackets as standard panel chrome, not a rare accent.
  `.hero-backdrop-card`'s own slightly larger/offset variant (22px, -8px
  offset vs `.card`'s 14px, -1px) is unchanged and still Login/Setup-only.

The **grid texture** background above remains the opt-in part of this
section - still just the **Login page** and **Setup wizard**, paired as
each other's "first impression" moment. Nowhere else in the app uses the
grid texture by default; every `.card` now carries corner accents, per §4.

---

## 10. Errors & connectivity

- **Error pages** (404, 500, etc.) stay fully themed: the same shell
  (header, dark background, icon set) with a themed message and a way
  back — not a plain generic error page. Low cost since the layout shell
  already exists.
- **Offline banner** — see §6.
- **Rate limits/quotas** (Spotify/Deezer API limits, etc.) are not shown
  as a persistent dashboard. They surface only as context inside the
  error/toast that results when a limit actually blocks an action (e.g.
  "Spotify rate limit hit, retrying in 30s").

---

## 11. Page patterns

### 11.1 Playlists (home/landing view)
List/table only — no card grid, no view toggle. Columns: Playlist Name,
Date Added, Source, Tracks, Duration, Missing Tracks, Schedule, Next Run,
Last Run, `[actions]`. Default sort: newest added first. Uses the
canonical data table (§8.6) in full: sticky header, full-row hover,
always-visible + right-click actions, bulk select with sticky bottom bar,
CSV export, no pagination. Source renders as a small monochrome logo
(the existing brand PNG forced to a white silhouette via
`filter: brightness(0) invert(1)`, not the brand's own colors — §1.5's
full-color exception is for standalone brand chips like Settings' service
cards, not a dense table column), not the service name as link text —
still a real link out to the source. Narrow columns (everything except
the name) are width-capped to their own content so the name column
absorbs the row's leftover space, rather than every column stretching
evenly on a wide viewport.

Clicking a row **expands it inline** to show a lightweight, read-only
track list preview within the table — it does not navigate away. An
explicit "Edit" action opens the full Editor page (§11.2) for actual
track management. This keeps a quick glance cheap while the full editing
surface stays a deliberate, dedicated view.

### 11.2 Editor
Serves as both the track editor *and* the playlist detail page (cover,
description, stats, schedule live in a header area above the track
table) — there's no separate detail page.
- Row actions always visible, per §8.6.
- Reordering is drag-and-drop via a dedicated drag-handle icon column,
  with keyboard-accessible move-up/move-down buttons as the non-pointer
  fallback.
- Track metadata is edited inline in the row, not in a modal.
- Columns are user-toggleable and drag-reorderable (§8.6). *Backend-
  scoped.*
- Each row gets an inline preview-playback control for the matched
  track. *Backend-scoped: needs streaming-service preview-audio
  integration; see §17.*

### 11.3 Generate Mix
Stays a modal. Advanced options stay progressively disclosed behind a
toggle; quick settings shown by default. Pre-save review shows the mix's
settings summary only, no track-list preview before commit. Template
selection is a card gallery (§8's card conventions), each card showing
name, a short description, and a preview of key parameters (e.g. "Rock ·
High Energy · 50 tracks") — not name-only.

### 11.4 Settings
Navigation moves from the current tabs to a **sidebar sub-nav** — the one
deliberate structural change in this doc; `.tab-buttons` (§8.5) remains
valid for other in-page sub-tabs, just not here anymore. Service
connection cards stay a grid (brand-logo treatment, §1). Destructive
actions (disconnect, delete) sit inline with each setting rather than a
grouped Danger Zone — because there's no grouping to slow a misclick
down, every inline destructive action must confirm through the modal
system (§8.11), no exceptions. Settings **autosave** on change; every
autosaving field needs a visible saved/saving indicator (inline
checkmark-flash or spinner-to-check icon), since there's no Save click to
signal completion — this matters more here than in most forms because
several fields have real side effects (credentials, schedules).

Stored API keys/secrets are **fully masked, with no reveal** — the most
conservative option, appropriate since these are write-only credentials
a user can always regenerate rather than needing to recall.

**User vs. Administration placement** is decided by where the setting's
data actually lives, not by guessing: anything stored per-user
(`user_settings`, a user's own `user_servers`/`oauth_connections` row —
Matching, Mixes, AI Features, Appearance, Sessions, Connected Services,
Library Scan, and the Plex server connection itself) stays in the main
User area, even when a feature *sounds* like it could be an admin
concern (AI Features holds each user's own API key by design, not one
shared instance-wide key — a deliberate choice, not an oversight).
Anything stored instance-wide (`admin_config`, or a query spanning every
user — Statistics, Users, Missing Tracks aggregate, Schedules/background
jobs, Deemix, Lidarr, YouTube, Logs, Reverse Proxy) belongs under
Administration, gated behind `.IsAdmin`. Reverse Proxy Setup was found
sitting in the User area's old "Server Info" section despite being pure
server-deployment documentation (edit `.env`, restart the systemd
service) a non-admin user can't act on — moved into Administration as
its own section; About/version stays in the User area's Server Info on
the reasoning that version info being visible to everyone is normal
convention and genuinely harmless to show.

Settings also gains:
- A text-size control (small/medium/large), see §2.
- An **active sessions/devices list with revoke**. *Backend-scoped: real
  auth-layer work; see §17.*
- App version + changelog link in the Settings footer.

### 11.5 Admin
Dashboard leads with summary stat tiles (users, active syncs, error
count), each with an inline sparkline, plus one larger trend line chart
(§8.10). The log viewer is a live-tailing stream, auto-scrolling as
entries arrive, not a paginated static table. User management is cards
with filters, not a dense table (the one place this doc chooses cards
over the table default, since user records carry more per-item context
than a row wants). Schedules/cron jobs use a calendar/timeline view
rather than a plain next-run list.

### 11.6 Missing Tracks
Table rows, compact density (§8.6). Resolution actions (search
alternative match, remove, ignore) are icon buttons with tooltips, not a
text dropdown, and use icons that actually match the action (a fixed
regression: "Retry matching" briefly shared a search-icon with "Match,"
and "Search Lidarr" was rendering a literal clock icon by copy-paste
mistake). Supports bulk resolution via the same bulk-select +
sticky-bottom-bar pattern used on Playlists (§8.6) — one consistent
bulk-action pattern app-wide rather than a one-off.

Manual matching replaces a raw "type a Plex ratingKey" text field — no
user has a reason to know what that is — with a "Match" (magnifying-glass
icon) button that opens a modal: a search box pre-filled with the track's
own title+artist, live results from the user's Plex library
(debounced, matching §8.12's search-field convention), click a result to
apply it immediately and close the modal. This isn't new backend
plumbing — it's a UI in front of exactly the same
`matching.InsertMatchedTrackIntoPlaylist` + `db.RecordManualMatch` path
the old ratingKey field already called, so a match made this way is
already remembered the same way and already survives a future automated
re-match (`matchOneTrack`, matchengine.go, checks remembered manual
matches before searching fresh) — nothing new to build there, only to
reuse.

When embedded inline under a Playlists-table row (via that row's own
Missing-Tracks-count button, §11.1), the list renders without its
standalone-page heading/collapsible wrapper — the playlist's name is
already visible one row up, so repeating it as a second heading inside a
second collapsible element read as two separate things to open for one
action rather than one.

### 11.7 Cross-Import
Single list, one row per track, with a color-coded confidence badge
**plus** the exact percentage (e.g. a green badge showing "94%"). The
overall flow (and the Setup wizard) shows a visible step indicator.

### 11.8 Backup & Restore
Two mechanisms, both present:
- Manual file download/upload, exactly as it works today.
- **New:** automatic tiered snapshots — daily, weekly, monthly, yearly —
  each tier retaining the last 4 (16 snapshots on disk at steady state,
  older ones pruned). Needs a snapshot list UI (tier, timestamp, size,
  restore/download/delete per row) alongside the existing manual
  controls. *Backend-scoped: the scheduling and retention-pruning logic
  is real engineering work, not a UI concern; see §17.*

### 11.9 Sharing
The Share Playlist modal supports link sharing and inviting a specific
user within the instance (extending the existing `share_form.html`/
`shared_with_me.html` pattern) — no QR code.

### 11.10 Notifications
The bell icon stays a dropdown panel, not a dedicated page. See §6 for
the toast → bell animation and §10 for offline/error handling. Feedback
severity maps to persistence: toast for transient confirmations (saved,
deleted), inline banner for anything persistent or actionable (a
validation error, a failed sync needing attention).

### 11.11 Collections (new)
A Kometa-style Plex Collection manager: rule-based ("smart"), manual, or
external-list/chart-driven collections across any Plex library type
(movie/show/music), refreshed on a schedule that reconciles Plex-side
membership. Gated to the account that actually owns the connected Plex
server (`user_servers.is_owner`, set at server-select time from Plex's own
`owned` flag) rather than this app's own admin flag - a Plex Collection is
a per-library-section concept tied to server ownership, not an
instance-admin privilege, and the nav link (mobile drawer only, after
"Generate Mixes") is hidden for non-owners accordingly.

- **List**: the standard DataTable (§8.6), column-filterable (see "Column
  filters" below) but without bulk-select/CSV export - no bulk operation
  across *collection definitions* exists (bulk actions do exist one level
  down, inside a collection's own Missing Films/TV Shows expando - see
  below). Columns: Name, Server (only shown once an account has more than
  one linked server), Library, Builder, Rules/Items summary, Sync Mode,
  Missing, Schedule (reuses the Playlists table's `.badge-button`
  frequency-pill pattern), row actions.
  - **Shows every real Plex collection, not just this app's own rows**:
    `collections.go`'s `unmanagedCollectionViews` sweeps every library on
    every server the user owns (`GetLibraries` + `GetCollections`) and
    merges in any collection with no matching `collections` DB row -
    dimmed (`.collection-row--unmanaged`), Builder badge reads "Smart" or
    "List" from Plex's own native smart-collection flag
    (`plex.Collection.Smart`) instead of this app's `BuilderType`, Rules/
    Items shows a plain item count, and the only action available is
    "Delete from Plex" (`DELETE /collections/unmanaged?serverId=&plexId=`,
    since there's no DB row to key off and nothing local to edit/refresh).
    This is a real cost, not a free read: every list render now does a
    live `GetLibraries`/`GetCollections` sweep across every owned
    server/library (~0.45s measured against a 921-collection/8-library
    account) - accepted as the price of the feature actually asked for
    ("show ALL collections on the Plex server"), not optimized away with a
    cache in this pass.
  - **Builder badge for `external_list` rows is a link**, not just a
    label - `collections.go`'s `sourceLinkFor` reconstructs the actual
    TMDb/IMDb/TVDb/Letterboxd list/chart URL from the collection's stored
    `ExternalListSource` (Provider + Mode + ListID) so a click opens the
    real source page in a new tab.
  - **Row-click item preview**: clicking anywhere on a managed row (not on
    an actual control - `isRowActionTarget`, `data-table.js`) expands a
    read-only Title/Year list of everything *currently* in the collection
    on Plex, exactly mirroring the Playlists table's row-click track
    preview (§11.1) - `toggleCollectionPreview` (`collections.html`, not
    `collection_list.html`, since that fragment itself reloads on every
    filter/sort/refresh and would redeclare the function each time) lazy-
    loads `GET /collections/{id}/items` into a hidden sibling `<tr>`
    (`.missing-row.preview-row`/`.is-open` - same collapsible-row mechanism
    as Missing below, distinct id). This is a **live** `GET
    /library/collections/{ratingKey}/items` call against Plex each time,
    not anything cached locally, so it always reflects actual current
    membership rather than this app's last-refreshed idea of it - unlike
    the Missing column below, which is exactly the opposite (what refresh
    couldn't match, not what's actually in the collection). Unmanaged rows
    have no click handler wired up (nothing local to key the request off
    of), matching every other row action's managed-only scope here.
    Reuses `.data-table` for the nested table itself (padding/border/hover/
    sticky-thead, all inherited) rather than a bespoke mini-table style -
    user feedback that a from-scratch look read as a disjointed sub-widget
    bolted onto the row rather than part of the same table, same reasoning
    `missing_list.html`'s own nested `.missing-table.data-table` already
    followed. Bounded to `max-height:340px` with its own scroll rather than
    growing the page to match a 193-item collection. A chevron in the Name
    cell (`.collection-preview-toggle`) flips 180° via a plain `tr.is-open`
    class `toggleCollectionPreview` sets on the row itself (a second toggle
    alongside the preview `<tr>`'s own `.is-open`, since they're sibling
    elements with nothing to inherit state from) - user feedback that
    nothing signalled a row was expandable, or which one was currently
    open, the same problem the Missing column's own `.expando-caret`
    already solves for that narrower case.
  - **"Refresh" row action auto-refreshes the table too**: `POST
    /collections/{id}/run` runs on `h.Queue` (a background job, DESIGN.md
    §11.10) rather than blocking the request, so its HTTP response can't
    itself carry updated row data - the job may still be running when it
    returns. `layout.html`'s existing toast MutationObserver (already
    watching `#notifications` for a job reaching "success", to fire a
    toast) now also dispatches a plain `collections-changed` CustomEvent on
    `document.body` at that same moment, reusing the exact event
    `collection_list.html`'s own `hx-trigger="load, collections-changed
    from:body"` already listens for (originally only fired by the schedule
    modal's own mutations, `schedules.go`) - so a completed refresh's
    updated Missing count/Last Run/membership actually show up without a
    manual page reload. Fires on every successful background job, not just
    collection refreshes specifically; harmless everywhere else since
    `#collections-list` simply doesn't exist outside the Collections page.
  - **External-list order is preserved into Plex**: a source like IMDb's
    Top 250 or a TMDb "popular"/"trending" chart is itself a ranking, not
    an arbitrary set. `resolveExternalListTargets`'s `targetKeys` already
    preserves that source order end to end (a plain ordered slice the whole
    way through matching, never a map) - the part that *didn't* just
    naturally follow from that, confirmed live against a real server: Plex's
    bulk `uri=`-batch add (`AddToCollection`/`CreateCollection`, what
    `reconcileCollectionMembership` calls) does **not** preserve the order
    items were listed in - a fresh 193-item add came back sorted by release
    date regardless of request order. The only thing that actually works is
    Plex's own manual-drag-reorder endpoint, one call per item (`PUT
    /library/collections/{id}/items/{itemId}/move?after={prevId}`) -
    `plex.Client.ReorderCollection` chains every `targetKeys` entry through
    it after `reconcileCollectionMembership`, alongside
    `SetCollectionCustomOrder` (switches the collection's own display mode
    to "Custom" - needs `type=18` alongside `sort=2` or Plex 400s it, the
    field-edit shape `SetCollectionSortTitle` uses 400s here too). Skips the
    per-item moves entirely when the current order already matches (cheap
    comparison first), so a refresh that didn't change the ranking doesn't
    re-pay for `len(targetKeys)` calls every time - only a first build or an
    actual reshuffle does. Verified end-to-end against the real IMDb Top
    250 collection (193 items, ~40s for a full reorder) - result matched
    IMDb's real ranking exactly, #1 The Shawshank Redemption through #193.
    Default behavior for every `external_list` collection, not an opt-in
    toggle; generalizes to any ranked source (TMDb charts included), not
    just IMDb.
  - **Missing column**: an inline expando exactly mirroring the Playlists
    table's Missing Tracks column (§11.1) - a warn-colored count badge
    with a chevron (`.expando-toggle`/`.expando-caret`, shared CSS in
    `chrome-interactions.css`) that flips 180° when open, lazy-loading
    `GET /collections/{id}/missing` into a hidden sibling `<tr>`
    (`.missing-row`/`.is-open`) rather than a separate page section. Only
    ever populated for `external_list` collections (see below); smart/
    manual rows and unmanaged rows always show "—". The expando itself has
    its own batch-select (`.bulk-select-scope`/`.bulk-actions-bar`, same
    generic `chrome-interactions.css` mechanism the Playlists page and the
    standalone Missing Tracks page use) with a "Dismiss Selected" bulk
    action (`POST /collections/missing/bulk-remove?collectionId=`).
  - **Column filters**: Name (search), Library (dropdown of every distinct
    library a visible row belongs to), Builder (Smart / Manual /
    External List/Chart / "Not tracked by Playlist Lab"), Server (when
    multi-server), Missing (any/has/none), Schedule (any/on/off), Last Run
    (any/succeeded/failed/never), Next Run (any/due/upcoming/not
    scheduled), and Date Added (from/to range) - the last five mirror the
    Playlists table's own Missing/Schedule/Last Run/Next Run/Date Added
    filters field-for-field (`collectionFilter` gained the same
    `Missing`/`Schedule`/`NextRun`/`LastRun`/`AddedAfter`/`AddedBefore`
    fields `rowFilter` already had, including reusing
    `db.GetLatestExecutionStatuses` for `LastRunStatus` - Collections and
    Playlists schedules share the same `schedule_executions` table). All
    columns reuse the Playlists table's exact `<details class="th-filter">`
    popover + query-string round-trip technique. One real difference from
    Playlists: Collections lazily loads its data via `GET
    /collections/list` rather than rendering everything into one full-page
    response, so its filter links can't reuse `playlists.go`'s `queryState`
    (hardcoded to link back to `/`) - `collectionsQueryState` is the same
    type adapted to link to `/collections/list` instead, and filter options
    are `hx-get` buttons rather than plain `<a href>`s (there's no
    graceful non-JS fallback route for a bare fragment endpoint). The
    shared visual CSS for this popover pattern (`.th-with-filter`,
    `.th-filter-btn`, `.th-filter-menu`, `.th-filter-option`) was moved out
    of `PlaylistsPage.css` into the already-global `chrome-interactions.css`
    once Collections needed it too, rather than being duplicated per page.
  - **Mobile (<768px)**: same table→card-list swap as Playlists (§11.1) -
    `.collections-cards` + a `.collections-mobile-search` search box +
    a `.collections-sortbar` sort `<select>`/direction toggle replace the
    desktop table (whose per-column `.th-filter` popovers are hidden below
    768px along with it), rather than leaving phones with a horizontally-
    scrolling table and no way to search. The sort `<select>`'s options are
    full precomputed `/collections/list?...` URLs (fired via `htmx.ajax`,
    `pushUrl: true`) rather than a bare sort key, since Collections'
    fragment-based filtering has no plain-navigation fallback to lean on
    the way Playlists' `location.href=` does. Row actions
    (edit/refresh/delete) are a single `collectionRowActions` named
    template shared by the desktop table and the cards, mirroring
    `home.html`'s `playlistRowActions` split, so the two views can't drift.
- **Create/edit modal**: reuses `.advanced-mix-*` modal chrome (same as
  `schedule_form.html`/`mix_advanced_form.html`). The Library picker
  auto-selects a matching library when the create is prefilled from
  something with a known media type - Browse Presets' Awards/Holiday/
  Franchise entries (all movie-only, `collectionPreset.MediaType`) and a
  TMDb-collection search result (`collection_preset_search.go`; a Trakt
  list result has no reliable type, so stays unpicked) - by Plex's own
  library `type` (`movie`/`show`), not by matching the library's *name*
  against "Movies"/"Films"/etc., which would break the moment someone
  named it something else (`newForm`'s `wantedPlexLibraryType`). First
  matching library wins if more than one exists; still a plain `<select>`
  either way, not a hard lock-in. Library and builder type
  (Smart / Manual / External List/Chart) are only choosable at creation -
  immutable afterward, to avoid ambiguous mid-life transitions between
  membership models. Smart rules are a flat, repeatable (field, value) row
  list, AND-combined only - no OR/nesting, matching what's realistically
  buildable as a plain form (see `collections.go`'s package doc for the
  full field list). Manual collections manage their item list via a
  debounced library search (reusing the Missing Tracks Match modal's
  click-to-add-immediately pattern) rather than a multi-select staged for
  a single submit.
  - **Duplicate-name prevention for Smart collections**: creating a new
    Smart collection whose name case-insensitively matches *any* existing
    collection already visible in that library - managed or unmanaged,
    Playlist-Lab-created or Kometa/PMM-created - is rejected (409) with an
    explicit error, rather than silently creating a second, redundant
    collection. This matters most for actor/studio-style collections
    (Kometa names these exactly after the actor/studio, e.g. "Rachel
    McAdams"), which are the case most likely to get accidentally
    recreated under a fresh source. Deliberately scoped to a same-library
    title match rather than decoding Plex's own smart-collection filter
    string (the `content` field on a fetched-by-id collection, e.g.
    `.../all?type=1&sort=...&actor=358036...`) - a name collision is a
    reliable, cheap proxy for "same selection" for the concrete
    actor/studio case that prompted this, without needing to resolve
    Plex's internal actor/studio IDs back to names.
  - **Poster**: once a collection has been refreshed at least once (has a
    real `plex_collection_id`), its edit modal gets a "Set Poster" field
    (image URL + button, `POST /collections/{id}/poster`) that reuses
    `plex.Client.UploadPlaylistPoster` verbatim - `POST
    /library/metadata/{ratingKey}/posters` is a generic Plex
    metadata-object endpoint despite that method's playlist-flavored name,
    not worth a duplicated method for this one extra caller.
- **External list/chart builder** (`builder_type = 'external_list'`):
  membership comes from a public list or chart on TMDb, IMDb, TVDb, or
  Letterboxd instead of an in-app rule set. Config (`Provider`, `Mode`,
  `ListID`, `Limit`) is stored as JSON in the same `collections.rules`
  column Smart rules use (`db.ExternalListSource`/
  `ParsedExternalListSource`) rather than a new column. Matching works the
  same way regardless of provider: `scheduler.resolveExternalListTargets`
  fetches the provider's items (each normalized into a
  `medialist.Item{GuidKey, MediaType, Title, Year}`), fetches the target
  library once with `plex.Client.GetLibraryItemsWithGuids` (paginated -
  `includeGuids=1`, which Plex omits by default, confirmed live), and
  matches by building a `map[GuidKey]ratingKey` from the library's own
  `Guid` array (`tmdb://`, `imdb://`, `tvdb://` prefixes) - confirmed live
  that Plex's own `guid=` search filter only matches its *internal*
  `plex://...` guid, not these external ids, so matching has to happen
  client-side instead of via a Plex query param.
  - **TMDb**: official v3 API, needs a free API key (Settings >
    Administration > TMDb). Supports a public list (`GetListItems`) or a
    Popular/Top Rated chart (`GetChart`, capped at 5 pages/≈100 items).
    Saving the key immediately tests it against TMDb's own
    `/authentication` endpoint (`tmdb.Client.ValidateKey`) and surfaces
    TMDb's real rejection message (e.g. "Invalid API key: You must be
    granted a valid key.") rather than a bare status code, so a bad key is
    caught at save time instead of on the next silent scheduled refresh.
  - **IMDb**: no API key, no official API at all - IMDb's list CSV-export
    endpoint is behind an anonymous-request AWS WAF challenge (confirmed
    live), so `internal/services/imdb` reuses this app's existing
    headless-browser scraper (`internal/services/browser`, already used
    for Apple Music/Spotify) to read a
    `<script type="application/ld+json">` `ItemList` block every IMDb list
    page and the `/chart/top/` (Top 250) page both embed - confirmed live
    on both page shapes, far more stable than scraping the visible DOM.
  - **TVDb**: v4 API (login + `/lists/{id}/extended`), needs a free API
    key (Settings > Administration > TVDb). List-only, no chart mode.
    *Not live-verified end to end* - no TVDb API key was available to test
    against during development; the auth/list endpoints were confirmed
    reachable with the expected error shapes, but a real list response
    was never seen, so a field-name mismatch in `tvdb.decodeListResponse`
    is the most likely place a real key would first surface a bug.
  - **Letterboxd**: no API, no id Plex understands natively - a two-hop
    scrape (also via the headless-browser scraper): the list page's poster
    grid links to each film's own page, and each film's page embeds a
    direct link to its TMDb entry, which is what actually gets matched.
    Paginates through `/page/2/`, `/page/3/`, ... up to 10 pages or until
    `Limit` is satisfied. This is N+1 page loads (list pages, then one per
    film) against the app's single shared headless-Chrome instance - a
    100-item list is on the order of minutes per refresh, not seconds;
    bounded by `Limit` (small by default) rather than fetched
    concurrently, since the shared browser allocator isn't built for many
    parallel tabs against one site. Upgrade path if this matters: cache
    each film-page resolution by slug (a film's TMDb id never changes) so
    a second collection referencing the same film doesn't re-scrape it.
- **Missing Films/TV Shows**: the movie/show analog of Missing Tracks
  (§11.1/§11.6) - any external-list/chart entry that doesn't match
  anything in the target library is persisted (`missing_collection_items`,
  keyed by `guid_key` so it works identically across all four providers)
  instead of silently dropped, replaced wholesale on every refresh
  (`db.ReplaceMissingCollectionItems`, same full-replace-per-run
  convention and internal de-duplication-by-key as `AddMissingTracks`) so
  a still-missing title's timestamp refreshes rather than duplicating, and
  a no-longer-missing one just disappears. Movie/show level only: a
  missing "tv" entry means the whole series isn't in the library at all,
  never an individual episode - detecting a *partially*-owned show would
  need pulling each season's episode list from the provider and diffing
  against Plex's own episodes for that show, a meaningfully larger,
  separate piece of work that was explicitly scoped out.
- **Scheduling**: reuses `schedule_form.html`/`handlers/schedules.go`
  unchanged, generalized to accept a `collectionId` alongside `playlistId`.
  Since the Collections page isn't `/`, a schedule save can't reuse the
  Playlists page's `HX-Redirect: /` refresh trick - it fires an
  `HX-Trigger: collections-changed` header instead, which the page's own
  `#collections-list` panel listens for for an in-place re-fetch.
  - The modal's "When the source changes" Replace/Accumulate choice only
    ever applied to playlist-linked schedules (`executePlaylistRefresh`
    reads `Config.UpdateMode`; `executeCollectionRefresh`/
    `RefreshCollection` never do - a Collection's own Sync/Add-only mode,
    set on the collection itself, is what actually controls its refresh
    behavior) - it's now hidden entirely (`{{if not .CollectionID}}`) for
    collection-linked schedules instead of showing a playlist-worded
    ("the playlist mirrors the source... Tracks that drop out...") choice
    that did nothing.
- **Investigated and not built: reordering collections on the Plex
  server.** Plex does expose an `index` field on collection metadata that
  looks like the custom-order key its own web UI's drag-and-drop writes,
  but there is no confirmed, working API to *set* it - two candidate
  endpoints (`PUT /library/metadata/{id}/move?after=`, modeled on this
  app's own `MovePlaylistItem`; and the "Move Hub" endpoint,
  `PUT /hubs/sections/{id}/manage/move/{id}?after=`) were tested live
  against two real collections and both returned 404 with nothing written,
  matching the Plex community's own reports that programmatic collection
  reordering is unreliable/undocumented even for Plex's intended use case.
  Not pursued further rather than guessing further writes against a real,
  already-curated 900+-collection library. If revisited, start by
  confirming a real working endpoint exists (e.g. by packet-capturing
  Plex's own web app performing a manual collection drag) before writing
  any matching/auto-sort logic against it.

---

## 12. Onboarding

- **Login page:** illustrated hero background (§9), no other decoration.
- **Setup wizard:** the same hero treatment as Login — they're both
  first-impression moments.
- **First-run tour:** a short, lightweight tooltip walkthrough (3–4
  steps) on first login, beyond the setup wizard itself. *Backend-scoped:
  needs a persisted "has seen tour" flag per user; see §17.*
- No public marketing/landing page — see §3.

---

## 13. Iconography, branding & entry points

- Logo: full wordmark on desktop, icon-only under the mobile breakpoint
  (formalizes current partial behavior).
- Favicon: same mark, re-exported in the new electric blue/cyan palette
  (§1).

---

## 14. Accessibility & personalization

- Dark theme only — no light-mode variant. The brief's identity is built
  around the dark control-room aesthetic; a light mode would need a
  near-total parallel palette for limited benefit.
- No separate high-contrast theme — the single dark theme is tuned to
  meet WCAG AA contrast on its own.
- `prefers-reduced-motion` not handled (§6, deliberate).
- Text-size setting (§2, §11.4).
- Blue/cyan accent (§1) is fixed, not user-customizable.
- Firefox gets matching thin dark scrollbars via
  `scrollbar-color: var(--border-light) transparent; scrollbar-width: thin;`
  alongside the existing `::-webkit-scrollbar` rules — near-zero cost,
  closes an existing Chromium/Safari-only gap.
- Touch targets stay 44px minimum on mobile, 48px under
  `(hover: none) and (pointer: coarse)` — unchanged, non-negotiable.

---

## 15. Global keyboard/interaction scope

No global keyboard-shortcut layer (e.g. "n" for new mix, "/" to focus a
filter) for now — consistent with skipping global search. Esc-to-close
and click-outside-to-dismiss on the modal system (§8.11) are the only
keyboard/interaction conventions guaranteed app-wide.

---

## 16. What stays exactly as-is

The base dark-navy scale (§1), spacing/radius/breakpoints (§3),
typography scale sizes/weights (§2), semantic status colors (§1), brand
logo treatment (§1), and base motion timing (§6) are **not changing**.
Everything else in this document is either a direct extension of those
foundations or one of the specific, individually-confirmed changes listed
above (primary/accent hue, Settings navigation, glow scope, and the page
patterns in §11).

---

## 17. Backend-scoped work

These are UI/UX decisions in this document whose implementation reaches
past templates and CSS into real backend engineering. Each needs its own
scoping pass before implementation, not just a design review:

- **Editor column preferences** — persisted per-user show/hide + order
  for track-table columns (§8.6, §11.2).
- **CSV export** — export endpoints for Playlists, Missing Tracks, and
  admin logs (§8.6).
- **Tiered automatic backups** — a scheduler generating daily/weekly/
  monthly/yearly snapshots with a 4-per-tier retention/pruning job
  (§11.8).
- **Live data updates over WebSocket** — server-side broadcast on state
  change, delivered via htmx's WebSocket extension rather than polling
  (§6).
- **Inline track preview playback** — streaming-service preview-audio
  integration per row in the Editor (§11.2).
- **Active sessions/devices list with revoke** — real auth-layer session
  tracking and revocation (§11.4).
- **First-run tour persistence** — a per-user "has seen tour" flag
  (§12).
- **Rate-limit-aware error messaging** — service adapters need to surface
  rate-limit failures in a form the UI can show contextually (§10),
  rather than a generic failure.
- **TanStack Charts integration** — this app has no JS framework today;
  adopting TanStack Charts (§8.10) means scoping in a framework adapter
  (or its standalone core, if viable) for the admin dashboard's charts
  specifically, plus a build step to bundle it if one doesn't already
  exist.
