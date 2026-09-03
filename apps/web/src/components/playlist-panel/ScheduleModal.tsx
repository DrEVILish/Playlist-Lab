import { useState, useEffect } from 'react';
import { useApp } from '../../contexts/AppContext';
import type { Schedule } from '@playlist-lab/shared';
import { getNextRunDate } from '../../utils/scheduleTime';
import { Modal, modalCloseButtonStyle } from '../Modal';
import { useConfirm } from '../../contexts/ConfirmContext';

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
  const confirmDialog = useConfirm();
  const [frequency, setFrequency] = useState<'daily' | 'weekly' | 'fortnightly' | 'monthly'>(schedule?.frequency ?? 'weekly');
  const [startDate, setStartDate] = useState(schedule?.startDate ?? new Date().toISOString().split('T')[0]);
  const [runTime, setRunTime] = useState(schedule?.config?.run_time ?? '');
  // Older schedules only stored the overwriteExisting boolean; true always
  // meant replace, and false meant "don't touch what's there".
  const [updateMode, setUpdateMode] = useState<'replace' | 'accumulate'>(
    schedule?.config?.updateMode ?? (schedule?.config?.overwriteExisting === false ? 'accumulate' : 'replace')
  );
  const [isSaving, setIsSaving] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<any[] | null>(null);

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
      const config = { updateMode, run_time: runTime || undefined };
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
    if (!await confirmDialog('Delete this schedule?')) return;
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
    <Modal onClose={onClose} contentStyle={{ maxWidth: '460px', width: '100%', maxHeight: '90vh', overflowY: 'auto' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
          <h2 style={{ margin: 0, fontSize: '1.15rem' }}>{schedule ? 'Manage Schedule' : 'Create Schedule'} — {playlistName}</h2>
          <button onClick={onClose} title="Close" style={modalCloseButtonStyle}>✕</button>
        </div>

        {error && <div className="error-message">{error}</div>}

        {schedule && (
          <div style={{ marginBottom: '0.75rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
            <div>Next run: {getNextRunDate(schedule)}</div>
            {schedule.lastRun && <div>Last run: {new Date(schedule.lastRun * 1000).toLocaleString()}</div>}
          </div>
        )}

        <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.75rem' }}>
          <div style={{ flex: 1 }}>
            <label style={{ display: 'block', marginBottom: '0.25rem', fontSize: '0.8rem', fontWeight: 500 }}>Frequency</label>
            <select
              value={frequency}
              onChange={(e) => setFrequency(e.target.value as any)}
              style={{ width: '100%', padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.9rem' }}
            >
              <option value="daily">Daily</option>
              <option value="weekly">Weekly</option>
              <option value="fortnightly">Fortnightly</option>
              <option value="monthly">Monthly</option>
            </select>
          </div>

          <div style={{ flex: 1 }}>
            <label style={{ display: 'block', marginBottom: '0.25rem', fontSize: '0.8rem', fontWeight: 500 }}>Start Date</label>
            <input
              type="date"
              value={startDate}
              onChange={(e) => setStartDate(e.target.value)}
              style={{ width: '100%', padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.9rem' }}
            />
          </div>

          <div style={{ flex: 1 }} title="Leave blank to run on the next 10-minute check after the start date">
            <label style={{ display: 'block', marginBottom: '0.25rem', fontSize: '0.8rem', fontWeight: 500 }}>Run Time</label>
            <select
              value={runTime}
              onChange={(e) => setRunTime(e.target.value)}
              style={{ width: '100%', padding: '0.5rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.9rem' }}
            >
              <option value="">Any time</option>
              {Array.from({ length: 144 }, (_, i) => {
                const hour = Math.floor(i / 6);
                const minute = (i % 6) * 10;
                const timeStr = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
                return <option key={timeStr} value={timeStr}>{timeStr}</option>;
              })}
            </select>
          </div>
        </div>

        <div style={{ marginBottom: '0.75rem' }}>
          <div style={{ fontSize: '0.8rem', fontWeight: 500, marginBottom: '0.4rem' }}>When the source changes</div>
          <div style={{ display: 'grid', gap: '0.4rem' }}>
            {([
              ['replace', 'Replace', 'The playlist mirrors the source. Tracks that drop out of the source are removed.'],
              ['accumulate', 'Accumulate', 'New tracks are added and nothing is ever removed - a weekly chart becomes a running archive.'],
            ] as const).map(([value, label, description]) => (
              <label key={value} style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem', fontSize: '0.85rem', cursor: 'pointer' }}>
                <input
                  type="radio"
                  name="schedule-update-mode"
                  value={value}
                  checked={updateMode === value}
                  onChange={() => setUpdateMode(value)}
                  style={{ marginTop: '0.2rem' }}
                />
                <span>
                  <span style={{ fontWeight: 500 }}>{label}</span>
                  <span style={{ display: 'block', color: 'var(--text-secondary)', fontSize: '0.8rem' }}>{description}</span>
                </span>
              </label>
            ))}
          </div>
        </div>

        {schedule && history && history.length > 0 && (
          <div style={{ marginBottom: '0.75rem' }}>
            <div style={{ fontSize: '0.8rem', fontWeight: 500, marginBottom: '0.4rem' }}>Recent runs</div>
            <div style={{ display: 'grid', gap: '0.4rem', maxHeight: '150px', overflowY: 'auto' }}>
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

        <div className="modal-actions" style={{ marginTop: '1rem' }}>
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
    </Modal>
  );
}
