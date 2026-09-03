import type { FC } from 'react';
import { useState, useEffect, useMemo } from 'react';
import { useApp } from '../contexts/AppContext';
import { useConfirm } from '../contexts/ConfirmContext';
import { useToast } from '../contexts/ToastContext';
import type { User, DeemixSettings, Schedule } from '@playlist-lab/shared';
import { getNextRunTimestamp } from '../utils/scheduleTime';
import './AdminPage.css';

interface AdminStats {
  userCount: number;
  activeUsers: number;
  playlistCount: number;
  missingTrackCount: number;
}

interface MissingTrackStat {
  title: string;
  artist: string;
  count: number;
  addedAt: number;
}

type MissingTrackSortKey = 'title' | 'artist' | 'count' | 'addedAt';

interface JobStatus {
  name: string;
  status: string;
  lastRun?: number;
  nextRun?: number | null;
}

type UserSchedule = Schedule & { username: string; playlistName: string | null };

const DEEMIX_TAG_FIELDS: Array<[key: string, label: string]> = [
  ['title', 'Title'],
  ['artist', 'Artist'],
  ['artists', 'All artists'],
  ['album', 'Album'],
  ['albumArtist', 'Album artist'],
  ['trackNumber', 'Track number'],
  ['trackTotal', 'Track total'],
  ['discNumber', 'Disc number'],
  ['discTotal', 'Disc total'],
  ['genre', 'Genre'],
  ['year', 'Year'],
  ['date', 'Date'],
  ['label', 'Label'],
  ['isrc', 'ISRC'],
  ['barcode', 'Barcode'],
  ['bpm', 'BPM'],
  ['length', 'Length'],
  ['explicit', 'Explicit flag'],
  ['cover', 'Cover art (embedded)'],
  ['replayGain', 'Replay gain'],
  ['lyrics', 'Lyrics'],
  ['syncedLyrics', 'Synced lyrics'],
  ['copyright', 'Copyright'],
  ['composer', 'Composer'],
  ['involvedPeople', 'Involved people'],
  ['source', 'Source'],
  ['rating', 'Rating'],
];

export const AdminPage: FC = () => {
  const { apiClient } = useApp();
  const confirmDialog = useConfirm();
  const toast = useToast();
  const [deemixingTrack, setDeemixingTrack] = useState<string | null>(null);
  const [isDeemixingAll, setIsDeemixingAll] = useState(false);
  const [stats, setStats] = useState<AdminStats | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [missingTracks, setMissingTracks] = useState<MissingTrackStat[]>([]);
  const [missingTracksFilter, setMissingTracksFilter] = useState('');
  const [missingTracksSortKey, setMissingTracksSortKey] = useState<MissingTrackSortKey>('count');
  const [missingTracksSortDir, setMissingTracksSortDir] = useState<'asc' | 'desc'>('desc');
  const [jobs, setJobs] = useState<JobStatus[]>([]);
  const [userSchedules, setUserSchedules] = useState<UserSchedule[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<'stats' | 'users' | 'missing' | 'jobs' | 'deemix' | 'lidarr' | 'logs'>('stats');
  const [actionLoading, setActionLoading] = useState<number | null>(null);
  // Below this width the Users/Missing Tracks tables (which need room for
  // 5-6 columns including an actions column) are replaced with a card list
  // instead of forcing a horizontal scroll to reach the action buttons.
  const [isMobile, setIsMobile] = useState(() => window.matchMedia('(max-width: 768px)').matches);

  const [errorLogs, setErrorLogs] = useState<Array<{ level: string; message: string; timestamp: string | null; [key: string]: unknown }>>([]);
  const [logsTruncated, setLogsTruncated] = useState(false);
  const [isLogsLoading, setIsLogsLoading] = useState(false);
  const [logsError, setLogsError] = useState<string | null>(null);
  const [logLevelFilter, setLogLevelFilter] = useState('');
  const [isClearingLogs, setIsClearingLogs] = useState(false);
  const [logLevel, setLogLevel] = useState('info');
  const [isLogLevelSaving, setIsLogLevelSaving] = useState(false);

  const [deemixArl, setDeemixArl] = useState('');
  // Result of the server's last ARL check (daily, plus on save and startup).
  // ARLs expire every few months and nothing used to say so until a user
  // clicked Deemix and got an error.
  const [arlStatus, setArlStatus] = useState<{ ok: boolean; error?: string; at: number } | null>(null);
  const [isCheckingArl, setIsCheckingArl] = useState(false);
  const [isDeemixLoading, setIsDeemixLoading] = useState(false);
  const [isDeemixSaving, setIsDeemixSaving] = useState(false);
  const [deemixError, setDeemixError] = useState<string | null>(null);
  const [deemixSuccess, setDeemixSuccess] = useState<string | null>(null);

  const [deemixSettings, setDeemixSettings] = useState<DeemixSettings | null>(null);
  const [isSettingsLoading, setIsSettingsLoading] = useState(false);
  const [isSettingsSaving, setIsSettingsSaving] = useState(false);
  const [settingsError, setSettingsError] = useState<string | null>(null);
  const [settingsSuccess, setSettingsSuccess] = useState<string | null>(null);

  const [lidarrUrl, setLidarrUrl] = useState('');
  const [lidarrApiKey, setLidarrApiKey] = useState('');
  const [isLidarrLoading, setIsLidarrLoading] = useState(false);
  const [isLidarrSaving, setIsLidarrSaving] = useState(false);
  const [lidarrError, setLidarrError] = useState<string | null>(null);
  const [lidarrSuccess, setLidarrSuccess] = useState<string | null>(null);

  useEffect(() => {
    loadData();
  }, []);

  useEffect(() => {
    const mq = window.matchMedia('(max-width: 768px)');
    const handler = () => setIsMobile(mq.matches);
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, []);

  useEffect(() => {
    if (activeTab === 'deemix') {
      loadDeemixArl();
      loadDeemixSettings();
    } else if (activeTab === 'lidarr') {
      loadLidarrConfig();
    } else if (activeTab === 'logs') {
      loadErrorLogs();
      apiClient.getLogLevel().then(({ level }) => setLogLevel(level)).catch(() => {});
    }
  }, [activeTab, logLevelFilter]);

  const handleLogLevelChange = async (level: string) => {
    const previous = logLevel;
    setLogLevel(level);
    setIsLogLevelSaving(true);
    try {
      await apiClient.updateLogLevel(level);
      toast.success(`Logging level set to ${level}`);
    } catch (err) {
      setLogLevel(previous);
      toast.error(err instanceof Error ? err.message : 'Failed to update logging level');
    } finally {
      setIsLogLevelSaving(false);
    }
  };

  const loadLidarrConfig = async () => {
    setIsLidarrLoading(true);
    setLidarrError(null);
    try {
      const { url, apiKey } = await apiClient.getLidarrConfig();
      setLidarrUrl(url);
      setLidarrApiKey(apiKey);
    } catch (err) {
      setLidarrError(err instanceof Error ? err.message : 'Failed to load Lidarr config');
    } finally {
      setIsLidarrLoading(false);
    }
  };

  const handleSaveLidarrConfig = async () => {
    setIsLidarrSaving(true);
    setLidarrError(null);
    setLidarrSuccess(null);
    try {
      await apiClient.updateLidarrConfig(lidarrUrl, lidarrApiKey);
      setLidarrSuccess('Lidarr config saved.');
    } catch (err) {
      setLidarrError(err instanceof Error ? err.message : 'Failed to save Lidarr config');
    } finally {
      setIsLidarrSaving(false);
    }
  };

  const loadErrorLogs = async () => {
    setIsLogsLoading(true);
    setLogsError(null);
    try {
      const { entries, truncated } = await apiClient.getAdminLogs(200, logLevelFilter);
      setErrorLogs(entries);
      setLogsTruncated(truncated);
    } catch (err) {
      setLogsError(err instanceof Error ? err.message : 'Failed to load logs');
    } finally {
      setIsLogsLoading(false);
    }
  };

  const handleClearLogs = async () => {
    if (!await confirmDialog('Delete all log entries for every user? This cannot be undone.')) return;
    setIsClearingLogs(true);
    try {
      await apiClient.clearAdminLogs();
      toast.success('Logs cleared');
      await loadErrorLogs();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to clear logs');
    } finally {
      setIsClearingLogs(false);
    }
  };

  const loadDeemixSettings = async () => {
    setIsSettingsLoading(true);
    setSettingsError(null);
    try {
      const { settings } = await apiClient.getDeemixSettings();
      setDeemixSettings(settings);
    } catch (err) {
      setSettingsError(err instanceof Error ? err.message : 'Failed to load Deemix settings');
    } finally {
      setIsSettingsLoading(false);
    }
  };

  const handleSaveDeemixSettings = async () => {
    if (!deemixSettings) return;
    setIsSettingsSaving(true);
    setSettingsError(null);
    setSettingsSuccess(null);
    try {
      await apiClient.updateDeemixSettings(deemixSettings);
      setSettingsSuccess('Settings saved - deemix-server restarted to apply them.');
    } catch (err) {
      setSettingsError(err instanceof Error ? err.message : 'Failed to save Deemix settings');
    } finally {
      setIsSettingsSaving(false);
    }
  };

  const updateDeemixField = <K extends keyof DeemixSettings>(key: K, value: DeemixSettings[K]) => {
    setDeemixSettings(prev => prev ? { ...prev, [key]: value } : prev);
    setSettingsSuccess(null);
  };

  const updateDeemixTag = (key: string, value: boolean) => {
    setDeemixSettings(prev => prev ? { ...prev, tags: { ...prev.tags, [key]: value } } : prev);
    setSettingsSuccess(null);
  };

  const loadDeemixArl = async () => {
    setIsDeemixLoading(true);
    setDeemixError(null);
    try {
      const { arl, status } = await apiClient.getDeemixArl();
      setDeemixArl(arl);
      setArlStatus(status);
    } catch (err) {
      setDeemixError(err instanceof Error ? err.message : 'Failed to load Deemix ARL');
    } finally {
      setIsDeemixLoading(false);
    }
  };

  const handleSaveDeemixArl = async () => {
    setIsDeemixSaving(true);
    setDeemixError(null);
    setDeemixSuccess(null);
    try {
      await apiClient.updateDeemixArl(deemixArl);
      setDeemixSuccess('Deemix ARL saved.');
      // The save kicks off a check server-side; read back what it found.
      setArlStatus(null);
      await handleCheckArl();
    } catch (err) {
      setDeemixError(err instanceof Error ? err.message : 'Failed to save Deemix ARL');
    } finally {
      setIsDeemixSaving(false);
    }
  };

  const handleCheckArl = async () => {
    setIsCheckingArl(true);
    try {
      const result = await apiClient.checkDeemixArl();
      setArlStatus({ ...result, at: Date.now() });
    } catch (err) {
      setDeemixError(err instanceof Error ? err.message : 'Failed to check Deemix ARL');
    } finally {
      setIsCheckingArl(false);
    }
  };

  const loadData = async () => {
    setIsLoading(true);
    setError(null);
    try {
      const [statsData, usersData, missingData, jobsData, schedulesData] = await Promise.all([
        apiClient.getAdminStats(),
        apiClient.getAdminUsers(),
        apiClient.getAdminMissingTracks(),
        apiClient.getAdminJobs(),
        apiClient.getAdminSchedules(),
      ]);
      setStats(statsData);
      setUsers(usersData);
      setMissingTracks(missingData);
      setJobs(jobsData);
      setUserSchedules(schedulesData);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load admin data');
    } finally {
      setIsLoading(false);
    }
  };

  const handleEnableUser = async (userId: number) => {
    setActionLoading(userId);
    try {
      await apiClient.enableUser(userId);
      // Refresh users list
      const usersData = await apiClient.getAdminUsers();
      setUsers(usersData);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to enable user');
    } finally {
      setActionLoading(null);
    }
  };

  const handleDisableUser = async (userId: number) => {
    setActionLoading(userId);
    try {
      await apiClient.disableUser(userId);
      const usersData = await apiClient.getAdminUsers();
      setUsers(usersData);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to disable user');
    } finally {
      setActionLoading(null);
    }
  };

  const handleDeleteUser = async (userId: number, username: string) => {
    if (!await confirmDialog(`Are you sure you want to delete user "${username}" and all their data?`)) {
      return;
    }
    setActionLoading(userId);
    try {
      await apiClient.deleteUser(userId);
      const [usersData, statsData] = await Promise.all([
        apiClient.getAdminUsers(),
        apiClient.getAdminStats(),
      ]);
      setUsers(usersData);
      setStats(statsData);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete user');
    } finally {
      setActionLoading(null);
    }
  };

  // Each search+queue now runs through the server's shared action queue -
  // the actual deemix download count/outcome shows up per-track in the
  // notification bell rather than in this call's return value.
  const handleDeemixDownload = async (track: MissingTrackStat) => {
    const key = `${track.title}::${track.artist}`;
    setDeemixingTrack(key);
    try {
      const { position } = await apiClient.deemixDownloadAdmin(track.title, track.artist);
      toast.success(position > 0 ? `Queued "${track.title}" - position ${position} in queue` : `Queued "${track.title}" for deemix`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to queue deemix download');
    } finally {
      setDeemixingTrack(null);
    }
  };

  const handleDeemixAll = async () => {
    setIsDeemixingAll(true);
    try {
      // One server-side job rather than a request per row: the loop used to
      // run here and finish with a success toast while every search and
      // download it queued was still to happen.
      const { message } = await apiClient.deemixAllAdmin(
        visibleMissingTracks.map(t => ({ title: t.title, artist: t.artist }))
      );
      toast.success(message);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to start Deemix All');
    } finally {
      setIsDeemixingAll(false);
    }
  };

  const handleMissingTracksSort = (key: MissingTrackSortKey) => {
    if (key === missingTracksSortKey) {
      setMissingTracksSortDir(dir => (dir === 'asc' ? 'desc' : 'asc'));
    } else {
      setMissingTracksSortKey(key);
      setMissingTracksSortDir(key === 'title' || key === 'artist' ? 'asc' : 'desc');
    }
  };

  const renderUserActions = (user: User) => (
    <>
      {user.isEnabled ? (
        <button
          className="btn"
          style={{ fontSize: '0.75rem', padding: '0.3rem 0.6rem' }}
          onClick={() => handleDisableUser(user.id)}
          disabled={actionLoading === user.id}
        >
          Disable
        </button>
      ) : (
        <button
          className="btn btn-primary"
          style={{ fontSize: '0.75rem', padding: '0.3rem 0.6rem' }}
          onClick={() => handleEnableUser(user.id)}
          disabled={actionLoading === user.id}
        >
          Enable
        </button>
      )}
      <button
        className="btn"
        style={{
          fontSize: '0.75rem',
          padding: '0.3rem 0.6rem',
          color: 'var(--error)',
          borderColor: 'var(--error)',
        }}
        onClick={() => handleDeleteUser(user.id, user.plexUsername)}
        disabled={actionLoading === user.id}
      >
        Delete
      </button>
    </>
  );

  const visibleMissingTracks = useMemo(() => {
    const query = missingTracksFilter.trim().toLowerCase();
    const filtered = query
      ? missingTracks.filter(t => t.title.toLowerCase().includes(query) || t.artist.toLowerCase().includes(query))
      : missingTracks;

    const dir = missingTracksSortDir === 'asc' ? 1 : -1;
    return [...filtered].sort((a, b) => {
      const key = missingTracksSortKey;
      if (key === 'title' || key === 'artist') return a[key].localeCompare(b[key]) * dir;
      return (a[key] - b[key]) * dir;
    });
  }, [missingTracks, missingTracksFilter, missingTracksSortKey, missingTracksSortDir]);

  if (isLoading) {
    return (
      <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-secondary)' }}>
        Loading admin data...
      </div>
    );
  }

  if (error) {
    return (
      <div style={{
        padding: '1rem',
        backgroundColor: 'rgba(244, 67, 54, 0.1)',
        border: '1px solid var(--error)',
        borderRadius: '4px',
        color: 'var(--error)',
      }}>
        {error}
      </div>
    );
  }

  return (
    <div className="page-container">
      {/* Redundant on mobile - the Settings nav item already reads "Admin" right above this, and vertical space is scarce there. Desktop keeps it. */}
      <h1 className="page-title admin-dashboard-title">Admin Dashboard</h1>

      {/* Tabs */}
      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '2rem', borderBottom: '1px solid var(--border-color)', overflowX: 'auto', WebkitOverflowScrolling: 'touch' }}>
        {[
          { id: 'stats' as const, label: 'Statistics' },
          { id: 'users' as const, label: 'Users' },
          { id: 'missing' as const, label: 'Missing Tracks' },
          { id: 'jobs' as const, label: 'Schedules' },
          { id: 'deemix' as const, label: 'Deemix' },
          { id: 'lidarr' as const, label: 'Lidarr' },
          { id: 'logs' as const, label: 'Logs' },
        ].map(tab => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            style={{
              padding: '0.75rem 1.5rem',
              border: 'none',
              background: 'none',
              color: activeTab === tab.id ? 'var(--primary-color)' : 'var(--text-secondary)',
              borderBottom: activeTab === tab.id ? '2px solid var(--primary-color)' : '2px solid transparent',
              cursor: 'pointer',
              fontWeight: activeTab === tab.id ? 500 : 400,
              flexShrink: 0,
              whiteSpace: 'nowrap',
            }}
          >
            {tab.label}
          </button>
        ))}
      </div>

      {/* Statistics Tab */}
      {activeTab === 'stats' && stats && (
        <div style={{ 
          display: 'grid', 
          gridTemplateColumns: 'repeat(auto-fit, minmax(250px, 1fr))', 
          gap: '1.5rem' 
        }}>
          <div className="card">
            <h3 style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
              Total Users
            </h3>
            <p style={{ fontSize: '2rem', fontWeight: 'bold', color: 'var(--primary-color)' }}>
              {stats.userCount}
            </p>
          </div>
          <div className="card">
            <h3 style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
              Active Users
            </h3>
            <p style={{ fontSize: '2rem', fontWeight: 'bold', color: 'var(--success)' }}>
              {stats.activeUsers}
            </p>
          </div>
          <div className="card">
            <h3 style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
              Total Playlists
            </h3>
            <p style={{ fontSize: '2rem', fontWeight: 'bold', color: 'var(--primary-color)' }}>
              {stats.playlistCount}
            </p>
          </div>
          <div className="card">
            <h3 style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
              Missing Tracks
            </h3>
            <p style={{ fontSize: '2rem', fontWeight: 'bold', color: 'var(--warning)' }}>
              {stats.missingTrackCount}
            </p>
          </div>
        </div>
      )}

      {/* Users Tab */}
      {activeTab === 'users' && (
        <div className="card">
          <h2 style={{ marginBottom: '1rem' }}>User Management</h2>
          <p style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '1rem' }}>
            Plex Home members are auto-approved. Other users need manual approval.
          </p>
          {users.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
              No users found
            </div>
          ) : isMobile ? (
            <div className="admin-cards">
              {users.map(user => (
                <div key={user.id} className="admin-card">
                  <div className="admin-card-row">
                    {user.plexThumb && (
                      <img
                        src={user.plexThumb}
                        alt=""
                        style={{ width: 32, height: 32, borderRadius: '50%', flexShrink: 0 }}
                      />
                    )}
                    <div className="admin-card-title">
                      <span className="admin-card-name">{user.plexUsername}</span>
                      <span className="admin-card-summary">
                        <span className={`badge ${user.isAdmin ? 'badge-primary' : ''}`}>{user.isAdmin ? 'Admin' : 'User'}</span>
                        <span className={`badge ${user.isEnabled ? 'badge-success' : 'badge-error'}`}>{user.isEnabled ? 'Enabled' : 'Disabled'}</span>
                      </span>
                    </div>
                  </div>
                  <div className="admin-card-meta">
                    Server: {user.hasServer ? '✓ Configured' : '—'}
                    {' · '}
                    Last login: {user.lastLogin ? new Date(user.lastLogin * 1000).toLocaleDateString() : '—'}
                  </div>
                  {!user.isAdmin && (
                    <div className="admin-card-actions">
                      {renderUserActions(user)}
                    </div>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                <thead>
                  <tr style={{ borderBottom: '1px solid var(--border-color)' }}>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 500 }}>User</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 500 }}>Role</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 500 }}>Status</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 500 }}>Server</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 500 }}>Last Login</th>
                    <th style={{ padding: '0.75rem', textAlign: 'right', fontWeight: 500 }}>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {users.map(user => (
                    <tr key={user.id} style={{ borderBottom: '1px solid var(--border-color)' }}>
                      <td style={{ padding: '0.75rem' }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                          {user.plexThumb && (
                            <img
                              src={user.plexThumb}
                              alt=""
                              style={{ width: 28, height: 28, borderRadius: '50%' }}
                            />
                          )}
                          <span>{user.plexUsername}</span>
                        </div>
                      </td>
                      <td style={{ padding: '0.75rem' }}>
                        <span style={{
                          padding: '0.2rem 0.5rem',
                          borderRadius: '4px',
                          fontSize: '0.75rem',
                          fontWeight: 500,
                          backgroundColor: user.isAdmin ? 'var(--primary-color)' : 'var(--surface-hover)',
                          color: user.isAdmin ? 'white' : 'var(--text-secondary)',
                        }}>
                          {user.isAdmin ? 'Admin' : 'User'}
                        </span>
                      </td>
                      <td style={{ padding: '0.75rem' }}>
                        <span style={{
                          padding: '0.2rem 0.5rem',
                          borderRadius: '4px',
                          fontSize: '0.75rem',
                          fontWeight: 500,
                          backgroundColor: user.isEnabled ? 'rgba(76, 175, 80, 0.15)' : 'rgba(244, 67, 54, 0.15)',
                          color: user.isEnabled ? 'var(--success)' : 'var(--error)',
                        }}>
                          {user.isEnabled ? 'Enabled' : 'Disabled'}
                        </span>
                      </td>
                      <td style={{ padding: '0.75rem', fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                        {user.hasServer ? '✓ Configured' : '—'}
                      </td>
                      <td style={{ padding: '0.75rem', fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                        {user.lastLogin ? new Date(user.lastLogin * 1000).toLocaleDateString() : '—'}
                      </td>
                      <td style={{ padding: '0.75rem', textAlign: 'right' }}>
                        {!user.isAdmin && (
                          <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
                            {renderUserActions(user)}
                          </div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {/* Missing Tracks Tab */}
      {activeTab === 'missing' && (
        <div className="card">
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '0.75rem', flexWrap: 'wrap', gap: '0.5rem' }}>
            <div>
              <h2 style={{ margin: 0 }}>Most Common Missing Tracks</h2>
              <p style={{ fontSize: '0.8125rem', color: 'var(--text-secondary)', margin: '0.125rem 0 0' }}>
                Tracks that multiple users are missing from their libraries
              </p>
            </div>
            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
              <input
                type="text"
                className="input"
                placeholder="Filter by track or artist..."
                value={missingTracksFilter}
                onChange={(e) => setMissingTracksFilter(e.target.value)}
                style={{ width: '220px', maxWidth: '100%' }}
              />
              {missingTracks.length > 0 && (
                <button
                  className="btn btn-secondary btn-small"
                  onClick={handleDeemixAll}
                  disabled={isDeemixingAll || deemixingTrack !== null || visibleMissingTracks.length === 0}
                  title="Search deemix and queue every track currently shown below for download"
                >
                  {isDeemixingAll ? 'Deemixing...' : 'Deemix All'}
                </button>
              )}
            </div>
          </div>
          {missingTracks.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
              No missing tracks data
            </div>
          ) : visibleMissingTracks.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
              No tracks match "{missingTracksFilter}"
            </div>
          ) : isMobile ? (
            <>
              <div className="admin-sortbar">
                <label htmlFor="missing-tracks-sort">Sort</label>
                <select
                  id="missing-tracks-sort"
                  value={missingTracksSortKey}
                  onChange={(e) => handleMissingTracksSort(e.target.value as MissingTrackSortKey)}
                >
                  <option value="count">Users Missing</option>
                  <option value="title">Track</option>
                  <option value="artist">Artist</option>
                  <option value="addedAt">Added</option>
                </select>
                <button
                  type="button"
                  onClick={() => setMissingTracksSortDir(dir => (dir === 'asc' ? 'desc' : 'asc'))}
                  title={missingTracksSortDir === 'asc' ? 'Ascending - tap for descending' : 'Descending - tap for ascending'}
                  aria-label={`Sort direction: ${missingTracksSortDir === 'asc' ? 'ascending' : 'descending'}`}
                >
                  {missingTracksSortDir === 'asc' ? '▲' : '▼'}
                </button>
              </div>
              <div className="admin-cards">
                {visibleMissingTracks.map((track, idx) => {
                  const key = `${track.title}::${track.artist}`;
                  return (
                    <div key={idx} className="admin-card">
                      <div className="admin-card-title">
                        <span className="admin-card-name">{track.title}</span>
                        <span className="admin-card-summary">
                          {track.artist} · added {new Date(track.addedAt * 1000).toLocaleDateString()}
                        </span>
                      </div>
                      <div className="admin-card-actions">
                        <span className="badge badge-warning">{track.count} missing</span>
                        <button
                          className="btn btn-secondary btn-small"
                          onClick={() => handleDeemixDownload(track)}
                          disabled={deemixingTrack === key}
                          title={track.artist.trim().toLowerCase() === 'various artists' ? 'Download the top 5 matches for this title (artist is "Various Artists")' : 'Search deemix and queue this track for download'}
                        >
                          {deemixingTrack === key ? '...' : 'Deemix'}
                        </button>
                      </div>
                    </div>
                  );
                })}
              </div>
            </>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                <thead>
                  <tr style={{ borderBottom: '1px solid var(--border-color)' }}>
                    {([
                      ['title', 'Track', 'left'],
                      ['artist', 'Artist', 'left'],
                      ['addedAt', 'Added', 'left'],
                      ['count', 'Users Missing', 'right'],
                    ] as [MissingTrackSortKey, string, string][]).map(([key, label, align]) => (
                      <th
                        key={key}
                        onClick={() => handleMissingTracksSort(key)}
                        style={{ padding: '0.5rem 0.75rem', textAlign: align as any, fontWeight: 500, cursor: 'pointer', userSelect: 'none', whiteSpace: 'nowrap' }}
                      >
                        {label}{missingTracksSortKey === key && (missingTracksSortDir === 'asc' ? ' ▲' : ' ▼')}
                      </th>
                    ))}
                    <th style={{ padding: '0.5rem 0.75rem' }}></th>
                  </tr>
                </thead>
                <tbody>
                  {visibleMissingTracks.map((track, idx) => {
                    const key = `${track.title}::${track.artist}`;
                    return (
                    <tr key={idx} style={{ borderBottom: '1px solid var(--border-color)' }}>
                      <td style={{ padding: '0.375rem 0.75rem' }}>{track.title}</td>
                      <td style={{ padding: '0.375rem 0.75rem', fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                        {track.artist}
                      </td>
                      <td style={{ padding: '0.375rem 0.75rem', fontSize: '0.875rem', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                        {new Date(track.addedAt * 1000).toLocaleDateString()}
                      </td>
                      <td style={{ padding: '0.375rem 0.75rem', textAlign: 'right', fontWeight: 500, color: 'var(--warning)' }}>
                        {track.count}
                      </td>
                      <td style={{ padding: '0.375rem 0.75rem', textAlign: 'right' }}>
                        <button
                          className="btn btn-secondary btn-small"
                          onClick={() => handleDeemixDownload(track)}
                          disabled={deemixingTrack === key}
                          title={track.artist.trim().toLowerCase() === 'various artists' ? 'Download the top 5 matches for this title (artist is "Various Artists")' : 'Search deemix and queue this track for download'}
                        >
                          {deemixingTrack === key ? '...' : 'Deemix'}
                        </button>
                      </td>
                    </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {/* Schedules Tab: system background jobs + every user's playlist/mix schedules, soonest first */}
      {activeTab === 'jobs' && (
        <>
          <div className="card" style={{ marginBottom: '1rem' }}>
            <h2 style={{ marginBottom: '1rem' }}>Background Jobs</h2>
            {jobs.length === 0 ? (
              <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
                No jobs data
              </div>
            ) : (
              <div style={{ display: 'grid', gap: '1rem' }}>
                {jobs.map((job, idx) => (
                  <div
                    key={idx}
                    style={{
                      padding: '1rem',
                      backgroundColor: 'var(--surface-hover)',
                      borderRadius: '4px',
                      display: 'flex',
                      justifyContent: 'space-between',
                      alignItems: 'center',
                      flexWrap: 'wrap',
                      gap: '0.5rem',
                    }}
                  >
                    <div>
                      <div style={{ fontWeight: 500 }}>{job.name}</div>
                      {job.lastRun && (
                        <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                          Last run: {new Date(job.lastRun).toLocaleString()}
                        </div>
                      )}
                      {job.nextRun && (
                        <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                          Next run: {new Date(job.nextRun).toLocaleString()}
                        </div>
                      )}
                    </div>
                    <span
                      style={{
                        padding: '0.25rem 0.75rem',
                        borderRadius: '4px',
                        fontSize: '0.875rem',
                        fontWeight: 500,
                        backgroundColor: job.status === 'running' ? 'var(--success)' : 'var(--surface)',
                        color: job.status === 'running' ? 'white' : 'var(--text-primary)',
                      }}
                    >
                      {job.status}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="card">
            <h2 style={{ marginBottom: '1rem' }}>User Schedules</h2>
            {userSchedules.length === 0 ? (
              <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
                No user schedules
              </div>
            ) : (
              <div style={{ display: 'grid', gap: '0.5rem' }}>
                {[...userSchedules]
                  .sort((a, b) => (getNextRunTimestamp(a) ?? Infinity) - (getNextRunTimestamp(b) ?? Infinity))
                  .map(schedule => {
                    const nextRun = getNextRunTimestamp(schedule);
                    return (
                      <div
                        key={schedule.id}
                        style={{
                          padding: '0.75rem 1rem',
                          backgroundColor: 'var(--surface-hover)',
                          borderRadius: '4px',
                          display: 'flex',
                          justifyContent: 'space-between',
                          alignItems: 'center',
                          flexWrap: 'wrap',
                          gap: '0.5rem 1rem',
                        }}
                      >
                        <div>
                          <div style={{ fontWeight: 500 }}>
                            {schedule.playlistName || schedule.config?.mixName || schedule.config?.templateName || 'Generate Mixes'}
                          </div>
                          <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                            {schedule.username} - {schedule.scheduleType === 'mix_generation' ? 'Mix generation' : 'Playlist refresh'} - {schedule.frequency}
                          </div>
                        </div>
                        <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', textAlign: 'right', flexShrink: 0 }}>
                          {nextRun ? new Date(nextRun).toLocaleString() : 'Not scheduled'}
                        </div>
                      </div>
                    );
                  })}
              </div>
            )}
          </div>
        </>
      )}
      {/* Logs Tab */}
      {activeTab === 'logs' && (
        <div className="card">
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem', flexWrap: 'wrap', gap: '0.5rem' }}>
            <div>
              <h2 style={{ margin: 0 }}>Logs</h2>
              <p style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', margin: '0.25rem 0 0' }}>
                Most recent entries from the server's shared log, across all users, newest first.
                {logsTruncated && ' Older entries exist but are not shown.'}
              </p>
            </div>
            <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
              <label style={{ fontSize: '0.8125rem', color: 'var(--text-secondary)' }}>
                Logging level:{' '}
                <select
                  className="input"
                  value={logLevel}
                  onChange={(e) => handleLogLevelChange(e.target.value)}
                  disabled={isLogLevelSaving}
                  style={{ width: 'auto' }}
                  title="How much detail the server writes to its log files going forward. Lower = less logged."
                >
                  <option value="error">Error only</option>
                  <option value="warn">Warn</option>
                  <option value="info">Info (default)</option>
                  <option value="debug">Debug (verbose)</option>
                </select>
              </label>
              <select
                className="input"
                value={logLevelFilter}
                onChange={(e) => setLogLevelFilter(e.target.value)}
                style={{ width: 'auto' }}
                title="Filter which levels are shown below"
              >
                <option value="">All levels</option>
                <option value="error">Error</option>
                <option value="warn">Warn</option>
                <option value="info">Info</option>
                <option value="debug">Debug</option>
              </select>
              <button className="btn btn-secondary btn-small" onClick={loadErrorLogs} disabled={isLogsLoading}>
                {isLogsLoading ? 'Refreshing...' : 'Refresh'}
              </button>
              <button
                className="btn btn-small"
                style={{ color: 'var(--error)', borderColor: 'var(--error)' }}
                onClick={handleClearLogs}
                disabled={isClearingLogs}
              >
                {isClearingLogs ? 'Clearing...' : 'Clear logs'}
              </button>
            </div>
          </div>

          {logsError ? (
            <div style={{ padding: '1rem', backgroundColor: 'rgba(244, 67, 54, 0.1)', border: '1px solid var(--error)', borderRadius: '4px', color: 'var(--error)' }}>
              {logsError}
            </div>
          ) : isLogsLoading && errorLogs.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>Loading error log...</div>
          ) : errorLogs.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>No errors logged</div>
          ) : (
            <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: '0.5rem' }}>
              {errorLogs.map((entry, idx) => {
                const { level, message, timestamp, ...rest } = entry;
                const extra = Object.fromEntries(Object.entries(rest).filter(([k, v]) => k !== 'service' && k !== 'raw' && v !== undefined));
                const hasExtra = Object.keys(extra).length > 0;
                return (
                  <details key={idx} style={{ backgroundColor: 'var(--surface-hover)', borderRadius: '4px', padding: '0.625rem 0.75rem' }}>
                    <summary className="admin-log-summary" style={{ cursor: hasExtra ? 'pointer' : 'default', listStyle: hasExtra ? undefined : 'none', display: 'flex', gap: '0.75rem', alignItems: 'baseline' }}>
                      <span style={{
                        flexShrink: 0,
                        fontSize: '0.6875rem',
                        fontWeight: 600,
                        textTransform: 'uppercase',
                        color: level === 'error' ? 'var(--error)' : 'var(--warning)',
                      }}>
                        {level || 'error'}
                      </span>
                      <span style={{ flexShrink: 0, fontSize: '0.75rem', color: 'var(--text-secondary)', fontVariantNumeric: 'tabular-nums' }}>
                        {timestamp || 'unknown time'}
                      </span>
                      <span className="admin-log-message" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: '0.875rem', minWidth: 0 }}>
                        {String(message)}
                      </span>
                    </summary>
                    {hasExtra && (
                      <pre style={{
                        marginTop: '0.5rem',
                        padding: '0.625rem',
                        backgroundColor: 'var(--surface)',
                        borderRadius: '4px',
                        fontSize: '0.75rem',
                        overflowX: 'auto',
                        whiteSpace: 'pre-wrap',
                        wordBreak: 'break-word',
                      }}>
                        {JSON.stringify(extra, null, 2)}
                      </pre>
                    )}
                  </details>
                );
              })}
            </div>
          )}
        </div>
      )}
      {/* Deemix Tab */}
      {activeTab === 'deemix' && (
        <div className="settings-section">
          <div className="settings-section-header">
            <h2>Deemix</h2>
            <p>
              The Deezer ARL token the local deemix-server logs in with to queue downloads for missing tracks.
              Get it from a browser logged into deezer.com: DevTools &rarr; Application/Storage &rarr; Cookies &rarr; <code>arl</code>.
            </p>
          </div>

          {deemixError && <div className="settings-alert settings-alert--error">{deemixError}</div>}
          {deemixSuccess && <div className="settings-alert settings-alert--success">{deemixSuccess}</div>}

          {isDeemixLoading ? (
            <p className="settings-loading">Loading...</p>
          ) : (
            <>
              <div className="settings-field-group">
                <div className="settings-field">
                  <label className="settings-label">Deezer ARL</label>
                  <input
                    type="password"
                    className="settings-input"
                    value={deemixArl}
                    onChange={(e) => { setDeemixArl(e.target.value); setDeemixSuccess(null); }}
                    placeholder="paste ARL token here"
                    disabled={isDeemixSaving}
                    autoComplete="off"
                  />
                </div>
              </div>

              {arlStatus && (
                <div className={`settings-alert settings-alert--${arlStatus.ok ? 'success' : 'error'}`}>
                  {arlStatus.ok
                    ? `ARL working - last checked ${new Date(arlStatus.at).toLocaleString()}.`
                    : `ARL not working: ${arlStatus.error} (checked ${new Date(arlStatus.at).toLocaleString()})`}
                </div>
              )}

              <div className="settings-actions">
                <button className="btn btn-primary" onClick={handleSaveDeemixArl} disabled={isDeemixSaving || !deemixArl}>
                  {isDeemixSaving ? 'Saving...' : 'Save ARL'}
                </button>
                <button className="btn btn-secondary" onClick={handleCheckArl} disabled={isCheckingArl || !deemixArl}>
                  {isCheckingArl ? 'Checking...' : 'Test ARL'}
                </button>
              </div>
            </>
          )}
        </div>
      )}

      {activeTab === 'deemix' && (
        <div className="settings-section" style={{ marginTop: '1.5rem' }}>
          <div className="settings-section-header">
            <h2>Deemix Download Settings</h2>
            <p>Quality, folder structure, and tagging for tracks deemix-server downloads. Saving restarts deemix-server to apply the change - any download in progress at that moment will need to be re-queued.</p>
          </div>

          {settingsError && <div className="settings-alert settings-alert--error">{settingsError}</div>}
          {settingsSuccess && <div className="settings-alert settings-alert--success">{settingsSuccess}</div>}

          {isSettingsLoading || !deemixSettings ? (
            <p className="settings-loading">Loading...</p>
          ) : (
            <>
              <div className="settings-field-group">
                <div className="settings-field">
                  <label className="settings-label">Quality</label>
                  <select
                    className="settings-select"
                    value={deemixSettings.maxBitrate}
                    onChange={(e) => updateDeemixField('maxBitrate', e.target.value)}
                    disabled={isSettingsSaving}
                  >
                    <option value="9">FLAC (lossless)</option>
                    <option value="3">MP3 320kbps</option>
                    <option value="1">MP3 128kbps</option>
                  </select>
                  <p className="settings-hint">Tried first for every download.</p>
                </div>
                <div className="settings-field">
                  <label className="settings-label">
                    <input
                      type="checkbox"
                      checked={deemixSettings.fallbackBitrate}
                      onChange={(e) => updateDeemixField('fallbackBitrate', e.target.checked)}
                      disabled={isSettingsSaving}
                    />
                    {' '}Fall back to the next best quality if the selected one isn't available
                  </label>
                </div>
              </div>

              <div className="settings-field-group">
                <div className="settings-field">
                  <label className="settings-label">Download location</label>
                  <input
                    type="text"
                    className="settings-input settings-input--mono"
                    value={deemixSettings.downloadLocation}
                    onChange={(e) => updateDeemixField('downloadLocation', e.target.value)}
                    disabled={isSettingsSaving}
                  />
                </div>
                <div className="settings-field">
                  <label className="settings-label">Track filename</label>
                  <input
                    type="text"
                    className="settings-input settings-input--mono"
                    value={deemixSettings.tracknameTemplate}
                    onChange={(e) => updateDeemixField('tracknameTemplate', e.target.value)}
                    disabled={isSettingsSaving}
                  />
                </div>
                <div className="settings-field">
                  <label className="settings-label">Album / single track filename</label>
                  <input
                    type="text"
                    className="settings-input settings-input--mono"
                    value={deemixSettings.albumTracknameTemplate}
                    onChange={(e) => updateDeemixField('albumTracknameTemplate', e.target.value)}
                    disabled={isSettingsSaving}
                  />
                  <p className="settings-hint">Used for both album tracks and singles (when "Give singles their own folder" is on below).</p>
                </div>
                <div className="settings-field">
                  <label className="settings-label">
                    <input
                      type="checkbox"
                      checked={deemixSettings.createArtistFolder}
                      onChange={(e) => updateDeemixField('createArtistFolder', e.target.checked)}
                      disabled={isSettingsSaving}
                    />
                    {' '}Create an artist folder
                  </label>
                  <input
                    type="text"
                    className="settings-input settings-input--mono"
                    value={deemixSettings.artistNameTemplate}
                    onChange={(e) => updateDeemixField('artistNameTemplate', e.target.value)}
                    disabled={isSettingsSaving || !deemixSettings.createArtistFolder}
                  />
                </div>
                <div className="settings-field">
                  <label className="settings-label">
                    <input
                      type="checkbox"
                      checked={deemixSettings.createAlbumFolder}
                      onChange={(e) => updateDeemixField('createAlbumFolder', e.target.checked)}
                      disabled={isSettingsSaving}
                    />
                    {' '}Create an album folder
                  </label>
                  <input
                    type="text"
                    className="settings-input settings-input--mono"
                    value={deemixSettings.albumNameTemplate}
                    onChange={(e) => updateDeemixField('albumNameTemplate', e.target.value)}
                    disabled={isSettingsSaving || !deemixSettings.createAlbumFolder}
                  />
                </div>
                <div className="settings-field">
                  <label className="settings-label">
                    <input
                      type="checkbox"
                      checked={deemixSettings.createSingleFolder}
                      onChange={(e) => updateDeemixField('createSingleFolder', e.target.checked)}
                      disabled={isSettingsSaving}
                    />
                    {' '}Give singles their own folder too (named the same way as an album)
                  </label>
                </div>
                <div className="settings-field">
                  <label className="settings-label">
                    <input
                      type="checkbox"
                      checked={deemixSettings.saveArtwork}
                      onChange={(e) => updateDeemixField('saveArtwork', e.target.checked)}
                      disabled={isSettingsSaving}
                    />
                    {' '}Save a cover.jpg file in each folder (separate from the artwork embedded in each track)
                  </label>
                </div>
              </div>

              <div className="settings-field-group">
                <div className="settings-field">
                  <label className="settings-label" style={{ justifyContent: 'space-between' }}>
                    <span>Tags saved into each file</span>
                    <button
                      type="button"
                      className="notification-clear-btn"
                      onClick={() => setDeemixSettings(prev => prev ? {
                        ...prev,
                        tags: Object.fromEntries(DEEMIX_TAG_FIELDS.map(([key]) => [key, true])) as any,
                      } : prev)}
                      disabled={isSettingsSaving}
                    >
                      Enable all
                    </button>
                  </label>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(160px, 1fr))', gap: '0.5rem 1rem' }}>
                    {DEEMIX_TAG_FIELDS.map(([key, label]) => (
                      <label key={key} className="settings-label" style={{ fontWeight: 400 }}>
                        <input
                          type="checkbox"
                          checked={!!deemixSettings.tags[key]}
                          onChange={(e) => updateDeemixTag(key, e.target.checked)}
                          disabled={isSettingsSaving}
                        />
                        {' '}{label}
                      </label>
                    ))}
                  </div>
                </div>
                <div className="settings-field">
                  <label className="settings-label">
                    <input
                      type="checkbox"
                      checked={!!deemixSettings.tags.saveID3v1}
                      onChange={(e) => updateDeemixTag('saveID3v1', e.target.checked)}
                      disabled={isSettingsSaving}
                    />
                    {' '}Also save ID3v1 tags (alongside ID3v2)
                  </label>
                </div>
              </div>

              <div className="settings-actions">
                <button className="btn btn-primary" onClick={handleSaveDeemixSettings} disabled={isSettingsSaving}>
                  {isSettingsSaving ? 'Saving...' : 'Save Settings'}
                </button>
              </div>
            </>
          )}
        </div>
      )}

      {activeTab === 'lidarr' && (
        <div className="settings-section">
          <div className="settings-section-header">
            <h2>Lidarr</h2>
            <p>
              A second missing-track acquisition path alongside Deemix. Finds (or adds and monitors) a
              track's artist in Lidarr and triggers a search across Lidarr's own configured indexers -
              Lidarr's indexers and download client still need to be set up in Lidarr itself.
            </p>
          </div>

          {lidarrError && <div className="settings-alert settings-alert--error">{lidarrError}</div>}
          {lidarrSuccess && <div className="settings-alert settings-alert--success">{lidarrSuccess}</div>}

          {isLidarrLoading ? (
            <p className="settings-loading">Loading...</p>
          ) : (
            <>
              <div className="settings-field-group">
                <div className="settings-field">
                  <label className="settings-label">Lidarr URL</label>
                  <input
                    type="text"
                    className="settings-input"
                    value={lidarrUrl}
                    onChange={(e) => { setLidarrUrl(e.target.value); setLidarrSuccess(null); }}
                    placeholder="http://127.0.0.1:8686"
                    disabled={isLidarrSaving}
                    autoComplete="off"
                  />
                </div>
                <div className="settings-field">
                  <label className="settings-label">API Key</label>
                  <input
                    type="password"
                    className="settings-input"
                    value={lidarrApiKey}
                    onChange={(e) => { setLidarrApiKey(e.target.value); setLidarrSuccess(null); }}
                    placeholder="Settings > General > Security in Lidarr"
                    disabled={isLidarrSaving}
                    autoComplete="off"
                  />
                </div>
              </div>

              <div className="settings-actions">
                <button className="btn btn-primary" onClick={handleSaveLidarrConfig} disabled={isLidarrSaving || !lidarrUrl || !lidarrApiKey}>
                  {isLidarrSaving ? 'Saving...' : 'Save Lidarr Config'}
                </button>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
};
