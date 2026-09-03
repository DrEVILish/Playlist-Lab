import { useState } from 'react';
import { CrossImportPage } from '../../pages/CrossImportPage';
import { Modal, modalCloseButtonStyle } from '../Modal';
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
 * from the unified playlist control panel. Every file format here (and only
 * these formats) is also importable via the Import page's "File" source -
 * see services/scrapers.ts's parseM3UFile/parseCSVFile/parsePLSFile/parseXSPFFile.
 * YouTube is a separate export path (an OAuth-driven match/review wizard,
 * not a file download) reusing CrossImportPage with the playlist preselected.
 */
// Above the server's EXPORT_QUEUE_THRESHOLD, POST /export responds 202 with
// a jobId instead of the file, and the file is fetched separately once the
// bell notification (or this poll) reports it's ready - see
// routes/export.ts. Below the threshold the response IS the file, same as
// always.
const EXPORT_POLL_INTERVAL_MS = 3000;
const EXPORT_POLL_TIMEOUT_MS = 10 * 60 * 1000;

function triggerDownload(blob: Blob, contentDisposition: string | null, fallbackFilename: string): void {
  const filenameMatch = contentDisposition?.match(/filename="(.+)"/);
  const filename = filenameMatch ? filenameMatch[1] : fallbackFilename;

  const url = window.URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  window.URL.revokeObjectURL(url);
  document.body.removeChild(a);
}

export function ExportModal({ playlistId, playlistName, trackCount, onClose }: { playlistId: string; playlistName: string; trackCount?: number; onClose: () => void }) {
  const [selectedFormat, setSelectedFormat] = useState<ExportFormat>('m3u8');
  const [exporting, setExporting] = useState(false);
  const [exportStatus, setExportStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [showYouTube, setShowYouTube] = useState(false);

  const handleExport = async () => {
    setExporting(true);
    setError(null);
    setExportStatus(null);
    const fallbackFilename = `${playlistName}${FORMATS.find(f => f.id === selectedFormat)?.extension}`;
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

      if (response.status === 202) {
        const { jobId, position } = await response.json();
        setExportStatus(`Large playlist - building the file in the background${position > 0 ? ` (position ${position} in queue)` : ''}...`);
        const downloaded = await pollForExport(jobId);
        triggerDownload(downloaded.blob, downloaded.contentDisposition, fallbackFilename);
        onClose();
        return;
      }

      const blob = await response.blob();
      triggerDownload(blob, response.headers.get('Content-Disposition'), fallbackFilename);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Export failed');
    } finally {
      setExporting(false);
      setExportStatus(null);
    }
  };

  // Polls the queued export's download endpoint (404 until the background
  // job finishes) rather than subscribing to the notification stream from
  // here - the modal only cares about "is the file ready yet", which this
  // answers directly without needing to filter the whole notification feed.
  const pollForExport = async (jobId: string): Promise<{ blob: Blob; contentDisposition: string | null }> => {
    const deadline = Date.now() + EXPORT_POLL_TIMEOUT_MS;
    while (Date.now() < deadline) {
      const response = await fetch(`/api/playlists/export/${jobId}/download`, { credentials: 'include' });
      if (response.ok) {
        return { blob: await response.blob(), contentDisposition: response.headers.get('Content-Disposition') };
      }
      if (response.status !== 404) {
        const data = await response.json().catch(() => ({}));
        throw new Error(data.error?.message || 'Export failed');
      }
      await new Promise(resolve => setTimeout(resolve, EXPORT_POLL_INTERVAL_MS));
    }
    throw new Error('Timed out waiting for the export to finish - check the notification bell');
  };

  if (showYouTube) {
    return (
      <Modal onClose={onClose} contentStyle={{ maxWidth: '95vw', width: '900px', maxHeight: '90vh', overflow: 'auto' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.5rem' }}>
            <button className="btn btn-secondary btn-small" onClick={() => setShowYouTube(false)}>← Back to Export</button>
            <button onClick={onClose} title="Close" style={modalCloseButtonStyle}>✕</button>
          </div>
          <CrossImportPage initialPlaylist={{ id: playlistId, name: playlistName, trackCount: trackCount ?? 0 }} />
      </Modal>
    );
  }

  return (
    <Modal onClose={onClose} contentStyle={{ maxWidth: '480px' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 style={{ margin: 0 }}>Export "{playlistName}"</h2>
          <button onClick={onClose} title="Close" style={modalCloseButtonStyle}>✕</button>
        </div>

        {error && <div className="export-error"><span>{error}</span></div>}
        {exportStatus && <div className="export-error" style={{ color: 'inherit' }}><span>{exportStatus}</span></div>}

        <div className="export-formats-list" role="radiogroup" aria-label="Export format">
          {FORMATS.map((format) => (
            <div
              key={format.id}
              className={`export-format-item ${selectedFormat === format.id ? 'active' : ''}`}
              role="radio"
              aria-checked={selectedFormat === format.id}
              tabIndex={0}
              onClick={() => setSelectedFormat(format.id)}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSelectedFormat(format.id); } }}
            >
              <div className="export-format-badge">{format.name}</div>
              <div className="export-format-info">
                <div className="export-format-title">{format.name}</div>
                <div className="export-format-description">{format.description}</div>
              </div>
            </div>
          ))}
          <div
            className="export-format-item"
            role="button"
            tabIndex={0}
            onClick={() => setShowYouTube(true)}
            onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setShowYouTube(true); } }}
          >
            <div className="export-format-badge">YT</div>
            <div className="export-format-info">
              <div className="export-format-title">YouTube</div>
              <div className="export-format-description">Match and create a playlist on YouTube</div>
            </div>
          </div>
        </div>

        <div className="modal-actions">
          <button className="btn btn-secondary" onClick={onClose} disabled={exporting}>Cancel</button>
          <button className="btn btn-primary" onClick={handleExport} disabled={exporting}>
            {exporting ? 'Exporting...' : 'Export'}
          </button>
        </div>
    </Modal>
  );
}
