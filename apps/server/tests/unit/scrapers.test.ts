/**
 * Unit tests for external service scrapers
 */

import axios from 'axios';
import {
  scrapeDeezerPlaylist,
  getDeezerCharts,
  scrapeSpotifyPlaylist,
  scrapeAppleMusicPlaylist,
  scrapeTidalPlaylist,
  scrapeYouTubeMusicPlaylist,
  scrapeAmazonMusicPlaylist,
  scrapeQobuzPlaylist,
  getListenBrainzPlaylists,
  scrapeAriaCharts,
  parseM3UFile,
  parseCSVFile,
  parsePLSFile,
  parseXSPFFile,
} from '../../src/services/scrapers';
import * as browserScrapers from '../../src/services/browser-scrapers';

// Mock axios
jest.mock('axios');
const mockedAxios = axios as jest.Mocked<typeof axios>;

// The Apple/Tidal/YouTube/Amazon/Qobuz scrapers all try Puppeteer-based
// browser scraping first (`services/browser-scrapers`), which launches a real
// headless browser against the real external site. Mock that module out so
// these unit tests stay fast, deterministic, and offline — they exercise the
// (also real, and now the primary) axios/regex-based fallback logic that
// runs once browser scraping is unavailable/fails, which is exactly what
// happens today in a sandbox with no Chromium available.
jest.mock('../../src/services/browser-scrapers');
const mockedBrowserScrapers = browserScrapers as jest.Mocked<typeof browserScrapers>;

// scrapeSpotifyPlaylist's primary method shells out to `curl` via
// child_process to scrape Spotify's embed page. Mock that out too so the
// test doesn't depend on real network access or a `curl` binary.
jest.mock('child_process', () => ({
  execFile: jest.fn((_cmd: string, _args: string[], _opts: any, callback: any) => {
    callback(new Error('curl not available in test environment'));
  }),
}));

// scrapeYouTubeMusicPlaylist's primary method uses the real `ytmusic-api`
// package, which itself makes real network calls to YouTube Music. Mock it
// so the API-based path fails deterministically and falls through to the
// (mocked) browser-scraping fallback.
jest.mock('ytmusic-api', () => ({
  default: jest.fn().mockImplementation(() => ({
    initialize: jest.fn().mockResolvedValue(undefined),
    getPlaylist: jest.fn().mockResolvedValue(null),
  })),
}));

describe('Scrapers Service', () => {
  beforeEach(() => {
    jest.clearAllMocks();

    // Default all browser-scraper entry points to reject, simulating an
    // environment with no headless browser available (as in this sandbox).
    // Individual tests can override with mockResolvedValueOnce if needed.
    const browserUnavailable = new Error('Browser scraping unavailable in test environment');
    mockedBrowserScrapers.scrapeAppleMusicWithBrowser.mockRejectedValue(browserUnavailable);
    mockedBrowserScrapers.scrapeTidalWithBrowser.mockRejectedValue(browserUnavailable);
    mockedBrowserScrapers.scrapeYouTubeMusicWithBrowser.mockRejectedValue(browserUnavailable);
    mockedBrowserScrapers.scrapeAmazonMusicWithBrowser.mockRejectedValue(browserUnavailable);
    mockedBrowserScrapers.scrapeQobuzWithBrowser.mockRejectedValue(browserUnavailable);
  });

  describe('scrapeDeezerPlaylist', () => {
    it('should scrape a Deezer playlist successfully', async () => {
      const mockResponse = {
        data: {
          id: '123',
          title: 'Test Playlist',
          description: 'Test Description',
          tracks: {
            data: [
              {
                title: 'Track 1',
                artist: { name: 'Artist 1' },
                album: { title: 'Album 1' },
              },
              {
                title: 'Track 2',
                artist: { name: 'Artist 2' },
                album: { title: 'Album 2' },
              },
            ],
          },
        },
      };

      mockedAxios.get.mockResolvedValueOnce(mockResponse);

      const result = await scrapeDeezerPlaylist('123');

      expect(result).toEqual({
        id: 'deezer-123',
        name: 'Test Playlist',
        description: 'Test Description',
        source: 'deezer',
        tracks: [
          { title: 'Track 1', artist: 'Artist 1', album: 'Album 1' },
          { title: 'Track 2', artist: 'Artist 2', album: 'Album 2' },
        ],
      });

      expect(mockedAxios.get).toHaveBeenCalledWith('https://api.deezer.com/playlist/123');
    });

    it('should handle missing artist names', async () => {
      const mockResponse = {
        data: {
          id: '123',
          title: 'Test Playlist',
          description: '',
          tracks: {
            data: [
              {
                title: 'Track 1',
                artist: {},
                album: { title: 'Album 1' },
              },
            ],
          },
        },
      };

      mockedAxios.get.mockResolvedValueOnce(mockResponse);

      const result = await scrapeDeezerPlaylist('123');

      expect(result.tracks[0].artist).toBe('Unknown');
    });

    it('should throw error on invalid response', async () => {
      mockedAxios.get.mockResolvedValueOnce({ data: {} });

      await expect(scrapeDeezerPlaylist('123')).rejects.toThrow('Invalid Deezer playlist response');
    });

    it('should throw error on network failure', async () => {
      mockedAxios.get.mockRejectedValueOnce(new Error('Network error'));

      await expect(scrapeDeezerPlaylist('123')).rejects.toThrow('Failed to scrape Deezer playlist');
    });
  });

  describe('getDeezerCharts', () => {
    it('should fetch global charts', async () => {
      const mockTopResponse = {
        data: {
          data: [
            {
              title: 'Top Track 1',
              artist: { name: 'Artist 1' },
              album: { title: 'Album 1' },
            },
          ],
        },
      };

      mockedAxios.get.mockResolvedValueOnce(mockTopResponse);

      const result = await getDeezerCharts('global');

      expect(result).toHaveLength(1);
      expect(result[0].id).toBe('deezer-top-global');
      expect(result[0].name).toBe('Top 50 Global');
      expect(result[0].tracks).toHaveLength(1);
    });

    it('should fetch country-specific charts', async () => {
      const mockTopResponse = {
        data: {
          data: [
            {
              title: 'Top Track 1',
              artist: { name: 'Artist 1' },
              album: { title: 'Album 1' },
            },
          ],
        },
      };

      const mockSearchResponse = {
        data: {
          data: [
            {
              id: '456',
              title: 'Top 50 United States',
            },
          ],
        },
      };

      const mockPlaylistResponse = {
        data: {
          data: [
            {
              title: 'US Track 1',
              artist: { name: 'US Artist 1' },
              album: { title: 'US Album 1' },
            },
          ],
        },
      };

      mockedAxios.get
        .mockResolvedValueOnce(mockTopResponse)
        .mockResolvedValueOnce(mockSearchResponse)
        .mockResolvedValueOnce(mockPlaylistResponse);

      const result = await getDeezerCharts('us');

      expect(result).toHaveLength(2);
      expect(result[1].id).toBe('deezer-top-us');
      expect(result[1].name).toBe('Top 50 United States');
    });

    it('should handle errors gracefully', async () => {
      mockedAxios.get.mockRejectedValue(new Error('Network error'));

      const result = await getDeezerCharts('global');

      expect(result).toEqual([]);
    });
  });

  describe('scrapeSpotifyPlaylist', () => {
    it('should throw a descriptive error when no scraping method succeeds', async () => {
      // With curl (embed scraping) mocked to fail, and no userId/db passed
      // (so the authenticated/Client-Credentials API methods are skipped
      // entirely), every method is exhausted and the final descriptive
      // error is thrown.
      await expect(scrapeSpotifyPlaylist('https://open.spotify.com/playlist/abc123')).rejects.toThrow(
        'Unable to fetch Spotify playlist data'
      );
    });

    it('should throw error on invalid URL', async () => {
      await expect(scrapeSpotifyPlaylist('https://invalid-url.com')).rejects.toThrow(
        'Invalid Spotify playlist URL'
      );
    });
  });

  describe('scrapeAppleMusicPlaylist', () => {
    it('should surface the browser-scraping failure', async () => {
      // scrapeAppleMusicPlaylist has no non-browser fallback: it wraps
      // whatever `scrapeAppleMusicWithBrowser` throws.
      await expect(scrapeAppleMusicPlaylist('https://music.apple.com/playlist/abc')).rejects.toThrow(
        'Failed to scrape Apple Music playlist'
      );
    });
  });

  describe('scrapeTidalPlaylist', () => {
    it('should fall through to the API/embed fallbacks and report failure', async () => {
      // Browser scraping is mocked to fail; the API and embed-page fallbacks
      // then run against the (unconfigured) mocked axios client, which also
      // fail, producing the final descriptive error.
      await expect(scrapeTidalPlaylist('https://tidal.com/playlist/abc-123')).rejects.toThrow(
        'Unable to fetch Tidal playlist'
      );
    });

    it('should throw error on invalid URL', async () => {
      await expect(scrapeTidalPlaylist('https://invalid-url.com')).rejects.toThrow(
        'Invalid Tidal playlist URL'
      );
    });
  });

  describe('scrapeYouTubeMusicPlaylist', () => {
    it('should fall through to the browser-scraping fallback and report failure', async () => {
      // ytmusic-api is mocked to return no playlist, and the browser-scraping
      // fallback is mocked to reject, so the wrapped final error surfaces.
      await expect(scrapeYouTubeMusicPlaylist('https://music.youtube.com/playlist?list=abc')).rejects.toThrow(
        'Failed to scrape YouTube Music playlist'
      );
    });
  });

  describe('scrapeAmazonMusicPlaylist', () => {
    it('should fall through to page scraping and report failure', async () => {
      // Real Amazon Music playlist URLs use the plural "playlists/" segment.
      await expect(scrapeAmazonMusicPlaylist('https://music.amazon.com/playlists/abc')).rejects.toThrow(
        'Unable to fetch Amazon Music playlist'
      );
    });
  });

  describe('scrapeQobuzPlaylist', () => {
    it('should fall through to the API/page fallbacks and report failure', async () => {
      // Real Qobuz playlist URLs are shaped like playlist/{name}/{numeric-id}.
      await expect(scrapeQobuzPlaylist('https://www.qobuz.com/playlist/my-playlist/123456')).rejects.toThrow(
        'Unable to fetch Qobuz playlist'
      );
    });
  });

  describe('getListenBrainzPlaylists', () => {
    it('should fetch ListenBrainz playlists successfully', async () => {
      const mockPlaylistsResponse = {
        data: {
          playlists: [
            {
              identifier: 'playlist-1',
            },
          ],
        },
      };

      const mockPlaylistDetailsResponse = {
        data: {
          playlist: {
            title: 'My Playlist',
            annotation: 'Test playlist',
            track: [
              {
                title: 'Track 1',
                creator: 'Artist 1',
              },
              {
                title: 'Track 2',
                creator: 'Artist 2',
              },
            ],
          },
        },
      };

      mockedAxios.get
        .mockResolvedValueOnce(mockPlaylistsResponse)
        .mockResolvedValueOnce(mockPlaylistDetailsResponse);

      const result = await getListenBrainzPlaylists('testuser');

      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        id: 'listenbrainz-playlist-1',
        name: 'My Playlist',
        description: 'Test playlist',
        source: 'listenbrainz',
        tracks: [
          { title: 'Track 1', artist: 'Artist 1' },
          { title: 'Track 2', artist: 'Artist 2' },
        ],
      });
    });

    it('should handle missing track data', async () => {
      const mockPlaylistsResponse = {
        data: {
          playlists: [
            {
              identifier: 'playlist-1',
            },
          ],
        },
      };

      const mockPlaylistDetailsResponse = {
        data: {
          playlist: {
            title: 'My Playlist',
            annotation: '',
            track: [
              {
                title: null,
                creator: null,
              },
            ],
          },
        },
      };

      mockedAxios.get
        .mockResolvedValueOnce(mockPlaylistsResponse)
        .mockResolvedValueOnce(mockPlaylistDetailsResponse);

      const result = await getListenBrainzPlaylists('testuser');

      expect(result[0].tracks[0]).toEqual({
        title: 'Unknown',
        artist: 'Unknown',
      });
    });

    it('should handle errors gracefully', async () => {
      mockedAxios.get.mockRejectedValueOnce(new Error('Network error'));

      await expect(getListenBrainzPlaylists('testuser')).rejects.toThrow(
        'Failed to fetch ListenBrainz playlists'
      );
    });

    it('should skip playlists that fail to fetch', async () => {
      const mockPlaylistsResponse = {
        data: {
          playlists: [
            { identifier: 'playlist-1' },
            { identifier: 'playlist-2' },
          ],
        },
      };

      const mockPlaylistDetailsResponse = {
        data: {
          playlist: {
            title: 'My Playlist',
            annotation: '',
            track: [{ title: 'Track 1', creator: 'Artist 1' }],
          },
        },
      };

      mockedAxios.get
        .mockResolvedValueOnce(mockPlaylistsResponse)
        .mockRejectedValueOnce(new Error('Playlist not found'))
        .mockResolvedValueOnce(mockPlaylistDetailsResponse);

      const result = await getListenBrainzPlaylists('testuser');

      expect(result).toHaveLength(1);
      expect(result[0].id).toBe('listenbrainz-playlist-2');
    });
  });

  describe('scrapeAriaCharts', () => {
    it('should resolve to an empty array (charts are scraped individually via scrapeAriaPlaylist)', async () => {
      // scrapeAriaCharts is intentionally a no-op now: individual ARIA chart
      // pages are fetched via scrapeAriaPlaylist (browser-based scraping)
      // instead, per the comment on the production function.
      await expect(scrapeAriaCharts()).resolves.toEqual([]);
    });
  });

  describe('parseM3UFile', () => {
    it('should parse M3U file with EXTINF tags (standard format)', () => {
      const content = `#EXTM3U
#EXTINF:180,Artist 1 - Track 1
/path/to/track1.mp3
#EXTINF:200,Artist 2 - Track 2
/path/to/track2.mp3`;

      const result = parseM3UFile(content, 'test.m3u');

      // The file extension is intentionally stripped from the display name
      // (see the "Remove extension from name" comment in parseM3UFile).
      expect(result.name).toBe('test');
      expect(result.source).toBe('file');
      expect(result.tracks).toHaveLength(2);
      expect(result.tracks[0]).toEqual({
        title: 'Track 1',
        artist: 'Artist 1',
      });
      expect(result.tracks[1]).toEqual({
        title: 'Track 2',
        artist: 'Artist 2',
      });
    });

    it('should parse M3U file with Apple Music format (Title - Artist)', () => {
      const content = `#EXTM3U
#EXTINF:232,Happy (From "Despicable Me 2") - Pharrell Williams
/path/to/track1.m4a
#EXTINF:215,Cool Kids (Radio Edit) - Echosmith
/path/to/track2.m4a
#EXTINF:180,Shake It Off - Taylor Swift
/path/to/track3.m4a`;

      const result = parseM3UFile(content, 'apple-playlist.m3u');

      expect(result.tracks).toHaveLength(3);
      expect(result.tracks[0]).toEqual({
        title: 'Happy (From "Despicable Me 2")',
        artist: 'Pharrell Williams',
      });
      expect(result.tracks[1]).toEqual({
        title: 'Cool Kids (Radio Edit)',
        artist: 'Echosmith',
      });
      expect(result.tracks[2]).toEqual({
        title: 'Shake It Off',
        artist: 'Taylor Swift',
      });
    });

    it('should parse M3U file without EXTINF tags', () => {
      const content = `#EXTM3U
Artist 1 - Track 1.mp3
Artist 2 - Track 2.mp3`;

      const result = parseM3UFile(content, 'test.m3u');

      expect(result.tracks).toHaveLength(2);
      expect(result.tracks[0]).toEqual({
        title: 'Track 1',
        artist: 'Artist 1',
      });
    });

    it('should handle tracks without artist separator', () => {
      const content = `#EXTM3U
#EXTINF:180,Track Without Artist
/path/to/track.mp3`;

      const result = parseM3UFile(content, 'test.m3u');

      expect(result.tracks[0]).toEqual({
        title: 'Track Without Artist',
        artist: 'Unknown',
      });
    });

    it('should handle empty lines and comments', () => {
      const content = `#EXTM3U
# This is a comment

#EXTINF:180,Artist 1 - Track 1
/path/to/track1.mp3

# Another comment
#EXTINF:200,Artist 2 - Track 2
/path/to/track2.mp3`;

      const result = parseM3UFile(content, 'test.m3u');

      expect(result.tracks).toHaveLength(2);
    });

    it('should handle Windows-style line endings', () => {
      const content = '#EXTM3U\r\n#EXTINF:180,Artist 1 - Track 1\r\n/path/to/track1.mp3';

      const result = parseM3UFile(content, 'test.m3u');

      expect(result.tracks).toHaveLength(1);
      expect(result.tracks[0].title).toBe('Track 1');
    });

    it('should split on the LAST " - " separator when a title has multiple dashes', () => {
      const content = `#EXTM3U
#EXTINF:180,Artist - With - Dashes - Track - Title - With - More
/path/to/track.mp3`;

      const result = parseM3UFile(content, 'test.m3u');

      // Only one EXTINF sample line is present, so Apple-format detection
      // doesn't kick in (it requires >= 2 samples) and the standard
      // "Artist - Title" parsing is used. The artist/title split happens at
      // the LAST " - " in the string (per `info.lastIndexOf(' - ')`), so
      // everything before it is treated as the artist and everything after
      // as the title.
      expect(result.tracks[0]).toEqual({
        title: 'More',
        artist: 'Artist - With - Dashes - Track - Title - With',
      });
    });

    it('should detect mixed format and default to standard when ambiguous', () => {
      const content = `#EXTM3U
#EXTINF:180,Some Artist - Some Track
/path/to/track1.mp3
#EXTINF:200,Another Artist - Another Track
/path/to/track2.mp3`;

      const result = parseM3UFile(content, 'test.m3u');

      // Should use standard format (Artist - Title) when detection is ambiguous
      expect(result.tracks[0]).toEqual({
        title: 'Some Track',
        artist: 'Some Artist',
      });
    });
  });

  // These three parsers exist specifically so that a playlist exported by
  // this app (routes/export.ts's generateCSV/generatePLS/generateXSPF) can be
  // re-imported unchanged - so each roundtrips against that exact format.
  describe('parseCSVFile', () => {
    it('should parse this app\'s own CSV export format (Track,Artist,Album,Duration,File Path)', () => {
      const content = 'Track,Artist,Album,Duration,File Path\n' +
        'Track 1,Artist 1,Album 1,3:00,/path/to/track1.mp3\n' +
        'Track 2,Artist 2,Album 2,3:20,/path/to/track2.mp3\n';

      const result = parseCSVFile(content, 'playlist.csv');

      expect(result.source).toBe('file');
      expect(result.tracks).toEqual([
        { title: 'Track 1', artist: 'Artist 1', album: 'Album 1' },
        { title: 'Track 2', artist: 'Artist 2', album: 'Album 2' },
      ]);
    });

    it('should handle quoted fields containing commas', () => {
      const content = 'Track,Artist,Album,Duration,File Path\n' +
        '"Track, With Comma",Artist,"Album, One",3:00,/path.mp3\n';

      const result = parseCSVFile(content, 'playlist.csv');

      expect(result.tracks[0]).toEqual({
        title: 'Track, With Comma',
        artist: 'Artist',
        album: 'Album, One',
      });
    });

    it('should fall back to the first two columns as title/artist when no recognized header is present', () => {
      const content = 'Foo,Bar\n1,2\n';

      const result = parseCSVFile(content, 'playlist.csv');

      expect(result.tracks).toEqual([{ title: 'Foo', artist: 'Bar', album: undefined }, { title: '1', artist: '2', album: undefined }]);
    });

    it('should throw when the file has only a header row and no data', () => {
      const content = 'Track,Artist,Album,Duration,File Path\n';

      expect(() => parseCSVFile(content, 'playlist.csv')).toThrow('No tracks found');
    });
  });

  describe('parsePLSFile', () => {
    it('should parse this app\'s own PLS export format (TitleN=Artist - Title)', () => {
      const content = '[playlist]\n' +
        'PlaylistName=My Playlist\n' +
        'NumberOfEntries=2\n\n' +
        'File1=/path/to/track1.mp3\n' +
        'Title1=Artist 1 - Track 1\n' +
        'Length1=180\n\n' +
        'File2=/path/to/track2.mp3\n' +
        'Title2=Artist 2 - Track 2\n' +
        'Length2=200\n\n' +
        'Version=2\n';

      const result = parsePLSFile(content, 'playlist.pls');

      expect(result.tracks).toEqual([
        { title: 'Track 1', artist: 'Artist 1' },
        { title: 'Track 2', artist: 'Artist 2' },
      ]);
    });

    it('should order tracks by entry number regardless of file order', () => {
      const content = 'Title2=Artist B - Track B\nTitle1=Artist A - Track A\n';

      const result = parsePLSFile(content, 'playlist.pls');

      expect(result.tracks.map(t => t.title)).toEqual(['Track A', 'Track B']);
    });

    it('should throw when no Title entries are present', () => {
      expect(() => parsePLSFile('[playlist]\nVersion=2\n', 'playlist.pls')).toThrow('No tracks found');
    });
  });

  describe('parseXSPFFile', () => {
    it('should parse this app\'s own XSPF export format', () => {
      const content = '<?xml version="1.0" encoding="UTF-8"?>\n' +
        '<playlist version="1" xmlns="http://xspf.org/ns/0/">\n' +
        '  <title>My Playlist</title>\n' +
        '  <trackList>\n' +
        '    <track>\n' +
        '      <location>file:///path/to/track1.mp3</location>\n' +
        '      <title>Track 1</title>\n' +
        '      <creator>Artist 1</creator>\n' +
        '      <album>Album 1</album>\n' +
        '      <duration>180000</duration>\n' +
        '    </track>\n' +
        '    <track>\n' +
        '      <location>file:///path/to/track2.mp3</location>\n' +
        '      <title>Track 2</title>\n' +
        '      <creator>Artist 2</creator>\n' +
        '      <album>Album 2</album>\n' +
        '      <duration>200000</duration>\n' +
        '    </track>\n' +
        '  </trackList>\n' +
        '</playlist>\n';

      const result = parseXSPFFile(content, 'playlist.xspf');

      expect(result.name).toBe('My Playlist');
      expect(result.tracks).toEqual([
        { title: 'Track 1', artist: 'Artist 1', album: 'Album 1' },
        { title: 'Track 2', artist: 'Artist 2', album: 'Album 2' },
      ]);
    });

    it('should unescape XML entities', () => {
      const content = '<playlist><trackList><track>' +
        '<title>Rock &amp; Roll</title><creator>AC/DC &lt;Band&gt;</creator>' +
        '</track></trackList></playlist>';

      const result = parseXSPFFile(content, 'playlist.xspf');

      expect(result.tracks[0]).toEqual({
        title: 'Rock & Roll',
        artist: 'AC/DC <Band>',
        album: undefined,
      });
    });

    it('should throw when no track entries are present', () => {
      expect(() => parseXSPFFile('<playlist><trackList></trackList></playlist>', 'playlist.xspf')).toThrow('No tracks found');
    });
  });
});
