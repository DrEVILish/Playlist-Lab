/**
 * Unit tests for the shared cross-user action queue.
 *
 * What these pin: a burst of enqueued jobs must never run more than
 * MAX_CONCURRENT (3) at once, later jobs must start in FIFO order as slots
 * free up, and the position handed back at enqueue time must match how many
 * still-queued jobs are actually ahead of it - the number the notification
 * bell shows a user for their own job, with no detail about anyone else's.
 */

import { enqueueAction } from '../../src/services/action-queue';
import { listNotifications, clearNotifications } from '../../src/services/job-notifications';

const USER = 9191;

describe('action queue', () => {
  beforeEach(() => clearNotifications(USER));

  it('never runs more than 3 jobs at once and runs the rest in FIFO order', async () => {
    let active = 0;
    let maxActive = 0;
    const startOrder: number[] = [];
    const resolvers: Array<() => void> = [];

    const jobs = Array.from({ length: 6 }, (_, i) =>
      new Promise<void>(resolve => {
        enqueueAction(USER, `Job ${i}`, async () => {
          active++;
          maxActive = Math.max(maxActive, active);
          startOrder.push(i);
          await new Promise<void>(r => resolvers.push(r));
          active--;
          resolve();
        });
      })
    );

    // Let the first wave of handlers actually start (enqueueAction kicks
    // processing synchronously, but the handler body runs on a microtask).
    await new Promise(r => setTimeout(r, 10));

    expect(maxActive).toBe(3);
    expect(startOrder).toEqual([0, 1, 2]);

    // Release the first wave and let the next one start.
    resolvers.splice(0).forEach(r => r());
    await new Promise(r => setTimeout(r, 10));

    expect(startOrder).toEqual([0, 1, 2, 3, 4, 5]);

    resolvers.splice(0).forEach(r => r());
    await Promise.all(jobs);
  });

  it('reports each job\'s own position among still-queued jobs, not who else is queued', async () => {
    const holds: Array<() => void> = [];
    const hold = () => new Promise<void>(r => holds.push(r));

    // Fill all 3 worker slots so the next enqueues actually sit in the queue.
    enqueueAction(USER, 'Running A', hold);
    enqueueAction(USER, 'Running B', hold);
    enqueueAction(USER, 'Running C', hold);

    const fourth = enqueueAction(USER, 'Queued D', hold);
    const fifth = enqueueAction(USER, "Someone else's job", hold);

    expect(fourth.position).toBe(0);
    expect(fifth.position).toBe(1);

    // The notification for the 4th job never carries any detail about the
    // 5th job (or vice versa) - only a bare position number.
    const notifications = listNotifications(USER);
    const fourthNotification = notifications.find(n => n.id === fourth.jobId)!;
    expect(fourthNotification.detail).toBe('Up next');
    expect(fourthNotification.detail).not.toContain("Someone else's job");

    holds.splice(0).forEach(r => r());
    await new Promise(r => setTimeout(r, 10));
    holds.splice(0).forEach(r => r());
  });
});
