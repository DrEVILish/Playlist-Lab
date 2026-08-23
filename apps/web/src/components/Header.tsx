import type { FC } from 'react';
import { useState, Suspense, lazy } from 'react';
import { createPortal } from 'react-dom';
import { Link } from 'react-router-dom';
import { MobileNav } from './MobileNav';
import { HeaderActivity } from './HeaderActivity';
import { StatusReportsModal } from './StatusReportsModal';
import { Modal } from './Modal';
import { SharedWithMeModal } from './playlist-panel/SharedWithMeModal';
import { useApp } from '../contexts/AppContext';
import { useConfirm } from '../contexts/ConfirmContext';
import { useToast } from '../contexts/ToastContext';
import './Header.css';

// Lazy-loaded: the Header (and therefore these buttons) is mounted on every
// authenticated page, but ImportPage/GenerateMixesPage/BackupRestorePage are
// large and usually opened well after initial load - splitting them into
// their own chunks keeps the landing page from paying for code most
// sessions never touch.
const ImportPage = lazy(() => import('../pages/ImportPage').then(m => ({ default: m.ImportPage })));
const GenerateMixesPage = lazy(() => import('../pages/GenerateMixesPage').then(m => ({ default: m.GenerateMixesPage })));
const BackupRestorePage = lazy(() => import('../pages/BackupRestorePage').then(m => ({ default: m.BackupRestorePage })));

const ModalFallback = () => <div style={{ padding: '3rem', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading...</div>;

interface HeaderProps {
  user?: { plexUsername: string; plexThumb?: string } | null;
  onLogout?: () => void;
}

export const Header: FC<HeaderProps> = ({ user, onLogout }) => {
  const { updateInfo, isUpdating, installUpdate } = useApp();
  const confirmDialog = useConfirm();
  const toast = useToast();
  const [showImport, setShowImport] = useState(false);
  const [showGenerate, setShowGenerate] = useState(false);
  const [showBackup, setShowBackup] = useState(false);
  const [showSharedWithMe, setShowSharedWithMe] = useState(false);
  const [showStatus, setShowStatus] = useState(false);

  const handleUpdate = async () => {
    if (!await confirmDialog(`Update to version ${updateInfo?.latestVersion}?\n\nThe application will restart automatically.`, { title: 'Update Playlist Lab?', confirmLabel: 'Update', danger: false })) return;
    try {
      await installUpdate();
    } catch (err) {
      toast.error(`Update failed: ${err instanceof Error ? err.message : 'Unknown error'}`);
    }
  };

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
              <button className="btn btn-secondary btn-small" onClick={() => setShowSharedWithMe(true)}>Shared With Me</button>
              <button className="btn btn-secondary btn-small" onClick={() => setShowBackup(true)}>Backup / Restore</button>
              <button className="btn btn-secondary btn-small" onClick={() => setShowStatus(true)}>Status</button>
              {updateInfo?.updateAvailable && (
                <button
                  className="btn btn-primary btn-small"
                  onClick={handleUpdate}
                  disabled={isUpdating}
                  title={`Update to v${updateInfo.latestVersion}`}
                >
                  {isUpdating ? 'Updating...' : 'Update Available'}
                </button>
              )}
            </nav>
          )}
        </div>

        {user && (
          <div className="header-user">
            <HeaderActivity />
            {user.plexThumb && (
              <img
                src={user.plexThumb}
                alt={user.plexUsername}
                className="header-user-avatar"
              />
            )}
            <span className="header-user-name">{user.plexUsername}</span>
            <Link to="/settings" className="header-settings-btn" title="Settings" aria-label="Settings">
              <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <circle cx="12" cy="12" r="3" />
                <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
              </svg>
            </Link>
            {onLogout && (
              <button onClick={onLogout} className="btn btn-secondary btn-logout">
                Logout
              </button>
            )}
          </div>
        )}
      </div>

      {showImport && createPortal(
        <Modal onClose={() => setShowImport(false)} contentStyle={{ maxWidth: '95vw', width: '1200px', maxHeight: '90vh', overflow: 'auto' }}>
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
            <button className="btn btn-secondary btn-small" onClick={() => setShowImport(false)}>Close</button>
          </div>
          <Suspense fallback={<ModalFallback />}><ImportPage /></Suspense>
        </Modal>,
        document.body
      )}

      {showGenerate && createPortal(
        <Modal onClose={() => setShowGenerate(false)} contentStyle={{ maxWidth: '95vw', width: '1200px', maxHeight: '90vh', overflow: 'auto' }}>
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
            <button className="btn btn-secondary btn-small" onClick={() => setShowGenerate(false)}>Close</button>
          </div>
          <Suspense fallback={<ModalFallback />}><GenerateMixesPage onNavigateAway={() => setShowGenerate(false)} /></Suspense>
        </Modal>,
        document.body
      )}

      {showBackup && createPortal(
        <Modal onClose={() => setShowBackup(false)} contentStyle={{ maxWidth: '900px', width: '95vw', maxHeight: '90vh', overflow: 'auto' }}>
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
            <button className="btn btn-secondary btn-small" onClick={() => setShowBackup(false)}>Close</button>
          </div>
          <Suspense fallback={<ModalFallback />}><BackupRestorePage /></Suspense>
        </Modal>,
        document.body
      )}

      {showSharedWithMe && createPortal(
        <SharedWithMeModal onClose={() => setShowSharedWithMe(false)} />,
        document.body
      )}

      {showStatus && createPortal(
        <StatusReportsModal onClose={() => setShowStatus(false)} />,
        document.body
      )}
    </header>
  );
};
