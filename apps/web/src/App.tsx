import { Suspense, lazy, useEffect } from 'react';
import { BrowserRouter, Routes, Route, Navigate, useNavigate } from 'react-router-dom';
import { AuthProvider, useAuth } from './contexts/AuthContext';
import { AppProvider } from './contexts/AppContext';
import { ConfirmProvider } from './contexts/ConfirmContext';
import { ToastProvider } from './contexts/ToastContext';
import { ProtectedRoute } from './components/ProtectedRoute';
import { ErrorBoundary, CHUNK_RELOAD_GUARD_KEY } from './components/ErrorBoundary';
import { Layout } from './components/Layout';
import { LoginPage } from './pages/LoginPage';
import { AuthCallbackPage } from './pages/AuthCallbackPage';
import { PlaylistsPage } from './pages/PlaylistsPage';
import { SharedWithMeModal } from './components/playlist-panel/SharedWithMeModal';
import { StatusReportsModal } from './components/StatusReportsModal';

// Lazy-loaded: these are large (ImportPage alone is 4000+ lines covering a
// dozen import sources) and, via the header's Import/Generate buttons, are
// usually opened well after initial load rather than needed for the first
// paint - splitting them into their own chunks keeps the landing page (the
// playlist table) from paying for code most sessions never touch.
const ImportPage = lazy(() => import('./pages/ImportPage').then(m => ({ default: m.ImportPage })));
const GenerateMixesPage = lazy(() => import('./pages/GenerateMixesPage').then(m => ({ default: m.GenerateMixesPage })));
const SettingsPage = lazy(() => import('./pages/SettingsPage').then(m => ({ default: m.SettingsPage })));
const BackupRestorePage = lazy(() => import('./pages/BackupRestorePage').then(m => ({ default: m.BackupRestorePage })));

const RouteFallback = () => <div style={{ padding: '3rem', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading...</div>;

// Shared With Me and Status are modal-shaped components (they render their
// own <Modal>, not a page layout) - on desktop the header opens them over
// whatever page is behind them, but they also need real routes so MobileNav
// (which only supports route links, not lifting modal-open state up from
// Header) can reach them at all. Routing straight to the modal works fine
// since Modal's overlay is position:fixed; "closing" just navigates home.
const SharedWithMeRoute = () => {
  const navigate = useNavigate();
  return <SharedWithMeModal onClose={() => navigate('/')} />;
};
const StatusRoute = () => {
  const navigate = useNavigate();
  return <StatusReportsModal onClose={() => navigate('/')} />;
};

function AppRoutes() {
  const { user, logout } = useAuth();

  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/auth/callback" element={<AuthCallbackPage />} />
      <Route
        path="/"
        element={
          <ProtectedRoute>
            <Layout user={user} onLogout={logout} />
          </ProtectedRoute>
        }
      >
        {/* Import and Generate Mixes open as modals from the header (see
            components/Header.tsx); their routes stay for bookmarks. Queue,
            Plex Home Users, Export to YouTube and Admin are no longer nav
            destinations - they moved into the header activity indicator,
            the Import menu, the per-playlist Export menu, and the Settings
            page respectively. */}
        <Route index element={<PlaylistsPage />} />
        <Route path="import" element={<Suspense fallback={<RouteFallback />}><ImportPage /></Suspense>} />
        <Route path="generate" element={<Suspense fallback={<RouteFallback />}><GenerateMixesPage /></Suspense>} />
        <Route path="settings" element={<Suspense fallback={<RouteFallback />}><SettingsPage /></Suspense>} />
        <Route path="backup" element={<Suspense fallback={<RouteFallback />}><BackupRestorePage /></Suspense>} />
        <Route path="shared-with-me" element={<SharedWithMeRoute />} />
        <Route path="status" element={<StatusRoute />} />
        <Route path="playlists" element={<Navigate to="/" replace />} />
        <Route path="playlists/*" element={<Navigate to="/" replace />} />
        <Route path="schedules" element={<Navigate to="/" replace />} />
        <Route path="queue" element={<Navigate to="/" replace />} />
        <Route path="cross-import" element={<Navigate to="/" replace />} />
        <Route path="admin" element={<Navigate to="/settings" replace />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

function App() {
  // A page that reaches this render without an ErrorBoundary trip is a
  // sign the currently-loaded chunks are good - clear the reload guard
  // (see ErrorBoundary.tsx) so a *future* stale-chunk error after another
  // deploy still gets its one automatic reload instead of being
  // permanently suppressed for the rest of this tab's lifetime.
  useEffect(() => {
    sessionStorage.removeItem(CHUNK_RELOAD_GUARD_KEY);
  }, []);

  return (
    <BrowserRouter>
      <ErrorBoundary>
        <AuthProvider>
          <AppProvider>
            <ToastProvider>
              <ConfirmProvider>
                <AppRoutes />
              </ConfirmProvider>
            </ToastProvider>
          </AppProvider>
        </AuthProvider>
      </ErrorBoundary>
    </BrowserRouter>
  );
}

export default App;
