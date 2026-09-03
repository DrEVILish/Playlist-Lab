/**
 * Unit tests for the schedule checker job.
 *
 * Regression coverage for a bug where runSingleSchedule() temporarily
 * monkey-patched the shared (process-wide singleton) DatabaseService's
 * getDueSchedules() method to force it to "see" only the one schedule being
 * manually run, then restored the original afterwards. Since
 * routes/schedules.ts invokes runSingleSchedule() fire-and-forget (not
 * awaited) - both for a single "Run Now" and in a loop for "Run All
 * Schedules" - two overlapping calls would race: whichever `finally`
 * resolved last would restore a stale, already-monkey-patched closure
 * instead of the true original, permanently breaking the background
 * scheduler until process restart.
 */

import Database from 'better-sqlite3';
import fs from 'fs';
import path from 'path';
import { DatabaseService } from '../../src/database/database';
import { runSingleSchedule } from '../../src/services/schedule-checker-job';

jest.mock('../../src/services/import', () => ({
  importPlaylist: jest.fn().mockResolvedValue({
    matched: [],
    unmatched: [],
    playlistName: 'Test Playlist',
  }),
}));

jest.mock('../../src/services/plex', () => ({
  resolvePlexToken: jest.fn((user: any, server: any) => server?.access_token || user?.plex_token),
  PlexClient: jest.fn().mockImplementation(() => ({
    getPlaylists: jest.fn().mockResolvedValue([]),
    deletePlaylist: jest.fn().mockResolvedValue(undefined),
    createPlaylist: jest.fn().mockResolvedValue({ ratingKey: 'new-rk' }),
    uploadPlaylistPoster: jest.fn().mockResolvedValue(undefined),
    getPlaylistTracks: jest.fn().mockResolvedValue([]),
    removeFromPlaylist: jest.fn().mockResolvedValue(undefined),
    addToPlaylist: jest.fn().mockResolvedValue(undefined),
  })),
}));

describe('runSingleSchedule', () => {
  let db: Database.Database;
  let dbService: DatabaseService;

  beforeEach(() => {
    db = new Database(':memory:');
    db.pragma('foreign_keys = ON');
    const schema = fs.readFileSync(path.join(__dirname, '../../src/database/schema.sql'), 'utf-8');
    db.exec(schema);
    dbService = new DatabaseService(db);
  });

  afterEach(() => {
    db.close();
  });

  it('does not corrupt getDueSchedules when two manual runs overlap', async () => {
    const user = dbService.createUser('plex1', 'user1', 'token1');
    dbService.saveUserServer(user.id, 'Server', 'client1', 'http://localhost:32400');

    const playlist1 = dbService.createPlaylist(user.id, 'pl1', 'Playlist 1', 'spotify', 'http://spotify/pl1');
    const playlist2 = dbService.createPlaylist(user.id, 'pl2', 'Playlist 2', 'spotify', 'http://spotify/pl2');

    const schedule1 = dbService.createSchedule(user.id, {
      playlist_id: playlist1.id,
      schedule_type: 'playlist_refresh',
      frequency: 'daily',
      start_date: '2020-01-01',
    });
    const schedule2 = dbService.createSchedule(user.id, {
      playlist_id: playlist2.id,
      schedule_type: 'playlist_refresh',
      frequency: 'daily',
      start_date: '2020-01-01',
    });

    // Simulate routes/schedules.ts firing off two "Run Now" calls without
    // awaiting them individually (fire-and-forget), so their execution
    // overlaps.
    await Promise.all([
      runSingleSchedule(dbService, schedule1),
      runSingleSchedule(dbService, schedule2),
    ]);

    // getDueSchedules must still be the real prototype method - not left
    // monkey-patched to some stale closure from one of the manual runs.
    expect(dbService.getDueSchedules).toBe(DatabaseService.prototype.getDueSchedules);
    expect(Object.prototype.hasOwnProperty.call(dbService, 'getDueSchedules')).toBe(false);

    // And it must still reflect live DB state rather than a hard-coded
    // single-schedule array: a freshly created, genuinely-due schedule
    // should show up.
    const freshPlaylist = dbService.createPlaylist(user.id, 'pl3', 'Playlist 3', 'spotify', 'http://spotify/pl3');
    const freshSchedule = dbService.createSchedule(user.id, {
      playlist_id: freshPlaylist.id,
      schedule_type: 'playlist_refresh',
      frequency: 'daily',
      start_date: '2020-01-01',
    });

    const due = dbService.getDueSchedules();
    const dueIds = due.map(s => s.id);
    expect(dueIds).toContain(freshSchedule.id);
    // schedule1/schedule2 already ran and had last_run updated, so they
    // should no longer be due under the daily frequency.
    expect(dueIds).not.toContain(schedule1.id);
    expect(dueIds).not.toContain(schedule2.id);
  });
});
