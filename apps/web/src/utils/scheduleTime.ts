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
