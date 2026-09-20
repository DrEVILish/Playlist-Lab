// Create mode only (three-way builderType radio: smart/manual/
// external_list, plus the external-list Source/List Type selects) -
// defined outside the once-guard below so it re-runs fresh every time the
// modal opens (see that guard's own comment) - only the event-listener
// attachment needs to happen once, this sync itself must happen on every
// open since the server always renders every provider's options and the
// DOM is freshly recreated each time.
function syncTMDbModeVisibility() {
  // Create mode has a real <select name="externalProvider">; edit mode
  // (provider is immutable after creation) has a hidden input with the
  // same name instead - querying by name finds whichever is present, so
  // this reads the collection's actual provider in both modes instead of
  // silently falling back to a hardcoded guess.
  var providerField = document.querySelector('[name="externalProvider"]');
  var provider = providerField ? providerField.value : 'tmdb';
  var modeSelect = document.getElementById('collection-tmdb-mode');
  var hasModes = provider === 'tmdb' || provider === 'imdb';
  var modeGroup = document.querySelector('.collection-tmdb-mode-group');
  if (modeGroup) modeGroup.classList.toggle('is-hidden', !hasModes);

  if (modeSelect) {
    // Hide every mode option that belongs to a different provider (an
    // option with no data-provider, i.e. "list", is always shown), and
    // fall back to "list" if the currently-selected one just got hidden.
    var options = modeSelect.querySelectorAll('option');
    var selectedStillValid = false;
    options.forEach(function (opt) {
      var forProvider = opt.getAttribute('data-provider');
      var visible = !forProvider || forProvider === provider;
      opt.hidden = !visible;
      if (opt.selected && visible) selectedStillValid = true;
    });
    if (!selectedStillValid) modeSelect.value = 'list';
  }

  var listIdField = document.querySelector('.collection-tmdb-list-id');
  var mode = modeSelect ? modeSelect.value : 'list';
  if (listIdField) {
    listIdField.classList.toggle('is-hidden', mode !== 'list' && mode !== 'collection');
    var label = listIdField.querySelector('label');
    if (label) label.textContent = mode === 'collection' ? 'Collection ID or URL' : 'List ID or URL';
  }
}
syncTMDbModeVisibility();

// Add/remove rows for a Collection's smart-builder rule list (DESIGN.md
// §11.11). Delegated on document so it keeps working for rows added after
// this script first ran. The modal's <script src> re-runs this file every
// time the modal reopens (htmx executes script tags in swapped content),
// so binding is guarded to run once - otherwise each reopen would stack
// another document-level listener and every click would fire N times.
if (!window.__collectionFormJSBound) {
  window.__collectionFormJSBound = true;
  document.addEventListener('click', function (e) {
    if (e.target.closest('[data-add-rule]')) {
      var container = document.getElementById('collection-rules');
      var tmpl = document.getElementById('collection-rule-row-template');
      if (container && tmpl) {
        container.appendChild(tmpl.content.cloneNode(true));
      }
    }
    var removeBtn = e.target.closest('[data-remove-rule]');
    if (removeBtn) {
      var row = removeBtn.closest('.collection-rule-row');
      if (row) row.remove();
    }
  });

  document.addEventListener('change', function (e) {
    if (e.target.name === 'builderType') {
      document.querySelectorAll('.collection-smart-fields').forEach(function (el) {
        el.classList.toggle('is-hidden', e.target.value !== 'smart');
      });
      document.querySelectorAll('.collection-tmdb-fields').forEach(function (el) {
        el.classList.toggle('is-hidden', e.target.value !== 'external_list');
      });
    }
    if (e.target.id === 'collection-external-provider' || e.target.id === 'collection-tmdb-mode') {
      syncTMDbModeVisibility();
    }
  });
}
