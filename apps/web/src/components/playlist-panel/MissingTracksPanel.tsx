import { useState, useRef, useEffect } from 'react';
import { useApp } from '../../contexts/AppContext';
import type { MissingTrack } from '@playlist-lab/shared';

interface RematchResult {
  ratingKey: string;
  title: string;
  artist: string;
  album: string;
  codec: string;
  bitrate: number;
  duration: number;
}

/**
 * Missing-tracks list (retry/match/remove) for a single playlist, shown
 * inline when a playlist row's "Missing Tracks" badge is expanded in the
 * unified playlist control panel. Extracted from the former standalone
 * Missing Tracks page.
 */
export function MissingTracksPanel({ playlistId, tracks, onChanged }: { playlistId: number; tracks: MissingTrack[]; onChanged: () => void }) {
  const { apiClient } = useApp();
  const [isRetrying, setIsRetrying] = useState(false);
  const [retryingTrackId, setRetryingTrackId] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [retryResult, setRetryResult] = useState<{ matched: number; remaining: number } | null>(null);

  const [rematchTrack, setRematchTrack] = useState<MissingTrack | null>(null);
  const [rematchQuery, setRematchQuery] = useState('');
  const [rematchResults, setRematchResults] = useState<RematchResult[]>([]);
  const [isSearchingRematch, setIsSearchingRematch] = useState(false);
  const backdropMouseDown = useRef(false);

  useEffect(() => {
    if (!rematchTrack) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') handleCloseRematch();
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [rematchTrack]);

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
      let lastCount = initialCount;
      let stableStreak = 0;
      for (let i = 0; i < 100 && stableStreak < 2; i++) {
        await new Promise(resolve => setTimeout(resolve, 3000));
        const data = await apiClient.getMissingTracks();
        const currentCount = data.missingTracks.find(g => g.playlistId === playlistId)?.tracks.length ?? 0;
        if (currentCount === lastCount) {
          stableStreak++;
        } else {
          stableStreak = 0;
          lastCount = currentCount;
        }
      }
      setRetryResult({ matched: Math.max(0, initialCount - lastCount), remaining: lastCount });
      onChanged();
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
      let stillMissing = true;
      for (let i = 0; i < 20 && stillMissing; i++) {
        await new Promise(resolve => setTimeout(resolve, 1500));
        const data = await apiClient.getMissingTracks();
        stillMissing = data.missingTracks.some(g => g.tracks.some(t => t.id === trackId));
      }
      if (stillMissing) {
        setError('Track still not found in your Plex library');
      } else {
        setRetryResult({ matched: 1, remaining: 0 });
      }
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to retry track');
    } finally {
      setRetryingTrackId(null);
    }
  };

  const handleRemoveTrack = async (id: number) => {
    if (!confirm('Are you sure you want to remove this track from the missing list?')) return;
    setError(null);
    try {
      await apiClient.removeMissingTrack(id);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove track');
    }
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
        body: JSON.stringify({ query: rematchQuery }),
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
        <button className="btn btn-primary btn-small" onClick={handleRetryAll} disabled={isRetrying}>
          {isRetrying ? 'Retrying...' : 'Retry All'}
        </button>
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
        <div
          style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, backgroundColor: 'rgba(0, 0, 0, 0.7)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 }}
          onMouseDown={(e) => { if (e.target === e.currentTarget) backdropMouseDown.current = true; }}
          onMouseUp={(e) => { if (e.target === e.currentTarget && backdropMouseDown.current) handleCloseRematch(); backdropMouseDown.current = false; }}
        >
          <div
            style={{ backgroundColor: 'var(--surface)', borderRadius: '8px', padding: '1.5rem', width: '950px', maxWidth: '95vw', maxHeight: '80vh', display: 'flex', flexDirection: 'column' }}
            onMouseDown={(e) => { backdropMouseDown.current = false; e.stopPropagation(); }}
          >
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
              <h2 style={{ margin: 0 }}>Manual Rematch</h2>
              <button onClick={handleCloseRematch} style={{ background: 'none', border: 'none', color: 'var(--text-secondary)', fontSize: '1.5rem', cursor: 'pointer' }}>×</button>
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
          </div>
        </div>
      )}
    </div>
  );
}
