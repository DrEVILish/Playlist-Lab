import { useState, useEffect } from 'react';
import { Modal, modalCloseButtonStyle } from '../Modal';
import { useApp } from '../../contexts/AppContext';
import '../../pages/SharePlaylistsPage.css';

interface ShareTarget {
  id: number;
  username: string;
  thumb?: string;
}

/**
 * Share-with-another-Playlist-Lab-user dialog for a single playlist. Copies
 * the playlist into the target user's own Plex library - a one-time copy,
 * not a live link, so edits on either side never propagate to the other.
 */
export function ShareModal({ playlistId, playlistName, onClose }: { playlistId: string; playlistName: string; onClose: () => void }) {
  const { apiClient } = useApp();
  const [users, setUsers] = useState<ShareTarget[]>([]);
  const [selectedUsers, setSelectedUsers] = useState<Set<number>>(new Set());
  const [isLoadingUsers, setIsLoadingUsers] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [sharing, setSharing] = useState(false);
  const [shareSuccess, setShareSuccess] = useState<string | null>(null);

  useEffect(() => {
    const loadUsers = async () => {
      setIsLoadingUsers(true);
      setError(null);
      try {
        const { users } = await apiClient.getShareTargets();
        setUsers(users);
      } catch (err: any) {
        setError(err.message || 'Failed to load users');
      } finally {
        setIsLoadingUsers(false);
      }
    };
    loadUsers();
  }, [apiClient]);

  const handleToggleUser = (id: number) => {
    const newSelected = new Set(selectedUsers);
    if (newSelected.has(id)) newSelected.delete(id);
    else newSelected.add(id);
    setSelectedUsers(newSelected);
  };

  const handleSelectAll = () => {
    setSelectedUsers(selectedUsers.size === users.length ? new Set() : new Set(users.map(u => u.id)));
  };

  const handleShareWithSelected = async () => {
    if (selectedUsers.size === 0) return;
    setSharing(true);
    setError(null);
    setShareSuccess(null);

    try {
      const targetIds = Array.from(selectedUsers);
      const results = await Promise.allSettled(
        targetIds.map(targetUserId => apiClient.sharePlaylist(playlistId, targetUserId))
      );

      const successful = results.filter(r => r.status === 'fulfilled').length;
      const failed = results.filter(r => r.status === 'rejected').length;

      if (successful > 0) setShareSuccess(`Shared "${playlistName}" with ${successful} user${successful > 1 ? 's' : ''}`);
      if (failed > 0) setError(`Failed to share with ${failed} user${failed > 1 ? 's' : ''}`);
      setSelectedUsers(new Set());
    } catch (err: any) {
      setError(err.message || 'Failed to share playlist');
    } finally {
      setSharing(false);
    }
  };

  return (
    <Modal onClose={onClose}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 style={{ margin: 0 }}>Share Playlist</h2>
          <button onClick={onClose} title="Close" style={modalCloseButtonStyle}>✕</button>
        </div>
        <p>Share "{playlistName}" with:</p>

        {error && <div className="error-message">{error}</div>}
        {shareSuccess && <div className="success-message">{shareSuccess}</div>}

        {isLoadingUsers ? (
          <div className="loading">Loading users...</div>
        ) : (
          <>
            {users.length > 1 && (
              <div className="select-all-container">
                <label className="checkbox-label">
                  <input type="checkbox" checked={selectedUsers.size === users.length} onChange={handleSelectAll} />
                  <span>Select All</span>
                </label>
              </div>
            )}

            <div className="share-target-list">
              {users.map(user => (
                <label key={user.id} className="share-target-item checkbox-item">
                  <input
                    type="checkbox"
                    checked={selectedUsers.has(user.id)}
                    onChange={() => handleToggleUser(user.id)}
                    disabled={sharing}
                  />
                  {user.thumb && <img src={user.thumb} alt={user.username} className="friend-avatar" />}
                  <span className="friend-name">{user.username}</span>
                </label>
              ))}
            </div>

            {users.length === 0 && (
              <div className="empty-state">
                <p>No other Playlist Lab users on this server yet.</p>
              </div>
            )}
          </>
        )}

        <div className="modal-actions">
          <button className="btn btn-secondary" onClick={onClose} disabled={sharing}>Close</button>
          <button
            className="btn btn-primary"
            onClick={handleShareWithSelected}
            disabled={sharing || selectedUsers.size === 0}
          >
            {sharing ? 'Sharing...' : `Share with ${selectedUsers.size} user${selectedUsers.size !== 1 ? 's' : ''}`}
          </button>
        </div>
    </Modal>
  );
}
