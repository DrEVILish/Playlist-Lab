import type { FC } from 'react';
import { useState } from 'react';
import { createPortal } from 'react-dom';
import { useApp } from '../contexts/AppContext';
import { useToast } from '../contexts/ToastContext';
import { usePopover } from '../hooks/usePopover';
import { waitForRetryCompletion } from '../utils/retryCompletion';
import './HeaderActions.css';

/** Fired after a bulk retry/deemix so PlaylistsPage reloads its
 * missing-tracks map - the header and the page don't share that state, and
 * a bulk action from up here changes what every row below shows. */
export const MISSING_CHANGED_EVENT = 'playlist-lab:missing-changed';

type ActionKey = 'retry' | 'deemix' | 'refresh';

/**
 * Library-wide bulk actions (retry matching, queue deemix downloads, run
 * every schedule), collected into one "Actions" menu beside the
 * notification bell. These used to be scattered - "Refresh All" sat in the
 * playlist page's stats row, "Retry All"/"Deemix All" only existed
 * per-playlist inside an expanded missing-tracks panel - so there was no
 * way to run any of them across the whole library.
 */
export const HeaderActions: FC = () => {
  const { apiClient } = useApp();
  const toast = useToast();
  const { open, toggle, close, ref, panelRef, menuStyle } = usePopover<HTMLDivElement>();
  const [busy, setBusy] = useState<ActionKey | null>(null);

  const run = async (kind: ActionKey, fn: () => Promise<void>) => {
    close();
    setBusy(kind);
    try {
      await fn();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Action failed');
    } finally {
      setBusy(null);
      window.dispatchEvent(new Event(MISSING_CHANGED_EVENT));
    }
  };

  const retryAll = () => run('retry', async () => {
    const response = await apiClient.retryMissingTracks();
    if (!response.started) {
      toast.error(response.message);
      return;
    }
    const { timedOut } = await waitForRetryCompletion(apiClient);
    toast[timedOut ? 'error' : 'success'](timedOut
      ? 'Retry is taking longer than expected - check the notification bell shortly.'
      : 'Retry finished.');
  });

  const deemixAll = () => run('deemix', async () => {
    // Match against Plex first, then queue whatever is still missing - both
    // steps run server-side on the shared action queue, so this returns as
    // soon as the job is accepted and progress shows up in the bell.
    const { message } = await apiClient.deemixAll();
    toast.success(message);
  });

  const refreshAll = () => run('refresh', async () => {
    const result = await apiClient.runAllSchedules();
    toast.success(result.message);
  });

  const items: Array<{ key: ActionKey; label: string; busyLabel: string; onClick: () => void; title: string }> = [
    { key: 'deemix', label: 'Deemix All', busyLabel: 'Deemixing...', onClick: deemixAll, title: 'Retry matching, then queue every still-missing track for deemix download' },
    { key: 'retry', label: 'Retry All', busyLabel: 'Retrying...', onClick: retryAll, title: 'Retry matching every missing track against your Plex library' },
    { key: 'refresh', label: 'Refresh', busyLabel: 'Refreshing...', onClick: refreshAll, title: 'Run every scheduled playlist refresh now' },
  ];

  return (
    <div className="header-actions-menu" ref={ref}>
      <button
        type="button"
        className={`header-actions-btn ${open ? 'active' : ''}`}
        onClick={toggle}
        aria-expanded={open}
        aria-haspopup="menu"
        title={busy ? items.find(i => i.key === busy)!.busyLabel : 'Actions'}
        aria-label="Actions"
      >
        {busy && <span className="header-actions-spinner" />}
        <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
          <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />
        </svg>
      </button>
      {open && createPortal(
        <div className="header-actions-popover" style={menuStyle} role="menu" ref={panelRef}>
          {items.map(item => (
            <button
              key={item.key}
              type="button"
              role="menuitem"
              className="header-actions-item"
              onClick={item.onClick}
              disabled={busy !== null}
              title={item.title}
            >
              {busy === item.key ? item.busyLabel : item.label}
            </button>
          ))}
        </div>,
        document.body
      )}
    </div>
  );
};
