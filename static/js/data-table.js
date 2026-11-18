// Shared behavior for the canonical data table (DESIGN.md §8.6), used by
// both the Playlists and Missing Tracks tables so this logic exists once:
//
// - Right-click anywhere on a row opens that row's "⋯" menu: a popover
//   ([data-row-menu-trigger] points at it), or a v4 <details class="row-menu">.
// - A global helper, isRowActionTarget(), lets each page's row-click
//   handler (e.g. "expand this row's track preview") ignore clicks that
//   landed on an actual action/checkbox instead of the row background.
(function () {
  document.addEventListener("contextmenu", function (e) {
    var row = e.target.closest && e.target.closest("tr[data-row-menu]");
    if (!row || isRowActionTarget(e)) return;
    var trigger = row.querySelector("[data-row-menu-trigger]");
    var pop = trigger && document.getElementById(trigger.getAttribute("popovertarget"));
    if (pop && pop.showPopover) {
      e.preventDefault();
      try { pop.showPopover({ source: trigger }); } catch (err) { pop.showPopover(); }
      return;
    }
    var menu = row.querySelector("details.row-menu");
    if (!menu) return;
    e.preventDefault();
    menu.open = true;
  });

  // Sortable headers: the th[aria-sort] itself is the click target
  // (CONTRACT.md "Tables"). A data-sort-href header navigates; one carrying
  // hx-get is handled by htmx's own click trigger. Enter/Space do the same
  // for keyboard users. Clicks on a header's filter dropdown are ignored.
  document.addEventListener("click", function (e) {
    var th = e.target.closest && e.target.closest("th[data-sort-href]");
    if (th && !e.target.closest("details, button")) location.href = th.dataset.sortHref;
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" && e.key !== " ") return;
    var th = e.target.matches && e.target.matches("th[aria-sort], th[data-sort-href], th[hx-get]") && e.target;
    if (th) { e.preventDefault(); th.click(); }
  });

  window.isRowActionTarget = function (evt) {
    return !!evt.target.closest(
      "a, button, input, select, details, summary, label"
    );
  };

  // Per-table "select all" checkbox (class="select-all-in-table") - used by
  // the Missing Tracks page, which (unlike the single-table Playlists page)
  // can render several small per-playlist tables on one page, each needing
  // its own independent select-all rather than one shared to the whole
  // document.
  document.addEventListener("change", function (e) {
    if (e.target.classList.contains("select-all-in-table")) {
      var table = e.target.closest("table");
      if (table) table.querySelectorAll("input.row-select").forEach(function (cb) {
        cb.checked = e.target.checked;
      });
    }
    // A scope's bulk-action bar ([data-bulk-bar]) only shows while at
    // least one of its rows is checked.
    var scope = e.target.closest(".bulk-select-scope");
    var bar = scope && scope.querySelector("[data-bulk-bar]");
    if (bar) bar.hidden = !scope.querySelector("input.row-select:checked");
    // A .bulk-bar shows itself (CSS); the app writes its count.
    var count = scope && scope.querySelector("[data-bulk-count]");
    if (count) count.textContent = scope.querySelectorAll("input.row-select:checked").length + " selected";
  });

  // Live-filters a column-header filter popover's own option buttons by
  // typed text (class="th-filter-search") - generic, not tied to any one
  // column, so a future filter with a long option list (Collections'
  // Builder column, DESIGN.md §11.11, is the first with enough options -
  // type + 4 providers - to actually need this) gets it for free just by
  // adding the input.
  document.addEventListener("input", function (e) {
    if (!e.target.classList.contains("th-filter-search")) return;
    var menu = e.target.closest(".th-filter-menu");
    if (!menu) return;
    var query = e.target.value.trim().toLowerCase();
    menu.querySelectorAll(".th-filter-option").forEach(function (opt) {
      opt.style.display = !query || opt.textContent.toLowerCase().includes(query) ? "" : "none";
    });
  });
})();
