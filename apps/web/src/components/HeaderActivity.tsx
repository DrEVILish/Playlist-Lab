import { FC, useEffect, useState } from 'react';
import { createPortal } from 'react-dom';
import { QueuePage } from '../pages/QueuePage';
import { useEscapeKey } from '../hooks/useEscapeKey';
import './HeaderActivity.css';

interface QueueStatus {
  processing: {
    id: string;
    source: string;
    playlistName?: string;
    progress?: { current: number; total: number; phase?: string };
  } | null;
  queued: Array<{ id: string }>;
}

interface RetryStatus {
  current: number;
  total: number;
}

/**
 * Compact replacement for the old standalone Queue page + floating widget:
 * a header progress pill showing the currently-processing import (and how
 * many more are queued behind it). Imports that finished scraping/matching
 * but haven't been reviewed/saved yet still need the fuller QueuePage UI
 * (rematching tracks, choosing overwrite options, etc.) - rather than lose
 * that, a "N to review" badge opens it in a modal on demand instead of it
 * being a permanent nav destination. A second pill shows missing-tracks
 * retry progress (see routes/missing.ts) - retries requested while one is
 * already running queue up instead of being rejected, so this is the only
 * visible sign of that queued work until it starts.
 */
export const HeaderActivity: FC = () => {
  const [status, setStatus] = useState<QueueStatus | null>(null);
  const [reviewCount, setReviewCount] = useState(0);
  const [retryStatus, setRetryStatus] = useState<RetryStatus | null>(null);
  const [showQueueModal, setShowQueueModal] = useState(false);

  useEffect(() => {
    const poll = async () => {
      try {
        const [queueRes, completedRes, retryRes] = await Promise.all([
          fetch('/api/import/queue', { credentials: 'include' }),
          fetch('/api/import/queue/completed', { credentials: 'include' }),
          fetch('/api/missing/retry-status', { credentials: 'include' }),
        ]);
        if (queueRes.ok) {
          const data = await queueRes.json();
          setStatus(data.processing || (data.queued && data.queued.length > 0) ? data : null);
        }
        if (completedRes.ok) {
          const data = await completedRes.json();
          setReviewCount((data.completed || []).length);
        }
        if (retryRes.ok) {
          const data = await retryRes.json();
          setRetryStatus(data.active || null);
        }
      } catch {
        // Silently fail - server might be restarting
      }
    };
    poll();
    const interval = setInterval(poll, 3000);
    return () => clearInterval(interval);
  }, []);

  useEscapeKey(showQueueModal, () => setShowQueueModal(false));

  const processing = status?.processing;
  const queuedCount = status?.queued?.length || 0;
  const hasActivity = !!processing || queuedCount > 0;

  const pct = processing?.progress && processing.progress.total > 0
    ? Math.min(100, Math.round((processing.progress.current / processing.progress.total) * 100))
    : null;
  const retryPct = retryStatus && retryStatus.total > 0
    ? Math.min(100, Math.round((retryStatus.current / retryStatus.total) * 100))
    : null;

  if (!hasActivity && reviewCount === 0 && !retryStatus) return null;

  return (
    <>
      {(hasActivity || reviewCount > 0) && (
        <button
          className="header-activity"
          onClick={() => setShowQueueModal(true)}
          title={reviewCount > 0
            ? `${reviewCount} import(s) finished matching tracks but were never saved as a playlist - click to review and save or discard them`
            : 'Import activity'}
        >
          {processing && (
            <>
              <span className="header-activity-spinner" />
              <span className="header-activity-label">
                {processing.playlistName || processing.source}
                {pct !== null ? ` ${pct}%` : '…'}
              </span>
              {pct !== null && (
                <span className="header-activity-bar">
                  <span className="header-activity-bar-fill" style={{ width: `${pct}%` }} />
                </span>
              )}
            </>
          )}
          {!processing && queuedCount > 0 && (
            <span className="header-activity-label">{queuedCount} queued</span>
          )}
          {reviewCount > 0 && (
            <span className="header-activity-badge">{reviewCount} import{reviewCount === 1 ? '' : 's'} unsaved</span>
          )}
        </button>
      )}

      {retryStatus && (
        <span className="header-activity" title="Retrying missing tracks against your Plex library">
          <span className="header-activity-spinner" />
          <span className="header-activity-label">
            Retrying {retryStatus.current}/{retryStatus.total}
          </span>
          {retryPct !== null && (
            <span className="header-activity-bar">
              <span className="header-activity-bar-fill" style={{ width: `${retryPct}%` }} />
            </span>
          )}
        </span>
      )}

      {showQueueModal && createPortal(
        <div className="modal-overlay" onClick={() => setShowQueueModal(false)}>
          <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '95vw', width: '1200px', maxHeight: '90vh', overflow: 'auto' }}>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
              <button className="btn btn-secondary btn-small" onClick={() => setShowQueueModal(false)}>Close</button>
            </div>
            <QueuePage />
          </div>
        </div>,
        document.body
      )}
    </>
  );
};
