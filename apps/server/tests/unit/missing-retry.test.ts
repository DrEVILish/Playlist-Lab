/**
 * Unit tests for POST /api/missing/retry.
 *
 * A retry batch does several real Plex API calls per track, so retrying a
 * large missing-tracks list can take well over a minute. The route used to
 * await that work synchronously, holding the HTTP connection open long
 * enough to trip client/proxy timeouts - which surfaced to users as "An
 * unknown error occurred" even though the retry was still working
 * server-side. It now responds immediately once the job is queued and runs
 * the matching in the background; these tests cover that contract plus the
 * per-user queue that lets a retry requested while one is already running
 * join in behind it (deduped, tracked via GET /retry-status) rather than
 * being rejected outright.
 */

import request from 'supertest';
import Database from 'better-sqlite3';
import express, { Express } from 'express';
import { initializeDatabase } from '../../src/database/init';
import { DatabaseService } from '../../src/database/database';
import { attachDatabase } from '../../src/middleware/auth';
import { errorHandler } from '../../src/middleware/error-handler';
import missingRoutes from '../../src/routes/missing';
import fs from 'fs';
import path from 'path';
import os from 'os';

// Lets each test control exactly when matchPlaylist "finishes", so the
// in-flight guard can be tested deterministically instead of racing real
// timers.
let resolveMatch: (() => void) | null = null;
let matchResult: any[] = [];

jest.mock('../../src/services/matching', () => ({
  matchPlaylist: jest.fn(async () => {
    await new Promise<void>((resolve) => {
      resolveMatch = resolve;
    });
    return matchResult;
  }),
}));

jest.mock('../../src/services/plex', () => ({
  PlexService: jest.fn().mockImplementation(() => ({
    createPlaylist: jest.fn().mockResolvedValue({ ratingKey: 'new-rk' }),
    addToPlaylist: jest.fn().mockResolvedValue(undefined),
    getPlaylistTracks: jest.fn().mockResolvedValue([]),
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

describe('POST /api/missing/retry', () => {
  let db: Database.Database;
  let dbService: DatabaseService;
  let app: Express;
  let userId: number;
  let playlistId: number;

  beforeEach(() => {
    db = createTestDatabase();
    dbService = new DatabaseService(db);
    resolveMatch = null;
    matchResult = [];

    const user = dbService.createUser('plex1', 'user1', 'token1');
    userId = user.id;
    dbService.saveUserServer(userId, 'Server', 'client1', 'http://localhost:32400', '1');
    const playlist = dbService.createPlaylist(userId, 'plex-pl-1', 'Test Playlist', 'spotify');
    playlistId = playlist.id;
    dbService.addMissingTracks(userId, playlistId, [
      { title: 'Song A', artist: 'Artist A', position: 0, source: 'spotify' },
    ]);

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

  it('responds immediately with started:true instead of waiting for matching to finish', async () => {
    const response = await request(app).post('/api/missing/retry').send({}).expect(200);

    expect(response.body).toEqual({
      started: true,
      totalTracks: 1,
      message: expect.stringContaining('1'),
    });

    // matchPlaylist is still awaiting our manual resolve - proves the
    // response above did not wait for it.
    expect(resolveMatch).not.toBeNull();

    resolveMatch!();
    await new Promise((r) => setImmediate(r));
  });

  it('actually removes the track from the missing list once the background match completes', async () => {
    matchResult = [
      { title: 'Song A', artist: 'Artist A', matched: true, plexRatingKey: 'rk-1' },
    ];

    await request(app).post('/api/missing/retry').send({}).expect(200);

    resolveMatch!();
    // Flush the fire-and-forget background chain (several awaited steps).
    for (let i = 0; i < 10; i++) {
      await new Promise((r) => setImmediate(r));
    }

    const remaining = dbService.getUserMissingTracks(userId);
    expect(remaining).toHaveLength(0);
  });

  it('queues an overlapping retry for the same user instead of rejecting it, then runs it once the first finishes', async () => {
    const first = await request(app).post('/api/missing/retry').send({}).expect(200);
    expect(first.body.started).toBe(true);
    expect(first.body.queued).toBeUndefined();

    const second = await request(app).post('/api/missing/retry').send({}).expect(200);
    expect(second.body.started).toBe(true);
    expect(second.body.queued).toBe(true);
    expect(second.body.message).toMatch(/queued/i);

    // Still only one background job in flight - the second call didn't start
    // its own matchPlaylist() run, it just joined the pending queue.
    const statusWhileFirstRuns = await request(app).get('/api/missing/retry-status').expect(200);
    expect(statusWhileFirstRuns.body.active).not.toBeNull();

    // Finish the first batch. Nothing matched (matchResult defaults to []),
    // so the track is still missing and the queued retry should pick it up
    // and start a second matchPlaylist() run automatically.
    resolveMatch!();
    for (let i = 0; i < 10; i++) {
      await new Promise((r) => setImmediate(r));
    }

    // The chain moved on to the queued batch rather than going idle - proof
    // it actually ran automatically instead of just being dropped.
    const statusBetweenBatches = await request(app).get('/api/missing/retry-status').expect(200);
    expect(statusBetweenBatches.body.active).not.toBeNull();

    resolveMatch!();
    for (let i = 0; i < 10; i++) {
      await new Promise((r) => setImmediate(r));
    }

    const statusAfterChain = await request(app).get('/api/missing/retry-status').expect(200);
    expect(statusAfterChain.body.active).toBeNull();
  });

  it('allows a new retry once the previous background job has finished', async () => {
    await request(app).post('/api/missing/retry').send({}).expect(200);
    resolveMatch!();
    for (let i = 0; i < 10; i++) {
      await new Promise((r) => setImmediate(r));
    }

    // No missing tracks left (matchResult defaults to [] -> nothing matched,
    // but the in-flight guard should already be cleared either way).
    const again = await request(app).post('/api/missing/retry').send({}).expect(200);
    // Nothing left to retry after the first (unmatched) pass re-reads an
    // empty result set only if a track was actually removed; since
    // matchResult is [] here the track is still missing, so this should
    // start a fresh retry rather than report "no tracks to retry".
    expect(again.body.started).toBe(true);
    resolveMatch!();
    await new Promise((r) => setImmediate(r));
  });
});
