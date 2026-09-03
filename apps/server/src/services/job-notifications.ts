import { randomUUID } from 'crypto';
import { EventEmitter } from 'events';

export type NotificationStatus = 'in-progress' | 'success' | 'error';

export interface JobNotification {
  id: string;
  type: 'deemix' | 'lidarr' | 'retry-match' | 'schedule' | 'import' | 'track-vanished' | 'action';
  title: string;
  detail?: string;
  status: NotificationStatus;
  /** 0-100, omitted when progress isn't known yet */
  progress?: number;
  createdAt: number;
  updatedAt: number;
}

// In-memory per-user notification feed for the header's live status area.
// Same durability tradeoff as the existing activeRetries/pendingRetries maps
// in routes/missing.ts - lost on restart, which is fine for transient job
// progress nobody needs to survive that.
const MAX_PER_USER = 50;
const store = new Map<number, JobNotification[]>();

// Emits a user's id whenever their feed changes, so the SSE endpoint can push
// instead of every client re-asking on a timer. One emitter for all users
// rather than one per user: the listener count is the number of open browser
// tabs, not the number of accounts, and a single 'change' event with the id
// on it is less bookkeeping than a map of emitters to create and tear down.
const changes = new EventEmitter();
// One listener per connected tab, and the default cap of 10 is low enough to
// print a spurious leak warning on an ordinary number of them.
changes.setMaxListeners(0);

export function onNotificationsChanged(listener: (userId: number) => void): () => void {
  changes.on('change', listener);
  return () => { changes.off('change', listener); };
}

export function addNotification(userId: number, data: Omit<JobNotification, 'id' | 'createdAt' | 'updatedAt'>): JobNotification {
  const notification: JobNotification = {
    ...data,
    id: randomUUID(),
    createdAt: Date.now(),
    updatedAt: Date.now(),
  };
  const list = store.get(userId) || [];
  list.unshift(notification);
  if (list.length > MAX_PER_USER) list.length = MAX_PER_USER;
  store.set(userId, list);
  changes.emit('change', userId);
  return notification;
}

export function updateNotification(userId: number, id: string, patch: Partial<Omit<JobNotification, 'id' | 'createdAt'>>): void {
  const notification = store.get(userId)?.find(n => n.id === id);
  if (!notification) return;
  // Undefined means "no new value for this field", not "clear it". Callers
  // build these patches from job progress events, where a field is routinely
  // absent for a phase - an import reports no total while it is still
  // scraping, for instance. Object.assign would write that undefined straight
  // over a real value, and since the UI only draws a progress bar when
  // progress is a number, one such event made the bar vanish mid-job.
  for (const [key, value] of Object.entries(patch)) {
    if (value !== undefined) (notification as any)[key] = value;
  }
  notification.updatedAt = Date.now();
  changes.emit('change', userId);
}

export function listNotifications(userId: number): JobNotification[] {
  return store.get(userId) || [];
}

export function dismissNotification(userId: number, id: string): void {
  const list = store.get(userId);
  if (!list) return;
  store.set(userId, list.filter(n => n.id !== id));
  changes.emit('change', userId);
}

/**
 * Clears finished notifications. An in-progress job is never cleared: its
 * entry is the only place its progress is reported, so dropping it doesn't
 * tidy the list, it hides work that is still happening (and the entry
 * reappears on its next progress update anyway).
 *
 * @param completedOnly - Keep failures too, so "Clear complete" leaves
 *   exactly the things still worth looking at.
 */
export function clearNotifications(userId: number, completedOnly = false): void {
  const list = store.get(userId) || [];
  store.set(userId, list.filter(n =>
    n.status === 'in-progress' || (completedOnly && n.status === 'error')
  ));
  changes.emit('change', userId);
}
