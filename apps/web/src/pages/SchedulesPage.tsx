import type { FC } from 'react';
import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import { useApp } from '../contexts/AppContext';
import type { Schedule } from '@playlist-lab/shared';
import { getNextRunTimestamp, getNextRunDate } from '../utils/scheduleTime';
import './SchedulesPage.css';

type SortKey = 'name' | 'nextRun' | 'lastRun' | 'status' | 'dateAdded';
type SortDir = 'asc' | 'desc';
type ExecutionStatus = 'running' | 'success' | 'failed' | 'never';

const STATUS_RANK: Record<ExecutionStatus, number> = { failed: 0, running: 1, success: 2, never: 3 };

export const SchedulesPage: FC = () => {
  const { schedules, apiClient, refreshSchedules, isLoading } = useApp();
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState<Schedule | null>(null);
  const [playlists, setPlaylists] = useState<any[]>([]);
  const [isLoadingPlaylists, setIsLoadingPlaylists] = useState(false);
  const [runningExecutions, setRunningExecutions] = useState<any[]>([]);
  const [selectedScheduleHistory, setSelectedScheduleHistory] = useState<{ scheduleId: number; history: any[] } | null>(null);
  const [missingByPlaylist, setMissingByPlaylist] = useState<Record<number, { count: number }>>({});
  const [retryingPlaylistId, setRetryingPlaylistId] = useState<number | null>(null);
  const [sortKey, setSortKey] = useState<SortKey>('nextRun');
  const [sortDir, setSortDir] = useState<SortDir>('asc');
  const [formData, setFormData] = useState({
    scheduleType: 'playlist_refresh' as 'playlist_refresh' | 'mix_generation',
    frequency: 'weekly' as 'daily' | 'weekly' | 'fortnightly' | 'monthly',
    startDate: (() => {
      const today = new Date();
      const year = today.getFullYear();
      const month = String(today.getMonth() + 1).padStart(2, '0');
      const day = String(today.getDate()).padStart(2, '0');
      return `${year}-${month}-${day}`;
    })(),
    runTime: '',
    playlistId: '',
    config: {} as any,
  });
  const [isSaving, setIsSaving] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [recentExecutions, setRecentExecutions] = useState<any[]>([]);
  const [isLoadingExecutions, setIsLoadingExecutions] = useState(false);
  const [previousRunningCount, setPreviousRunningCount] = useState(0);

  // Load recent executions on mount
  useEffect(() => {
    const fetchRecentExecutions = async () => {
      setIsLoadingExecutions(true);
      try {
        const response = await apiClient.getRecentExecutions(100);
        setRecentExecutions(response.executions || []);
      } catch (err) {
        console.error('Failed to fetch recent executions:', err);
      } finally {
        setIsLoadingExecutions(false);
      }
    };

    fetchRecentExecutions();
  }, [apiClient]);

  // Load missing-track counts per playlist, to show in the schedules table
  // and drive the "Retry Import" action.
  const loadMissingTracks = async (): Promise<Record<number, { count: number }>> => {
    try {
      const response = await apiClient.getMissingTracks();
      const byPlaylist: Record<number, { count: number }> = {};
      for (const group of response.missingTracks || []) {
        byPlaylist[group.playlistId] = { count: group.tracks.length };
      }
      setMissingByPlaylist(byPlaylist);
      return byPlaylist;
    } catch (err) {
      console.error('Failed to fetch missing tracks:', err);
      return {};
    }
  };

  useEffect(() => {
    loadMissingTracks();
  }, [apiClient]);

  // Poll for running executions and trigger refresh on state changes
  useEffect(() => {
    const fetchRunningExecutions = async () => {
      try {
        const response = await apiClient.getRunningExecutions();
        const currentRunning = response.executions || [];
        setRunningExecutions(currentRunning);

        const currentCount = currentRunning.length;

        // Trigger refresh when:
        // 1. A schedule starts (count increases)
        // 2. A schedule completes (count decreases)
        if (previousRunningCount !== currentCount) {
          console.log(`Running executions changed: ${previousRunningCount} -> ${currentCount}`);

          // Refresh schedules to update last_run times
          await refreshSchedules();

          // Refresh recent executions to show completed runs
          const execResponse = await apiClient.getRecentExecutions(100);
          setRecentExecutions(execResponse.executions || []);

          // Missing-track counts may have changed too (a run can add/clear them)
          await loadMissingTracks();

          setPreviousRunningCount(currentCount);
        }
      } catch (err) {
        console.error('Failed to fetch running executions:', err);
      }
    };

    fetchRunningExecutions();
    const interval = setInterval(fetchRunningExecutions, 10000); // Poll every 10 seconds

    return () => clearInterval(interval);
  }, [apiClient, refreshSchedules, previousRunningCount]);

  // Handle URL parameters for pre-selecting playlist or mix type
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const playlistId = params.get('playlist');
    const mixType = params.get('mixType');
    const settingsParam = params.get('settings');
    const templateId = params.get('templateId');
    const templateName = params.get('templateName');

    if (playlistId) {
      // Pre-select playlist for refresh schedule
      setFormData(prev => ({
        ...prev,
        scheduleType: 'playlist_refresh',
        playlistId: playlistId,
      }));
      setShowCreateForm(true);
    } else if (mixType && settingsParam) {
      // Pre-select mix generation schedule
      try {
        const settings = JSON.parse(decodeURIComponent(settingsParam));
        setFormData(prev => ({
          ...prev,
          scheduleType: 'mix_generation',
          config: {
            mixType,
            ...settings,
          },
        }));
        setShowCreateForm(true);
      } catch (err) {
        console.error('Failed to parse mix settings from URL:', err);
      }
    } else if (templateId) {
      // Pre-select template for schedule
      setFormData(prev => ({
        ...prev,
        scheduleType: 'mix_generation',
        config: {
          templateId: parseInt(templateId),
          templateName: templateName || 'Template Mix',
        },
      }));
      setShowCreateForm(true);
    }

    // Clear URL parameters after state is set
    if (playlistId || mixType || templateId) {
      window.history.replaceState({}, '', '/schedules');
    }
  }, []);

  useEffect(() => {
    if (showCreateForm && formData.scheduleType === 'playlist_refresh' && !editingSchedule) {
      loadPlaylists();
    }
  }, [showCreateForm, formData.scheduleType]);

  const loadPlaylists = async () => {
    setIsLoadingPlaylists(true);
    try {
      const response = await apiClient.getPlaylists();
      setPlaylists(response.playlists || []);
    } catch (err) {
      console.error('Failed to load playlists:', err);
    } finally {
      setIsLoadingPlaylists(false);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setIsSaving(true);

    console.log('Submitting schedule with formData:', formData);
    console.log('Is editing:', !!editingSchedule);
    console.log('Schedule ID:', editingSchedule?.id);

    try {
      if (editingSchedule) {
        console.log('Calling updateSchedule with:', {
          id: editingSchedule.id,
          data: formData
        });
        await apiClient.updateSchedule(editingSchedule.id, {
          ...formData,
          playlistId: formData.playlistId ? parseInt(formData.playlistId) : undefined,
          config: { ...(formData.config as any), run_time: formData.runTime || undefined },
        });
      } else {
        await apiClient.createSchedule({
          ...formData,
          playlistId: formData.playlistId ? parseInt(formData.playlistId) : undefined,
          config: { ...(formData.config as any), run_time: formData.runTime || undefined },
        } as any);
      }
      await refreshSchedules();
      setShowCreateForm(false);
      setEditingSchedule(null);
      resetForm();

      // Refresh recent executions
      const response = await apiClient.getRecentExecutions(100);
      setRecentExecutions(response.executions || []);
    } catch (err) {
      console.error('Error saving schedule:', err);
      setError(err instanceof Error ? err.message : 'Failed to save schedule');
    } finally {
      setIsSaving(false);
    }
  };

  const handleEdit = (schedule: Schedule) => {
    console.log('Editing schedule:', schedule);
    console.log('Schedule startDate:', schedule.startDate);
    setEditingSchedule(schedule);
    setFormData({
      scheduleType: schedule.scheduleType,
      frequency: schedule.frequency,
      startDate: schedule.startDate,
      runTime: schedule.config?.run_time || '',
      playlistId: schedule.playlistId ? String(schedule.playlistId) : '',
      config: schedule.config || {},
    });
    console.log('FormData after setting:', {
      scheduleType: schedule.scheduleType,
      frequency: schedule.frequency,
      startDate: schedule.startDate,
      config: schedule.config || {},
    });
    setShowCreateForm(true);
  };

  const handleDelete = async (schedule: Schedule) => {
    if (!confirm(`Are you sure you want to delete this schedule?`)) {
      return;
    }

    setIsDeleting(true);
    setError(null);
    try {
      await apiClient.deleteSchedule(schedule.id);
      await refreshSchedules();

      // Refresh recent executions
      const response = await apiClient.getRecentExecutions(100);
      setRecentExecutions(response.executions || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete schedule');
    } finally {
      setIsDeleting(false);
    }
  };

  const resetForm = () => {
    const today = new Date();
    const year = today.getFullYear();
    const month = String(today.getMonth() + 1).padStart(2, '0');
    const day = String(today.getDate()).padStart(2, '0');
    const todayStr = `${year}-${month}-${day}`;

    setFormData({
      scheduleType: 'playlist_refresh',
      frequency: 'weekly',
      startDate: todayStr,
      runTime: '',
      playlistId: '',
      config: {} as any,
    });
  };

  const handleCancel = () => {
    setShowCreateForm(false);
    setEditingSchedule(null);
    resetForm();
    setError(null);
  };

  // Close the create/edit modal on Escape
  useEffect(() => {
    if (!showCreateForm) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !isSaving) {
        handleCancel();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [showCreateForm, isSaving]);

  const handleViewHistory = async (schedule: Schedule) => {
    try {
      const response = await apiClient.getScheduleExecutions(schedule.id, 20);
      setSelectedScheduleHistory({
        scheduleId: schedule.id,
        history: response.executions || []
      });
    } catch (err) {
      console.error('Failed to load execution history:', err);
      setError('Failed to load execution history');
    }
  };

  const getScheduleTitle = (schedule: Schedule) => {
    // Check if it's a template schedule
    if (schedule.config?.templateName) {
      return schedule.config.templateName;
    }
    // Check if it's a quick mix schedule
    if (schedule.config?.mixName) {
      return schedule.config.mixName;
    }
    // Check if it's a chart import schedule
    if (schedule.config?.chartName) {
      // Use custom playlist name if set, otherwise use chart name
      return schedule.config.playlistName || schedule.config.chartName;
    }
    // For regular playlist refresh or mix generation, return a descriptive title
    if (schedule.scheduleType === 'playlist_refresh') {
      return 'Playlist Refresh';
    }
    return 'Mix Generation';
  };

  const getSourceLabel = (source?: string) => source ? source.charAt(0).toUpperCase() + source.slice(1) : 'source';

  const SourceLink: FC<{ source?: string; sourceUrl?: string }> = ({ source, sourceUrl }) => {
    if (!sourceUrl) return null;
    return (
      <a
        href={sourceUrl}
        target="_blank"
        rel="noopener noreferrer"
        style={{ fontSize: '0.8125rem', color: 'var(--primary-color)' }}
        title={`View original playlist on ${getSourceLabel(source)}`}
      >
        View on {getSourceLabel(source)} ↗
      </a>
    );
  };

  const getLatestExecution = (scheduleId: number) => {
    const executions = recentExecutions.filter(e => e.scheduleId === scheduleId);
    if (executions.length === 0) return undefined;
    return executions.reduce((latest, e) => (e.startedAt > latest.startedAt ? e : latest));
  };

  const getStatus = (schedule: Schedule): ExecutionStatus => {
    if (runningExecutions.some(e => e.scheduleId === schedule.id)) return 'running';
    const latest = getLatestExecution(schedule.id);
    return latest?.status ?? 'never';
  };

  const getMissingCount = (schedule: Schedule) =>
    schedule.playlistId ? missingByPlaylist[schedule.playlistId]?.count ?? 0 : 0;

  const handleRetryImport = async (schedule: Schedule) => {
    if (!schedule.playlistId) return;
    setRetryingPlaylistId(schedule.playlistId);
    setError(null);
    try {
      const response = await apiClient.retryMissingTracks(schedule.playlistId);
      if (!response.started) {
        setError(response.message);
        return;
      }
      // Retries run in the background (see routes/missing.ts) since a full
      // batch can take minutes. Poll briefly so the "Retrying..." state and
      // the missing-track count reflect reality instead of clearing
      // instantly while the retry is still working.
      let lastCount = missingByPlaylist[schedule.playlistId]?.count ?? 0;
      let stableStreak = 0;
      for (let i = 0; i < 40 && stableStreak < 2; i++) {
        await new Promise(resolve => setTimeout(resolve, 3000));
        const byPlaylist = await loadMissingTracks();
        const currentCount = byPlaylist[schedule.playlistId]?.count ?? 0;
        if (currentCount === lastCount) {
          stableStreak++;
        } else {
          stableStreak = 0;
          lastCount = currentCount;
        }
      }
    } catch (err) {
      console.error('Failed to retry missing tracks:', err);
      setError('Failed to retry missing tracks');
    } finally {
      setRetryingPlaylistId(null);
    }
  };

  const toggleSort = (key: SortKey) => {
    if (sortKey === key) {
      setSortDir(prev => (prev === 'asc' ? 'desc' : 'asc'));
    } else {
      setSortKey(key);
      setSortDir('asc');
    }
  };

  const SortableHeader: FC<{ label: string; sortKeyName: SortKey }> = ({ label, sortKeyName }) => (
    <th className="sortable" onClick={() => toggleSort(sortKeyName)}>
      {label}
      {sortKey === sortKeyName && (
        <span className="sort-arrow">{sortDir === 'asc' ? '▲' : '▼'}</span>
      )}
    </th>
  );

  const sortedSchedules = [...schedules].sort((a, b) => {
    let cmp = 0;
    switch (sortKey) {
      case 'name':
        cmp = getScheduleTitle(a).localeCompare(getScheduleTitle(b));
        break;
      case 'nextRun': {
        const av = getNextRunTimestamp(a);
        const bv = getNextRunTimestamp(b);
        cmp = (av ?? Infinity) - (bv ?? Infinity);
        break;
      }
      case 'lastRun':
        cmp = (a.lastRun ?? 0) - (b.lastRun ?? 0);
        break;
      case 'status':
        cmp = STATUS_RANK[getStatus(a)] - STATUS_RANK[getStatus(b)];
        break;
      case 'dateAdded':
        cmp = (a.createdAt ?? 0) - (b.createdAt ?? 0);
        break;
    }
    return sortDir === 'asc' ? cmp : -cmp;
  });

  return (
    <div className="page-container">
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h1 className="page-title">Schedules</h1>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          {!showCreateForm && schedules.length > 0 && (
            <button
              className="btn btn-secondary"
              onClick={async () => {
                if (confirm(`Run all ${schedules.length} schedule(s) now?`)) {
                  try {
                    const response = await apiClient.runAllSchedules();
                    alert(response.message);
                    // Refresh to show new executions
                    await refreshSchedules();
                    const execResponse = await apiClient.getRecentExecutions(100);
                    setRecentExecutions(execResponse.executions || []);
                  } catch (err) {
                    console.error('Failed to run all schedules:', err);
                    setError('Failed to run all schedules');
                  }
                }
              }}
            >
              Run All Schedules
            </button>
          )}
          {!showCreateForm && recentExecutions.length > 0 && (
            <button
              className="btn btn-secondary"
              onClick={async () => {
                if (confirm('Are you sure you want to clear all execution history?')) {
                  try {
                    await apiClient.clearAllExecutions();
                    setRecentExecutions([]);
                  } catch (err) {
                    console.error('Failed to clear executions:', err);
                    setError('Failed to clear execution history');
                  }
                }
              }}
              title="Clear all execution history"
            >
              Clear Execution History
            </button>
          )}
          {!showCreateForm && (
            <button
              className="btn btn-primary"
              onClick={() => setShowCreateForm(true)}
            >
              Create Schedule
            </button>
          )}
        </div>
      </div>

      {error && (
        <div style={{
          padding: '1rem',
          marginBottom: '1rem',
          backgroundColor: 'rgba(244, 67, 54, 0.1)',
          border: '1px solid var(--error)',
          borderRadius: '4px',
          color: 'var(--error)',
        }}>
          {error}
        </div>
      )}

      {showCreateForm && (
        <div
          style={{
            position: 'fixed',
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            backgroundColor: 'rgba(0, 0, 0, 0.5)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            zIndex: 1000,
            padding: '1rem',
          }}
          onClick={handleCancel}
          role="dialog"
          aria-modal="true"
          aria-labelledby="schedule-form-title"
        >
          <div
            className="card"
            style={{ maxWidth: '600px', width: '100%', maxHeight: '90vh', overflow: 'auto' }}
            onClick={(e) => e.stopPropagation()}
          >
            <h2 id="schedule-form-title" style={{ marginBottom: '1rem' }}>
              {editingSchedule ? 'Edit Schedule' : 'Create Schedule'}
            </h2>
            <form onSubmit={handleSubmit}>
              <div style={{ display: 'grid', gap: '1rem' }}>
                {!editingSchedule && (
                  <div>
                    <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                      Schedule Type
                    </label>
                    <select
                      value={formData.scheduleType}
                      onChange={(e) => {
                        const newType = e.target.value as any;
                        setFormData({ ...formData, scheduleType: newType, config: {} });
                        if (newType === 'playlist_refresh') {
                          loadPlaylists();
                        }
                      }}
                      style={{
                        width: '100%',
                        padding: '0.75rem',
                        border: '1px solid var(--border-color)',
                        borderRadius: '4px',
                        backgroundColor: 'var(--surface)',
                        color: 'var(--text-primary)',
                      }}
                    >
                      <option value="playlist_refresh">Playlist Refresh</option>
                      <option value="mix_generation">Mix Generation</option>
                    </select>
                  </div>
                )}

                {!editingSchedule && formData.scheduleType === 'playlist_refresh' && (
                  <div>
                    <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                      Playlist
                    </label>
                    {isLoadingPlaylists ? (
                      <div style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>
                        Loading playlists...
                      </div>
                    ) : (
                      <select
                        value={formData.playlistId || ''}
                        onChange={(e) => setFormData({
                          ...formData,
                          playlistId: e.target.value
                        })}
                        required
                        style={{
                          width: '100%',
                          padding: '0.75rem',
                          border: '1px solid var(--border-color)',
                          borderRadius: '4px',
                          backgroundColor: 'var(--surface)',
                          color: 'var(--text-primary)',
                        }}
                      >
                        <option value="">Select a playlist</option>
                        {playlists.map(playlist => (
                          <option key={playlist.id} value={playlist.id}>
                            {playlist.name} ({playlist.trackCount} tracks)
                          </option>
                        ))}
                      </select>
                    )}
                  </div>
                )}

                {editingSchedule && formData.config?.chartName && (
                  <div>
                    <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                      Playlist Name in Plex
                    </label>
                    <input
                      type="text"
                      value={formData.config?.playlistName || formData.config?.chartName || ''}
                      onChange={(e) => setFormData({
                        ...formData,
                        config: { ...formData.config, playlistName: e.target.value }
                      })}
                      placeholder="Enter playlist name"
                      style={{
                        width: '100%',
                        padding: '0.75rem',
                        border: '1px solid var(--border-color)',
                        borderRadius: '4px',
                        backgroundColor: 'var(--surface)',
                        color: 'var(--text-primary)',
                      }}
                    />
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                      Chart: {formData.config?.chartName}
                    </div>
                  </div>
                )}

                {formData.scheduleType === 'playlist_refresh' && (
                  <div>
                    <label style={{
                      display: 'flex',
                      alignItems: 'center',
                      padding: '0.75rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                      cursor: 'pointer',
                      backgroundColor: 'var(--surface)',
                    }}>
                      <input
                        type="checkbox"
                        checked={formData.config?.overwriteExisting ?? true}
                        onChange={(e) => setFormData({
                          ...formData,
                          config: { ...formData.config, overwriteExisting: e.target.checked }
                        })}
                        style={{ marginRight: '0.75rem' }}
                      />
                      <div>
                        <div style={{ fontWeight: 500 }}>Overwrite existing playlist</div>
                        <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                          If a playlist with the same name exists in Plex, it will be replaced on each run
                        </div>
                      </div>
                    </label>
                  </div>
                )}

                {formData.config?.templateId && (
                  <div>
                    <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                      Template
                    </label>
                    <div style={{
                      padding: '0.75rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                    }}>
                      {formData.config?.templateName || `Template #${formData.config?.templateId}`}
                    </div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                      This schedule will generate a new playlist from this template
                    </div>
                  </div>
                )}

                <div>
                  <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                    Frequency
                  </label>
                  <select
                    value={formData.frequency}
                    onChange={(e) => setFormData({ ...formData, frequency: e.target.value as any })}
                    style={{
                      width: '100%',
                      padding: '0.75rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                    }}
                  >
                    <option value="daily">Daily</option>
                    <option value="weekly">Weekly</option>
                    <option value="fortnightly">Fortnightly</option>
                    <option value="monthly">Monthly</option>
                  </select>
                </div>

                <div>
                  <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                    Start Date
                  </label>
                  <input
                    type="date"
                    value={formData.startDate}
                    onChange={(e) => setFormData({ ...formData, startDate: e.target.value })}
                    required
                    style={{
                      width: '100%',
                      padding: '0.75rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                    }}
                  />
                </div>

                <div>
                  <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                    Run Time (optional)
                  </label>
                  <select
                    value={formData.runTime}
                    onChange={(e) => setFormData({ ...formData, runTime: e.target.value })}
                    style={{
                      width: '100%',
                      padding: '0.75rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                    }}
                  >
                    <option value="">Any time (next 10-minute check)</option>
                    {Array.from({ length: 144 }, (_, i) => {
                      const hour = Math.floor(i / 6);
                      const minute = (i % 6) * 10;
                      const timeStr = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
                      return <option key={timeStr} value={timeStr}>{timeStr}</option>;
                    })}
                  </select>
                  <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                    Schedules are checked every 10 minutes
                  </div>
                </div>
              </div>

              <div style={{ display: 'flex', gap: '1rem', marginTop: '1.5rem' }}>
                <button
                  type="submit"
                  className="btn btn-primary"
                  disabled={isSaving}
                  style={{ flex: 1 }}
                >
                  {isSaving ? 'Saving...' : editingSchedule ? 'Update' : 'Create'}
                </button>
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={handleCancel}
                  disabled={isSaving}
                  style={{ flex: 1 }}
                >
                  Cancel
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {isLoading || isLoadingExecutions ? (
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-secondary)' }}>
          Loading schedules...
        </div>
      ) : schedules.length === 0 ? (
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-secondary)' }}>
          No schedules configured
        </div>
      ) : (
        <div className="schedules-table-container">
          <table className="schedules-table">
            <thead>
              <tr>
                <SortableHeader label="Name" sortKeyName="name" />
                <SortableHeader label="Next Run" sortKeyName="nextRun" />
                <SortableHeader label="Last Run" sortKeyName="lastRun" />
                <SortableHeader label="Status" sortKeyName="status" />
                <th>Overwrite</th>
                <th>Source</th>
                <th>Missing Tracks</th>
                <SortableHeader label="Date Added" sortKeyName="dateAdded" />
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {sortedSchedules.map(schedule => {
                const isChartImport = !!schedule.config?.chartName;
                const isRunning = runningExecutions.some(e => e.scheduleId === schedule.id);
                const status = getStatus(schedule);
                const latestExecution = getLatestExecution(schedule.id);
                const missingCount = getMissingCount(schedule);
                const overwrite = schedule.scheduleType === 'playlist_refresh'
                  ? (schedule.config?.overwriteExisting ?? true)
                  : null;
                const statusLabel = status === 'running' ? 'Running'
                  : status === 'success' ? 'Success'
                  : status === 'failed' ? 'Failed'
                  : 'Never run';

                return (
                  <tr key={schedule.id}>
                    <td>
                      <div style={{ fontWeight: 500 }}>{getScheduleTitle(schedule)}</div>
                      <div style={{ display: 'flex', gap: '0.375rem', marginTop: '0.25rem', flexWrap: 'wrap' }}>
                        {isChartImport && <span className="badge chart-import">Chart Import</span>}
                        <span className="badge frequency">{schedule.frequency}</span>
                      </div>
                    </td>
                    <td>
                      {getNextRunDate(schedule)}
                      {schedule.config?.run_time ? ` at ${schedule.config.run_time}` : ''}
                    </td>
                    <td>{schedule.lastRun ? new Date(schedule.lastRun * 1000).toLocaleString() : '—'}</td>
                    <td>
                      <span className={`status-badge ${status}`}>
                        {status === 'running' && <span className="pulse-dot" />}
                        {statusLabel}
                      </span>
                      {status === 'failed' && latestExecution?.errorMessage && (
                        <div style={{ fontSize: '0.75rem', color: '#F44336', marginTop: '0.25rem', maxWidth: '220px' }}>
                          {latestExecution.errorMessage}
                        </div>
                      )}
                    </td>
                    <td>{overwrite === null ? '—' : overwrite ? 'Yes' : 'No'}</td>
                    <td>{schedule.sourceUrl ? <SourceLink source={schedule.source} sourceUrl={schedule.sourceUrl} /> : '—'}</td>
                    <td>
                      {missingCount > 0 && schedule.playlistId ? (
                        <Link
                          to={`/playlists?missingFor=${schedule.playlistId}`}
                          style={{ color: 'var(--warning-color)', fontWeight: 500 }}
                          title="View these missing tracks"
                        >
                          {missingCount} ↗
                        </Link>
                      ) : missingCount}
                    </td>
                    <td>{schedule.createdAt ? new Date(schedule.createdAt * 1000).toLocaleDateString() : '—'}</td>
                    <td className="col-actions">
                      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                        <button
                          className="btn btn-primary btn-small"
                          onClick={async () => {
                            try {
                              await apiClient.runSchedule(schedule.id);
                              await refreshSchedules();
                              const execResponse = await apiClient.getRecentExecutions(100);
                              setRecentExecutions(execResponse.executions || []);
                            } catch (err) {
                              console.error('Failed to run schedule:', err);
                              setError('Failed to run schedule');
                            }
                          }}
                          disabled={isDeleting || isRunning}
                          title="Run this schedule now"
                        >
                          Run Now
                        </button>
                        {missingCount > 0 && schedule.playlistId && (
                          <button
                            className="btn btn-secondary btn-small"
                            onClick={() => handleRetryImport(schedule)}
                            disabled={retryingPlaylistId === schedule.playlistId}
                            title="Retry matching the missing tracks for this playlist"
                          >
                            {retryingPlaylistId === schedule.playlistId ? 'Retrying...' : 'Retry Import'}
                          </button>
                        )}
                        <button
                          className="btn btn-secondary btn-small"
                          onClick={() => handleViewHistory(schedule)}
                          disabled={isDeleting}
                          title="View execution history"
                        >
                          History
                        </button>
                        <button
                          className="btn btn-secondary btn-small"
                          onClick={() => handleEdit(schedule)}
                          disabled={isDeleting}
                        >
                          Edit
                        </button>
                        <button
                          className="btn btn-secondary btn-small"
                          onClick={() => handleDelete(schedule)}
                          disabled={isDeleting}
                        >
                          Delete
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {selectedScheduleHistory && (
        <div style={{
          position: 'fixed',
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          backgroundColor: 'rgba(0, 0, 0, 0.5)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          zIndex: 1000,
          padding: '1rem',
        }} onClick={() => setSelectedScheduleHistory(null)}>
          <div className="card" style={{
            maxWidth: '800px',
            width: '100%',
            maxHeight: '80vh',
            overflow: 'auto',
          }} onClick={(e) => e.stopPropagation()}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
              <div>
                <h2 style={{ margin: 0 }}>Execution History</h2>
                {(() => {
                  const historySchedule = schedules.find(s => s.id === selectedScheduleHistory.scheduleId);
                  return historySchedule?.sourceUrl ? (
                    <div style={{ marginTop: '0.25rem' }}>
                      <SourceLink source={historySchedule.source} sourceUrl={historySchedule.sourceUrl} />
                    </div>
                  ) : null;
                })()}
              </div>
              <button
                className="btn btn-secondary btn-small"
                onClick={() => setSelectedScheduleHistory(null)}
              >
                Close
              </button>
            </div>

            {selectedScheduleHistory.history.length === 0 ? (
              <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
                No execution history yet
              </div>
            ) : (
              <div style={{ display: 'grid', gap: '0.75rem' }}>
                {selectedScheduleHistory.history.map((execution) => (
                  <div key={execution.id} style={{
                    padding: '1rem',
                    border: '1px solid var(--border-color)',
                    borderRadius: '4px',
                    backgroundColor: execution.status === 'running' ? 'rgba(33, 150, 243, 0.05)' :
                                   execution.status === 'success' ? 'rgba(76, 175, 80, 0.05)' :
                                   'rgba(244, 67, 54, 0.05)',
                  }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'start', marginBottom: '0.5rem' }}>
                      <div>
                        <div style={{ fontWeight: 500, marginBottom: '0.25rem' }}>
                          {execution.playlistName || 'Playlist'}
                        </div>
                        <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                          Started: {new Date(execution.startedAt * 1000).toLocaleString()}
                        </div>
                        {execution.completedAt && (
                          <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                            Completed: {new Date(execution.completedAt * 1000).toLocaleString()}
                            {' '}({Math.round((execution.completedAt - execution.startedAt) / 60)}m {(execution.completedAt - execution.startedAt) % 60}s)
                          </div>
                        )}
                      </div>
                      <span style={{
                        padding: '0.25rem 0.5rem',
                        borderRadius: '4px',
                        fontSize: '0.75rem',
                        fontWeight: 500,
                        backgroundColor: execution.status === 'running' ? 'rgba(33, 150, 243, 0.2)' :
                                       execution.status === 'success' ? 'rgba(76, 175, 80, 0.2)' :
                                       'rgba(244, 67, 54, 0.2)',
                        color: execution.status === 'running' ? '#2196F3' :
                               execution.status === 'success' ? '#4CAF50' :
                               '#F44336',
                      }}>
                        {execution.status === 'running' ? 'Running' :
                         execution.status === 'success' ? 'Success' :
                         'Failed'}
                      </span>
                    </div>

                    {execution.status === 'success' && (
                      <div style={{ display: 'flex', gap: '1rem', fontSize: '0.875rem', marginTop: '0.5rem' }}>
                        <div style={{ color: '#4CAF50' }}>
                          ✓ {execution.tracksMatched} matched
                        </div>
                        {execution.tracksUnmatched > 0 && (
                          <div style={{ color: '#FF9800' }}>
                            ⚠ {execution.tracksUnmatched} missing
                          </div>
                        )}
                      </div>
                    )}

                    {execution.status === 'failed' && execution.errorMessage && (
                      <div style={{
                        marginTop: '0.5rem',
                        padding: '0.5rem',
                        backgroundColor: 'rgba(244, 67, 54, 0.1)',
                        borderRadius: '4px',
                        fontSize: '0.875rem',
                        color: '#F44336',
                      }}>
                        {execution.errorMessage}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};
