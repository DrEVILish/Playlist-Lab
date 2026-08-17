import { useState, useEffect, useRef } from 'react';
import { useApp } from '../../contexts/AppContext';
import '../../pages/EditPlaylistsPage.css';

export interface EditablePlaylist {
  id: string;
  name: string;
  trackCount: number;
  duration: number;
  composite?: string;
}

interface Track {
  id?: string;
  ratingKey: string;
  playlistItemID?: number;
  title: string;
  artist: string;
  album: string;
  duration: number;
  codec?: string;
  bitrate?: number;
}

/**
 * Full playlist track editor (reorder, remove, add, replace, cover upload,
 * duplicate detection, inline preview playback) for a single playlist.
 * Extracted from the former standalone Edit Playlists page so it can be
 * used inline in the unified playlist control panel (see
 * pages/PlaylistsControlPanel.tsx) instead of requiring its own page with
 * its own playlist picker.
 */
export function PlaylistEditor({ playlist, onPlaylistUpdated }: { playlist: EditablePlaylist; onPlaylistUpdated?: () => void }) {
  const { apiClient, server } = useApp();
  const [tracks, setTracks] = useState<Track[]>([]);
  const [tracksLoading, setTracksLoading] = useState(false);
  const [draggedIndex, setDraggedIndex] = useState<number | null>(null);
  const [showAddTracksModal, setShowAddTracksModal] = useState(false);
  const [uploadingCover, setUploadingCover] = useState(false);
  const [searchArtist, setSearchArtist] = useState('');
  const [searchTrack, setSearchTrack] = useState('');
  const [searchAlbum, setSearchAlbum] = useState('');
  const [searchResults, setSearchResults] = useState<Track[]>([]);
  const [searching, setSearching] = useState(false);
  const [selectedTracks, setSelectedTracks] = useState<Set<string>>(new Set());
  const [selectedForRemoval, setSelectedForRemoval] = useState<Set<number>>(new Set());
  const [removingTracks, setRemovingTracks] = useState(false);
  const [currentlyPlaying, setCurrentlyPlaying] = useState<string | null>(null);
  const [audioElement] = useState(() => new Audio());
  const [coverUrl, setCoverUrl] = useState(playlist.composite);

  // Replace track state
  const [showReplaceModal, setShowReplaceModal] = useState(false);
  const [trackToReplace, setTrackToReplace] = useState<Track | null>(null);
  const [replaceSearchQuery, setReplaceSearchQuery] = useState('');
  const [replaceSearchResults, setReplaceSearchResults] = useState<Track[]>([]);
  const [searchingReplace, setSearchingReplace] = useState(false);

  const scrollPositionRef = useRef<number>(0);
  const shouldRestoreScroll = useRef<boolean>(false);

  useEffect(() => {
    loadTracks();
    return () => {
      audioElement.pause();
      audioElement.src = '';
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playlist.id]);

  useEffect(() => {
    if (shouldRestoreScroll.current && tracks.length > 0) {
      const scrollContainer = document.querySelector('.tracks-panel');
      if (scrollContainer) {
        requestAnimationFrame(() => {
          scrollContainer.scrollTop = scrollPositionRef.current;
          shouldRestoreScroll.current = false;
        });
      }
    }
  }, [tracks]);

  const loadTracks = async () => {
    try {
      setTracksLoading(true);
      const response = await apiClient.getPlaylistTracks(playlist.id);
      setTracks((response.tracks || []).map((track: any) => ({
        ...track,
        playlistItemID: track.playlistItemID,
      })));
    } catch (err: any) {
      console.error('Failed to load tracks:', err);
      setTracks([]);
    } finally {
      setTracksLoading(false);
    }
  };

  const handleSelectDuplicates = () => {
    const seen = new Map<string, number>();
    const duplicates = new Set<number>();
    tracks.forEach((track) => {
      const normalizedKey = `${track.title.toLowerCase().trim()}|${track.artist.toLowerCase().trim()}`;
      if (seen.has(normalizedKey)) {
        if (track.playlistItemID !== undefined) duplicates.add(track.playlistItemID);
      } else {
        seen.set(normalizedKey, 1);
      }
    });
    setSelectedForRemoval(duplicates);
  };

  const handleToggleForRemoval = (playlistItemID: number) => {
    const newSelected = new Set(selectedForRemoval);
    if (newSelected.has(playlistItemID)) newSelected.delete(playlistItemID);
    else newSelected.add(playlistItemID);
    setSelectedForRemoval(newSelected);
  };

  const handleRemoveSelected = async () => {
    if (selectedForRemoval.size === 0) return;
    if (!confirm(`Remove ${selectedForRemoval.size} selected track(s) from the playlist?`)) return;

    try {
      setRemovingTracks(true);
      for (const playlistItemID of selectedForRemoval) {
        await apiClient.removeTrackFromPlaylist(playlist.id, playlistItemID.toString());
      }
      await loadTracks();
      setSelectedForRemoval(new Set());
      onPlaylistUpdated?.();
    } catch (err: any) {
      alert(err.message || 'Failed to remove some tracks');
      await loadTracks();
    } finally {
      setRemovingTracks(false);
    }
  };

  const handleRemoveTrack = async (playlistItemId: number) => {
    if (!confirm('Remove this track from the playlist?')) return;
    try {
      await apiClient.removeTrackFromPlaylist(playlist.id, playlistItemId.toString());
      setTracks(tracks.filter(t => t.playlistItemID !== playlistItemId));
      onPlaylistUpdated?.();
    } catch (err: any) {
      alert(err.message || 'Failed to remove track');
    }
  };

  const handleDragStart = (index: number) => setDraggedIndex(index);

  const handleDragOver = (e: React.DragEvent, index: number) => {
    e.preventDefault();
    if (draggedIndex === null || draggedIndex === index) return;
    const newTracks = [...tracks];
    const draggedTrack = newTracks[draggedIndex];
    newTracks.splice(draggedIndex, 1);
    newTracks.splice(index, 0, draggedTrack);
    setTracks(newTracks);
    setDraggedIndex(index);
  };

  const handleDragEnd = async () => {
    if (draggedIndex === null) {
      setDraggedIndex(null);
      return;
    }
    const originalTracks = [...tracks];
    try {
      const draggedTrack = tracks[draggedIndex];
      if (!draggedTrack.playlistItemID) {
        setDraggedIndex(null);
        return;
      }
      const afterIndex = draggedIndex - 1;
      const afterTrackId = afterIndex < 0 ? '0' : tracks[afterIndex].playlistItemID?.toString() || '0';

      const response = await fetch(`/api/playlists/${playlist.id}/tracks/${draggedTrack.playlistItemID}/move`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ afterId: afterTrackId }),
      });

      if (!response.ok) throw new Error(`Failed to reorder tracks: ${response.status} ${response.statusText}`);
    } catch (err: any) {
      alert(`Failed to reorder tracks: ${err.message}`);
      setTracks(originalTracks);
    }
    setDraggedIndex(null);
  };

  const handlePlayTrack = (track: Track) => {
    if (!server) return;
    if (currentlyPlaying === track.ratingKey) {
      audioElement.pause();
      setCurrentlyPlaying(null);
      return;
    }
    if (currentlyPlaying) audioElement.pause();

    audioElement.src = `/api/proxy/audio?ratingKey=${track.ratingKey}`;
    audioElement.play().catch(err => {
      console.error('Failed to play track:', err);
      alert('Failed to play track');
    });
    setCurrentlyPlaying(track.ratingKey);
    audioElement.onended = () => setCurrentlyPlaying(null);
  };

  const resizeImage = (file: File, maxWidth: number, maxHeight: number): Promise<Blob> => {
    return new Promise((resolve, reject) => {
      const img = new Image();
      const canvas = document.createElement('canvas');
      const ctx = canvas.getContext('2d');
      img.onload = () => {
        let width = img.width;
        let height = img.height;
        if (width > height) {
          if (width > maxWidth) {
            height = (height * maxWidth) / width;
            width = maxWidth;
          }
        } else if (height > maxHeight) {
          width = (width * maxHeight) / height;
          height = maxHeight;
        }
        canvas.width = width;
        canvas.height = height;
        ctx?.drawImage(img, 0, 0, width, height);
        canvas.toBlob((blob) => blob ? resolve(blob) : reject(new Error('Failed to resize image')), 'image/jpeg', 0.9);
      };
      img.onerror = () => reject(new Error('Failed to load image'));
      img.src = URL.createObjectURL(file);
    });
  };

  const handleCoverUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;

    if (!file.type.startsWith('image/')) {
      alert('Please select an image file');
      return;
    }
    if (file.size > 5 * 1024 * 1024) {
      alert('Image must be less than 5MB');
      return;
    }

    try {
      setUploadingCover(true);
      const resizedBlob = await resizeImage(file, 1000, 1000);
      const formData = new FormData();
      formData.append('cover', resizedBlob, file.name);

      const response = await fetch(`/api/playlists/${playlist.id}/cover`, {
        method: 'POST',
        credentials: 'include',
        body: formData,
      });

      if (!response.ok) {
        const error = await response.json().catch(() => ({ message: 'Failed to upload cover' }));
        throw new Error(error.message || 'Failed to upload cover');
      }

      setCoverUrl(`${playlist.composite}?t=${Date.now()}`);
      onPlaylistUpdated?.();
      alert('Cover uploaded successfully!');
    } catch (err: any) {
      alert(err.message || 'Failed to upload cover');
    } finally {
      setUploadingCover(false);
      e.target.value = '';
    }
  };

  const handleSearchTracks = async () => {
    if (!searchArtist.trim() && !searchTrack.trim() && !searchAlbum.trim()) {
      alert('Please enter at least one search term');
      return;
    }
    try {
      setSearching(true);
      const params = new URLSearchParams();
      if (searchArtist.trim()) params.append('artist', searchArtist.trim());
      if (searchTrack.trim()) params.append('track', searchTrack.trim());
      if (searchAlbum.trim()) params.append('album', searchAlbum.trim());

      const response = await fetch(`/api/search?${params.toString()}`, { credentials: 'include' });
      if (!response.ok) throw new Error('Search failed');
      const data = await response.json();
      const results = data.results || [];
      setSearchResults(results.map((item: any) => ({
        id: item.ratingKey,
        ratingKey: item.ratingKey,
        title: item.title,
        artist: item.grandparentTitle || 'Unknown Artist',
        album: item.parentTitle || 'Unknown Album',
        duration: item.duration || 0,
        codec: item.Media?.[0]?.audioCodec || 'N/A',
        bitrate: item.Media?.[0]?.bitrate || 0,
      })));
    } catch (err) {
      console.error('Search failed:', err);
      alert('Failed to search tracks');
    } finally {
      setSearching(false);
    }
  };

  const handleToggleTrack = (trackId: string) => {
    const newSelected = new Set(selectedTracks);
    if (newSelected.has(trackId)) newSelected.delete(trackId);
    else newSelected.add(trackId);
    setSelectedTracks(newSelected);
  };

  const handleAddSelectedTracks = async () => {
    if (selectedTracks.size === 0) return;
    try {
      const trackIds = Array.from(selectedTracks);
      const trackUris = trackIds.map(id => `server://${server?.clientId}/com.plexapp.plugins.library/library/metadata/${id}`);

      const response = await fetch(`/api/playlists/${playlist.id}/tracks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ trackUris }),
      });
      if (!response.ok) throw new Error('Failed to add tracks');

      await loadTracks();
      setSelectedTracks(new Set());
      setSearchArtist('');
      setSearchTrack('');
      setSearchAlbum('');
      setSearchResults([]);
      setShowAddTracksModal(false);
      onPlaylistUpdated?.();
      alert(`Added ${trackIds.length} track(s) to playlist`);
    } catch (err) {
      console.error('Failed to add tracks:', err);
      alert('Failed to add tracks to playlist');
    }
  };

  const handleOpenReplaceModal = (track: Track) => {
    const scrollContainer = document.querySelector('.tracks-panel');
    if (scrollContainer) {
      scrollPositionRef.current = scrollContainer.scrollTop;
      shouldRestoreScroll.current = true;
    }
    setTrackToReplace(track);
    setReplaceSearchQuery(`${track.artist} ${track.title}`);
    setReplaceSearchResults([]);
    setShowReplaceModal(true);
  };

  const handleCloseReplaceModal = () => {
    setShowReplaceModal(false);
    setTrackToReplace(null);
    setReplaceSearchQuery('');
    setReplaceSearchResults([]);
    if (shouldRestoreScroll.current) {
      const scrollContainer = document.querySelector('.tracks-panel');
      if (scrollContainer) {
        requestAnimationFrame(() => {
          scrollContainer.scrollTop = scrollPositionRef.current;
          shouldRestoreScroll.current = false;
        });
      }
    }
  };

  const handleSearchReplace = async () => {
    if (!replaceSearchQuery.trim()) return;
    setSearchingReplace(true);
    try {
      const response = await fetch('/api/import/plex/search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ query: replaceSearchQuery }),
      });
      if (!response.ok) throw new Error('Search failed');
      const data = await response.json();
      const results = data.tracks || [];
      setReplaceSearchResults(results.map((item: any) => ({
        id: item.ratingKey,
        ratingKey: item.ratingKey,
        title: item.title,
        artist: item.artist || 'Unknown Artist',
        album: item.album || 'Unknown Album',
        duration: item.duration || 0,
        codec: item.codec || 'N/A',
        bitrate: item.bitrate || 0,
      })));
    } catch (err) {
      console.error('Search failed:', err);
      alert('Failed to search tracks');
    } finally {
      setSearchingReplace(false);
    }
  };

  const handleReplaceTrack = async (newTrack: Track) => {
    if (!trackToReplace) return;
    try {
      if (newTrack.ratingKey === trackToReplace.ratingKey) {
        handleCloseReplaceModal();
        await loadTracks();
        return;
      }

      const scrollContainer = document.querySelector('.tracks-panel');
      if (scrollContainer) {
        scrollPositionRef.current = scrollContainer.scrollTop;
        shouldRestoreScroll.current = true;
      }

      const oldTrackIndex = tracks.findIndex(t => t.playlistItemID === trackToReplace.playlistItemID);
      if (oldTrackIndex === -1) throw new Error('Track not found in playlist');

      const oldTrackRatingKey = trackToReplace.ratingKey;
      const afterIndex = oldTrackIndex - 1;
      const afterTrackId = afterIndex < 0 ? '0' : tracks[afterIndex].playlistItemID?.toString() || '0';

      const trackUri = `server://${server?.clientId}/com.plexapp.plugins.library/library/metadata/${newTrack.ratingKey}`;
      const addResponse = await fetch(`/api/playlists/${playlist.id}/tracks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ trackUris: [trackUri] }),
      });
      if (!addResponse.ok) throw new Error('Failed to add replacement track');

      await loadTracks();
      const updatedTracks = await apiClient.getPlaylistTracks(playlist.id);
      const newTrackItem = updatedTracks.tracks[updatedTracks.tracks.length - 1];
      if (!newTrackItem.playlistItemID) throw new Error('New track does not have a playlist item ID');

      const moveResponse = await fetch(`/api/playlists/${playlist.id}/tracks/${newTrackItem.playlistItemID}/move`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ afterId: afterTrackId }),
      });
      if (!moveResponse.ok) throw new Error('Failed to move replacement track to correct position');

      const tracksAfterMove = await apiClient.getPlaylistTracks(playlist.id);
      const oldTrackAfterMove = tracksAfterMove.tracks.find((t: any) => t.ratingKey === oldTrackRatingKey);
      if (!oldTrackAfterMove || !oldTrackAfterMove.playlistItemID) throw new Error('Could not find old track after move');

      await apiClient.removeTrackFromPlaylist(playlist.id, oldTrackAfterMove.playlistItemID.toString());
      await loadTracks();
      onPlaylistUpdated?.();
      handleCloseReplaceModal();
    } catch (err: any) {
      alert(`Failed to replace track: ${err.message || 'Unknown error'}`);
      shouldRestoreScroll.current = false;
      await loadTracks();
    }
  };

  const formatDuration = (ms: number) => {
    const seconds = Math.floor(ms / 1000);
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    return `${minutes}:${remainingSeconds.toString().padStart(2, '0')} mins`;
  };

  const formatBitrate = (bitrate?: number) => (bitrate ? `${Math.round(bitrate)} kbps` : 'N/A');

  const getCoverUrl = (composite?: string) => {
    if (!composite || !server) return null;
    return `/api/proxy/image?url=${encodeURIComponent(`${server.url}${composite}`)}`;
  };

  return (
    <div className="tracks-panel" style={{ padding: 0 }}>
      <div className="playlist-header">
        <div className="playlist-cover-section">
          <div className="playlist-cover-container">
            {coverUrl ? (
              <img
                src={getCoverUrl(coverUrl) || ''}
                alt={playlist.name}
                className="playlist-cover"
                onError={(e) => {
                  e.currentTarget.style.display = 'none';
                  const placeholder = e.currentTarget.parentElement?.querySelector('.playlist-cover-placeholder');
                  if (placeholder) (placeholder as HTMLElement).style.display = 'flex';
                }}
              />
            ) : null}
            <div className="playlist-cover-placeholder" style={{ display: coverUrl ? 'none' : 'flex' }}>
              No Cover
            </div>
          </div>
          <label className="upload-cover-btn" title="Change cover">
            {uploadingCover ? (
              <>
                <span className="upload-icon">⏳</span>
                Uploading...
              </>
            ) : (
              <>
                <svg className="upload-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
                  <polyline points="17 8 12 3 7 8" />
                  <line x1="12" y1="3" x2="12" y2="15" />
                </svg>
                Change Cover
              </>
            )}
            <input type="file" accept="image/*" onChange={handleCoverUpload} disabled={uploadingCover} style={{ display: 'none' }} />
          </label>
        </div>

        <div className="playlist-info-section">
          <h1 className="playlist-title">{playlist.name}</h1>
          <div className="playlist-stats">
            {tracks.length} tracks • {formatDuration(playlist.duration || 0)}
          </div>

          <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
            <button className="btn-primary add-tracks-btn" onClick={() => setShowAddTracksModal(true)}>
              + Add Tracks
            </button>
            <button className="btn-secondary" onClick={handleSelectDuplicates} title="Select all duplicate tracks (same song appearing multiple times)">
              Select Duplicates
            </button>
            {selectedForRemoval.size > 0 && (
              <button className="btn-secondary" onClick={handleRemoveSelected} disabled={removingTracks} style={{ color: '#ef4444' }}>
                {removingTracks ? 'Removing...' : `Remove Selected (${selectedForRemoval.size})`}
              </button>
            )}
          </div>
        </div>
      </div>

      {tracksLoading ? (
        <p>Loading tracks...</p>
      ) : tracks.length === 0 ? (
        <p>No tracks in this playlist</p>
      ) : (
        <div className="tracks-table-container">
          <table className="tracks-table">
            <thead>
              <tr>
                <th className="col-select"></th>
                <th className="col-play"></th>
                <th className="col-replace"></th>
                <th className="col-drag"></th>
                <th className="col-number">#</th>
                <th className="col-title">Title</th>
                <th className="col-artist">Artist</th>
                <th className="col-album">Album</th>
                <th className="col-codec">Codec</th>
                <th className="col-bitrate">Bitrate</th>
                <th className="col-duration">Duration</th>
                <th className="col-actions"></th>
              </tr>
            </thead>
            <tbody>
              {tracks.map((track, index) => (
                <tr
                  key={`${track.ratingKey}-${track.playlistItemID || index}`}
                  draggable
                  onDragStart={() => handleDragStart(index)}
                  onDragOver={(e) => handleDragOver(e, index)}
                  onDragEnd={handleDragEnd}
                  className={`${draggedIndex === index ? 'dragging' : ''} ${track.playlistItemID !== undefined && selectedForRemoval.has(track.playlistItemID) ? 'selected-for-removal' : ''}`}
                >
                  <td className="col-select">
                    <input
                      type="checkbox"
                      checked={track.playlistItemID !== undefined && selectedForRemoval.has(track.playlistItemID)}
                      onChange={() => track.playlistItemID !== undefined && handleToggleForRemoval(track.playlistItemID)}
                      onClick={(e) => e.stopPropagation()}
                    />
                  </td>
                  <td className="col-play">
                    <button
                      className={`btn-play ${currentlyPlaying === track.ratingKey ? 'playing' : ''}`}
                      onClick={() => handlePlayTrack(track)}
                      title={currentlyPlaying === track.ratingKey ? 'Pause' : 'Play track'}
                    >
                      {currentlyPlaying === track.ratingKey ? (
                        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <rect x="6" y="4" width="4" height="16" />
                          <rect x="14" y="4" width="4" height="16" />
                        </svg>
                      ) : (
                        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <polygon points="5 3 19 12 5 21 5 3" />
                        </svg>
                      )}
                    </button>
                  </td>
                  <td className="col-replace">
                    <button className="btn-replace" onClick={() => handleOpenReplaceModal(track)} title="Replace track">
                      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                        <path d="M21 2v6h-6M3 12a9 9 0 0 1 15-6.7L21 8M3 22v-6h6M21 12a9 9 0 0 1-15 6.7L3 16" />
                      </svg>
                    </button>
                  </td>
                  <td className="col-drag">
                    <span className="drag-handle">☰</span>
                  </td>
                  <td className="col-number">{index + 1}</td>
                  <td className="col-title">{track.title}</td>
                  <td className="col-artist">{track.artist}</td>
                  <td className="col-album">{track.album}</td>
                  <td className="col-codec">{track.codec || 'N/A'}</td>
                  <td className="col-bitrate">{formatBitrate(track.bitrate)}</td>
                  <td className="col-duration">{formatDuration(track.duration)}</td>
                  <td className="col-actions">
                    <button
                      className="btn-icon"
                      onClick={() => track.playlistItemID && handleRemoveTrack(track.playlistItemID)}
                      title="Remove track"
                      disabled={!track.playlistItemID}
                    >
                      ×
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {showAddTracksModal && (
        <div className="modal-overlay" onClick={() => setShowAddTracksModal(false)}>
          <div className="modal-content add-tracks-modal" onClick={(e) => e.stopPropagation()}>
            <h3>Add Tracks to Playlist</h3>

            <div className="search-section">
              <input
                type="text"
                className="search-input"
                placeholder="Artist name..."
                value={searchArtist}
                onChange={(e) => setSearchArtist(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleSearchTracks()}
              />
              <input
                type="text"
                className="search-input"
                placeholder="Track name..."
                value={searchTrack}
                onChange={(e) => setSearchTrack(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleSearchTracks()}
              />
              <input
                type="text"
                className="search-input"
                placeholder="Album name..."
                value={searchAlbum}
                onChange={(e) => setSearchAlbum(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleSearchTracks()}
              />
              <button className="btn-primary" onClick={handleSearchTracks} disabled={searching}>
                {searching ? 'Searching...' : 'Search'}
              </button>
            </div>

            {searchResults.length > 0 && (
              <div className="search-results">
                <p>{searchResults.length} results found. Select tracks to add:</p>
                <div className="results-list">
                  {searchResults.map((track) => track.id && (
                    <div
                      key={track.id}
                      className={`result-item ${selectedTracks.has(track.id) ? 'selected' : ''}`}
                      onClick={() => track.id && handleToggleTrack(track.id)}
                    >
                      <input type="checkbox" checked={track.id ? selectedTracks.has(track.id) : false} onChange={() => {}} />
                      <div className="result-info">
                        <div className="result-title">{track.title}</div>
                        <div className="result-meta">{track.artist} • {track.album}</div>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}

            <div className="modal-actions">
              {selectedTracks.size > 0 && (
                <button className="btn-primary" onClick={handleAddSelectedTracks}>
                  Add {selectedTracks.size} Track(s)
                </button>
              )}
              <button
                className="btn-secondary"
                onClick={() => {
                  setShowAddTracksModal(false);
                  setSearchArtist('');
                  setSearchTrack('');
                  setSearchAlbum('');
                  setSearchResults([]);
                  setSelectedTracks(new Set());
                }}
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}

      {showReplaceModal && trackToReplace && (
        <div className="modal-overlay" onClick={handleCloseReplaceModal}>
          <div className="modal-content add-tracks-modal" onClick={(e) => e.stopPropagation()}>
            <h3>Replace Track</h3>

            <div className="replace-track-info">
              <p><strong>Current Track:</strong></p>
              <p>{trackToReplace.artist} - {trackToReplace.title}</p>
              <p className="track-meta">{trackToReplace.album}</p>
            </div>

            <div className="search-section">
              <input
                type="text"
                className="search-input"
                placeholder="Search for replacement track..."
                value={replaceSearchQuery}
                onChange={(e) => setReplaceSearchQuery(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && handleSearchReplace()}
              />
              <button className="btn-primary" onClick={handleSearchReplace} disabled={searchingReplace}>
                {searchingReplace ? 'Searching...' : 'Search'}
              </button>
            </div>

            {replaceSearchResults.length > 0 && (
              <div className="search-results">
                <p>{replaceSearchResults.length} results found. Select a track to replace with:</p>
                <div className="results-list">
                  {replaceSearchResults.map((track) => track.id && (
                    <div key={track.id} className="result-item" onClick={() => handleReplaceTrack(track)} style={{ cursor: 'pointer' }}>
                      <div className="result-info">
                        <div className="result-title">{track.title}</div>
                        <div className="result-meta">{track.artist} • {track.album}</div>
                        <div className="result-meta">{track.codec} • {formatBitrate(track.bitrate)}</div>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}

            <div className="modal-actions">
              <button className="btn-secondary" onClick={handleCloseReplaceModal}>Cancel</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
