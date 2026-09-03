/**
 * Unit tests for POST /api/import/playlist-name.
 *
 * The import modal only needs one string - the playlist's name - but it used
 * to get it from /preview, which scrapes the entire playlist: an embed-page
 * fetch with a 15s budget and slower fallbacks behind it. The name field sat
 * on "Fetching name…" for 30 seconds or more as a result. This route answers
 * from cache, or from Spotify's public oEmbed endpoint, and never fails the
 * request - a blank name is valid, because the real one is filled in from
 * the scrape when the import actually runs.
 */

import request from 'supertest';
import express, { Express } from 'express';
import importRoutes from '../../src/routes/import';
import { errorHandler } from '../../src/middleware/error-handler';

describe('POST /api/import/playlist-name', () => {
  let app: Express;
  let cachedName: string | null;

  beforeEach(() => {
    cachedName = null;
    global.fetch = jest.fn() as any;

    app = express();
    app.use(express.json());
    app.use((req: any, _res, next) => {
      req.session = { userId: 1 };
      // The import router applies requireAuth, so the stub has to satisfy
      // that too - only getCachedPlaylist matters to the route under test.
      req.dbService = {
        getCachedPlaylist: () => (cachedName ? { name: cachedName, tracks: [] } : null),
        getUserById: () => ({ id: 1, plex_user_id: 'p1', plex_username: 'tester', plex_token: 't', plex_thumb: null }),
        isAdmin: () => true,
        isUserEnabled: () => true,
      };
      next();
    });
    app.use('/api/import', importRoutes);
    app.use(errorHandler);
  });

  const post = (body: any) => request(app).post('/api/import/playlist-name').send(body);

  it('returns the name from oEmbed', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: true,
      json: async () => ({ title: "Today's Top Hits" }),
    });

    const res = await post({ url: 'https://open.spotify.com/playlist/abc' }).expect(200);

    expect(res.body.name).toBe("Today's Top Hits");
  });

  it('answers from cache without asking Spotify at all', async () => {
    cachedName = 'Previously Imported';

    const res = await post({ url: 'https://open.spotify.com/playlist/abc' }).expect(200);

    expect(res.body.name).toBe('Previously Imported');
    expect(global.fetch).not.toHaveBeenCalled();
  });

  it('returns a blank name rather than an error when Spotify is unreachable', async () => {
    (global.fetch as jest.Mock).mockRejectedValue(new Error('network down'));

    const res = await post({ url: 'https://open.spotify.com/playlist/abc' }).expect(200);

    // Blank leaves the field editable and lets the import fill it in later;
    // an error here would block a modal that is otherwise perfectly usable.
    expect(res.body.name).toBe('');
  });

  it('returns a blank name when oEmbed rejects the URL', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({ ok: false, status: 404, json: async () => ({}) });

    const res = await post({ url: 'https://open.spotify.com/playlist/nope' }).expect(200);

    expect(res.body.name).toBe('');
  });

  it('rejects a request with no url', async () => {
    await post({}).expect(400);
  });
});
