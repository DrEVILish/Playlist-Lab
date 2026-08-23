import { useState } from 'react';
import { useApp } from '../../contexts/AppContext';
import type { MissingTrack } from '@playlist-lab/shared';
import { Modal } from '../Modal';
import { useConfirm } from '../../contexts/ConfirmContext';

interface RematchResult {
  ratingKey: string;
  title: string;
  artist: string;
  album: string;
  codec: string;
  bitrate: number;
  duration: number;
  /** The same 0-100 score findBestMatch() computes during a real import
   * (services/matching.ts's scorePlexCandidate), not a re-derived one. */
  matchScore?: number;
  /** Whether this score clears the user's configured minimum match score. */
  matched?: boolean;
}

/**
 * Missing-tracks list (retry/match/remove) for a single playlist, shown
 * inline when a playlist row's "Missing Tracks" badge is expanded in the
 * unified playlist control panel. Extracted from the former standalone
 * Missing Tracks page.
 */
export function MissingTracksPanel({ playlistId, tracks, onChanged }: { playlistId: number; tracks: MissingTrack[]; onChanged: () => void }) {
  const { apiClient } = useApp();
  const confirmDialog = useConfirm();
  const [isRetrying, setIsRetrying] = useState(false);
  const [retryingTrackId, setRetryingTrackId] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [retryResult, setRetryResult] = useState<{ matched: number; remaining: number } | null>(null);

  const [rematchTrack, setRematchTrack] = useState<MissingTrack | null>(null);
  const [rematchQuery, setRematchQuery] = useState('');
  const [rematchResults, setRematchResults] = useState<RematchResult[]>([]);
  const [isSearchingRematch, setIsSearchingRematch] = useState(false);
  const [isClearing, setIsClearing] = useState(false);

  /**
   * Polls the retry-status endpoint (real server-reported progress, not a
   * derived guess) until the current retry chain - including anything
   * queued behind it, since they share one per-user chain server-side -
   * finishes, and reports whether it failed outright. Waits for `active` to
   * appear at least once before treating a null response as "done", since a
   * queued-but-not-yet-started batch has no retry-status entry until the
   * one ahead of it finishes; without that, a track queued behind a large
   * "Retry All" would otherwise look instantly finished.
   */
  const waitForRetryCompletion = async (maxWaitMs = 5 * 60 * 1000): Promise<{ error?: string; timedOut?: boolean }> => {
    const start = Date.now();
    let hasStarted = false;
    while (Date.now() - start < maxWaitMs) {
      await new Promise(resolve => setTimeout(resolve, 1500));
      const { active } = await apiClient.getMissingRetryStatus();
      if (active?.error) return { error: active.error };
      if (active) {
        hasStarted = true;
      } else if (hasStarted) {
        return {};
      }
    }
    return { timedOut: true };
  };

  const handleRetryAll = async () => {
    setIsRetrying(true);
    setError(null);
    setRetryResult(null);
    try {
      const initialCount = tracks.length;
      const response = await apiClient.retryMissingTracks(playlistId);
      if (!response.started) {
        setError(response.message);
        return;
      }
      const { error: retryError, timedOut } = await waitForRetryCompletion();
      onChanged();
      if (retryError) {
        setError(`Retry failed: ${retryError}`);
      } else if (timedOut) {
        setError('Retry is taking longer than expected - check back shortly.');
      } else {
        const data = await apiClient.getMissingTracks();
        const remaining = data.missingTracks.find(g => g.playlistId === playlistId)?.tracks.length ?? 0;
        setRetryResult({ matched: Math.max(0, initialCount - remaining), remaining });
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to retry matching');
    } finally {
      setIsRetrying(false);
    }
  };

  const handleRetryTrack = async (trackId: number) => {
    setRetryingTrackId(trackId);
    setError(null);
    try {
      const response = await apiClient.retryMissingTracks(undefined, [trackId]);
      if (!response.started) {
        setError(response.message);
        return;
      }
      const { error: retryError, timedOut } = await waitForRetryCompletion();
      onChanged();
      if (retryError) {
        setError(`Retry failed: ${retryError}`);
      } else if (timedOut) {
        setError('Retry is taking longer than expected - check back shortly.');
      } else {
        const data = await apiClient.getMissingTracks();
        const stillMissing = data.missingTracks.some(g => g.tracks.some(t => t.id === trackId));
        if (stillMissing) {
          setError('Track still not found in your Plex library');
        } else {
          setRetryResult({ matched: 1, remaining: 0 });
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to retry track');
    } finally {
      setRetryingTrackId(null);
    }
  };

  const handleRemoveTrack = async (id: number) => {
    if (!await confirmDialog('Are you sure you want to remove this track from the missing list?')) return;
    setError(null);
    try {
      await apiClient.removeMissingTrack(id);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove track');
    }
  };

  const handleClearPlaylist = async () => {
    if (!await confirmDialog('Are you sure you want to clear all missing tracks for this playlist?')) return;
    setIsClearing(true);
    setError(null);
    try {
      await apiClient.clearPlaylistMissingTracks(playlistId);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to clear tracks');
    } finally {
      setIsClearing(false);
    }
  };

  const handleExportCSV = () => {
    const csv = [
      ['Title', 'Artist', 'Album', 'Source', 'Added At'],
      ...tracks.map(track => [track.title, track.artist, track.album || '', track.source, new Date(track.addedAt).toLocaleDateString()]),
    ].map(row => row.map(cell => `"${cell}"`).join(',')).join('\n');

    const blob = new Blob([csv], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `missing-tracks-${playlistId}-${new Date().toISOString().split('T')[0]}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const handleOpenRematch = (track: MissingTrack) => {
    setRematchTrack(track);
    const firstArtist = track.artist.split(/\s*[,&\/]\s*/)[0].trim();
    setRematchQuery(`${firstArtist} ${track.title}`);
    setRematchResults([]);
  };

  const handleCloseRematch = () => {
    setRematchTrack(null);
    setRematchQuery('');
    setRematchResults([]);
  };

  const handleSearchRematch = async () => {
    if (!rematchQuery.trim()) return;
    setIsSearchingRematch(true);
    try {
      const response = await fetch('/api/import/plex/search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({
          query: rematchQuery,
          originalTitle: rematchTrack?.title,
          originalArtist: rematchTrack?.artist,
        }),
      });
      if (!response.ok) throw new Error('Search failed');
      const data = await response.json();
      setRematchResults(data.tracks || []);
    } catch {
      setRematchResults([]);
    } finally {
      setIsSearchingRematch(false);
    }
  };

  const handleSelectRematch = async (result: RematchResult) => {
    if (!rematchTrack) return;
    setError(null);
    try {
      await apiClient.rematchMissingTrack(rematchTrack.id, result.ratingKey);
      handleCloseRematch();
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to rematch track');
    }
  };

  return (
    <div style={{ padding: '1rem', borderTop: '1px solid var(--border)' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
        <span style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
          {tracks.length} missing track{tracks.length !== 1 ? 's' : ''}
        </span>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <button className="btn btn-secondary btn-small" onClick={handleExportCSV} title="Download this playlist's missing tracks as a CSV file">
            Export CSV
          </button>
          <button className="btn btn-secondary btn-small" onClick={handleClearPlaylist} disabled={isClearing} title="Give up on matching these and remove them from the missing list">
            {isClearing ? 'Clearing...' : 'Clear'}
          </button>
          <button className="btn btn-primary btn-small" onClick={handleRetryAll} disabled={isRetrying}>
            {isRetrying ? 'Retrying...' : 'Retry All'}
          </button>
        </div>
      </div>

      {error && <div className="error-message" style={{ marginBottom: '0.75rem' }}>{error}</div>}
      {retryResult && (
        <div style={{ marginBottom: '0.75rem', padding: '0.5rem 0.75rem', backgroundColor: 'rgba(76, 175, 80, 0.1)', border: '1px solid var(--success)', borderRadius: '4px', color: 'var(--success)', fontSize: '0.875rem' }}>
          Found and added {retryResult.matched} track(s). {retryResult.remaining} still missing.
        </div>
      )}

      {tracks.map((track, idx) => (
        <div
          key={track.id}
          style={{
            padding: '0.75rem',
            borderBottom: idx < tracks.length - 1 ? '1px solid var(--border)' : 'none',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
          }}
        >
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 500 }}>{track.title}</div>
            <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
              {track.artist}{track.album && ` • ${track.album}`}
            </div>
          </div>
          <div style={{ display: 'flex', gap: '0.5rem' }}>
            <button className="btn btn-primary btn-small" onClick={() => handleRetryTrack(track.id)} disabled={isRetrying || retryingTrackId === track.id} title="Retry matching this track">
              {retryingTrackId === track.id ? '...' : 'Retry'}
            </button>
            <button className="btn btn-secondary btn-small" onClick={() => handleOpenRematch(track)} title="Manually search and match this track">
              Match
            </button>
            <button className="btn btn-secondary btn-small" onClick={() => handleRemoveTrack(track.id)} title="Remove from missing list">
              Remove
            </button>
          </div>
        </div>
      ))}

      {rematchTrack && (
        <Modal onClose={handleCloseRematch} ariaLabel="Manual Rematch" contentStyle={{ width: '950px', maxWidth: '95vw', maxHeight: '80vh', display: 'flex', flexDirection: 'column' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
              <h2 style={{ margin: 0 }}>Manual Rematch</h2>
              <button onClick={handleCloseRematch} aria-label="Close" style={{ background: 'none', border: 'none', color: 'var(--text-secondary)', fontSize: '1.5rem', cursor: 'pointer' }}>×</button>
            </div>

            <div style={{ padding: '0.75rem', backgroundColor: 'rgba(100, 181, 246, 0.1)', border: '1px solid rgba(100, 181, 246, 0.3)', borderRadius: '4px', marginBottom: '1rem' }}>
              <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>Original Track:</div>
              <div>{rematchTrack.artist} - {rematchTrack.title}</div>
            </div>

            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
              <input
                type="text"
                value={rematchQuery}
                onChange={(e) => setRematchQuery(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleSearchRematch()}
                placeholder="Search your Plex library..."
                style={{ flex: 1, padding: '0.5rem 0.75rem', borderRadius: '4px', border: '1px solid var(--border)', backgroundColor: 'var(--background)', color: 'var(--text-primary)' }}
              />
              <button className="btn btn-primary" onClick={handleSearchRematch} disabled={isSearchingRematch}>
                {isSearchingRematch ? '...' : 'Search'}
              </button>
            </div>

            {rematchResults.length > 0 ? (
              <div style={{ flex: 1, overflowY: 'auto', border: '1px solid var(--border)', borderRadius: '4px' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.875rem' }}>
                  <thead>
                    <tr style={{ backgroundColor: 'var(--surface)', borderBottom: '1px solid var(--border)', position: 'sticky', top: 0 }}>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '90px' }} title="The same score a real import would compute for this candidate">Match</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Title</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Artist</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Album</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '80px' }}>Format</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '90px' }}>Bitrate</th>
                      <th style={{ padding: '0.75rem', textAlign: 'center', fontWeight: 600, width: '80px' }}>Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rematchResults.map((result, idx) => (
                      <tr key={idx} style={{ borderBottom: idx < rematchResults.length - 1 ? '1px solid var(--border)' : 'none' }}>
                        <td style={{ padding: '0.75rem' }}>
                          {result.matchScore !== undefined ? (
                            <span
                              title={result.matched ? 'Would auto-match at your current settings' : "Below your minimum match score - won't auto-match"}
                              style={{
                                display: 'inline-block',
                                padding: '0.125rem 0.5rem',
                                borderRadius: '4px',
                                fontSize: '0.75rem',
                                fontWeight: 600,
                                color: result.matched ? 'var(--success)' : (result.matchScore >= 50 ? 'var(--warning)' : 'var(--error)'),
                                backgroundColor: result.matched ? 'rgba(102, 187, 106, 0.1)' : (result.matchScore >= 50 ? 'rgba(255, 167, 38, 0.1)' : 'rgba(239, 83, 80, 0.1)'),
                              }}
                            >
                              {Math.round(result.matchScore)}%
                            </span>
                          ) : '-'}
                        </td>
                        <td style={{ padding: '0.75rem', fontWeight: 500 }}>{result.title}</td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>{result.artist || '-'}</td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>{result.album || '-'}</td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>{result.codec || '-'}</td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>{result.bitrate ? `${result.bitrate} kbps` : '-'}</td>
                        <td style={{ padding: '0.75rem', textAlign: 'center' }}>
                          <button className="btn btn-primary btn-small" onClick={() => handleSelectRematch(result)} style={{ fontSize: '0.75rem', padding: '0.25rem 0.75rem' }}>
                            Select
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : isSearchingRematch ? (
              <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>Searching...</div>
            ) : (
              <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>Search your Plex library to find a match</div>
            )}

            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '1rem' }}>
              <button className="btn btn-secondary" onClick={handleCloseRematch}>Cancel</button>
            </div>
        </Modal>
      )}
    </div>
  );
}
