import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider, useAuth } from './contexts/AuthContext';
import { AppProvider } from './contexts/AppContext';
import { ProtectedRoute } from './components/ProtectedRoute';
import { Layout } from './components/Layout';
import { LoginPage } from './pages/LoginPage';
import { AuthCallbackPage } from './pages/AuthCallbackPage';
import { ImportPage } from './pages/ImportPage';
import { GenerateMixesPage } from './pages/GenerateMixesPage';
import { HomePage } from './pages/HomePage';
import { SettingsPage } from './pages/SettingsPage';

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
        {/* Home = playlists + schedules combined (see pages/HomePage.tsx).
            Import and Generate Mixes open as modals from the header (see
            components/Header.tsx); their routes stay for bookmarks. Queue,
            Plex Home Users, Export to YouTube and Admin are no longer nav
            destinations - they moved into the header activity indicator,
            the Import menu, the per-playlist Export menu, and the Settings
            page respectively. */}
        <Route index element={<HomePage />} />
        <Route path="import" element={<ImportPage />} />
        <Route path="generate" element={<GenerateMixesPage />} />
        <Route path="settings" element={<SettingsPage />} />
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
