/**
 * Unit tests for the missing-tracks deemix routes: POST /:id/deemix-download
 * and POST /deemix-all.
 *
 * Two bugs pinned here:
 *  - Error messages for a failed deemix queue attempt used to be generic
 *    ("No deemix match found for this track", "Failed to queue deemix
 *    download") with no way to tell which of a user's hundreds of missing
 *    tracks they were about. They must name the track.
 *  - "Deemix All" used to report success (a checkmark in the notification
 *    bell) the moment tracks were *queued* for search, before deemix had
 *    actually found or downloaded anything - "Queued" is not "done". The
 *    notification the route creates must start (and stay, until the batch
 *    actually finishes) in-progress.
 */

import request from 'supertest';
import Database from 'better-sqlite3';
import express, { Express } from 'express';
import { initializeDatabase } from '../../src/database/init';
import { DatabaseService } from '../../src/database/database';
import { attachDatabase } from '../../src/middleware/auth';
import { errorHandler } from '../../src/middleware/error-handler';
import missingRoutes from '../../src/routes/missing';
import { listNotifications, clearNotifications } from '../../src/services/job-notifications';
import fs from 'fs';
import path from 'path';
import os from 'os';

let findBestDeemixMatchesMock: jest.Mock;
let queueDeemixDownloadMock: jest.Mock;
// Lets a test hold the Plex-retry pre-check open (mirrors the pattern in
// missing-retry.test.ts) so it can observe the "Deemix All" notification
// while the batch is deliberately still running, rather than racing a fully
// mocked (and therefore near-instant) background chain to completion.
let resolveMatch: (() => void) | null = null;

jest.mock('../../src/services/deemix', () => ({
  findBestDeemixMatches: jest.fn(),
  resolveDownloadUrl: jest.fn(async (match: any) => match.link),
  queueDeemixDownload: jest.fn(),
  startDeemixDownload: jest.fn(),
}));

jest.mock('../../src/services/matching', () => ({
  matchPlaylist: jest.fn(async () => new Promise((resolve) => { resolveMatch = () => resolve([]); })),
  buildRememberedMatchMap: jest.fn(() => new Map()),
  rememberMatches: jest.fn(),
  insertMatchedTrackIntoPlaylist: jest.fn(),
}));

jest.mock('../../src/services/plex', () => ({
  PlexService: jest.fn().mockImplementation(() => ({})),
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

describe('missing tracks deemix routes', () => {
  let db: Database.Database;
  let dbService: DatabaseService;
  let app: Express;
  let userId: number;
  let playlistId: number;

  beforeEach(() => {
    db = createTestDatabase();
    dbService = new DatabaseService(db);
    findBestDeemixMatchesMock = require('../../src/services/deemix').findBestDeemixMatches;
    queueDeemixDownloadMock = require('../../src/services/deemix').queueDeemixDownload;
    findBestDeemixMatchesMock.mockReset();
    queueDeemixDownloadMock.mockReset();
    resolveMatch = null;

    const user = dbService.createUser('plex1', 'user1', 'token1');
    userId = user.id;
    clearNotifications(userId);
    dbService.saveUserServer(userId, 'Server', 'client1', 'http://localhost:32400', '1');
    const playlist = dbService.createPlaylist(userId, 'plex-pl-1', 'Test Playlist', 'spotify');
    playlistId = playlist.id;

    app = express();
    app.use(express.json());
    app.use((req, _res, next) => {
      (req as any).session = { userId };
      next();
    });
    app.use(attachDatabase(dbService));
    app.use('/api/missing', missingRoutes);
    app.use(errorHandler);
  });

  afterEach(() => {
    cleanupTestDatabase(db);
  });

  describe('POST /:id/deemix-download', () => {
    it('names the track in the 404 when no deemix match is good enough', async () => {
      dbService.addMissingTracks(userId, playlistId, [
        { title: 'Obscure B-Side', artist: 'Some Artist', position: 0, source: 'spotify' },
      ]);
      const [track] = dbService.getUserMissingTracks(userId);
      findBestDeemixMatchesMock.mockResolvedValue([]);

      const response = await request(app)
        .post(`/api/missing/${track.id}/deemix-download`)
        .expect(404);

      expect(response.body.error.message).toContain('Some Artist - Obscure B-Side');
    });

    it('names the track in the 500 when queueing the download itself fails', async () => {
      dbService.addMissingTracks(userId, playlistId, [
        { title: 'Rare Track', artist: 'Another Artist', position: 0, source: 'spotify' },
      ]);
      const [track] = dbService.getUserMissingTracks(userId);
      findBestDeemixMatchesMock.mockResolvedValue([
        { match: { id: 1, title: 'Rare Track', link: 'https://deezer.com/track/1', artist: { name: 'Another Artist' }, album: {} }, score: 95 },
      ]);
      queueDeemixDownloadMock.mockRejectedValue(new Error('deemix rejected the download (NotLoggedIn)'));

      const response = await request(app)
        .post(`/api/missing/${track.id}/deemix-download`)
        .expect(500);

      expect(response.body.error.message).toContain('Another Artist - Rare Track');
      expect(response.body.error.message).toContain('NotLoggedIn');
    });
  });

  describe('POST /deemix-all', () => {
    it('creates an in-progress notification, not one that already reads as complete', async () => {
      dbService.addMissingTracks(userId, playlistId, [
        { title: 'Track One', artist: 'Artist One', position: 0, source: 'spotify' },
      ]);
      findBestDeemixMatchesMock.mockResolvedValue([
        { match: { id: 1, title: 'Track One', link: 'https://deezer.com/track/1', artist: { name: 'Artist One' }, album: {} }, score: 95 },
      ]);
      queueDeemixDownloadMock.mockResolvedValue({ uuid: 'track_1_3', title: 'Track One', artist: 'Artist One' });

      await request(app).post('/api/missing/deemix-all').send({ playlistId }).expect(200);

      // The bug: this used to be reported as already finished ('success')
      // the instant the batch was queued, rather than while it runs. Caught
      // deliberately mid-run here (matchPlaylist's pre-check is still
      // pending - see resolveMatch) - a race that resolves everything
      // instantly would let a premature 'success' slip through unnoticed.
      const notification = listNotifications(userId).find(n => n.title.includes('Deemix All'));
      expect(notification).toBeDefined();
      expect(notification!.status).toBe('in-progress');

      resolveMatch!();
      for (let i = 0; i < 10; i++) {
        await new Promise((r) => setImmediate(r));
      }
    });

    it('marks the batch notification success only once every track has actually been queued', async () => {
      dbService.addMissingTracks(userId, playlistId, [
        { title: 'Track One', artist: 'Artist One', position: 0, source: 'spotify' },
      ]);
      findBestDeemixMatchesMock.mockResolvedValue([
        { match: { id: 1, title: 'Track One', link: 'https://deezer.com/track/1', artist: { name: 'Artist One' }, album: {} }, score: 95 },
      ]);
      queueDeemixDownloadMock.mockResolvedValue({ uuid: 'track_1_3', title: 'Track One', artist: 'Artist One' });

      await request(app).post('/api/missing/deemix-all').send({ playlistId }).expect(200);

      resolveMatch!();
      // Flush the fire-and-forget background action.
      for (let i = 0; i < 10; i++) {
        await new Promise((r) => setImmediate(r));
      }

      const notification = listNotifications(userId).find(n => n.title.includes('Deemix All'));
      expect(notification!.status).toBe('success');
      expect(notification!.detail).toContain('Queued 1 of 1');
    });
  });
});
