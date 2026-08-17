import { useState } from 'react';
import '../../pages/ExportPlaylistsPage.css';

type ExportFormat = 'm3u' | 'm3u8' | 'pls' | 'xspf' | 'csv' | 'txt';

interface FormatOption {
  id: ExportFormat;
  name: string;
  description: string;
  extension: string;
}

const FORMATS: FormatOption[] = [
  { id: 'm3u', name: 'M3U', description: 'Standard format - compatible with most players', extension: '.m3u' },
  { id: 'm3u8', name: 'M3U8', description: 'UTF-8 encoded - best for international characters', extension: '.m3u8' },
  { id: 'pls', name: 'PLS', description: 'Winamp format - compatible with portable players', extension: '.pls' },
  { id: 'xspf', name: 'XSPF', description: 'XML format - includes full metadata', extension: '.xspf' },
  { id: 'csv', name: 'CSV', description: 'Spreadsheet format - for databases', extension: '.csv' },
  { id: 'txt', name: 'TXT', description: 'Plain "Artist - Title" list - for sharing/notes', extension: '.txt' },
];

/**
 * Export-to-file dialog for a single playlist. Extracted from the former
 * standalone Export Playlists page so it can be launched as a row action
 * from the unified playlist control panel. Every format here (and only
 * these formats) is also importable via the Import page's "File" source -
 * see services/scrapers.ts's parseM3UFile/parseCSVFile/parsePLSFile/parseXSPFFile.
 */
export function ExportModal({ playlistId, playlistName, onClose }: { playlistId: string; playlistName: string; onClose: () => void }) {
  const [selectedFormat, setSelectedFormat] = useState<ExportFormat>('m3u8');
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleExport = async () => {
    setExporting(true);
    setError(null);
    try {
      const response = await fetch('/api/playlists/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ playlistId, format: selectedFormat, pathType: 'absolute' }),
      });

      if (!response.ok) {
        const data = await response.json();
        throw new Error(data.error || 'Export failed');
      }

      const blob = await response.blob();
      const contentDisposition = response.headers.get('Content-Disposition');
      const filenameMatch = contentDisposition?.match(/filename="(.+)"/);
      const filename = filenameMatch ? filenameMatch[1] : `${playlistName}${FORMATS.find(f => f.id === selectedFormat)?.extension}`;

      const url = window.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      window.URL.revokeObjectURL(url);
      document.body.removeChild(a);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Export failed');
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '480px' }}>
        <h2>Export "{playlistName}"</h2>

        {error && <div className="export-error"><span>{error}</span></div>}

        <div className="export-formats-list">
          {FORMATS.map((format) => (
            <div
              key={format.id}
              className={`export-format-item ${selectedFormat === format.id ? 'active' : ''}`}
              onClick={() => setSelectedFormat(format.id)}
            >
              <div className="export-format-badge">{format.name}</div>
              <div className="export-format-info">
                <div className="export-format-title">{format.name}</div>
                <div className="export-format-description">{format.description}</div>
              </div>
            </div>
          ))}
        </div>

        <div className="modal-actions">
          <button className="btn btn-secondary" onClick={onClose} disabled={exporting}>Cancel</button>
          <button className="btn btn-primary" onClick={handleExport} disabled={exporting}>
            {exporting ? 'Exporting...' : 'Export'}
          </button>
        </div>
      </div>
    </div>
  );
}
