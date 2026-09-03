/**
 * Unit tests for a scheduled playlist refresh's two update modes.
 *
 *   replace    - the playlist mirrors its source, so the old Plex playlist is
 *                deleted and rebuilt from the refreshed tracks. That is what
 *                keeps the playlist in the source's order, which matters for
 *                a chart.
 *   accumulate - the playlist only ever grows. The existing Plex playlist is
 *                added to in place, so it keeps its ratingKey, its cover and
 *                anything the user added by hand, and tracks that dropped out
 *                of the source stay put.
 */

import Database from 'better-sqlite3';
import fs from 'fs';
import path from 'path';
import { DatabaseService } from '../../src/database/database';
import { runSingleSchedule } from '../../src/services/schedule-checker-job';

// Inlined rather than referencing a const: jest hoists mock factories above
// every declaration in the file.
jest.mock('../../src/services/import', () => ({
  importPlaylist: jest.fn().mockResolvedValue({
    matched: [
      { matched: true, plexRatingKey: '100' }, // already in the playlist
      { matched: true, plexRatingKey: '200' }, // new this run
    ],
    unmatched: [],
    playlistName: 'Test Playlist',
  }),
}));

const plexCalls: Record<string, jest.Mock> = {
  deletePlaylist: jest.fn().mockResolvedValue(undefined),
  createPlaylist: jest.fn().mockResolvedValue({ ratingKey: 'new-rk' }),
  addToPlaylist: jest.fn().mockResolvedValue(undefined),
  // The playlist already holds track 100, plus track 999 which has since
  // dropped out of the source.
  getPlaylistTracks: jest.fn().mockResolvedValue([{ ratingKey: '100' }, { ratingKey: '999' }]),
  getPlaylists: jest.fn().mockResolvedValue([]),
  uploadPlaylistPoster: jest.fn().mockResolvedValue(undefined),
};

jest.mock('../../src/services/plex', () => ({
  PlexClient: jest.fn().mockImplementation(() => plexCalls),
  resolvePlexToken: jest.fn((user: any, server: any) => server?.access_token || user?.plex_token),
}));

describe('scheduled refresh update modes', () => {
  let db: Database.Database;
  let dbService: DatabaseService;
  let userId: number;
  let playlistId: number;

  beforeEach(() => {
    jest.clearAllMocks();
    db = new Database(':memory:');
    db.pragma('foreign_keys = ON');
    db.exec(fs.readFileSync(path.join(__dirname, '../../src/database/schema.sql'), 'utf-8'));
    dbService = new DatabaseService(db);

    const user = dbService.createUser('plex1', 'user1', 'token1');
    userId = user.id;
    dbService.saveUserServer(userId, 'Server', 'client1', 'http://localhost:32400', '1');
    playlistId = dbService.createPlaylist(userId, 'existing-rk', 'Test Playlist', 'spotify', 'http://spotify/pl').id;
  });

  afterEach(() => db.close());

  const runWithConfig = async (config: Record<string, unknown>) => {
    const schedule = dbService.createSchedule(userId, {
      playlist_id: playlistId,
      schedule_type: 'playlist_refresh',
      frequency: 'daily',
      start_date: '2020-01-01',
      // createSchedule stringifies this itself - passing a string here would
      // double-encode it, and the job would parse config back out as a string
      // with no fields on it.
      config,
    } as any);
    await runSingleSchedule(dbService, schedule);
  };

  it('replace deletes the old playlist and rebuilds it', async () => {
    await runWithConfig({ updateMode: 'replace' });

    expect(plexCalls.deletePlaylist).toHaveBeenCalledWith('existing-rk');
    expect(plexCalls.createPlaylist).toHaveBeenCalled();
    expect(plexCalls.addToPlaylist).not.toHaveBeenCalled();
  });

  it('accumulate adds only the new tracks and never deletes', async () => {
    await runWithConfig({ updateMode: 'accumulate' });

    expect(plexCalls.deletePlaylist).not.toHaveBeenCalled();
    expect(plexCalls.createPlaylist).not.toHaveBeenCalled();

    // Track 100 is already there, so only 200 is added - and 999, which left
    // the source, is left alone.
    const [, addedUris] = plexCalls.addToPlaylist.mock.calls[0];
    expect(addedUris).toHaveLength(1);
    expect(addedUris[0]).toContain('/200');
  });

  it('defaults to replace when no mode is stored', async () => {
    await runWithConfig({});

    expect(plexCalls.deletePlaylist).toHaveBeenCalledWith('existing-rk');
  });

  it('treats a legacy overwriteExisting:false schedule as accumulate', async () => {
    // That flag used to create a second playlist of the same name on every
    // run, piling up duplicates in Plex rather than tracks in one playlist.
    await runWithConfig({ overwriteExisting: false });

    expect(plexCalls.deletePlaylist).not.toHaveBeenCalled();
    expect(plexCalls.addToPlaylist).toHaveBeenCalled();
  });
});
