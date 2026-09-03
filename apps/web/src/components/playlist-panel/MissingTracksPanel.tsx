import { useState } from 'react';
import { useApp } from '../../contexts/AppContext';
import type { MissingTrack } from '@playlist-lab/shared';
import { Modal, modalCloseButtonStyle } from '../Modal';
import { useConfirm } from '../../contexts/ConfirmContext';
import { useToast } from '../../contexts/ToastContext';
import { waitForRetryCompletion } from '../../utils/retryCompletion';
import './MissingTracksPanel.css';

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
 *
 * `compact` is for the mobile card list, where five per-track text buttons
 * plus a four-button toolbar wrapped into an unreadable block of controls.
 * It keeps only the one action that's actually common (Retry) inline and
 * folds the rest into a per-track overflow menu, so a track is one line
 * again and the list stays scannable.
 */
export function MissingTracksPanel({ playlistId, tracks, onChanged, compact = false }: { playlistId: number; tracks: MissingTrack[]; onChanged: () => void; compact?: boolean }) {
  const { apiClient } = useApp();
  const confirmDialog = useConfirm();
  const toast = useToast();
  const [isRetrying, setIsRetrying] = useState(false);
  const [retryingTrackId, setRetryingTrackId] = useState<number | null>(null);
  // True while a submitted retry is still waiting behind another retry
  // that's already running server-side (they share one per-user chain) -
  // lets the button say "Queued..." instead of a generic "..." the whole
  // time, since that wait can be much longer than a single track normally
  // takes once it's actually running.
  const [isQueued, setIsQueued] = useState(false);
  const [deemixTrackId, setDeemixTrackId] = useState<number | null>(null);
  const [isDeemixingAll, setIsDeemixingAll] = useState(false);
  const [lidarrTrackId, setLidarrTrackId] = useState<number | null>(null);

  const [rematchTrack, setRematchTrack] = useState<MissingTrack | null>(null);
  // Artist and title are searched as separate fields rather than one box:
  // the server can only guess where an artist name ends in a single string
  // ("The Rolling Stones Paint It Black" splits as artist "The Rolling"),
  // and Plex's own filters take them separately anyway.
  const [rematchArtist, setRematchArtist] = useState('');
  const [rematchTitle, setRematchTitle] = useState('');
  const [rematchResults, setRematchResults] = useState<RematchResult[]>([]);
  const [isSearchingRematch, setIsSearchingRematch] = useState(false);
  const [isClearing, setIsClearing] = useState(false);

  const handleRetryAll = async () => {
    setIsRetrying(true);
    setIsQueued(true);
    try {
      const response = await apiClient.retryMissingTracks(playlistId);
      if (!response.started) {
        toast.error(response.message);
        return;
      }
      const { timedOut } = await waitForRetryCompletion(apiClient, undefined, () => setIsQueued(false));
      onChanged();
      if (timedOut) {
        toast.error('Retry is taking longer than expected - check the notification bell shortly.');
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to retry matching');
    } finally {
      setIsRetrying(false);
      setIsQueued(false);
    }
  };

  const handleRetryTrack = async (trackId: number) => {
    setRetryingTrackId(trackId);
    setIsQueued(true);
    try {
      const response = await apiClient.retryMissingTracks(undefined, [trackId]);
      if (!response.started) {
        toast.error(response.message);
        return;
      }
      const { timedOut } = await waitForRetryCompletion(apiClient, undefined, () => setIsQueued(false));
      onChanged();
      if (timedOut) {
        toast.error('Retry is taking longer than expected - check the notification bell shortly.');
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to retry track');
    } finally {
      setRetryingTrackId(null);
      setIsQueued(false);
    }
  };

  const handleDeemixDownload = async (track: MissingTrack) => {
    setDeemixTrackId(track.id);
    try {
      await apiClient.deemixDownload(track.id);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to queue deemix download');
    } finally {
      setDeemixTrackId(null);
    }
  };

  const handleDeemixAll = async () => {
    setIsDeemixingAll(true);
    try {
      // The whole sequence - retry matching against Plex first, then queue
      // whatever is still missing - runs server-side now. It used to be this
      // loop, one request per track from the browser, which meant closing
      // the panel or the tab partway through silently abandoned the rest.
      const { message } = await apiClient.deemixAll(playlistId);
      toast.success(message);
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to start Deemix All');
    } finally {
      setIsDeemixingAll(false);
    }
  };

  const handleLidarrDownload = async (track: MissingTrack) => {
    setLidarrTrackId(track.id);
    try {
      await apiClient.lidarrDownload(track.id);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to trigger Lidarr search');
    } finally {
      setLidarrTrackId(null);
    }
  };

  const handleRemoveTrack = async (id: number) => {
    if (!await confirmDialog('Are you sure you want to remove this track from the missing list?')) return;
    try {
      await apiClient.removeMissingTrack(id);
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to remove track');
    }
  };

  const handleClearPlaylist = async () => {
    if (!await confirmDialog('Are you sure you want to clear all missing tracks for this playlist?')) return;
    setIsClearing(true);
    try {
      await apiClient.clearPlaylistMissingTracks(playlistId);
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to clear tracks');
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
    // Only the first credited artist - a "A, B & C" credit rarely matches
    // how the library tags the track, and the user can edit it anyway.
    setRematchArtist(track.artist.split(/\s*[,&\/]\s*/)[0].trim());
    setRematchTitle(track.title);
    setRematchResults([]);
  };

  const handleCloseRematch = () => {
    setRematchTrack(null);
    setRematchArtist('');
    setRematchTitle('');
    setRematchResults([]);
  };

  const handleSearchRematch = async () => {
    if (!rematchArtist.trim() && !rematchTitle.trim()) return;
    setIsSearchingRematch(true);
    try {
      const response = await fetch('/api/import/plex/search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({
          artist: rematchArtist.trim(),
          title: rematchTitle.trim(),
          // The track as originally imported - results are scored against
          // this, not against what's typed above, so an edited search still
          // reports how well each candidate matches the real missing track.
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
    try {
      await apiClient.rematchMissingTrack(rematchTrack.id, result.ratingKey);
      handleCloseRematch();
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to rematch track');
    }
  };

  return (
    <div className={`missing-tracks-panel${compact ? ' missing-tracks-panel-compact' : ''}`}>
      <div className="missing-tracks-toolbar">
        <span style={{ color: 'var(--text-secondary)' }}>
          {tracks.length} missing track{tracks.length !== 1 ? 's' : ''}
        </span>
        <div className="missing-tracks-toolbar-actions">
          <button className="btn btn-secondary btn-small" onClick={handleExportCSV} title="Download this playlist's missing tracks as a CSV file">
            Export CSV
          </button>
          <button className="btn btn-secondary btn-small" onClick={handleClearPlaylist} disabled={isClearing} title="Give up on matching these and remove them from the missing list">
            {isClearing ? 'Clearing...' : 'Clear'}
          </button>
          <button className="btn btn-secondary btn-small" onClick={handleDeemixAll} disabled={isDeemixingAll} title="Search deemix and queue every missing track in this playlist for download">
            {isDeemixingAll ? 'Deemixing...' : 'Deemix All'}
          </button>
          <button className="btn btn-primary btn-small" onClick={handleRetryAll} disabled={isRetrying} title={isRetrying && isQueued ? 'Queued behind another retry already in progress' : undefined}>
            {isRetrying ? (isQueued ? 'Queued...' : 'Retrying...') : 'Retry All'}
          </button>
        </div>
      </div>

      {tracks.map((track, idx) => {
        const secondaryActions = (
          <>
            <button className="btn btn-secondary btn-small" onClick={() => handleOpenRematch(track)} title="Manually search and match this track">
              Match
            </button>
            <button className="btn btn-secondary btn-small" onClick={() => handleDeemixDownload(track)} disabled={deemixTrackId === track.id} title="Search deemix and queue this track for download">
              {deemixTrackId === track.id ? '...' : 'Deemix'}
            </button>
            <button className="btn btn-secondary btn-small" onClick={() => handleLidarrDownload(track)} disabled={lidarrTrackId === track.id} title="Find this track's artist in Lidarr and trigger a search">
              {lidarrTrackId === track.id ? '...' : 'Lidarr'}
            </button>
            <button className="btn btn-secondary btn-small" onClick={() => handleRemoveTrack(track.id)} title="Remove from missing list">
              Remove
            </button>
          </>
        );

        return (
          <div
            key={track.id}
            className="missing-track-row"
            style={{ borderBottom: idx < tracks.length - 1 ? '1px solid var(--border)' : 'none' }}
          >
            <div className="missing-track-info">
              <div className="missing-track-title">{track.title}</div>
              <div className="missing-track-sub">
                {track.artist}{track.album && ` • ${track.album}`}
              </div>
            </div>
            <div className="missing-track-actions">
              <button className="btn btn-primary btn-small" onClick={() => handleRetryTrack(track.id)} disabled={isRetrying || retryingTrackId === track.id} title={retryingTrackId === track.id && isQueued ? 'Queued behind another retry already in progress' : 'Retry matching this track'}>
                {retryingTrackId === track.id ? (isQueued ? 'Queued...' : '...') : 'Retry'}
              </button>
              {compact ? (
                /* `name` makes these an exclusive accordion, so opening one
                   track's menu closes any other - no outside-click handler
                   or open-menu state needed. */
                <details className="missing-track-more" name={`missing-more-${playlistId}`}>
                  <summary aria-label={`More actions for ${track.title}`}>⋯</summary>
                  <div className="missing-track-more-menu">{secondaryActions}</div>
                </details>
              ) : secondaryActions}
            </div>
          </div>
        );
      })}

      {rematchTrack && (
        <Modal onClose={handleCloseRematch} ariaLabel="Manual Rematch" contentStyle={{ width: '950px', maxWidth: '95vw', maxHeight: '80vh', display: 'flex', flexDirection: 'column' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
              <h2 style={{ margin: 0 }}>Manual Rematch</h2>
              <button onClick={handleCloseRematch} title="Close" style={modalCloseButtonStyle}>✕</button>
            </div>

            <div style={{ padding: '0.75rem', backgroundColor: 'rgba(100, 181, 246, 0.1)', border: '1px solid rgba(100, 181, 246, 0.3)', borderRadius: '4px', marginBottom: '1rem' }}>
              <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>Original Track:</div>
              <div>{rematchTrack.artist} - {rematchTrack.title}</div>
            </div>

            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
              <input
                type="text"
                value={rematchArtist}
                onChange={(e) => setRematchArtist(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleSearchRematch()}
                placeholder="Artist"
                aria-label="Track artist"
                style={{ flex: 1, padding: '0.5rem 0.75rem', borderRadius: '4px', border: '1px solid var(--border)', backgroundColor: 'var(--background)', color: 'var(--text-primary)' }}
              />
              <input
                type="text"
                value={rematchTitle}
                onChange={(e) => setRematchTitle(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleSearchRematch()}
                placeholder="Track name"
                aria-label="Track name"
                style={{ flex: 2, padding: '0.5rem 0.75rem', borderRadius: '4px', border: '1px solid var(--border)', backgroundColor: 'var(--background)', color: 'var(--text-primary)' }}
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
