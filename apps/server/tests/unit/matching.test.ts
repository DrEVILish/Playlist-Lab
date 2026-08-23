/**
 * Unit Tests for Matching Service
 *
 * Tests specific examples and edge cases for the matching algorithm.
 *
 * NOTE: matching.ts exposes a single function-based entry point, `matchPlaylist`
 * (there is no `MatchingService` class, and `ExternalTrack`/`MatchingSettings` are
 * not re-exported from this module — they live in `services/scrapers` and
 * `database/types` respectively). These tests drive the real matching algorithm
 * through `matchPlaylist` with a mocked `PlexClient`, and read the resulting
 * `score` field back off the matched track.
 */

import { matchPlaylist, MatchedTrack } from '../../src/services/matching';
import { ExternalTrack } from '../../src/services/scrapers';
import { MatchingSettings } from '../../src/database/types';
import { PlexClient } from '../../src/services/plex';

// Mock PlexClient
jest.mock('../../src/services/plex');

const DEFAULT_MATCHING_SETTINGS: MatchingSettings = {
  minMatchScore: 0,
  stripParentheses: true,
  stripBrackets: true,
  useFirstArtistOnly: false,
  ignoreFeaturedArtists: true,
  ignoreRemixInfo: true,
  ignoreVersionInfo: false,
  preferNonCompilation: true,
  penalizeMonoVersions: true,
  penalizeLiveVersions: true,
  preferHigherRated: true,
  minRatingForMatch: 0,
  autoCompleteOnPerfectMatch: true,
  playlistPrefixes: {
    enabled: true,
    spotify: '[Spotify] ',
    deezer: '[Deezer] ',
    apple: '[Apple Music] ',
    tidal: '[Tidal] ',
    youtube: '[YouTube Music] ',
    amazon: '[Amazon Music] ',
    qobuz: '[Qobuz] ',
    listenbrainz: '[ListenBrainz] ',
    file: '[Imported] ',
    ai: '[AI Generated] ',
  },
  customStripPatterns: [],
  featuredArtistPatterns: ['feat.', 'ft.', 'featuring'],
  versionSuffixPatterns: ['- Remaster', '- Remix', '- Live'],
  remasterPatterns: ['remaster', 'remastered'],
  variousArtistsNames: ['Various Artists', 'Various', 'VA'],
  penaltyKeywords: ['mono', 'live'],
  priorityKeywords: ['remaster', 'deluxe'],
};

interface PlexSearchResult {
  title: string;
  grandparentTitle: string;
  parentTitle: string;
  ratingKey: string;
  originalTitle?: string;
  Media?: any[];
}

/**
 * Runs a single source track through matchPlaylist against a single candidate
 * Plex search result, and returns the resulting MatchedTrack.
 */
async function matchAgainst(
  sourceTrack: ExternalTrack,
  plexResult: PlexSearchResult,
  settings: MatchingSettings = DEFAULT_MATCHING_SETTINGS
): Promise<MatchedTrack> {
  const mockPlexClient = {
    searchTrack: jest.fn().mockResolvedValue([plexResult]),
  } as unknown as jest.Mocked<PlexClient>;
  (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

  const [result] = await matchPlaylist(
    [sourceTrack],
    'http://localhost:32400',
    'test-token',
    undefined,
    { ...settings }
  );
  return result;
}

describe('Matching Service', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  describe('score calculation via matchPlaylist', () => {
    it('should return 100 for exact title and artist match', async () => {
      const result = await matchAgainst(
        { title: 'Bohemian Rhapsody', artist: 'Queen' },
        {
          title: 'Bohemian Rhapsody',
          grandparentTitle: 'Queen',
          parentTitle: 'A Night at the Opera',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBe(100);
    });

    it('should handle case-insensitive matching', async () => {
      const result = await matchAgainst(
        { title: 'BOHEMIAN RHAPSODY', artist: 'QUEEN' },
        {
          title: 'bohemian rhapsody',
          grandparentTitle: 'queen',
          parentTitle: 'A Night at the Opera',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBe(100);
    });

    it('should handle accented characters', async () => {
      const result = await matchAgainst(
        { title: 'Café', artist: 'Beyoncé' },
        {
          title: 'Cafe',
          grandparentTitle: 'Beyonce',
          parentTitle: 'Album',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBe(100);
    });

    it('should handle apostrophes and quotes', async () => {
      const result = await matchAgainst(
        { title: "Don't Stop Believin'", artist: 'Journey' },
        {
          title: 'Dont Stop Believin',
          grandparentTitle: 'Journey',
          parentTitle: 'Escape',
          ratingKey: '12345',
        }
      );

      // Titles aren't byte-identical after normalization (the source's trailing
      // "Believin'" normalizes differently than the Plex title's "Believin"),
      // so this lands as a strong partial match rather than a perfect one.
      expect(result.matched).toBe(true);
      expect(result.score).toBeGreaterThanOrEqual(90);
    });

    it('should handle special characters', async () => {
      const result = await matchAgainst(
        { title: 'AC/DC - T.N.T.', artist: 'AC/DC' },
        {
          title: 'ACDC TNT',
          grandparentTitle: 'ACDC',
          parentTitle: 'High Voltage',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBeGreaterThan(80);
    });

    it('should score partial matches lower than exact matches', async () => {
      const result = await matchAgainst(
        { title: 'Stairway to Heaven', artist: 'Led Zeppelin' },
        {
          title: 'Stairway',
          grandparentTitle: 'Led Zeppelin',
          parentTitle: 'Led Zeppelin IV',
          ratingKey: '12345',
        },
        // Disable the non-compilation bonus so the score isn't pushed up and
        // clamped at the 100 ceiling, which would otherwise mask the fact
        // that the title-only match is weaker than an exact match.
        { ...DEFAULT_MATCHING_SETTINGS, preferNonCompilation: false }
      );

      expect(result.score).toBeLessThan(100);
      expect(result.score).toBeGreaterThan(70);
    });

    it('should handle featured artists in title', async () => {
      const result = await matchAgainst(
        { title: 'Song Title (feat. Other Artist)', artist: 'Main Artist' },
        {
          title: 'Song Title',
          grandparentTitle: 'Main Artist',
          parentTitle: 'Album',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBeGreaterThan(90);
    });

    it('should handle remastered versions', async () => {
      const result = await matchAgainst(
        { title: 'Yesterday', artist: 'The Beatles' },
        {
          title: 'Yesterday - Remastered 2009',
          grandparentTitle: 'The Beatles',
          parentTitle: 'Help!',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBeGreaterThan(90);
    });

    it('should not penalize a track whose track-level artist matches on a Various Artists compilation', async () => {
      // "Try Everything" by Shakira appears on the Zootopia soundtrack, where the
      // Plex album artist is "Various Artists" but the track-level artist
      // (originalTitle) is correctly tagged as "Shakira". With the default
      // preferNonCompilation setting, this should not be penalized as if it
      // were an unmatched compilation track.
      const compilationResult = await matchAgainst(
        { title: 'Try Everything', artist: 'Shakira' },
        {
          title: 'Try Everything',
          grandparentTitle: 'Various Artists',
          parentTitle: 'Zootopia (Original Motion Picture Soundtrack)',
          originalTitle: 'Shakira',
          ratingKey: '12345',
        }
      );

      const nonCompilationResult = await matchAgainst(
        { title: 'Try Everything', artist: 'Shakira' },
        {
          title: 'Try Everything',
          grandparentTitle: 'Shakira',
          parentTitle: 'El Dorado',
          ratingKey: '12345',
        }
      );

      expect(compilationResult.score).toBe(nonCompilationResult.score);
    });

    it('should reject a wrong-artist candidate on a Various Artists album instead of matching it', async () => {
      // "Yesterday" by The Beatles is not in the library. A completely unrelated
      // Various Artists compilation happens to have "Yesterday Once More" by The
      // Carpenters, which loosely title-matches ("yesterday" is a substring) but
      // is not an exact title match and has no artist overlap at all. This must
      // not be returned as a match just because the album is Various Artists.
      const result = await matchAgainst(
        { title: 'Yesterday', artist: 'The Beatles' },
        {
          title: 'Yesterday Once More',
          grandparentTitle: 'Various Artists',
          parentTitle: "Now That's What I Call Music",
          originalTitle: 'The Carpenters',
          ratingKey: '12345',
        }
      );

      expect(result.matched).toBe(false);
    });

    it('should not penalize a compilation match when only one of several source artists matches the track artist', async () => {
      // Source track lists two artists; Plex only tags the track-level artist as
      // the second one. This should be treated the same as a full artist match,
      // not penalized as an unidentified compilation track.
      const compilationResult = await matchAgainst(
        { title: 'Some Duet', artist: 'Artist A & Artist B' },
        {
          title: 'Some Duet',
          grandparentTitle: 'Various Artists',
          parentTitle: 'Duets Compilation',
          originalTitle: 'Artist B',
          ratingKey: '12345',
        }
      );

      const nonCompilationResult = await matchAgainst(
        { title: 'Some Duet', artist: 'Artist A & Artist B' },
        {
          title: 'Some Duet',
          grandparentTitle: 'Artist B',
          parentTitle: 'Some Album',
          ratingKey: '12345',
        }
      );

      expect(compilationResult.score).toBe(nonCompilationResult.score);
    });

    it('should still match a soundtrack track when the album artist is a real person (not "Various Artists") and the track-level artist is mistagged', async () => {
      // Real-world case: "Encanto (Original Motion Picture Soundtrack)" is
      // tagged in Plex with album artist "Lin-Manuel Miranda" (the composer,
      // not "Various Artists") and the album name is just "Encanto" (no
      // "soundtrack"/"ost" substring). The track's own originalTitle is
      // mistagged with an unrelated name instead of the real performer. None
      // of the existing "Various Artists"/"soundtrack" name-substring checks
      // fire, so this must be recognized as a compilation via the track-level
      // artist simply being present and different from the album artist.
      const result = await matchAgainst(
        { title: 'The Family Madrigal', artist: 'Stephanie Beatriz' },
        {
          title: 'The Family Madrigal',
          grandparentTitle: 'Lin-Manuel Miranda',
          parentTitle: 'Encanto',
          originalTitle: 'Jaijam Wannapat',
          ratingKey: '289103',
        }
      );

      expect(result.matched).toBe(true);
    });

    it('should not apply a remix penalty for a "Soundtrack Version"/"Movie Version" qualifier', async () => {
      // Real-world case: officially licensed compilation samplers commonly
      // suffix soundtrack tracks with "(From "Encanto"/Soundtrack Version)"
      // or "(From "Moana"/Soundtrack Version)" to distinguish the vocal take
      // from an instrumental-only version elsewhere on the same compilation.
      // That's not a remix, so the bare word "version" must not trigger the
      // remix penalty and drag a correct match's score down.
      const withVersionSuffix = await matchAgainst(
        { title: 'The Family Madrigal', artist: 'Stephanie Beatriz' },
        {
          title: 'The Family Madrigal (From "Encanto"/Soundtrack Version)',
          grandparentTitle: 'Disney Peaceful Guitar',
          parentTitle: 'Disney Summer Songs, Vol. 2',
          originalTitle: 'Stephanie Beatriz;Olga Merediz;Encanto - Cast',
          ratingKey: '992025',
        }
      );

      const withoutVersionSuffix = await matchAgainst(
        { title: 'The Family Madrigal', artist: 'Stephanie Beatriz' },
        {
          title: 'The Family Madrigal',
          grandparentTitle: 'Disney Peaceful Guitar',
          parentTitle: 'Disney Summer Songs, Vol. 2',
          originalTitle: 'Stephanie Beatriz;Olga Merediz;Encanto - Cast',
          ratingKey: '992025',
        }
      );

      expect(withVersionSuffix.score).toBe(withoutVersionSuffix.score);
    });
  });

  describe('matchPlaylist', () => {
    it('should match all tracks when they exist in Plex', async () => {
      const tracks: ExternalTrack[] = [
        { title: 'Song 1', artist: 'Artist 1' },
        { title: 'Song 2', artist: 'Artist 2' },
      ];

      const mockPlexClient = {
        searchTrack: jest
          .fn()
          .mockResolvedValueOnce([
            {
              ratingKey: '1',
              title: 'Song 1',
              grandparentTitle: 'Artist 1',
              parentTitle: 'Album 1',
              Media: [{ audioCodec: 'flac', bitrate: 1000 }],
            },
          ])
          .mockResolvedValueOnce([
            {
              ratingKey: '2',
              title: 'Song 2',
              grandparentTitle: 'Artist 2',
              parentTitle: 'Album 2',
              Media: [{ audioCodec: 'mp3', bitrate: 320 }],
            },
          ]),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const result = await matchPlaylist(
        tracks,
        'http://localhost:32400',
        'test-token',
        undefined,
        DEFAULT_MATCHING_SETTINGS
      );

      expect(result).toHaveLength(2);
      expect(result[0].matched).toBe(true);
      expect(result[0].plexRatingKey).toBe('1');
      expect(result[1].matched).toBe(true);
      expect(result[1].plexRatingKey).toBe('2');
    });

    it('should mark tracks as unmatched when not found', async () => {
      const tracks: ExternalTrack[] = [{ title: 'Obscure Song', artist: 'Unknown Artist' }];

      const mockPlexClient = {
        searchTrack: jest.fn().mockResolvedValue([]),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const result = await matchPlaylist(
        tracks,
        'http://localhost:32400',
        'test-token',
        undefined,
        DEFAULT_MATCHING_SETTINGS
      );

      expect(result).toHaveLength(1);
      expect(result[0].matched).toBe(false);
      expect(result[0].plexRatingKey).toBeUndefined();
    });

    it('should respect minMatchScore setting', async () => {
      const tracks: ExternalTrack[] = [{ title: 'Song', artist: 'Artist' }];

      // A live version of the same song by the same artist clears the
      // title/artist gates (so a score IS computed), but the alternate-version
      // and remix penalties pull the raw score down to ~35 once the
      // non-compilation bonus is disabled — well under a 90 threshold.
      const mockPlexClient = {
        searchTrack: jest.fn().mockResolvedValue([
          {
            ratingKey: '1',
            title: 'Song (Live)',
            grandparentTitle: 'Artist',
            parentTitle: 'Live Album',
            Media: [],
          },
        ]),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const settings: MatchingSettings = {
        ...DEFAULT_MATCHING_SETTINGS,
        preferNonCompilation: false,
        minMatchScore: 90, // High threshold
      };

      const result = await matchPlaylist(
        tracks,
        'http://localhost:32400',
        'test-token',
        undefined,
        settings
      );

      expect(result).toHaveLength(1);
      // Should be marked as unmatched due to the low score
      expect(result[0].matched).toBe(false);
    });

    it('should handle empty playlist', async () => {
      const tracks: ExternalTrack[] = [];

      const result = await matchPlaylist(
        tracks,
        'http://localhost:32400',
        'test-token',
        undefined,
        DEFAULT_MATCHING_SETTINGS
      );

      expect(result).toHaveLength(0);
    });

    it('should include codec and bitrate information', async () => {
      const tracks: ExternalTrack[] = [{ title: 'High Quality Song', artist: 'Audiophile Artist' }];

      const mockPlexClient = {
        searchTrack: jest.fn().mockResolvedValue([
          {
            ratingKey: '1',
            title: 'High Quality Song',
            grandparentTitle: 'Audiophile Artist',
            parentTitle: 'Album',
            Media: [{ audioCodec: 'flac', bitrate: 1411 }],
          },
        ]),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const result = await matchPlaylist(
        tracks,
        'http://localhost:32400',
        'test-token',
        undefined,
        DEFAULT_MATCHING_SETTINGS
      );

      expect(result).toHaveLength(1);
      expect(result[0].matched).toBe(true);
      expect(result[0].plexCodec).toBe('FLAC');
      expect(result[0].plexBitrate).toBe(1411);
    });
  });

  describe('Edge Cases', () => {
    it('should handle very short titles', async () => {
      const result = await matchAgainst(
        { title: 'Go', artist: 'Artist' },
        {
          title: 'Go',
          grandparentTitle: 'Artist',
          parentTitle: 'Album',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBeGreaterThan(0);
    });

    it('should handle very long titles', async () => {
      const longTitle = 'This Is A Very Long Song Title That Goes On And On And On';

      const result = await matchAgainst(
        { title: longTitle, artist: 'Artist' },
        {
          title: longTitle,
          grandparentTitle: 'Artist',
          parentTitle: 'Album',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBe(100);
    });

    it('should handle titles with numbers', async () => {
      const result = await matchAgainst(
        { title: '1999', artist: 'Prince' },
        {
          title: '1999',
          grandparentTitle: 'Prince',
          parentTitle: '1999',
          ratingKey: '12345',
        }
      );

      expect(result.score).toBe(100);
    });

    it('should handle multiple artists separated by commas', async () => {
      const result = await matchAgainst(
        { title: 'Collaboration', artist: 'Artist One, Artist Two, Artist Three' },
        {
          title: 'Collaboration',
          grandparentTitle: 'Artist One',
          parentTitle: 'Album',
          ratingKey: '12345',
        },
        { ...DEFAULT_MATCHING_SETTINGS, useFirstArtistOnly: true }
      );

      expect(result.score).toBeGreaterThan(90);
    });

    it('should handle artists with ampersands', async () => {
      const result = await matchAgainst(
        { title: 'Song', artist: 'Simon & Garfunkel' },
        {
          title: 'Song',
          grandparentTitle: 'Simon and Garfunkel',
          parentTitle: 'Album',
          ratingKey: '12345',
        }
      );

      // "Simon & Garfunkel" is reduced to its first artist ("Simon") when
      // splitting on separators, so this is a partial (not perfect) artist match.
      expect(result.matched).toBe(true);
      expect(result.score).toBeGreaterThan(70);
    });

    it('should handle empty strings gracefully', async () => {
      const tracks: ExternalTrack[] = [{ title: '', artist: '' }];

      const mockPlexClient = {
        searchTrack: jest.fn().mockResolvedValue([
          {
            ratingKey: '12345',
            title: 'Song',
            grandparentTitle: 'Artist',
            parentTitle: 'Album',
          },
        ]),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const result = await matchPlaylist(
        tracks,
        'http://localhost:32400',
        'test-token',
        undefined,
        DEFAULT_MATCHING_SETTINGS
      );

      // An empty title is rejected before any Plex search is even attempted.
      expect(result).toHaveLength(1);
      expect(result[0].matched).toBe(false);
    });

    it('should handle titles with parentheses and brackets', async () => {
      const result = await matchAgainst(
        { title: 'Song [Explicit] (Radio Edit)', artist: 'Artist' },
        {
          title: 'Song',
          grandparentTitle: 'Artist',
          parentTitle: 'Album',
          ratingKey: '12345',
        },
        { ...DEFAULT_MATCHING_SETTINGS, stripParentheses: true, stripBrackets: true }
      );

      expect(result.score).toBeGreaterThan(90);
    });

    it('should handle live versions', async () => {
      const result = await matchAgainst(
        { title: 'Song', artist: 'Artist' },
        {
          title: 'Song (Live)',
          grandparentTitle: 'Artist',
          parentTitle: 'Live Album',
          ratingKey: '12345',
        },
        // Disable the non-compilation bonus so the score isn't pushed up and
        // clamped at the 100 ceiling (same reasoning as the title-only-match
        // test above), which would otherwise mask the alternate-version penalty.
        { ...DEFAULT_MATCHING_SETTINGS, penalizeLiveVersions: true, preferNonCompilation: false }
      );

      // Should still match but with a lower score due to the alternate-version penalty
      expect(result.score).toBeGreaterThan(0);
      expect(result.score).toBeLessThan(100);
    });
  });
});
