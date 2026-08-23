import { useState, useEffect } from 'react';
import { Modal } from '../Modal';
import '../../pages/SharePlaylistsPage.css';

interface PlexFriend {
  username: string;
  email: string;
  thumb?: string;
  friendlyName?: string;
}

/**
 * Share-with-Plex-friends dialog for a single playlist. Extracted from the
 * former standalone Share Playlists page so it can be launched as a row
 * action from the unified playlist control panel.
 */
export function ShareModal({ playlistId, playlistName, onClose }: { playlistId: string; playlistName: string; onClose: () => void }) {
  const [friends, setFriends] = useState<PlexFriend[]>([]);
  const [selectedFriends, setSelectedFriends] = useState<Set<string>>(new Set());
  const [isLoadingFriends, setIsLoadingFriends] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [sharing, setSharing] = useState(false);
  const [shareSuccess, setShareSuccess] = useState<string | null>(null);

  useEffect(() => {
    const loadFriends = async () => {
      setIsLoadingFriends(true);
      setError(null);
      try {
        const response = await fetch('/api/plex/friends', { credentials: 'include' });
        if (response.ok) {
          const data = await response.json();
          setFriends(data.friends || []);
        } else {
          setError('Failed to load Plex friends');
        }
      } catch (err: any) {
        setError(err.message || 'Failed to load Plex friends');
      } finally {
        setIsLoadingFriends(false);
      }
    };
    loadFriends();
  }, []);

  const handleToggleFriend = (username: string) => {
    const newSelected = new Set(selectedFriends);
    if (newSelected.has(username)) newSelected.delete(username);
    else newSelected.add(username);
    setSelectedFriends(newSelected);
  };

  const handleSelectAll = () => {
    setSelectedFriends(selectedFriends.size === friends.length ? new Set() : new Set(friends.map(f => f.username)));
  };

  const handleShareWithSelected = async () => {
    if (selectedFriends.size === 0) return;
    setSharing(true);
    setError(null);
    setShareSuccess(null);

    try {
      const friendsList = Array.from(selectedFriends);
      const results = await Promise.allSettled(
        friendsList.map(friendUsername =>
          fetch(`/api/playlists/${playlistId}/share-to-friend`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'include',
            body: JSON.stringify({ friendUsername }),
          })
        )
      );

      const successful = results.filter(r => r.status === 'fulfilled').length;
      const failed = results.filter(r => r.status === 'rejected').length;

      if (successful > 0) setShareSuccess(`Shared "${playlistName}" with ${successful} friend${successful > 1 ? 's' : ''}`);
      if (failed > 0) setError(`Failed to share with ${failed} friend${failed > 1 ? 's' : ''}`);
      setSelectedFriends(new Set());
    } catch (err: any) {
      setError(err.message || 'Failed to share playlist');
    } finally {
      setSharing(false);
    }
  };

  return (
    <Modal onClose={onClose}>
        <h2>Share Playlist</h2>
        <p>Share "{playlistName}" with:</p>

        {error && <div className="error-message">{error}</div>}
        {shareSuccess && <div className="success-message">{shareSuccess}</div>}

        {isLoadingFriends ? (
          <div className="loading">Loading friends...</div>
        ) : (
          <>
            {friends.length > 1 && (
              <div className="select-all-container">
                <label className="checkbox-label">
                  <input type="checkbox" checked={selectedFriends.size === friends.length} onChange={handleSelectAll} />
                  <span>Select All</span>
                </label>
              </div>
            )}

            <div className="share-target-list">
              {friends.map(friend => (
                <label key={friend.username} className="share-target-item checkbox-item">
                  <input
                    type="checkbox"
                    checked={selectedFriends.has(friend.username)}
                    onChange={() => handleToggleFriend(friend.username)}
                    disabled={sharing}
                  />
                  {friend.thumb && <img src={friend.thumb} alt={friend.username} className="friend-avatar" />}
                  <span className="friend-name">{friend.friendlyName || friend.username}</span>
                </label>
              ))}
            </div>

            {friends.length === 0 && (
              <div className="empty-state">
                <p>No Plex friends available to share with. Add friends in your Plex account settings and grant them library access.</p>
              </div>
            )}
          </>
        )}

        <div className="modal-actions">
          <button className="btn btn-secondary" onClick={onClose} disabled={sharing}>Close</button>
          <button
            className="btn btn-primary"
            onClick={handleShareWithSelected}
            disabled={sharing || selectedFriends.size === 0}
          >
            {sharing ? 'Sharing...' : `Share with ${selectedFriends.size} friend${selectedFriends.size !== 1 ? 's' : ''}`}
          </button>
        </div>
    </Modal>
  );
}
