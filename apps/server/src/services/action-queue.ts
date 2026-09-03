import { addNotification, updateNotification } from './job-notifications';
import { logger } from '../utils/logger';

/**
 * Shared cross-user FIFO queue for server-side actions that do real Plex/API
 * work (playlist reorders, AI generation, cross-service imports, ...) so a
 * burst of requests from multiple users can't all run at once and so each
 * user can see how far down the queue their own request is. Bounded to a
 * small worker pool rather than one-at-a-time, since these actions are
 * independent of each other and don't need strict serialization - only a cap.
 *
 * In-memory only, not DB-persisted (unlike import-queue.ts's import_queue
 * table): these actions are seconds-to-low-minutes and trivially re-clickable
 * by the user, so losing a queued/in-flight one on a rare server restart is
 * an acceptable tradeoff - the same one job-notifications.ts already makes
 * for this class of transient progress.
 */

const MAX_CONCURRENT = 3;

interface QueuedAction {
  id: string;
  userId: number;
  handler: () => Promise<void>;
}

const queue: QueuedAction[] = [];
let active = 0;

/**
 * Queues an action and returns immediately with the caller's own position
 * (0 = about to run). `handler` does the real work and is responsible for
 * calling updateNotification(userId, notificationId, ...) itself to report
 * progress and the final success/error outcome, the same way every other
 * background job in this app already does.
 */
export function enqueueAction(
  userId: number,
  title: string,
  handler: (notificationId: string) => Promise<void>,
  notificationType: Parameters<typeof addNotification>[1]['type'] = 'action'
): { jobId: string; position: number } {
  const notification = addNotification(userId, {
    type: notificationType,
    title,
    detail: 'Queued',
    status: 'in-progress',
  });

  const job: QueuedAction = {
    id: notification.id,
    userId,
    handler: () => handler(notification.id),
  };
  queue.push(job);
  const position = queue.length - 1;

  refreshQueuedPositions();
  processQueue();

  return { jobId: job.id, position };
}

function refreshQueuedPositions(): void {
  queue.forEach((job, index) => {
    updateNotification(job.userId, job.id, {
      detail: index === 0 ? "Up next" : `Queued - position ${index + 1}`,
    });
  });
}

function processQueue(): void {
  while (active < MAX_CONCURRENT && queue.length > 0) {
    const job = queue.shift()!;
    active++;
    updateNotification(job.userId, job.id, { detail: 'Processing...' });
    refreshQueuedPositions();

    job.handler()
      .catch((error: any) => {
        // Handlers report their own success/error via updateNotification -
        // this only catches a handler that threw without doing so itself,
        // so the notification doesn't get stuck on "Processing..." forever.
        logger.error('Action queue job failed unexpectedly', { error: error.message, jobId: job.id, userId: job.userId });
        updateNotification(job.userId, job.id, { status: 'error', detail: error.message || 'Action failed' });
      })
      .finally(() => {
        active--;
        processQueue();
      });
  }
}
