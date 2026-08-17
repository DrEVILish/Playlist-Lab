import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider, useAuth } from './contexts/AuthContext';
import { AppProvider } from './contexts/AppContext';
import { ProtectedRoute } from './components/ProtectedRoute';
import { Layout } from './components/Layout';
import { LoginPage } from './pages/LoginPage';
import { AuthCallbackPage } from './pages/AuthCallbackPage';
import { ImportPage } from './pages/ImportPage';
import { QueuePage } from './pages/QueuePage';
import { CrossImportPage } from './pages/CrossImportPage';
import { GenerateMixesPage } from './pages/GenerateMixesPage';
import { PlaylistsPage } from './pages/PlaylistsPage';
import { PlexHomePage } from './pages/PlexHomePage';
import { SchedulesPage } from './pages/SchedulesPage';
import { SettingsPage } from './pages/SettingsPage';
import { AdminPage } from './pages/AdminPage';

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
        {/* Home is the unified playlist table (see pages/PlaylistsPage.tsx) -
            edit/schedule/reimport/share/export/backup all live there as row
            actions. Import and Generate Mixes open as modals from the header
            (see components/Header.tsx); their routes stay for bookmarks. */}
        <Route index element={<PlaylistsPage />} />
        <Route path="import" element={<ImportPage />} />
        <Route path="queue" element={<QueuePage />} />
        <Route path="cross-import" element={<CrossImportPage />} />
        <Route path="generate" element={<GenerateMixesPage />} />
        <Route path="playlists" element={<Navigate to="/" replace />} />
        <Route path="playlists/edit" element={<Navigate to="/" replace />} />
        <Route path="playlists/share" element={<Navigate to="/" replace />} />
        <Route path="playlists/export" element={<Navigate to="/" replace />} />
        <Route path="playlists/backup" element={<Navigate to="/" replace />} />
        <Route path="playlists/missing" element={<Navigate to="/" replace />} />
        <Route path="playlists/home-users" element={<PlexHomePage />} />
        <Route path="schedules" element={<SchedulesPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="admin" element={<AdminPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <AppProvider>
          <AppRoutes />
        </AppProvider>
      </AuthProvider>
    </BrowserRouter>
  );
}

export default App;
