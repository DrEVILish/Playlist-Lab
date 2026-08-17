import type { Schedule } from '@playlist-lab/shared';

/**
 * Returns a schedule's next-run time as a millisecond timestamp, or null when
 * it can't be determined. Shared between the Schedules tab (for display and
 * sorting) and the control panel stats strip (for the "next run" summary).
 */
export function getNextRunTimestamp(schedule: Schedule): number | null {
  try {
    if (!schedule.lastRun) {
      const startDate = new Date(schedule.startDate + 'T00:00:00');
      return isNaN(startDate.getTime()) ? null : startDate.getTime();
    }

    const lastRun = new Date(schedule.lastRun * 1000);
    if (isNaN(lastRun.getTime())) return null;

    const daysToAdd = {
      daily: 1,
      weekly: 7,
      fortnightly: 14,
      monthly: 30,
    }[schedule.frequency];

    if (!daysToAdd) return null;

    const nextRun = new Date(lastRun);
    nextRun.setDate(nextRun.getDate() + daysToAdd);
    return nextRun.getTime();
  } catch {
    return null;
  }
}

export function getNextRunDate(schedule: Schedule): string {
  const ts = getNextRunTimestamp(schedule);
  if (ts === null) return schedule.lastRun ? 'Invalid date' : 'Not scheduled';
  return new Date(ts).toLocaleDateString();
}

/** Compact "in 2d" / "in 5h" / "due" form of a schedule's next run, for
 * space-constrained at-a-glance UI (the playlist table's Schedule column). */
export function getNextRunRelative(schedule: Schedule): string {
  const ts = getNextRunTimestamp(schedule);
  if (ts === null) return '';
  const diffMs = ts - Date.now();
  if (diffMs <= 0) return 'due';
  const hours = Math.round(diffMs / 3600000);
  if (hours < 1) return '<1h';
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}
