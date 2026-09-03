/**
 * Unit tests for the in-memory job notification feed.
 *
 * The bug these pin: progress patches are built from job progress events,
 * where a field is routinely absent for a phase - an import reports no total
 * while it is still scraping, so `progress` comes through as undefined.
 * Object.assign wrote that undefined straight over the real value, and since
 * the UI only draws a progress bar when progress is a number, a single
 * scraping-phase event made the bar disappear for the rest of the job.
 */

import {
  addNotification,
  updateNotification,
  listNotifications,
  clearNotifications,
  onNotificationsChanged,
} from '../../src/services/job-notifications';

const USER = 4242;

describe('job notifications', () => {
  beforeEach(() => clearNotifications(USER));

  const start = () =>
    addNotification(USER, { type: 'import', title: 'Playlist', detail: 'Starting...', status: 'in-progress', progress: 0 });

  it('keeps the last known progress when an update omits it', () => {
    const n = start();
    updateNotification(USER, n.id, { progress: 40, detail: 'Matching tracks' });

    // The shape an import emits while scraping: a detail, no total to derive
    // a percentage from.
    updateNotification(USER, n.id, { detail: 'Loading Spotify playlist...', progress: undefined });

    expect(listNotifications(USER)[0].progress).toBe(40);
    expect(listNotifications(USER)[0].detail).toBe('Loading Spotify playlist...');
  });

  it('still applies a real progress value, including zero', () => {
    const n = start();
    updateNotification(USER, n.id, { progress: 75 });
    expect(listNotifications(USER)[0].progress).toBe(75);

    updateNotification(USER, n.id, { progress: 0 });
    expect(listNotifications(USER)[0].progress).toBe(0);
  });

  it('notifies listeners so the SSE stream can push instead of being polled', () => {
    const seen: number[] = [];
    const unsubscribe = onNotificationsChanged(id => seen.push(id));

    const n = start();
    updateNotification(USER, n.id, { progress: 10 });
    unsubscribe();
    updateNotification(USER, n.id, { progress: 20 });

    expect(seen).toEqual([USER, USER]);
  });

  it('ignores an update for a notification that no longer exists', () => {
    expect(() => updateNotification(USER, 'gone', { progress: 50 })).not.toThrow();
  });
});

// The bug this pins: "Clear complete" and "Clear all" both used to be able
// to drop an in-progress job from the list. Once dropped, its progress had
// nowhere to be shown until its next update quietly put the entry straight
// back - which read as the button not working. Neither button may ever
// touch a running job; "Clear complete" additionally must leave failures in
// place, since those are exactly what a user still needs to see.
describe('clearNotifications', () => {
  // A fresh user id per test, not just a describe-level constant:
  // clearNotifications deliberately never removes an in-progress entry, so
  // the previous test's still-running notification would otherwise survive
  // a beforeEach reset and leak into the next test's assertions.
  let clearUser: number;
  beforeEach(() => { clearUser = Math.floor(Math.random() * 1_000_000) + 10_000; });

  const add = (status: 'in-progress' | 'success' | 'error', title: string) =>
    addNotification(clearUser, { type: 'import', title, status });

  it('"Clear complete" (completedOnly) removes only successful ones, keeping failures and running jobs', () => {
    add('in-progress', 'Still running');
    add('success', 'Finished ok');
    add('error', 'Failed');

    clearNotifications(clearUser, true);

    const remaining = listNotifications(clearUser).map(n => n.title);
    expect(remaining).toEqual(expect.arrayContaining(['Still running', 'Failed']));
    expect(remaining).not.toContain('Finished ok');
    expect(remaining).toHaveLength(2);
  });

  it('"Clear all" (not completedOnly) removes both successes and failures, but never a running job', () => {
    add('in-progress', 'Still running');
    add('success', 'Finished ok');
    add('error', 'Failed');

    clearNotifications(clearUser, false);

    const remaining = listNotifications(clearUser).map(n => n.title);
    expect(remaining).toEqual(['Still running']);
  });
});
