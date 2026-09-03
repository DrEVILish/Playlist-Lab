/**
 * Unit Tests for Schedule Routes
 * 
 * Tests CRUD operations and validation for schedule API endpoints.
 */

import request from 'supertest';
import Database from 'better-sqlite3';
import { initializeDatabase } from '../../src/database/init';
import { DatabaseService } from '../../src/database/database';
import express, { Express } from 'express';
import schedulesRoutes from '../../src/routes/schedules';
import { attachDatabase } from '../../src/middleware/auth';
import { errorHandler } from '../../src/middleware/error-handler';
import path from 'path';
import fs from 'fs';
import os from 'os';

// run-all's concurrency cap is exercised below by tracking how many mocked
// runSingleSchedule calls are in flight at once.
let concurrentCalls = 0;
let maxConcurrentCalls = 0;
let completedCalls = 0;

jest.mock('../../src/services/schedule-checker-job', () => ({
  runSingleSchedule: jest.fn(async () => {
    concurrentCalls++;
    maxConcurrentCalls = Math.max(maxConcurrentCalls, concurrentCalls);
    await new Promise(resolve => setTimeout(resolve, 20));
    concurrentCalls--;
    completedCalls++;
  }),
}));

/**
 * Create a temporary test database
 */
function createTestDatabase(): Database.Database {
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'playlist-lab-test-'));
  const dbPath = path.join(tempDir, 'test.db');
  return initializeDatabase(dbPath);
}

/**
 * Clean up test database
 */
function cleanupTestDatabase(db: Database.Database): void {
  const dbPath = db.name;
  db.close();
  
  if (dbPath && dbPath !== ':memory:') {
    try {
      fs.unlinkSync(dbPath);
      fs.rmdirSync(path.dirname(dbPath));
    } catch (error) {
      // Ignore cleanup errors
    }
  }
}

describe('Schedule Routes', () => {
  let db: Database.Database;
  let dbService: DatabaseService;
  let app: Express;
  let userId: number;
  let playlistId: number;

  beforeEach(() => {
    db = createTestDatabase();
    dbService = new DatabaseService(db);
    
    // Create test user
    const user = dbService.createUser('test-user-1', 'Test User', 'test-token');
    userId = user.id;

    // Create test playlist
    const playlist = dbService.createPlaylist(userId, 'plex-playlist-1', 'Test Playlist', 'spotify');
    playlistId = playlist.id;

    // Create app with mocked session
    app = express();
    app.use(express.json());
    app.use((req, _res, next) => {
      // Mock session
      (req as any).session = { userId };
      next();
    });
    app.use(attachDatabase(dbService));
    app.use('/api/schedules', schedulesRoutes);
    app.use(errorHandler);
  });

  afterEach(() => {
    cleanupTestDatabase(db);
  });

  describe('GET /api/schedules', () => {
    it('should return empty array when user has no schedules', async () => {
      const response = await request(app)
        .get('/api/schedules')
        .expect(200);

      expect(response.body.success).toBe(true);
      expect(response.body.schedules).toEqual([]);
    });

    it('should return user schedules', async () => {
      // Create schedules
      dbService.createSchedule(userId, {
        schedule_type: 'mix_generation',
        frequency: 'daily',
        start_date: '2024-01-01',
        config: { mixTypes: ['weekly'] }
      });

      dbService.createSchedule(userId, {
        playlist_id: playlistId,
        schedule_type: 'playlist_refresh',
        frequency: 'weekly',
        start_date: '2024-01-01'
      });

      const response = await request(app)
        .get('/api/schedules')
        .expect(200);

      expect(response.body.success).toBe(true);
      expect(response.body.schedules).toHaveLength(2);
      expect(response.body.schedules[0].scheduleType).toBe('mix_generation');
      expect(response.body.schedules[1].scheduleType).toBe('playlist_refresh');
    });

    it('should include the source playlist link for playlist-refresh schedules', async () => {
      const sourcedPlaylist = dbService.createPlaylist(
        userId,
        'plex-playlist-2',
        'Sourced Playlist',
        'spotify',
        'https://open.spotify.com/playlist/abc123'
      );
      dbService.createSchedule(userId, {
        playlist_id: sourcedPlaylist.id,
        schedule_type: 'playlist_refresh',
        frequency: 'weekly',
        start_date: '2024-01-01'
      });

      const response = await request(app).get('/api/schedules').expect(200);

      const schedule = response.body.schedules[0];
      expect(schedule.source).toBe('spotify');
      expect(schedule.sourceUrl).toBe('https://open.spotify.com/playlist/abc123');
    });

    it('should include the source chart link for chart-import schedules', async () => {
      dbService.createSchedule(userId, {
        schedule_type: 'playlist_refresh',
        frequency: 'daily',
        start_date: '2024-01-01',
        config: {
          chartName: 'Billboard Hot 100',
          chartSource: 'billboard',
          chartUrl: 'https://www.billboard.com/charts/hot-100/'
        }
      });

      const response = await request(app).get('/api/schedules').expect(200);

      const schedule = response.body.schedules[0];
      expect(schedule.source).toBe('billboard');
      expect(schedule.sourceUrl).toBe('https://www.billboard.com/charts/hot-100/');
    });

    it('should omit source fields when there is no linked playlist or chart URL', async () => {
      dbService.createSchedule(userId, {
        schedule_type: 'mix_generation',
        frequency: 'daily',
        start_date: '2024-01-01',
        config: { mixTypes: ['weekly'] }
      });

      const response = await request(app).get('/api/schedules').expect(200);

      const schedule = response.body.schedules[0];
      expect(schedule.sourceUrl).toBeUndefined();
    });
  });

  describe('POST /api/schedules', () => {
    it('should create a mix generation schedule', async () => {
      const response = await request(app)
        .post('/api/schedules')
        .send({
          schedule_type: 'mix_generation',
          frequency: 'daily',
          start_date: '2024-01-01',
          config: { mixTypes: ['weekly', 'daily'] }
        })
        .expect(201);

      expect(response.body.success).toBe(true);
      expect(response.body.schedule).toBeDefined();
      expect(response.body.schedule.scheduleType).toBe('mix_generation');
      expect(response.body.schedule.frequency).toBe('daily');
      expect(response.body.schedule.userId).toBe(userId);
    });

    it('should create a playlist refresh schedule', async () => {
      const response = await request(app)
        .post('/api/schedules')
        .send({
          playlist_id: playlistId,
          schedule_type: 'playlist_refresh',
          frequency: 'weekly',
          start_date: '2024-01-01'
        })
        .expect(201);

      expect(response.body.success).toBe(true);
      expect(response.body.schedule).toBeDefined();
      expect(response.body.schedule.scheduleType).toBe('playlist_refresh');
      expect(response.body.schedule.playlistId).toBe(playlistId);
    });

    it('should reject schedule with missing required fields', async () => {
      const response = await request(app)
        .post('/api/schedules')
        .send({
          schedule_type: 'mix_generation'
          // Missing frequency and start_date
        })
        .expect(400);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Missing required fields');
    });

    it('should reject schedule with invalid schedule_type', async () => {
      const response = await request(app)
        .post('/api/schedules')
        .send({
          schedule_type: 'invalid_type',
          frequency: 'daily',
          start_date: '2024-01-01'
        })
        .expect(400);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Invalid schedule_type');
    });

    it('should reject schedule with invalid frequency', async () => {
      const response = await request(app)
        .post('/api/schedules')
        .send({
          schedule_type: 'mix_generation',
          frequency: 'invalid_frequency',
          start_date: '2024-01-01'
        })
        .expect(400);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Invalid frequency');
    });

    it('should reject playlist_refresh schedule without playlist_id', async () => {
      const response = await request(app)
        .post('/api/schedules')
        .send({
          schedule_type: 'playlist_refresh',
          frequency: 'daily',
          start_date: '2024-01-01'
        })
        .expect(400);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('playlist_id is required');
    });
  });

  describe('PUT /api/schedules/:id', () => {
    it('should update schedule frequency', async () => {
      const schedule = dbService.createSchedule(userId, {
        schedule_type: 'mix_generation',
        frequency: 'daily',
        start_date: '2024-01-01'
      });

      const response = await request(app)
        .put(`/api/schedules/${schedule.id}`)
        .send({
          frequency: 'weekly'
        })
        .expect(200);

      expect(response.body.success).toBe(true);
      expect(response.body.schedule.frequency).toBe('weekly');
    });

    it('should update schedule config', async () => {
      const schedule = dbService.createSchedule(userId, {
        schedule_type: 'mix_generation',
        frequency: 'daily',
        start_date: '2024-01-01',
        config: { mixTypes: ['weekly'] }
      });

      const response = await request(app)
        .put(`/api/schedules/${schedule.id}`)
        .send({
          config: { mixTypes: ['weekly', 'daily', 'timecapsule'] }
        })
        .expect(200);

      expect(response.body.success).toBe(true);
      // transformSchedule() already parses config into an object before
      // sending it in the API response (see src/routes/schedules.ts), so
      // no JSON.parse is needed here.
      expect(response.body.schedule.config).toEqual({
        mixTypes: ['weekly', 'daily', 'timecapsule']
      });
    });

    it('should return 404 for non-existent schedule', async () => {
      const response = await request(app)
        .put('/api/schedules/99999')
        .send({
          frequency: 'weekly'
        })
        .expect(404);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Schedule not found');
    });

    it('should reject invalid schedule ID', async () => {
      const response = await request(app)
        .put('/api/schedules/invalid')
        .send({
          frequency: 'weekly'
        })
        .expect(400);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Invalid schedule ID');
    });

    it('should reject invalid frequency', async () => {
      const schedule = dbService.createSchedule(userId, {
        schedule_type: 'mix_generation',
        frequency: 'daily',
        start_date: '2024-01-01'
      });

      const response = await request(app)
        .put(`/api/schedules/${schedule.id}`)
        .send({
          frequency: 'invalid_frequency'
        })
        .expect(400);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Invalid frequency');
    });
  });

  describe('DELETE /api/schedules/:id', () => {
    it('should delete a schedule', async () => {
      const schedule = dbService.createSchedule(userId, {
        schedule_type: 'mix_generation',
        frequency: 'daily',
        start_date: '2024-01-01'
      });

      const response = await request(app)
        .delete(`/api/schedules/${schedule.id}`)
        .expect(200);

      expect(response.body.success).toBe(true);
      expect(response.body.message).toContain('deleted successfully');

      // Verify schedule is deleted
      const deletedSchedule = dbService.getScheduleById(schedule.id);
      expect(deletedSchedule).toBeNull();
    });

    it('should return 404 for non-existent schedule', async () => {
      const response = await request(app)
        .delete('/api/schedules/99999')
        .expect(404);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Schedule not found');
    });

    it('should reject invalid schedule ID', async () => {
      const response = await request(app)
        .delete('/api/schedules/invalid')
        .expect(400);

      expect(response.body.error).toBeDefined();
      expect(response.body.error.message).toContain('Invalid schedule ID');
    });
  });

  describe('POST /api/schedules/run-all', () => {
    it('caps concurrent schedule execution instead of running all at once', async () => {
      concurrentCalls = 0;
      maxConcurrentCalls = 0;
      completedCalls = 0;

      const scheduleCount = 6;
      for (let i = 0; i < scheduleCount; i++) {
        dbService.createSchedule(userId, {
          schedule_type: 'mix_generation',
          frequency: 'daily',
          start_date: '2024-01-01',
          config: { mixTypes: ['weekly'] }
        });
      }

      const response = await request(app)
        .post('/api/schedules/run-all')
        .expect(200);

      expect(response.body.triggered).toBe(scheduleCount);

      // The response returns before the background work finishes - wait for
      // all mocked runs to complete.
      while (completedCalls < scheduleCount) {
        await new Promise(resolve => setTimeout(resolve, 10));
      }

      expect(maxConcurrentCalls).toBeLessThanOrEqual(3);
      expect(maxConcurrentCalls).toBeGreaterThan(1);
    });
  });
});
