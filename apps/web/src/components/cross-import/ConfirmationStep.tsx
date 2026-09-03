import type { FC } from 'react';
import { useState, useEffect, useRef } from 'react';
import type { TargetInfo, TargetConfig } from '../../pages/CrossImportPage';
import type { MatchResult } from './types';

interface Props {
  jobId: number;
  matchResults: MatchResult[];
  target: TargetInfo;
  targetConfig: TargetConfig;
  playlistName: string;
  onStartOver: () => void;
}

// Runs through the server's shared action queue now instead of finishing
// within the request - the real playlist-creation result shows up later in
// the notification bell (top right), not synchronously here.
interface ExecuteResult {
  position: number;
}

export const ConfirmationStep: FC<Props> = ({
  jobId,
  matchResults,
  target,
  targetConfig,
  playlistName,
  onStartOver,
}) => {
  const [loading, setLoading] = useState(true);
  const [result, setResult] = useState<ExecuteResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const executedRef = useRef(false);

  const execute = async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetch('/api/cross-import/execute', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({
          jobId,
          reviewedTracks: matchResults,
          targetId: target.id,
          targetConfig,
          playlistName,
        }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        throw new Error(data.error?.message || `Failed to create playlist (${res.status})`);
      }
      const data = await res.json();
      setResult(data);
    } catch (err: any) {
      setError(err.message || 'Failed to create playlist');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (executedRef.current) return;
    executedRef.current = true;
    execute();
  }, []);

  if (loading) {
    return (
      <div className="confirmation-container">
        <div className="loading-spinner" style={{ margin: '0 auto 1rem' }} />
        <p style={{ color: 'var(--text-secondary)' }}>Creating playlist on {target.name}…</p>
      </div>
    );
  }

  if (error) {
    return (
      <div className="confirmation-container">
        <div className="confirmation-icon">❌</div>
        <h2 className="confirmation-title">Something went wrong</h2>
        <p className="confirmation-subtitle">{error}</p>
        <div style={{ display: 'flex', gap: '1rem', justifyContent: 'center' }}>
          <button className="btn btn-primary" onClick={execute}>Retry</button>
          <button className="btn btn-secondary" onClick={onStartOver}>Start over</button>
        </div>
      </div>
    );
  }

  return (
    <div className="confirmation-container">
      <div className="confirmation-icon">✅</div>
      <h2 className="confirmation-title">Queued for import</h2>
      <p className="confirmation-subtitle">
        {result && result.position > 0
          ? `Creating "${playlistName}" on ${target.name} - position ${result.position} in queue. Check the notification bell (top right) for the result.`
          : `Creating "${playlistName}" on ${target.name} - check the notification bell (top right) for the result.`}
      </p>
      <button className="btn btn-primary" onClick={onStartOver}>
        Start another import
      </button>
    </div>
  );
};
