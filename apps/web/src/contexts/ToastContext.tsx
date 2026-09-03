import { createContext, useContext, useState, useCallback, useMemo, type FC, type ReactNode } from 'react';
import type { JobNotification } from '@playlist-lab/shared';

interface ToastApi {
  success: (message: string) => void;
  error: (message: string) => void;
}

interface ClientNotificationApi {
  items: JobNotification[];
  dismiss: (id: string) => void;
  /** Local notifications are all terminal results, so `completedOnly` only
   * decides whether failures are kept - nothing local is ever in progress. */
  clear: (completedOnly?: boolean) => void;
}

const ToastContext = createContext<ToastApi | undefined>(undefined);
const ClientNotificationContext = createContext<ClientNotificationApi | undefined>(undefined);

/** Replacement for window.alert(): `toast.success('Saved!')` /
 * `toast.error('Failed to save')`. The name and shape are unchanged from
 * when this rendered auto-dismissing banners in the corner of the screen;
 * these now go to the header's notification center instead, so every
 * message the app produces lives in exactly one place the user can go back
 * to, rather than some appearing there and others flashing past in the
 * bottom-right corner and disappearing. */
export const useToast = (): ToastApi => {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within a ToastProvider');
  return ctx;
};

/** The client-side half of the notification center's feed - results of
 * actions that complete inside the browser and have no server-side job
 * behind them to poll. NotificationCenter merges these with the server feed
 * from GET /api/notifications. */
export const useClientNotifications = (): ClientNotificationApi => {
  const ctx = useContext(ClientNotificationContext);
  if (!ctx) throw new Error('useClientNotifications must be used within a ToastProvider');
  return ctx;
};

// Matches the server feed's own per-user cap (MAX_PER_USER in
// services/job-notifications.ts) so a long session can't grow this without
// bound.
const MAX_ITEMS = 50;

export const ToastProvider: FC<{ children: ReactNode }> = ({ children }) => {
  const [items, setItems] = useState<JobNotification[]>([]);

  const push = useCallback((status: 'success' | 'error', message: string) => {
    const now = Date.now();
    // Prefixed so NotificationCenter can tell a client-side entry from a
    // server one and dismiss it locally instead of POSTing an id the server
    // has never heard of.
    const id = `local-${crypto.randomUUID()}`;
    const entry: JobNotification = { id, type: 'action', title: message, status, createdAt: now, updatedAt: now };
    setItems(prev => [entry, ...prev].slice(0, MAX_ITEMS));
  }, []);

  const toastApi = useMemo<ToastApi>(() => ({
    success: (message) => push('success', message),
    error: (message) => push('error', message),
  }), [push]);

  const clientApi = useMemo<ClientNotificationApi>(() => ({
    items,
    dismiss: (id) => setItems(prev => prev.filter(n => n.id !== id)),
    clear: (completedOnly?: boolean) => setItems(prev =>
      prev.filter(n => n.status === 'in-progress' || (completedOnly && n.status === 'error'))
    ),
  }), [items]);

  return (
    <ToastContext.Provider value={toastApi}>
      <ClientNotificationContext.Provider value={clientApi}>
        {children}
      </ClientNotificationContext.Provider>
    </ToastContext.Provider>
  );
};
