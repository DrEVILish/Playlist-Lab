/**
 * Unit tests for the playlist sort and dedupe utilities.
 *
 * Both rewrite a playlist in place through Plex's per-item API - there is no
 * bulk reorder or bulk delete - so what matters is that the right items are
 * moved or removed, and in the right order.
 *
 * Both routes now run through the shared action queue (see
 * services/action-queue.ts) instead of finishing within the request, so each
 * test posts, waits a tick for the queued job to run against the mocked Plex
 * client, then asserts against the mock calls and the resulting notification
 * rather than the (now immediate, pre-result) HTTP response body.
 */

import request from 'supertest';
import express, { Express } from 'express';
import playlistRoutes from '../../src/routes/playlists';
import { errorHandler } from '../../src/middleware/error-handler';
import { listNotifications, clearNotifications } from '../../src/services/job-notifications';

const USER_ID = 1;

// Queued jobs run against fully-mocked (synchronously-resolving) Plex calls,
// so a couple of event-loop ticks is enough for the job to finish.
const flush = () => new Promise(resolve => setTimeout(resolve, 20));

const plex = {
  getPlaylistTracks: jest.fn(),
  movePlaylistItem: jest.fn().mockResolvedValue(undefined),
  removeFromPlaylist: jest.fn().mockResolvedValue(undefined),
};

jest.mock('../../src/services/plex', () => ({
  PlexService: jest.fn().mockImplementation(() => plex),
  PlexClient: jest.fn().mockImplementation(() => plex),
  resolvePlexToken: jest.fn((user: any, server: any) => server?.access_token || user?.plex_token),
}));

const track = (playlistItemID: number, over: Record<string, unknown> = {}) => ({
  playlistItemID,
  ratingKey: String(playlistItemID),
  title: 'T',
  grandparentTitle: 'A',
  parentTitle: 'Al',
  year: 2000,
  duration: 1000,
  ...over,
});

describe('playlist utilities', () => {
  let app: Express;

  beforeEach(() => {
    jest.clearAllMocks();
    clearNotifications(USER_ID);
    app = express();
    app.use(express.json());
    app.use((req: any, _res, next) => {
      req.session = { userId: 1 };
      req.dbService = {
        getUserById: () => ({ id: 1, plex_token: 't', plex_user_id: 'p', plex_username: 'u', plex_thumb: null }),
        getUserServer: () => ({ server_url: 'http://plex', library_id: '1', server_client_id: 'c' }),
        getPlaylistByPlexId: () => null,
        updatePlaylist: jest.fn(),
        isAdmin: () => true,
        isUserEnabled: () => true,
      };
      next();
    });
    app.use('/api/playlists', playlistRoutes);
    app.use(errorHandler);
  });

  describe('sort', () => {
    it('moves tracks into the requested order', async () => {
      plex.getPlaylistTracks.mockResolvedValue([
        track(1, { title: 'Charlie' }),
        track(2, { title: 'alpha' }),
        track(3, { title: 'Bravo' }),
      ]);

      await request(app).post('/api/playlists/rk/sort').send({ by: 'title', direction: 'asc' }).expect(202);
      await flush();

      // Case-insensitive, and each track is placed after the previous one.
      expect(plex.movePlaylistItem.mock.calls.map(c => [c[1], c[2]])).toEqual([
        ['2', '0'],
        ['3', '2'],
        ['1', '3'],
      ]);
    });

    it('sorts descending when asked', async () => {
      plex.getPlaylistTracks.mockResolvedValue([
        track(1, { year: 1990 }),
        track(2, { year: 2020 }),
      ]);

      await request(app).post('/api/playlists/rk/sort').send({ by: 'year', direction: 'desc' }).expect(202);
      await flush();

      expect(plex.movePlaylistItem.mock.calls.map(c => c[1])).toEqual(['2', '1']);
    });

    it('rejects a field it cannot sort on', async () => {
      await request(app).post('/api/playlists/rk/sort').send({ by: 'loudness' }).expect(400);
      expect(plex.movePlaylistItem).not.toHaveBeenCalled();
    });
  });

  describe('dedupe', () => {
    it('removes later repeats and keeps the first of each', async () => {
      plex.getPlaylistTracks.mockResolvedValue([
        { playlistItemID: 1, ratingKey: '100' },
        { playlistItemID: 2, ratingKey: '200' },
        { playlistItemID: 3, ratingKey: '100' }, // repeat
        { playlistItemID: 4, ratingKey: '100' }, // repeat
      ]);

      await request(app).post('/api/playlists/rk/dedupe').send({}).expect(202);
      await flush();

      // The first occurrence (item 1) survives; only the later ones go.
      expect(plex.removeFromPlaylist.mock.calls.map(c => c[1])).toEqual(['3', '4']);
      expect(listNotifications(USER_ID)[0]).toMatchObject({ status: 'success', detail: 'Removed 2 duplicate(s)' });
    });

    it('does nothing when there are no duplicates', async () => {
      plex.getPlaylistTracks.mockResolvedValue([
        { playlistItemID: 1, ratingKey: '100' },
        { playlistItemID: 2, ratingKey: '200' },
      ]);

      await request(app).post('/api/playlists/rk/dedupe').send({}).expect(202);
      await flush();

      expect(plex.removeFromPlaylist).not.toHaveBeenCalled();
      expect(listNotifications(USER_ID)[0]).toMatchObject({ status: 'success', detail: 'Removed 0 duplicate(s)' });
    });

    it('treats different recordings of the same song as distinct', async () => {
      plex.getPlaylistTracks.mockResolvedValue([
        { playlistItemID: 1, ratingKey: '100', title: 'Song' },
        { playlistItemID: 2, ratingKey: '999', title: 'Song' },
      ]);

      await request(app).post('/api/playlists/rk/dedupe').send({}).expect(202);
      await flush();

      expect(listNotifications(USER_ID)[0]).toMatchObject({ status: 'success', detail: 'Removed 0 duplicate(s)' });
    });
  });
});
