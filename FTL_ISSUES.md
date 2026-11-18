# ftl-themes issues found while adopting it in Playlist Lab

Playlist Lab ships no CSS of its own: every page is styled by one
ftl-themes bundle, using the L1 app shell and core component classes. These
are the problems hit doing that, for the ftl-themes maintainer.

- **Upstream version:** `DrEVILish/ftl-themes` `main` (v5.3.0 + Unreleased),
  `dist/themes.json` version `53597a4e1d10`, pinned at `35aaf838`. First filed against `b417e94`
  (v4.1, `d76a646cc68b`).
- **Browser used to verify:** Chromium (headless), at 390px, 1000px and
  1280px.
- **Workarounds** live in this repo; each is marked in code with a comment
  pointing back here. Remove them once the matching fix ships.

## Status after v5.3

| # | Issue | Status |
|---|---|---|
| 1 | `[hidden]` ignored by components | **Fixed in v5.** Workarounds removed. |
| 2 | `.nav-collapse` broken in Chromium 131+ | **Fixed in v5.** The app uses `.nav-collapse` again. |
| 3 | `.modal` shrinks to its content | **Fixed in v5.** Inline `width:100%` removed from all modals. |
| 4 | `.modal-header` not a flex row | **Fixed in v5.** `row`/`push` helpers removed. |
| 5 | Bar buttons and muted text vanish | **Fixed in v5.** |
| 6 | Sticky `th` traps dropdowns | **Fixed in v5.** |
| 7 | Links in `<th>` invisible (lcars) | Open. Workaround unchanged (the `th` is the sort target). |
| 8 | `.scroll` clips header dropdowns | **Resolved by v5 popovers.** Menus and column filters are now `[popover]` (top layer); tables sit in `.table-wrap`. |
| 9 | No inline `.input-group` | Open. |
| 10 | `.nav-item` wraps | **Fixed** (nav items no longer wrap). `&nbsp;` labels removed. |
| 11 | `imac-g3` variants missing from `themes.json` | Open: still no `variants` array in `53597a4e1d10`. |
| 12 | `.panel-header` ignores density | **Fixed in v5** (`--surface-pad`). |
| 13 | Migration tooling/docs | Partly moot: the v4 codemod is retired. Stale count in CONTRACT.md fixed. |
| 14 | Links vanish in a selected table row | **New, open.** See below. |

The original write-ups follow, kept for the record.

Ordered by impact.

---

## 1. `[hidden]` doesn't hide components that set `display`

**Severity:** high. It breaks any show/hide that uses the standard attribute.

The reset has no `[hidden] { display: none }`, so every component that sets
its own `display` ignores the `hidden` attribute. That includes `.alert`
(flex), `.badge` (inline-flex), `.spinner` (inline-block), `.btn`
(inline-flex), `.toolbar`, `.stack`, `.field` and others.

**Repro:**
```html
<div class="alert alert-warning" hidden>still visible</div>
<span class="badge" hidden>1</span>
```
`getComputedStyle(el).display` returns `flex` / `inline-flex`.

**Suggested fix:** add `[hidden] { display: none !important; }` to
`core/reset.css`. Most resets do this; inside `@layer ui` the `!important`
still lets an app opt out.

**Our workaround:** `hidden` goes on a plain wrapper `<div>`/`<span>` around
the component (offline banner, bell badge/spinner, bulk-action bars). Tab
panels and show/hide sections that are themselves `.stack`/`.panel`/`.field`
are toggled through `style.display` from JS instead.

---

## 2. `.nav-collapse` is broken in current Chromium

**Severity:** high. The desktop nav never paints.

The "always laid out above 720px" rule relies on overriding the UA's
`details:not([open]) > :not(summary)` hide. Since Chromium 131, a closed
`<details>` hides its content through
`::details-content { content-visibility: hidden }`, which `display: flex`
cannot override.

**Repro:** the CONTRACT example (`<details class="nav-collapse">` …) at 1280px.
- `getComputedStyle(details, '::details-content').contentVisibility` is
  `"hidden"`.
- `nav.checkVisibility()` is `false`; the nav takes space but never paints.

**Second problem:** `.nav-collapse[open] > .nav { flex-direction: column }`
isn't media-gated. So forcing the details open on desktop with JS (the
obvious workaround) stacks the nav vertically at every width.

**Suggested fix:**
- Inside the `min-width: 721px` block, add
  `.nav-collapse::details-content { content-visibility: visible; display: contents; }`
  (keeping the existing rule for older engines).
- Move the `[open]` column layout inside `max-width: 720px`.

**Our workaround:** we dropped `.nav-collapse` and use a plain
`<nav class="nav scroll">`, which scrolls sideways when it doesn't fit (see
`templates/layout.html`).

---

## 3. `.modal` shrinks to its content

**Severity:** medium. Form modals render narrow and change width as you use
them.

`.modal` and `.modal-sm|-lg|-xl` only set `max-width`. Inside the flex
`.modal-overlay`, the modal is shrink-to-fit:
- A form modal (100%-width inputs) renders at its min-content width (~290px
  for a `modal-lg`).
- Its width jumps when an accordion inside opens.

CONTRACT.md describes the sizes as widths ("`min(48rem, …)`").

**Suggested fix:** use `width: min(32rem, calc(100vw - 2rem))` per size, and
keep `max-width` as is.

**Our workaround:** an inline `style="width:100%"` on every `.modal`. Apart
from ftl's own documented inline APIs (`--columns`, chart colours), this is
the app's only inline layout style.

---

## 4. `.modal-header` isn't a flex row

**Severity:** medium. The close button is misplaced in every modal.

CONTRACT documents `<div class="modal-header">Title <button class="btn-close">`,
but `.modal-header` has no layout:
- `.btn-close` is never end-aligned.
- With a block title (`<h2>`) the close button wraps onto its own line under
  the title.

**Suggested fix:** in `.modal-header`, set
`display: flex; align-items: center; gap: .5rem` and give `.btn-close`
`margin-inline-start: auto`.

**Our workaround:** `class="modal-header row is-items-center"` plus
`class="btn-close push"` on all 18 modals.

---

## 5. Buttons and muted text in `.app-bar` can vanish

**Severity:** medium. The theme lint doesn't check these.

Themes re-point `--nav-brand-fg`/`--nav-item-fg` for their bar, as CONTRACT
requires, but nothing else inside `.app-bar` gets the same treatment. Under
windows95, a `.btn.btn-secondary` in the bar computes to
`color: rgb(0,0,128); background: transparent` over the navy bar, so it's
invisible. `.text-muted` in the bar is close to invisible too.

**Suggested fix:**
- Either add bar-scoped `--btn-*` / `--muted` overrides, or document that only
  `.nav-*` is safe in the bar.
- Extend `scripts/check.py`'s bar-contrast rule to `.btn` variants.

**Our workaround:** Logout is rendered as a `.nav-item`, and the username has
no `.text-muted`.

---

## 6. `.table.is-sticky` headers trap anchored dropdowns

**Severity:** medium. It hits the column-filter pattern directly.

`.table.is-sticky thead th { position: sticky; z-index: 1 }` creates a
stacking context. A `.anchor > .dropdown|.popover` inside a header cell (a
column filter) is stuck at z-index 1. Neighbouring sticky header cells and
later positioned content (e.g. a nested sticky table in an expanded row)
paint over it.

**Suggested fix:** raise the cell while it holds an open surface, e.g.
`.table.is-sticky thead th:has(details[open], :popover-open) { z-index: 3; }`.

**Our workaround:** nested tables aren't sticky.

---

## 7. Links inside `<th>` can disappear (lcars)

**Severity:** low-medium.

An `<a>` in a header cell takes `--link`. In lcars that equals the
`--table-head-bg` orange, so header labels are invisible. A sort link inside
a header is a very common pattern.

**Suggested fix:** `.table thead a { color: inherit; }`, or a
`--table-head-link` token.

**Our workaround:** following CONTRACT's `aria-sort` pattern, the `th` itself
is now the click target:
- `aria-sort` is `ascending` / `descending` on the sorted column and `none`
  on other sortable columns.
- `data-sort-href` or `hx-get` sits on the `th`.
- `static/js/data-table.js` handles keyboard activation.

---

## 8. `.scroll` around a table clips header dropdowns

**Severity:** low-medium.

CONTRACT recommends `.scroll` on inner scroll boxes (e.g. a table wrapper).
On a short table, that wrapper's `overflow: auto` clips any `.anchor`
dropdown hanging below the header. There's no documented way to get a
horizontally scrolling table and header dropdowns together.

**Suggested fix:** CSS can't pair `overflow-x: auto` with
`overflow-y: visible` (the latter computes to `auto`), so a class tweak
won't do it. Render dropdowns and popovers in the top layer (`popover` +
CSS anchor positioning) so no scroll container can clip them, or at least
document the trade-off.

**Our workaround:** no wrapper; `.app-main` (already `overflow: auto`)
scrolls wide tables.

---

## 9. `.input-group` has no inline variant

**Severity:** low.

`.input-group` is `width: 100%`, and `.is-auto` only applies to
`.input`/`.select`. So an input + button pair can't sit inline in a
`.toolbar` next to other controls: every group takes a full line.

**Suggested fix:** `.input-group.is-auto { width: auto; display: inline-flex; }`.

**Our workaround:** `.btn-group` in toolbars, which loses the joined border.

---

## 10. `.nav-item` wraps inside itself

**Severity:** low.

In a constrained bar, multi-word items ("Shared With Me") wrap onto 2–3 lines
inside the item. `.nav-item` sets no `white-space: nowrap` /
`flex-shrink: 0`, and `.nav` has no overflow strategy other than
`.nav-collapse` (see #2).

**Our workaround:** `&nbsp;` in the labels.

---

## 11. `imac-g3` variants missing from `themes.json`

**Severity:** low, but it contradicts the contract.

CONTRACT.md names `themes/imac-g3` as *the* worked example of palette
variants (Bondi/Blueberry/Grape/Tangerine), and `dist/imac-g3.css` does ship
`[data-variant="blueberry|grape|tangerine"]`. Its `dist/themes.json` entry
has no `variants` array, though, so a manifest-driven picker never offers
them.

**Suggested fix:** have `build_manifest.py` pick up imac-g3's variants (the
README table format may differ from the other themes').

---

## 12. `.panel-header` doesn't track `--density`

**Severity:** low.

`.panel` padding is `calc(1rem * var(--density))`, but `.panel-header` uses a
fixed `margin: -1rem -1rem 1rem`. With density ≠ 1 the header no longer sits
flush with the panel edges.

**Suggested fix:** use the same `calc(1rem * var(--density))` in the
negative margin.

---

## 13. Migration tooling and docs (v3 → v4)

**Severity:** low.

- **Self-referencing tokens:** `migrate-v4.py` turns the bridge pattern
  CONTRACT.md recommends ("Adopting ftl-themes in an existing app") into
  self-referencing custom properties whenever an app token shares a bare v4
  name. For example, `--ftl-surface: var(--surface)` becomes
  `--surface: var(--surface)`, a cycle that computes to nothing.
  MIGRATING-v4.md lists the bare-name collisions but doesn't warn about this,
  or show a v4 bridge shape that works. That shape is: scope app defaults
  under `:root:not([data-theme])`, and use `revert-layer` for shared names.
- **`--check` false positive:** `migrate-v4.py --check` flags any `ftl-`
  token as legacy, including app-owned names such as an `ftl-bridge.css`
  file.
- **Stale theme count:** CONTRACT "Token-only bundle" still says
  "26 themes"; 34 ship.

---

## Feature requests (not bugs)

- **Text size as a user display option:** alongside density / motion /
  contrast. We scale the root `font-size` (`<html style="font-size: calc(N *
  100%)">`). That works because no bundle uses `px` font sizes, which is
  worth guaranteeing in the contract and lint.
- **Danger item in `.dropdown`:** `.dropdown-item` has no danger variant;
  `.context-menu-item.is-danger` does. We use `.context-menu` for row menus
  partly for that reason.

## 14. Links inside a selected row can disappear

**Severity:** medium. Any table with row selection and a link column.

`tr.is-selected` (and a row whose first-cell checkbox is checked) sets the
row background and `--row-selected-fg`, but an `<a>` in the row keeps
`--link`. Under `windows95` that is navy link text on the navy selection
bar, so it vanishes; `lcars` drops to orange on lilac.

**Repro:** a `.table` row with `<td><a href="#">Name</a></td>` and a checked
first-column checkbox, in `windows95`.

**Suggested fix:** in core, `.table tr:is(.is-selected, :has(> td:first-child input:checked)) a { color: var(--row-selected-fg, inherit); }`,
and have `check.py` test link contrast on the selected row.

**Our workaround:** the playlist name is plain text (the pencil opens the
editor). The Source column's outbound link is still affected.

## New in v5 adoption (feature requests)

- **`.dropdown` has no divider.** `.context-menu-divider` exists, so the
  account menu uses `.context-menu`; a `.dropdown-divider` (or documenting
  `.context-menu` for general menus) would help.
- **`.page-heading-meta` is for short facts only.** It lays out each child
  as a flex item, so a sentence with an inline link breaks at the link.
  Worth a line in `docs/components/sections.md` ("use a plain `<p>` for a
  description").
- **Bulk-bar count without script.** `.bulk-bar` shows itself from CSS, but
  the app still needs JS for "2 selected"; a CSS-counter recipe
  (`counter-increment` on checked row boxes) would make it script-free.
