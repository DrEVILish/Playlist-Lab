/**
 * Unit tests for DELETE /api/playlists/by-plex-id/:ratingKey.
 *
 * The pre-existing DELETE /api/playlists/:id expects our internal numeric
 * playlist id, not a Plex ratingKey - but GET /api/playlists returns
 * playlists keyed by ratingKey (see playlists-list.test.ts), and most
 * playlists in a real library were never imported through this app so have
 * no numeric id at all. This route deletes by ratingKey directly, cleaning
 * up our tracking row too when one happens to exist.
 */

import request from 'supertest';
import Database from 'better-sqlite3';
import express, { Express } from 'express';
import { initializeDatabase } from '../../src/database/init';
import { DatabaseService } from '../../src/database/database';
import { attachDatabase } from '../../src/middleware/auth';
import { errorHandler } from '../../src/middleware/error-handler';
import playlistsRoutes from '../../src/routes/playlists';
import fs from 'fs';
import path from 'path';
import os from 'os';

const deletePlaylistMock = jest.fn().mockResolvedValue(undefined);

jest.mock('../../src/services/plex', () => ({
  PlexService: jest.fn().mockImplementation(() => ({
    deletePlaylist: deletePlaylistMock,
  })),
  resolvePlexToken: jest.fn((user: any, server: any) => server?.access_token || user?.plex_token),
}));

function createTestDatabase(): Database.Database {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'playlist-lab-test-'));
  return initializeDatabase(path.join(tempDir, 'test.db'));
}

function cleanupTestDatabase(db: Database.Database): void {
  const dbPath = db.name;
  db.close();
  if (dbPath && dbPath !== ':memory:') {
    try {
      fs.unlinkSync(dbPath);
      fs.rmdirSync(path.dirname(dbPath));
    } catch {
      // ignore
    }
  }
}

describe('DELETE /api/playlists/by-plex-id/:ratingKey', () => {
  let db: Database.Database;
  let dbService: DatabaseService;
  let app: Express;
  let userId: number;

  beforeEach(() => {
    db = createTestDatabase();
    dbService = new DatabaseService(db);
    deletePlaylistMock.mockClear();

    const user = dbService.createUser('plex1', 'user1', 'token1');
    userId = user.id;
    dbService.saveUserServer(userId, 'Server', 'client1', 'http://localhost:32400', '1');

    app = express();
    app.use(express.json());
    app.use((req, _res, next) => {
      (req as any).session = { userId };
      next();
    });
    app.use(attachDatabase(dbService));
    app.use('/api/playlists', playlistsRoutes);
    app.use(errorHandler);
  });

  afterEach(() => {
    cleanupTestDatabase(db);
  });

  it('deletes a playlist never imported through this app (no DB row)', async () => {
    await request(app).delete('/api/playlists/by-plex-id/plex-rk-999').expect(200);

    expect(deletePlaylistMock).toHaveBeenCalledWith('plex-rk-999');
  });

  it('also removes our tracking row when one exists for the ratingKey', async () => {
    const tracked = dbService.createPlaylist(userId, 'plex-rk-123', 'Tracked Playlist', 'spotify');

    await request(app).delete('/api/playlists/by-plex-id/plex-rk-123').expect(200);

    expect(deletePlaylistMock).toHaveBeenCalledWith('plex-rk-123');
    expect(dbService.getPlaylistById(tracked.id)).toBeNull();
  });

  it('does not touch another user\'s tracking row with the same ratingKey', async () => {
    const otherUser = dbService.createUser('plex2', 'user2', 'token2');
    const otherTracked = dbService.createPlaylist(otherUser.id, 'plex-rk-shared', 'Other User Playlist', 'deezer');

    await request(app).delete('/api/playlists/by-plex-id/plex-rk-shared').expect(200);

    expect(dbService.getPlaylistById(otherTracked.id)).not.toBeNull();
  });

  it('is routed before the numeric :id route (does not get swallowed by it)', async () => {
    // A ratingKey of "by-plex-id" would collide with the route segment
    // itself if this route were registered after `/:id` - registration
    // order here is what prevents that.
    const response = await request(app).delete('/api/playlists/by-plex-id/289103').expect(200);
    expect(response.body).toEqual({ success: true });
    expect(deletePlaylistMock).toHaveBeenCalledWith('289103');
  });
});
