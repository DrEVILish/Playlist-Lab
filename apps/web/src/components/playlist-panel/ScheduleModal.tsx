import { useState, useEffect } from 'react';
import { useApp } from '../../contexts/AppContext';
import type { Schedule } from '@playlist-lab/shared';
import { getNextRunDate } from '../../utils/scheduleTime';
import { useEscapeKey } from '../../hooks/useEscapeKey';

/**
 * Create/manage the refresh schedule for a single playlist. Extracted from
 * the former standalone Schedules page's create form and row actions, scoped
 * to one playlist for use as a row action in the unified playlist control
 * panel. Mix-generation schedules (not tied to any existing playlist) aren't
 * relevant here and remain reachable from Generate Mixes as before.
 */
export function ScheduleModal({
  playlistId,
  playlistName,
  schedule,
  onClose,
}: {
  playlistId: string;
  playlistName: string;
  schedule?: Schedule;
  onClose: () => void;
}) {
  const { apiClient, refreshSchedules } = useApp();
  const [frequency, setFrequency] = useState<'daily' | 'weekly' | 'fortnightly' | 'monthly'>(schedule?.frequency ?? 'weekly');
  const [startDate, setStartDate] = useState(schedule?.startDate ?? new Date().toISOString().split('T')[0]);
  const [runTime, setRunTime] = useState(schedule?.config?.run_time ?? '');
  const [overwriteExisting, setOverwriteExisting] = useState(schedule?.config?.overwriteExisting ?? true);
  const [isSaving, setIsSaving] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<any[] | null>(null);

  useEscapeKey(true, onClose);

  useEffect(() => {
    if (!schedule) return;
    apiClient.getScheduleExecutions(schedule.id, 10)
      .then(res => setHistory(res.executions || []))
      .catch(() => setHistory([]));
  }, [schedule, apiClient]);

  const handleSave = async () => {
    setIsSaving(true);
    setError(null);
    try {
      const config = { overwriteExisting, run_time: runTime || undefined };
      if (schedule) {
        await apiClient.updateSchedule(schedule.id, { frequency, startDate, config } as any);
      } else {
        await apiClient.createSchedule({
          scheduleType: 'playlist_refresh',
          playlistId: parseInt(playlistId, 10),
          frequency,
          startDate,
          config,
        } as any);
      }
      await refreshSchedules();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save schedule');
    } finally {
      setIsSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!schedule) return;
    if (!confirm('Delete this schedule?')) return;
    setIsDeleting(true);
    setError(null);
    try {
      await apiClient.deleteSchedule(schedule.id);
      await refreshSchedules();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete schedule');
      setIsDeleting(false);
    }
  };

  const handleRunNow = async () => {
    if (!schedule) return;
    setError(null);
    try {
      await apiClient.runSchedule(schedule.id);
      await refreshSchedules();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to run schedule');
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '520px' }}>
        <h2>{schedule ? 'Manage Schedule' : 'Create Schedule'} — {playlistName}</h2>

        {error && <div className="error-message">{error}</div>}

        {schedule && (
          <div style={{ marginBottom: '1rem', fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
            <div>Next run: {getNextRunDate(schedule)}</div>
            {schedule.lastRun && <div>Last run: {new Date(schedule.lastRun * 1000).toLocaleString()}</div>}
          </div>
        )}

        <div style={{ display: 'grid', gap: '1rem' }}>
          <div>
            <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>Frequency</label>
            <select
              value={frequency}
              onChange={(e) => setFrequency(e.target.value as any)}
              style={{ width: '100%', padding: '0.75rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)' }}
            >
              <option value="daily">Daily</option>
              <option value="weekly">Weekly</option>
              <option value="fortnightly">Fortnightly</option>
              <option value="monthly">Monthly</option>
            </select>
          </div>

          <div>
            <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>Start Date</label>
            <input
              type="date"
              value={startDate}
              onChange={(e) => setStartDate(e.target.value)}
              style={{ width: '100%', padding: '0.75rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)' }}
            />
          </div>

          <div>
            <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>Run Time (optional)</label>
            <select
              value={runTime}
              onChange={(e) => setRunTime(e.target.value)}
              style={{ width: '100%', padding: '0.75rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)' }}
            >
              <option value="">Any time (next 10-minute check)</option>
              {Array.from({ length: 144 }, (_, i) => {
                const hour = Math.floor(i / 6);
                const minute = (i % 6) * 10;
                const timeStr = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
                return <option key={timeStr} value={timeStr}>{timeStr}</option>;
              })}
            </select>
          </div>

          <label style={{ display: 'flex', alignItems: 'center', padding: '0.75rem', border: '1px solid var(--border-color)', borderRadius: '4px', cursor: 'pointer', backgroundColor: 'var(--surface)' }}>
            <input type="checkbox" checked={overwriteExisting} onChange={(e) => setOverwriteExisting(e.target.checked)} style={{ marginRight: '0.75rem' }} />
            <div>
              <div style={{ fontWeight: 500 }}>Overwrite existing playlist</div>
              <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
                If a playlist with the same name exists in Plex, it will be replaced on each run
              </div>
            </div>
          </label>
        </div>

        {schedule && history && history.length > 0 && (
          <div style={{ marginTop: '1rem' }}>
            <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Recent runs</div>
            <div style={{ display: 'grid', gap: '0.5rem', maxHeight: '150px', overflowY: 'auto' }}>
              {history.map((execution) => (
                <div key={execution.id} style={{ fontSize: '0.8125rem', color: 'var(--text-secondary)', display: 'flex', justifyContent: 'space-between' }}>
                  <span>{new Date(execution.startedAt * 1000).toLocaleString()}</span>
                  <span style={{ color: execution.status === 'success' ? 'var(--success)' : execution.status === 'failed' ? 'var(--error)' : 'var(--primary-color)' }}>
                    {execution.status}
                  </span>
                </div>
              ))}
            </div>
          </div>
        )}

        <div className="modal-actions" style={{ marginTop: '1.5rem' }}>
          {schedule && (
            <>
              <button className="btn btn-secondary" onClick={handleRunNow}>Run Now</button>
              <button className="btn btn-secondary" onClick={handleDelete} disabled={isDeleting} style={{ color: 'var(--error)' }}>
                {isDeleting ? 'Deleting...' : 'Delete Schedule'}
              </button>
            </>
          )}
          <button className="btn btn-secondary" onClick={onClose} disabled={isSaving}>Cancel</button>
          <button className="btn btn-primary" onClick={handleSave} disabled={isSaving}>
            {isSaving ? 'Saving...' : schedule ? 'Save Changes' : 'Create Schedule'}
          </button>
        </div>
      </div>
    </div>
  );
}
