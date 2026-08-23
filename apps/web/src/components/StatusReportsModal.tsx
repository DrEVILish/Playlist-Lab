import type { FC, CSSProperties } from 'react';
import { useState, useEffect, useMemo } from 'react';
import { useApp } from '../contexts/AppContext';
import { Modal } from './Modal';

const rowStyle: CSSProperties = { display: 'flex', justifyContent: 'space-between', padding: '0.375rem 0', borderBottom: '1px solid var(--border)' };

export const StatusReportsModal: FC<{ onClose: () => void }> = ({ onClose }) => {
  const { apiClient, playlists, schedules } = useApp();
  const [missingTotal, setMissingTotal] = useState(0);
  const [recentExecutions, setRecentExecutions] = useState<any[]>([]);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    const load = async () => {
      setIsLoading(true);
      try {
        const [missing, executions] = await Promise.all([
          apiClient.getMissingTracks(),
          apiClient.getRecentExecutions(100),
        ]);
        const total = (missing.missingTracks || []).reduce((sum: number, g: any) => sum + (g.tracks?.length || 0), 0);
        setMissingTotal(total);
        setRecentExecutions(executions.executions || []);
      } catch (err) {
        console.error('Failed to load status report data:', err);
      } finally {
        setIsLoading(false);
      }
    };
    load();
  }, [apiClient]);

  const stats = useMemo(() => {
    const totalTracks = playlists.reduce((sum, p) => sum + (p.trackCount || 0), 0);
    const totalDuration = playlists.reduce((sum, p) => sum + (p.duration || 0), 0);
    const smartCount = playlists.filter(p => p.smart).length;

    const bySource = new Map<string, number>();
    for (const p of playlists) {
      const label = p.source && p.source !== 'plex' ? p.source.charAt(0).toUpperCase() + p.source.slice(1) : 'Plex';
      bySource.set(label, (bySource.get(label) || 0) + 1);
    }

    const byFrequency = new Map<string, number>();
    for (const s of schedules) {
      byFrequency.set(s.frequency, (byFrequency.get(s.frequency) || 0) + 1);
    }

    let succeeded = 0, failed = 0;
    for (const e of recentExecutions) {
      if (e.status === 'success') succeeded++;
      else if (e.status === 'failed') failed++;
    }

    return {
      totalPlaylists: playlists.length,
      totalTracks,
      totalDuration,
      smartCount,
      bySource: Array.from(bySource.entries()).sort((a, b) => b[1] - a[1]),
      totalSchedules: schedules.length,
      byFrequency: Array.from(byFrequency.entries()).sort((a, b) => b[1] - a[1]),
      succeeded,
      failed,
    };
  }, [playlists, schedules, recentExecutions]);

  const formatDuration = (ms: number) => {
    const totalHours = Math.floor(ms / 3600000);
    const days = Math.floor(totalHours / 24);
    const hours = totalHours % 24;
    return days > 0 ? `${days}d ${hours}h` : `${hours}h`;
  };

  return (
    <Modal onClose={onClose} contentStyle={{ maxWidth: '700px', width: '95vw', maxHeight: '90vh', overflow: 'auto' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
          <h2 style={{ margin: 0 }}>Status &amp; Reports</h2>
          <button className="btn btn-secondary btn-small" onClick={onClose}>Close</button>
        </div>

        {isLoading ? (
          <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>Loading...</div>
        ) : (
          <div style={{ display: 'grid', gap: '1.5rem' }}>
            <div className="playlists-stats">
              <div className="playlists-stat">
                <span className="playlists-stat-value">{stats.totalPlaylists}</span>
                <span className="playlists-stat-label">Playlists</span>
              </div>
              <div className="playlists-stat">
                <span className="playlists-stat-value">{stats.totalTracks}</span>
                <span className="playlists-stat-label">Total Tracks</span>
              </div>
              <div className="playlists-stat">
                <span className="playlists-stat-value">{formatDuration(stats.totalDuration)}</span>
                <span className="playlists-stat-label">Total Duration</span>
              </div>
              <div className={`playlists-stat ${missingTotal > 0 ? 'warn' : ''}`}>
                <span className="playlists-stat-value">{missingTotal}</span>
                <span className="playlists-stat-label">Missing Tracks</span>
              </div>
            </div>

            <section>
              <h3 style={{ marginBottom: '0.5rem' }}>Playlists by Source</h3>
              {stats.bySource.map(([label, count]) => (
                <div key={label} style={rowStyle}><span>{label}</span><span>{count}</span></div>
              ))}
            </section>

            <section>
              <h3 style={{ marginBottom: '0.5rem' }}>Schedules</h3>
              <div style={rowStyle}><span>Total scheduled</span><span>{stats.totalSchedules}</span></div>
              {stats.byFrequency.map(([freq, count]) => (
                <div key={freq} style={rowStyle}><span style={{ textTransform: 'capitalize' }}>{freq}</span><span>{count}</span></div>
              ))}
              <div style={rowStyle}><span>Last 100 runs</span><span>{stats.succeeded} succeeded, {stats.failed} failed</span></div>
            </section>

            <section>
              <h3 style={{ marginBottom: '0.5rem' }}>Smart Playlists</h3>
              <div style={rowStyle}><span>Plex smart playlists</span><span>{stats.smartCount} of {stats.totalPlaylists}</span></div>
            </section>
          </div>
        )}
    </Modal>
  );
};
