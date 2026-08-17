import type { FC } from 'react';
import { useState, useEffect } from 'react';
import { useApp } from '../contexts/AppContext';

interface PlexHomeUser {
  id: string;
  title: string;
  username: string;
  thumb?: string;
}

interface HomeUserPlaylist {
  id: string;
  name: string;
  trackCount: number;
}

/**
 * Compact "import a copy of a Plex Home user's playlist into my own
 * account" flow, used as an Import-menu source tab. This replaces the old
 * standalone Plex Home Users page's role as a navigation destination; that
 * page's deeper per-user playlist editor (track reordering, cover upload,
 * add/replace tracks) isn't reachable from here anymore since it's playlist
 * management rather than import - only the copy-to-self action moved over.
 */
export const ImportFromPlexHome: FC = () => {
  const { refreshPlaylists } = useApp();
  const [users, setUsers] = useState<PlexHomeUser[]>([]);
  const [isLoadingUsers, setIsLoadingUsers] = useState(true);
  const [selectedUser, setSelectedUser] = useState<PlexHomeUser | null>(null);
  const [playlists, setPlaylists] = useState<HomeUserPlaylist[]>([]);
  const [isLoadingPlaylists, setIsLoadingPlaylists] = useState(false);
  const [importingId, setImportingId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      setIsLoadingUsers(true);
      setError(null);
      try {
        const res = await fetch('/api/plex-home/users', { credentials: 'include' });
        if (res.ok) {
          const data = await res.json();
          setUsers(data.homeUsers || []);
        } else {
          const data = await res.json().catch(() => ({}));
          setError(data.error?.message || 'Failed to load Plex Home users');
        }
      } catch (err: any) {
        setError(err.message || 'Failed to load Plex Home users');
      } finally {
        setIsLoadingUsers(false);
      }
    })();
  }, []);

  const selectUser = async (user: PlexHomeUser) => {
    setSelectedUser(user);
    setPlaylists([]);
    setIsLoadingPlaylists(true);
    setError(null);
    try {
      const res = await fetch(`/api/plex-home/users/${user.id}/playlists`, { credentials: 'include' });
      if (res.ok) {
        const data = await res.json();
        setPlaylists(data.playlists || []);
      } else {
        const data = await res.json().catch(() => ({}));
        setError(data.error?.message || 'Failed to load playlists');
      }
    } catch (err: any) {
      setError(err.message || 'Failed to load playlists');
    } finally {
      setIsLoadingPlaylists(false);
    }
  };

  const handleImport = async (playlist: HomeUserPlaylist) => {
    if (!selectedUser) return;
    setImportingId(playlist.id);
    setError(null);
    setSuccess(null);
    try {
      const res = await fetch(`/api/plex-home/playlists/${playlist.id}/copy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({
          sourceHomeUserId: selectedUser.id,
          targetHomeUserId: 'current',
          newName: playlist.name,
        }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        throw new Error(data.error?.message || 'Failed to import playlist');
      }
      setSuccess(`Imported "${playlist.name}" from ${selectedUser.title}`);
      await refreshPlaylists();
    } catch (err: any) {
      setError(err.message || 'Failed to import playlist');
    } finally {
      setImportingId(null);
    }
  };

  return (
    <div>
      <p style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '1rem' }}>
        Pick a Plex Home user, then a playlist of theirs, to copy it into your own account.
      </p>

      {error && <div className="error-message" style={{ marginBottom: '1rem' }}>{error}</div>}
      {success && <div className="settings-alert settings-alert--success" style={{ marginBottom: '1rem' }}>{success}</div>}

      {isLoadingUsers ? (
        <div style={{ padding: '1rem', color: 'var(--text-secondary)' }}>Loading Plex Home users...</div>
      ) : users.length === 0 ? (
        <div style={{ padding: '1rem', color: 'var(--text-secondary)' }}>No other Plex Home users found on this server.</div>
      ) : (
        <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '1.5rem' }}>
          {users.map(user => (
            <button
              key={user.id}
              className={`btn ${selectedUser?.id === user.id ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => selectUser(user)}
            >
              {user.title}
            </button>
          ))}
        </div>
      )}

      {selectedUser && (
        isLoadingPlaylists ? (
          <div style={{ padding: '1rem', color: 'var(--text-secondary)' }}>Loading playlists...</div>
        ) : playlists.length === 0 ? (
          <div style={{ padding: '1rem', color: 'var(--text-secondary)' }}>{selectedUser.title} has no playlists.</div>
        ) : (
          <div style={{ display: 'grid', gap: '0.5rem' }}>
            {playlists.map(playlist => (
              <div key={playlist.id} className="card" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '0.75rem 1rem' }}>
                <div>
                  <div style={{ fontWeight: 500 }}>{playlist.name}</div>
                  <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>{playlist.trackCount} tracks</div>
                </div>
                <button
                  className="btn btn-primary btn-small"
                  onClick={() => handleImport(playlist)}
                  disabled={importingId === playlist.id}
                >
                  {importingId === playlist.id ? 'Importing...' : 'Import'}
                </button>
              </div>
            ))}
          </div>
        )
      )}
    </div>
  );
};
