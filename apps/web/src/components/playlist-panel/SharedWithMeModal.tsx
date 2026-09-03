import { useState, useEffect } from 'react';
import { Modal, modalCloseButtonStyle } from '../Modal';
import '../../pages/SharePlaylistsPage.css';

interface SharedPlaylist {
  id: number;
  playlistName: string;
  sharedByUsername: string;
  sharedAt: string;
  plexPlaylistId: string;
}

/**
 * Playlists other Playlist Lab users have shared with you. Extracted from
 * the former standalone Share Playlists page's "Shared Playlists" tab.
 */
export function SharedWithMeModal({ onClose }: { onClose: () => void }) {
  const [sharedPlaylists, setSharedPlaylists] = useState<SharedPlaylist[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      setIsLoading(true);
      try {
        const response = await fetch('/api/playlists/shared-with-me', { credentials: 'include' });
        if (response.ok) {
          const data = await response.json();
          setSharedPlaylists(data.sharedPlaylists || []);
        } else {
          setError('Failed to load shared playlists');
        }
      } catch (err: any) {
        setError(err.message || 'Failed to load shared playlists');
      } finally {
        setIsLoading(false);
      }
    })();
  }, []);

  return (
    <Modal onClose={onClose} contentStyle={{ maxWidth: '560px' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 style={{ margin: 0 }}>Shared With Me</h2>
          <button onClick={onClose} title="Close" style={modalCloseButtonStyle}>✕</button>
        </div>
        <p className="share-description">Playlists other Playlist Lab users have shared with you.</p>

        {error && <div className="error-message">{error}</div>}

        {isLoading ? (
          <div className="loading">Loading...</div>
        ) : sharedPlaylists.length === 0 ? (
          <div className="empty-state" style={{ textAlign: 'center', padding: '1.5rem' }}>
            <p>You don't have any playlists shared with you yet.</p>
          </div>
        ) : (
          <div className="playlist-share-list">
            {sharedPlaylists.map((shared) => (
              <div key={shared.id} className="playlist-share-item">
                <div className="playlist-share-info">
                  <div className="playlist-share-name">{shared.playlistName}</div>
                  <div className="playlist-share-meta">
                    Shared by {shared.sharedByUsername} • {new Date(shared.sharedAt).toLocaleDateString()}
                  </div>
                </div>
                <a href={`/playlists/${shared.plexPlaylistId}`} className="btn btn-primary btn-small">View</a>
              </div>
            ))}
          </div>
        )}

        <div className="modal-actions">
          <button className="btn btn-secondary" onClick={onClose}>Close</button>
        </div>
    </Modal>
  );
}
