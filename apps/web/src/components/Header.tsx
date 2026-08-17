import type { FC } from 'react';
import { useState } from 'react';
import { createPortal } from 'react-dom';
import { Link } from 'react-router-dom';
import { MobileNav } from './MobileNav';
import { ImportPage } from '../pages/ImportPage';
import { GenerateMixesPage } from '../pages/GenerateMixesPage';
import './Header.css';

interface HeaderProps {
  user?: { plexUsername: string; plexThumb?: string } | null;
  onLogout?: () => void;
}

export const Header: FC<HeaderProps> = ({ user, onLogout }) => {
  const [showImport, setShowImport] = useState(false);
  const [showGenerate, setShowGenerate] = useState(false);

  return (
    <header className="header">
      <div className="header-content">
        <div className="header-left">
          <MobileNav />
          <Link to="/" className="header-logo">
            <img src="/logo.svg" alt="Playlist Lab" className="header-logo-icon" />
            <h1><span className="logo-playlist">Playlist </span><span className="logo-lab">Lab</span></h1>
          </Link>
          {user && (
            <nav className="header-actions">
              <button className="btn btn-secondary btn-small" onClick={() => setShowImport(true)}>Import</button>
              <button className="btn btn-secondary btn-small" onClick={() => setShowGenerate(true)}>Generate</button>
            </nav>
          )}
        </div>

        {user && (
          <div className="header-user">
            <Link to="/settings" className="header-settings-btn" title="Settings" aria-label="Settings">
              <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <circle cx="12" cy="12" r="3" />
                <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
              </svg>
            </Link>
            {user.plexThumb && (
              <img
                src={user.plexThumb}
                alt={user.plexUsername}
                className="header-user-avatar"
              />
            )}
            <span className="header-user-name">{user.plexUsername}</span>
            {onLogout && (
              <button onClick={onLogout} className="btn btn-secondary btn-logout">
                Logout
              </button>
            )}
          </div>
        )}
      </div>

      {showImport && createPortal(
        <div className="modal-overlay" onClick={() => setShowImport(false)}>
          <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '95vw', width: '1200px', maxHeight: '90vh', overflow: 'auto' }}>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
              <button className="btn btn-secondary btn-small" onClick={() => setShowImport(false)}>Close</button>
            </div>
            <ImportPage />
          </div>
        </div>,
        document.body
      )}

      {showGenerate && createPortal(
        <div className="modal-overlay" onClick={() => setShowGenerate(false)}>
          <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '95vw', width: '1200px', maxHeight: '90vh', overflow: 'auto' }}>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
              <button className="btn btn-secondary btn-small" onClick={() => setShowGenerate(false)}>Close</button>
            </div>
            <GenerateMixesPage />
          </div>
        </div>,
        document.body
      )}
    </header>
  );
};
