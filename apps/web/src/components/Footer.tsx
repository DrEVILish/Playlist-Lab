import type { FC } from 'react';
import { useState, useEffect } from 'react';
import './Footer.css';

export const Footer: FC = () => {
  const [version, setVersion] = useState<string>('');
  const [updateAvailable, setUpdateAvailable] = useState(false);
  const [updateInfo, setUpdateInfo] = useState<any>(null);
  const [isUpdating, setIsUpdating] = useState(false);

  // Fetch version on mount and poll for changes (detects post-update server restart)
  useEffect(() => {
    let initialVersion = '';

    const fetchVersion = async () => {
      try {
        const res = await fetch('/api/version', { credentials: 'include' });
        if (res.ok) {
          const data = await res.json();
          if (!initialVersion) {
            initialVersion = data.version;
            setVersion(data.version);
          } else if (data.version !== initialVersion) {
            window.location.reload();
          }
        }
      } catch {
        // Silently fail - server might be restarting
      }
    };

    fetchVersion();
    const interval = setInterval(fetchVersion, 30 * 1000);
    return () => clearInterval(interval);
  }, []);

  useEffect(() => {
    const checkForUpdates = async () => {
      try {
        const res = await fetch('/api/update/check', { credentials: 'include' });
        if (res.ok) {
          const data = await res.json();
          setUpdateAvailable(data.updateAvailable);
          setUpdateInfo(data);
        }
      } catch {
        // Silently fail
      }
    };

    checkForUpdates();
    const interval = setInterval(checkForUpdates, 6 * 60 * 60 * 1000);
    return () => clearInterval(interval);
  }, []);

  const handleUpdate = async () => {
    if (!confirm(`Update to version ${updateInfo.latestVersion}?\n\nThe application will restart automatically.`)) {
      return;
    }
    setIsUpdating(true);
    try {
      const res = await fetch('/api/update/install', { method: 'POST', credentials: 'include' });
      if (!res.ok) {
        const data = await res.json();
        alert(`Update failed: ${data.error || 'Unknown error'}`);
        setIsUpdating(false);
      }
      // On success, keep showing "Updating..." until the server restarts and version-poll reloads the page.
    } catch (err) {
      if (err instanceof Error && err.message.includes('Failed to fetch')) {
        // Server is restarting - expected, keep showing "Updating..."
      } else {
        alert(`Update failed: ${err instanceof Error ? err.message : 'Unknown error'}`);
        setIsUpdating(false);
      }
    }
  };

  return (
    <footer className="footer">
      <div className="footer-content">
        <p className="footer-text">
          © {new Date().getFullYear()} Playlist Lab. Multi-user web application for managing Plex playlists.
        </p>
        {version && (
          <div className="footer-version">
            <span>v{version}</span>
            {updateAvailable && (
              <button
                className="footer-update-btn"
                onClick={handleUpdate}
                disabled={isUpdating}
                title={`Update to v${updateInfo?.latestVersion}`}
              >
                {isUpdating ? 'Updating...' : 'Update'}
              </button>
            )}
          </div>
        )}
      </div>
    </footer>
  );
};
