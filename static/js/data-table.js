// Shared behavior for the canonical data table (DESIGN.md §8.6), used by
// both the Playlists and Missing Tracks tables so this logic exists once:
//
// - Right-click anywhere on a row opens that row's "..." row-menu (the same
//   hover-revealed .row-actions icons, just forced visible) as a themed
//   context-menu alternative - not a separately-templated floating menu.
//   ponytail: reveal-in-place rather than a cursor-positioned floating
//   panel - much less code, and every action stays a real, keyboard-
//   reachable, already-tabbable button. Upgrade to a floating menu if a
//   pixel-perfect "menu follows cursor" interaction is ever requested.
// - A global helper, isRowActionTarget(), lets each page's row-click
//   handler (e.g. "expand this row's track preview") ignore clicks that
//   landed on an actual action/checkbox instead of the row background.
(function () {
  function closestDataRow(el) {
    return el.closest && el.closest("tr[data-row-menu]");
  }

  document.addEventListener("contextmenu", function (e) {
    var row = closestDataRow(e.target);
    if (!row) return;
    e.preventDefault();
    document.querySelectorAll("tr.row-menu-open").forEach(function (r) {
      if (r !== row) r.classList.remove("row-menu-open");
    });
    row.classList.add("row-menu-open");
  });

  document.addEventListener("click", function (e) {
    var openRow = document.querySelector("tr.row-menu-open");
    // Bug: comparing against closestDataRow(e.target) truthiness alone
    // only closed the open menu when the click landed outside every row -
    // clicking a *different* row's non-action area (not the open one) left
    // the first row's menu stuck open, since that row is also "a data row".
    if (openRow && closestDataRow(e.target) !== openRow) {
      openRow.classList.remove("row-menu-open");
    }
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
    if (!e.target.classList.contains("select-all-in-table")) return;
    var table = e.target.closest("table");
    if (!table) return;
    table.querySelectorAll("input.row-select").forEach(function (cb) {
      cb.checked = e.target.checked;
    });
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
