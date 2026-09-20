// Keeps the Title Format field's live preview (dynamic_collection_form.html)
// in sync as the user types or changes Type - mirrors the example words in
// internal/handlers/templates.go's facetExampleWords (a genre set previews
// against "Action", a decade set against "1990s", etc.) so what's shown
// actually looks like something that type of set would generate, not one
// generic word regardless of type. Delegated + guarded the same way
// collection-form.js is, so a reopened modal doesn't stack listeners.
var dynamicCollectionExampleWords = {
  genre: 'Action', decade: '1990s', year: '1999', content_rating: 'PG-13',
  studio: 'A24', actor: 'Tom Hanks', director: 'Christopher Nolan', writer: 'Aaron Sorkin', mood: 'Chill', style: 'Acoustic',
};

function updateDynamicCollectionTitlePreview() {
  var input = document.getElementById('dynamic-collection-title-format');
  var preview = document.getElementById('dynamic-collection-title-preview');
  if (!input || !preview) return;
  var typeSelect = document.getElementById('dynamic-collection-type');
  var facetType = typeSelect ? typeSelect.value : '';
  var example = dynamicCollectionExampleWords[facetType] || 'Action';
  preview.textContent = input.value.includes('<<key_name>>')
    ? input.value.replace('<<key_name>>', example)
    : (input.value || '—');
}

if (!window.__dynamicCollectionFormJSBound) {
  window.__dynamicCollectionFormJSBound = true;
  document.addEventListener('input', function (e) {
    if (e.target.id === 'dynamic-collection-title-format') updateDynamicCollectionTitlePreview();
  });
  document.addEventListener('change', function (e) {
    if (e.target.id === 'dynamic-collection-type') updateDynamicCollectionTitlePreview();
  });
}
