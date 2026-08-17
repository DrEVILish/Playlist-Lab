/**
 * Unit tests for GET /api/playlists.
 *
 * This route fetches the live playlist list from Plex and cross-references
 * it against this app's own `playlists` tracking table (by plex_playlist_id)
 * so each entry also carries `dbId` (our internal numeric id, needed to look
 * up its schedule/missing tracks), `source`, and `sourceUrl` when the
 * playlist was imported through this app. Playlists that exist in Plex but
 * were never imported through this app (e.g. created directly in Plex)
 * should come back with no `dbId`/real `source` instead of a bogus or
 * mismatched one.
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

let mockPlexPlaylists: any[] = [];

jest.mock('../../src/services/plex', () => ({
  PlexService: jest.fn().mockImplementation(() => ({
    getPlaylists: jest.fn(async () => mockPlexPlaylists),
  })),
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

describe('GET /api/playlists', () => {
  let db: Database.Database;
  let dbService: DatabaseService;
  let app: Express;
  let userId: number;

  beforeEach(() => {
    db = createTestDatabase();
    dbService = new DatabaseService(db);
    mockPlexPlaylists = [];

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

  it('attaches dbId/source/sourceUrl for a playlist tracked in our DB', async () => {
    const tracked = dbService.createPlaylist(userId, 'plex-rk-123', 'My Playlist', 'spotify', 'https://open.spotify.com/playlist/abc');

    mockPlexPlaylists = [
      { ratingKey: 'plex-rk-123', title: 'My Playlist', playlistType: 'audio', leafCount: 10, duration: 600000 },
    ];

    const response = await request(app).get('/api/playlists').expect(200);

    expect(response.body.playlists).toHaveLength(1);
    expect(response.body.playlists[0]).toMatchObject({
      id: 'plex-rk-123',
      dbId: tracked.id,
      source: 'spotify',
      sourceUrl: 'https://open.spotify.com/playlist/abc',
      trackCount: 10,
    });
  });

  it('leaves dbId undefined and source "plex" for a playlist never imported through this app', async () => {
    mockPlexPlaylists = [
      { ratingKey: 'plex-rk-999', title: 'Untracked Playlist', playlistType: 'audio', leafCount: 5, duration: 300000 },
    ];

    const response = await request(app).get('/api/playlists').expect(200);

    expect(response.body.playlists).toHaveLength(1);
    expect(response.body.playlists[0].dbId).toBeUndefined();
    expect(response.body.playlists[0].source).toBe('plex');
    expect(response.body.playlists[0].sourceUrl).toBeUndefined();
  });

  it('does not cross-match a different user\'s tracked playlist with the same plex_playlist_id', async () => {
    const otherUser = dbService.createUser('plex2', 'user2', 'token2');
    dbService.createPlaylist(otherUser.id, 'plex-rk-shared', 'Other User Playlist', 'deezer', 'https://deezer.com/playlist/1');

    mockPlexPlaylists = [
      { ratingKey: 'plex-rk-shared', title: 'Shared Ratingkey Playlist', playlistType: 'audio', leafCount: 3, duration: 100000 },
    ];

    const response = await request(app).get('/api/playlists').expect(200);

    expect(response.body.playlists[0].dbId).toBeUndefined();
    expect(response.body.playlists[0].source).toBe('plex');
  });

  it('filters out non-audio playlists', async () => {
    mockPlexPlaylists = [
      { ratingKey: 'rk-1', title: 'Audio Playlist', playlistType: 'audio', leafCount: 1, duration: 1000 },
      { ratingKey: 'rk-2', title: 'Video Playlist', playlistType: 'video', leafCount: 1, duration: 1000 },
    ];

    const response = await request(app).get('/api/playlists').expect(200);

    expect(response.body.playlists).toHaveLength(1);
    expect(response.body.playlists[0].id).toBe('rk-1');
  });
});
