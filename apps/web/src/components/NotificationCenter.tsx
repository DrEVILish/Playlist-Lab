import { FC, useEffect, useRef, useState } from 'react';
import { useApp } from '../contexts/AppContext';
import { useClientNotifications } from '../contexts/ToastContext';
import type { JobNotification } from '@playlist-lab/shared';
import './NotificationCenter.css';

function relativeTime(ts: number): string {
  const seconds = Math.max(0, Math.round((Date.now() - ts) / 1000));
  if (seconds < 60) return 'just now';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  return `${hours}h ago`;
}

const TYPE_LABEL: Record<JobNotification['type'], string> = {
  deemix: 'Deemix download',
  lidarr: 'Lidarr search',
  'retry-match': 'Matching missing tracks',
  schedule: 'Scheduled refresh',
  import: 'Import',
  'track-vanished': 'Tracks vanished',
  action: 'Action',
};

/**
 * Header bell for the notification center: a live-polled feed of background
 * jobs (deemix downloads, missing-tracks retry batches) that the user can
 * open to see current progress, successes and failures, and dismiss.
 */
export const NotificationCenter: FC = () => {
  const { apiClient, refreshPlaylists } = useApp();
  const clientNotifications = useClientNotifications();
  const [serverNotifications, setServerNotifications] = useState<JobNotification[]>([]);
  const [isOpen, setIsOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  // Last-seen status per notification id, so a poll can tell a job just
  // finished (rather than re-triggering on every poll while it's already
  // 'success') and refresh the playlists table - the only place a
  // completed import/schedule run surfaces on the client otherwise.
  const lastStatusRef = useRef<Map<string, JobNotification['status']>>(new Map());
  // Held in a ref so the stream can call the current one without having to
  // reconnect every time its identity changes (it changes on each playlist
  // refresh), and without capturing a stale one that could refresh against a
  // server the user has since switched away from.
  const refreshPlaylistsRef = useRef(refreshPlaylists);
  refreshPlaylistsRef.current = refreshPlaylists;

  // Pushed from the server rather than polled: background jobs report
  // progress at their own irregular pace, so any fixed interval was either
  // lagging the work or asking repeatedly for nothing. EventSource also
  // reconnects on its own after a dropped connection or a server restart,
  // which is the only reason the old poll loop swallowed its errors.
  useEffect(() => {
    const source = new EventSource('/api/notifications/stream', { withCredentials: true });

    source.onmessage = (event) => {
      let notifications: JobNotification[];
      try {
        ({ notifications } = JSON.parse(event.data));
      } catch {
        return;
      }
      const lastStatus = lastStatusRef.current;
      const justFinished = notifications.some(n =>
        (n.type === 'import' || n.type === 'schedule' || n.type === 'action') &&
        n.status === 'success' &&
        lastStatus.get(n.id) !== 'success'
      );
      lastStatusRef.current = new Map(notifications.map(n => [n.id, n.status]));
      if (justFinished) refreshPlaylistsRef.current();
      setServerNotifications(notifications);
    };

    return () => source.close();
  }, []);

  useEffect(() => {
    if (!isOpen) return;
    const handleClickOutside = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [isOpen]);

  const handleDismiss = async (id: string) => {
    if (id.startsWith('local-')) {
      clientNotifications.dismiss(id);
      return;
    }
    setServerNotifications(prev => prev.filter(n => n.id !== id));
    try {
      await apiClient.dismissNotification(id);
    } catch {
      // Next poll will resync if this failed
    }
  };

  // Neither button touches a running job: its entry is the only place that
  // job's progress is shown, and clearing it would just make in-flight work
  // invisible until its next update put it straight back.
  const handleClear = async (completedOnly: boolean) => {
    clientNotifications.clear(completedOnly);
    setServerNotifications(prev => prev.filter(n =>
      n.status === 'in-progress' || (completedOnly && n.status === 'error')
    ));
    try {
      await apiClient.clearNotifications(completedOnly);
    } catch {
      // Next poll will resync if this failed
    }
  };

  // Client-side action results and server-side job progress are one feed to
  // the user - the split only matters for where a dismiss is sent.
  const notifications = [...clientNotifications.items, ...serverNotifications];

  const inProgressCount = notifications.filter(n => n.status === 'in-progress').length;

  // Active jobs stay pinned above finished ones; within each group, most
  // recently updated (i.e. most recently completed) first.
  const sortedNotifications = [...notifications].sort((a, b) => {
    const aActive = a.status === 'in-progress' ? 0 : 1;
    const bActive = b.status === 'in-progress' ? 0 : 1;
    if (aActive !== bActive) return aActive - bActive;
    return b.updatedAt - a.updatedAt;
  });

  return (
    <div className="notification-center" ref={containerRef}>
      <button
        className="notification-bell"
        onClick={() => setIsOpen(open => !open)}
        title="Notifications"
        aria-label="Notifications"
      >
        {inProgressCount > 0 && <span className="notification-bell-spinner" />}
        <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" />
          <path d="M13.73 21a2 2 0 0 1-3.46 0" />
        </svg>
        {notifications.length > 0 && <span className="notification-bell-badge">{notifications.length}</span>}
      </button>

      {isOpen && (
        <div className="notification-panel">
          <div className="notification-panel-header">
            <span>Notifications</span>
            <span className="notification-panel-actions">
              {notifications.some(n => n.status === 'success') && (
                <button className="notification-clear-btn" onClick={() => handleClear(true)} title="Clear the ones that finished successfully, keeping failures and running jobs">
                  Clear complete
                </button>
              )}
              {notifications.some(n => n.status !== 'in-progress') && (
                <button className="notification-clear-btn" onClick={() => handleClear(false)} title="Clear everything that has finished, keeping running jobs">
                  Clear all
                </button>
              )}
            </span>
          </div>
          <div className="notification-list">
            {notifications.length === 0 && (
              <div className="notification-empty">No notifications</div>
            )}
            {sortedNotifications.map(n => (
              <div key={n.id} className={`notification-item notification-item--${n.status}`}>
                <div className="notification-item-icon">
                  {n.status === 'in-progress' && <span className="notification-item-spinner" />}
                  {n.status === 'success' && <span className="notification-item-check">✓</span>}
                  {n.status === 'error' && <span className="notification-item-error">✕</span>}
                </div>
                <div className="notification-item-body">
                  {/* What kind of job this is leads, so a glance down the
                      list tells you what's happening; the specific track,
                      album or playlist it's happening to is the subtitle. */}
                  <div className="notification-item-title">{TYPE_LABEL[n.type]}</div>
                  <div className="notification-item-detail">
                    {n.title}{n.detail ? ` · ${n.detail}` : ''}
                  </div>
                  {n.status === 'in-progress' && typeof n.progress === 'number' && (
                    <span className="notification-item-bar">
                      <span className="notification-item-bar-fill" style={{ width: `${n.progress}%` }} />
                    </span>
                  )}
                  <div className="notification-item-time">{relativeTime(n.updatedAt)}</div>
                </div>
                <button
                  className="notification-item-dismiss"
                  onClick={() => handleDismiss(n.id)}
                  aria-label="Dismiss"
                  title="Dismiss"
                >
                  ×
                </button>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
};
