/**
 * Unit Tests for Plex API Client
 * 
 * Tests all Plex client methods:
 * - searchTrack
 * - getLibraries
 * - getPlayHistory
 * - createPlaylist
 * - getPlaylistTracks
 * - addToPlaylist
 * - removeFromPlaylist
 * - deletePlaylist
 */

import axios from 'axios';
import { PlexClient, PlexAuthError, resolvePlexToken } from '../../src/services/plex';

// Mock axios
jest.mock('axios');
const mockedAxios = axios as jest.Mocked<typeof axios>;

/**
 * The fields that identify a track, as searchTrack() returns them.
 * PlexClient slims every track it returns (slimTrack in services/plex.ts):
 * artwork, summary and rating fields are blanked before anything is cached,
 * because an artist-catalog fetch can pull thousands of tracks and
 * matching.ts only ever scores against the titles. These tests are about
 * which tracks come back and in what order, so they compare on that rather
 * than on whole-object equality with the raw Plex fixture.
 */
function identifyingFields(track: any) {
  const picked: Record<string, unknown> = {};
  for (const key of ['ratingKey', 'title', 'originalTitle', 'grandparentTitle', 'parentTitle']) {
    if (track[key] !== undefined) picked[key] = track[key];
  }
  return picked;
}

describe('PlexClient', () => {
  let client: PlexClient;
  const serverUrl = 'http://localhost:32400';
  const token = 'test-token';
  const clientId = 'test-client';

  beforeEach(() => {
    // Reset mocks
    jest.clearAllMocks();

    // Create mock axios instance
    const mockAxiosInstance = {
      get: jest.fn(),
      post: jest.fn(),
      put: jest.fn(),
      delete: jest.fn(),
      interceptors: {
        request: { use: jest.fn() },
        response: { use: jest.fn() },
      },
    };

    mockedAxios.create.mockReturnValue(mockAxiosInstance as any);

    client = new PlexClient(serverUrl, token, clientId);
  });

  describe('searchTrack', () => {
    it('should search for tracks in a specific library via hub search, filtering by library', async () => {
      // When only a query + libraryId are supplied (no artist/title), searchTrack falls
      // back to the combined hub search and then filters the hub results by
      // librarySectionID for the requested library.
      const mockTracks = [
        {
          ratingKey: '123',
          title: 'Test Track',
          grandparentTitle: 'Test Artist',
          parentTitle: 'Test Album',
          type: 'track',
          librarySectionID: 1,
        },
      ];

      const mockResponse = {
        data: {
          MediaContainer: {
            size: 1,
            Hub: [
              {
                type: 'track',
                Metadata: mockTracks,
              },
            ],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.searchTrack('test query', '1');

      expect(mockInstance.get).toHaveBeenCalledWith('/hubs/search', {
        params: {
          query: 'test query',
          limit: 100,
        },
      });
      expect(result).toMatchObject(mockTracks.map(identifyingFields));
    });

    it('should search globally using hub search when no library specified', async () => {
      const mockTracks = [
        {
          ratingKey: '456',
          title: 'Another Track',
          grandparentTitle: 'Another Artist',
          parentTitle: 'Another Album',
          type: 'track',
        },
      ];

      const mockResponse = {
        data: {
          MediaContainer: {
            size: 1,
            Hub: [
              {
                type: 'track',
                Metadata: mockTracks,
              },
            ],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.searchTrack('test query');

      expect(mockInstance.get).toHaveBeenCalledWith('/hubs/search', {
        params: {
          query: 'test query',
          limit: 100,
        },
      });
      expect(result).toMatchObject(mockTracks.map(identifyingFields));
    });

    it('should return empty array when no tracks found', async () => {
      const mockResponse = {
        data: {
          MediaContainer: {
            size: 0,
            Metadata: [],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.searchTrack('nonexistent', '1');

      expect(result).toEqual([]);
    });

    it('should throw error when server is unreachable', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Network Error');
      error.code = 'ECONNREFUSED';
      error.isAxiosError = true;
      mockInstance.get.mockRejectedValue(error);

      await expect(client.searchTrack('test')).rejects.toThrow(
        'Plex server is unreachable'
      );
    });

    it('should throw error when token is invalid', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Unauthorized');
      error.isAxiosError = true;
      error.response = { status: 401 };
      mockInstance.get.mockRejectedValue(error);

      await expect(client.searchTrack('test')).rejects.toThrow('Invalid Plex token');
    });

    // When artist + title + libraryId are all supplied, searchTrack runs an
    // "artist-first" cascade: (1) look up a matching Artist entity, (2) direct
    // artist.title+track.title filter, (3) track.originalTitle (track-level
    // artist) filter, (4) title-only search filtered client-side by either
    // originalTitle or grandparentTitle. These cases pin down that a track
    // whose *track* artist (not album artist) matches the source artist is
    // still found - the exact data path behind soundtrack/compilation tracks
    // like "Life Is a Highway" by Rascal Flatts (album artist "Various Artists").
    describe('artist-first cascade (libraryId + artist + title)', () => {
      const empty = { data: { MediaContainer: { Metadata: [] } } };

      it('should fall through to the track.originalTitle filter when no Artist entity matches the source artist', async () => {
        const compilationTrack = {
          ratingKey: '999',
          title: 'Life Is a Highway',
          grandparentTitle: 'Various Artists',
          parentTitle: 'Cars (Original Motion Picture Soundtrack)',
          originalTitle: 'Rascal Flatts',
        };

        const mockInstance = mockedAxios.create() as any;
        mockInstance.get
          .mockResolvedValueOnce(empty) // step 1: no "Rascal Flatts" Artist entity
          .mockResolvedValueOnce(empty) // step 2: direct artist.title+track.title filter
          .mockResolvedValueOnce({
            data: { MediaContainer: { Metadata: [compilationTrack] } },
          }); // step 3: track.originalTitle filter

        const result = await client.searchTrack(
          '',
          '1',
          'Rascal Flatts',
          'Life Is a Highway'
        );

        expect(result).toMatchObject([identifyingFields(compilationTrack)]);
        expect(mockInstance.get).toHaveBeenNthCalledWith(
          3,
          '/library/sections/1/all',
          {
            params: {
              type: 10,
              'track.title': 'Life Is a Highway',
              'track.originalTitle': 'Rascal Flatts',
            },
          }
        );
      });

      it('should rank the artist-matching track first in step 4, but still include other title matches for matching.ts to consider', async () => {
        // Step 4 used to hard-filter out anything that didn't match the
        // search artist by originalTitle/grandparentTitle. That discarded
        // legitimately correct tracks before matching.ts's compilation-aware
        // scoring (which can accept an exact title match on a soundtrack
        // album even without an artist match) ever got to see them - e.g. a
        // soundtrack album artist tagged as the composer, with the real
        // per-track performer missing/mistagged. So step 4 now ranks instead
        // of filtering.
        const wantedTrack = {
          ratingKey: '999',
          title: 'Go the Distance',
          grandparentTitle: 'Various Artists',
          parentTitle: 'Hercules (Original Motion Picture Soundtrack)',
          originalTitle: 'Roger Bart',
        };
        const unrelatedTrack = {
          ratingKey: '111',
          title: 'Go the Distance',
          grandparentTitle: 'Michael Bolton',
          parentTitle: 'All That Matters',
        };
        const unsupportedFilterError: any = new Error('Bad Request');
        unsupportedFilterError.isAxiosError = true;
        unsupportedFilterError.response = { status: 400 };

        const mockInstance = mockedAxios.create() as any;
        mockInstance.get
          .mockResolvedValueOnce(empty) // step 1: no matching Artist entity
          .mockResolvedValueOnce(empty) // step 2: direct filter
          .mockRejectedValueOnce(unsupportedFilterError) // step 3: unsupported filter param
          .mockResolvedValueOnce({
            data: { MediaContainer: { Metadata: [unrelatedTrack, wantedTrack] } },
          }); // step 4: title-only search, ranked client-side

        const result = await client.searchTrack(
          '',
          '1',
          'Roger Bart',
          'Go the Distance'
        );

        expect(result).toMatchObject([identifyingFields(wantedTrack), identifyingFields(unrelatedTrack)]);
      });

      it('should prefer the direct artist.title match when the source artist is genuinely the album artist', async () => {
        const track = {
          ratingKey: '42',
          title: 'Yesterday',
          grandparentTitle: 'The Beatles',
          parentTitle: 'Help!',
        };
        const artistEntity = { ratingKey: '7', title: 'The Beatles' };

        const mockInstance = mockedAxios.create() as any;
        mockInstance.get
          .mockResolvedValueOnce({ data: { MediaContainer: { Metadata: [artistEntity] } } }) // step 1: artist found
          .mockResolvedValueOnce({ data: { MediaContainer: { Metadata: [track] } } }); // step 1b: allLeaves for that artist

        const result = await client.searchTrack('', '1', 'The Beatles', 'Yesterday');

        expect(result).toMatchObject([identifyingFields(track)]);
        expect(mockInstance.get).toHaveBeenCalledTimes(2);
      });
    });
  });

  describe('getLibraries', () => {
    it('should return all libraries with id, name, and type', async () => {
      const mockLibraries = [
        {
          key: '1',
          title: 'Music',
          type: 'artist',
          agent: 'com.plexapp.agents.lastfm',
          scanner: 'Plex Music Scanner',
          language: 'en',
          uuid: 'uuid-1',
          updatedAt: 1234567890,
          createdAt: 1234567890,
          scannedAt: 1234567890,
          content: true,
          directory: true,
          contentChangedAt: 1234567890,
          hidden: 0,
        },
        {
          key: '2',
          title: 'Movies',
          type: 'movie',
          agent: 'com.plexapp.agents.imdb',
          scanner: 'Plex Movie Scanner',
          language: 'en',
          uuid: 'uuid-2',
          updatedAt: 1234567890,
          createdAt: 1234567890,
          scannedAt: 1234567890,
          content: true,
          directory: true,
          contentChangedAt: 1234567890,
          hidden: 0,
        },
      ];

      const mockResponse = {
        data: {
          MediaContainer: {
            size: 2,
            Directory: mockLibraries,
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.getLibraries();

      expect(mockInstance.get).toHaveBeenCalledWith('/library/sections');
      // getLibraries() returns all sections unfiltered; callers (e.g. the
      // /api/servers/libraries route and the web client) filter by type.
      expect(result).toHaveLength(2);
      expect(result[0].type).toBe('artist');
      expect(result[0].name).toBe('Music');
      expect(result[1].type).toBe('movie');
      expect(result[1].name).toBe('Movies');
    });

    it('should return empty array when no music libraries exist', async () => {
      const mockResponse = {
        data: {
          MediaContainer: {
            size: 0,
            Directory: [],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.getLibraries();

      expect(result).toEqual([]);
    });

    it('should throw error when server is unreachable', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Network Error');
      error.code = 'ETIMEDOUT';
      error.isAxiosError = true;
      mockInstance.get.mockRejectedValue(error);

      await expect(client.getLibraries()).rejects.toThrow(
        'Plex server is unreachable'
      );
    });
  });

  describe('getPlayHistory', () => {
    it('should return play history for a library', async () => {
      const mockHistory = [
        {
          historyKey: '1',
          key: '/library/metadata/123',
          ratingKey: '123',
          title: 'Test Track',
          type: 'track',
          thumb: '/library/metadata/123/thumb',
          parentThumb: '/library/metadata/122/thumb',
          grandparentThumb: '/library/metadata/121/thumb',
          grandparentTitle: 'Test Artist',
          parentTitle: 'Test Album',
          index: 1,
          parentIndex: 1,
          viewedAt: 1234567890,
          accountID: 1,
          deviceID: 1,
        },
      ];

      const mockResponse = {
        data: {
          MediaContainer: {
            size: 1,
            Metadata: mockHistory,
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.getPlayHistory('1', 50);

      expect(mockInstance.get).toHaveBeenCalledWith(
        '/status/sessions/history/all',
        {
          params: {
            librarySectionID: '1',
            'X-Plex-Container-Size': 50,
          },
        }
      );
      expect(result).toEqual(mockHistory);
    });

    it('should use default limit of 100 when not specified', async () => {
      const mockResponse = {
        data: {
          MediaContainer: {
            size: 0,
            Metadata: [],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      await client.getPlayHistory('1');

      expect(mockInstance.get).toHaveBeenCalledWith(
        '/status/sessions/history/all',
        {
          params: {
            librarySectionID: '1',
            'X-Plex-Container-Size': 100,
          },
        }
      );
    });

    it('should return empty array when no history exists', async () => {
      const mockResponse = {
        data: {
          MediaContainer: {
            size: 0,
            Metadata: [],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.getPlayHistory('1');

      expect(result).toEqual([]);
    });
  });

  describe('createPlaylist', () => {
    it('should create a playlist with tracks', async () => {
      const mockPlaylist = {
        ratingKey: '999',
        key: '/playlists/999',
        guid: 'com.plexapp.agents.none://playlist-999',
        type: 'playlist',
        title: 'Test Playlist',
        summary: '',
        smart: false,
        playlistType: 'audio',
        composite: '/playlists/999/composite',
        duration: 180000,
        leafCount: 2,
        addedAt: 1234567890,
        updatedAt: 1234567890,
      };

      const mockCreateResponse = {
        data: {
          MediaContainer: {
            size: 1,
            Metadata: [mockPlaylist],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.post.mockResolvedValue(mockCreateResponse);
      mockInstance.put.mockResolvedValue({ data: {} });

      const trackUris = [
        'server://localhost:32400/com.plexapp.plugins.library/library/metadata/123',
        'server://localhost:32400/com.plexapp.plugins.library/library/metadata/124',
      ];

      const result = await client.createPlaylist(
        'Test Playlist',
        'server://localhost:32400/com.plexapp.plugins.library/library/sections/1/all?type=10',
        trackUris
      );

      // createPlaylist builds the URL manually (matching python-plexapi's
      // joinArgs behavior) rather than passing an axios `params` object.
      const libraryUri = 'server://localhost:32400/com.plexapp.plugins.library/library/sections/1/all?type=10';
      const expectedCreateUrl = `/playlists?type=audio&title=${encodeURIComponent('Test Playlist')}&smart=0&uri=${encodeURIComponent(libraryUri)}`;
      expect(mockInstance.post).toHaveBeenCalledWith(expectedCreateUrl, null);

      // addToPlaylist batches ratingKeys into a single comma-separated URI
      const batchUri = 'server://localhost:32400/com.plexapp.plugins.library/library/metadata/123,124';
      expect(mockInstance.put).toHaveBeenCalledWith(
        `/playlists/999/items?uri=${encodeURIComponent(batchUri)}`,
        null
      );

      expect(result).toEqual(mockPlaylist);
    });

    it('should create empty playlist when no tracks provided', async () => {
      const mockPlaylist = {
        ratingKey: '999',
        title: 'Empty Playlist',
        leafCount: 0,
      };

      const mockCreateResponse = {
        data: {
          MediaContainer: {
            size: 1,
            Metadata: [mockPlaylist],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.post.mockResolvedValue(mockCreateResponse);

      const result = await client.createPlaylist(
        'Empty Playlist',
        'server://localhost:32400/com.plexapp.plugins.library/library/sections/1/all?type=10',
        []
      );

      expect(mockInstance.post).toHaveBeenCalled();
      expect(mockInstance.put).not.toHaveBeenCalled();
      expect(result).toEqual(mockPlaylist);
    });

    it('should throw error when playlist creation fails', async () => {
      const mockCreateResponse = {
        data: {
          MediaContainer: {
            size: 0,
            Metadata: [],
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.post.mockResolvedValue(mockCreateResponse);

      await expect(
        client.createPlaylist('Test', 'uri', [])
      ).rejects.toThrow('Failed to create playlist - no playlist returned');
    });
  });

  describe('getPlaylistTracks', () => {
    it('should return tracks from a playlist', async () => {
      const mockTracks = [
        {
          ratingKey: '123',
          title: 'Track 1',
          grandparentTitle: 'Artist 1',
          parentTitle: 'Album 1',
        },
        {
          ratingKey: '124',
          title: 'Track 2',
          grandparentTitle: 'Artist 2',
          parentTitle: 'Album 2',
        },
      ];

      const mockResponse = {
        data: {
          MediaContainer: {
            size: 2,
            Metadata: mockTracks,
          },
        },
      };

      const mockInstance = mockedAxios.create() as any;
      mockInstance.get.mockResolvedValue(mockResponse);

      const result = await client.getPlaylistTracks('999');

      expect(mockInstance.get).toHaveBeenCalledWith('/playlists/999/items');
      expect(result).toEqual(mockTracks);
    });

    it('should throw error when playlist not found', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Not Found');
      error.isAxiosError = true;
      error.response = { status: 404 };
      mockInstance.get.mockRejectedValue(error);

      await expect(client.getPlaylistTracks('999')).rejects.toThrow(
        'Playlist not found'
      );
    });
  });

  describe('addToPlaylist', () => {
    it('should add tracks to a playlist', async () => {
      const mockInstance = mockedAxios.create() as any;
      mockInstance.put.mockResolvedValue({ data: {} });

      const trackUris = [
        'server://localhost:32400/com.plexapp.plugins.library/library/metadata/123',
        'server://localhost:32400/com.plexapp.plugins.library/library/metadata/124',
      ];

      await client.addToPlaylist('999', trackUris);

      // addToPlaylist batches ratingKeys into a single comma-separated URI
      // appended to the query string (matching python-plexapi behavior).
      const batchUri = 'server://localhost:32400/com.plexapp.plugins.library/library/metadata/123,124';
      expect(mockInstance.put).toHaveBeenCalledWith(
        `/playlists/999/items?uri=${encodeURIComponent(batchUri)}`,
        null
      );
    });

    it('should throw error when playlist not found', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Not Found');
      error.isAxiosError = true;
      error.response = { status: 404 };
      mockInstance.put.mockRejectedValue(error);

      await expect(client.addToPlaylist('999', ['uri'])).rejects.toThrow(
        'Playlist not found'
      );
    });
  });

  describe('removeFromPlaylist', () => {
    it('should remove a track from a playlist', async () => {
      const mockInstance = mockedAxios.create() as any;
      mockInstance.delete.mockResolvedValue({ data: {} });

      await client.removeFromPlaylist('999', '123');

      expect(mockInstance.delete).toHaveBeenCalledWith(
        '/playlists/999/items/123'
      );
    });

    it('should throw error when playlist or item not found', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Not Found');
      error.isAxiosError = true;
      error.response = { status: 404 };
      mockInstance.delete.mockRejectedValue(error);

      await expect(client.removeFromPlaylist('999', '123')).rejects.toThrow(
        'Playlist or item not found'
      );
    });
  });

  describe('deletePlaylist', () => {
    it('should delete a playlist', async () => {
      const mockInstance = mockedAxios.create() as any;
      mockInstance.delete.mockResolvedValue({ data: {} });

      await client.deletePlaylist('999');

      expect(mockInstance.delete).toHaveBeenCalledWith('/playlists/999');
    });

    it('should throw error when playlist not found', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Not Found');
      error.isAxiosError = true;
      error.response = { status: 404 };
      mockInstance.delete.mockRejectedValue(error);

      await expect(client.deletePlaylist('999')).rejects.toThrow(
        'Playlist not found'
      );
    });

    it('should throw error when server is unreachable', async () => {
      const mockInstance = mockedAxios.create() as any;
      const error: any = new Error('Network Error');
      error.code = 'ECONNREFUSED';
      error.isAxiosError = true;
      mockInstance.delete.mockRejectedValue(error);

      await expect(client.deletePlaylist('999')).rejects.toThrow(
        'Plex server is unreachable'
      );
    });
  });

  describe('URI builders', () => {
    it('should build track URI correctly', () => {
      // Without an explicit machineIdentifier, buildTrackUri falls back to
      // the client's clientId (server:// URIs require a machine identifier,
      // not the server's URL).
      const uri = client.buildTrackUri('123');
      expect(uri).toBe(
        'server://test-client/com.plexapp.plugins.library/library/metadata/123'
      );
    });

    it('should build library URI correctly', () => {
      const uri = client.buildLibraryUri('1');
      expect(uri).toBe(
        'server://test-client/com.plexapp.plugins.library/library/sections/1'
      );
    });
  });

  describe('401 handling', () => {
    it('turns any 401 response into a PlexAuthError carrying a 401 status', async () => {
      // The conversion lives in the client's response interceptor so it
      // covers every method, including the ones with no 401 branch of their
      // own. Pull the registered rejection handler back out and drive it.
      const use = (mockedAxios.create.mock.results[0].value as any).interceptors.response.use;
      const onRejected = use.mock.calls[0][1];

      await expect(onRejected({ response: { status: 401 } })).rejects.toBeInstanceOf(PlexAuthError);
      await expect(onRejected({ response: { status: 401 } })).rejects.toMatchObject({
        statusCode: 401,
        code: 'PLEX_AUTH_INVALID',
      });
    });

    it('leaves non-401 failures alone', async () => {
      const use = (mockedAxios.create.mock.results[0].value as any).interceptors.response.use;
      const onRejected = use.mock.calls[0][1];
      const original = { response: { status: 500 }, message: 'boom' };

      await expect(onRejected(original)).rejects.toBe(original);
    });
  });

  describe('resolvePlexToken', () => {
    // Regression test for a bug where every direct-to-PMS call used the
    // plex.tv account token even for servers merely shared with (not owned
    // by) the user. Plex rejects the account token there with a 401, which
    // the app read as "the session expired" and bounced the user back to
    // /login - a loop re-authenticating with Plex could never break, since
    // that only ever refreshes the account token, not the server-specific one.
    it('prefers the server-specific access token when one is saved', () => {
      const user = { plex_token: 'account-token' };
      const userServer = { access_token: 'server-specific-token' };
      expect(resolvePlexToken(user, userServer)).toBe('server-specific-token');
    });

    it('falls back to the account token for owned servers (no access_token saved)', () => {
      const user = { plex_token: 'account-token' };
      const userServer = { access_token: null };
      expect(resolvePlexToken(user, userServer)).toBe('account-token');
    });

    it('falls back to the account token when there is no server row at all', () => {
      const user = { plex_token: 'account-token' };
      expect(resolvePlexToken(user, null)).toBe('account-token');
      expect(resolvePlexToken(user, undefined)).toBe('account-token');
    });
  });
});
