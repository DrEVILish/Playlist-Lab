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

import { matchPlaylist, scorePlexCandidate, buildRememberedMatchMap, rememberMatches, MatchedTrack } from '../../src/services/matching';
import { ExternalTrack } from '../../src/services/scrapers';
import { MatchingSettings } from '../../src/database/types';
import { PlexClient, PlexAuthError } from '../../src/services/plex';
import { matchLogger } from '../../src/utils/logger';

// The real kuromoji package parses its ~40MB bundled dictionary off disk to
// build a tokenizer - slow, and unnecessary here (see
// matching-unicode.test.ts for tests that actually exercise a kanji reading).
// This file's own kanji case only needs kuromoji to resolve without kanji
// text hanging the suite; an empty reading (falls back to the untouched
// surface form, which findBestMatch treats as "no useful variant") does that.
jest.mock('kuromoji', () => ({
  builder: () => ({
    build: (callback: (err: Error | null, tokenizer: { tokenize: (text: string) => { surface_form: string; reading?: string }[] }) => void) => {
      callback(null, { tokenize: (text: string) => [{ surface_form: text, reading: undefined }] });
    },
  }),
}));

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

    it('does not triple-penalize a compilation title-only match down to near zero', async () => {
      // Real-world case: "GATE OF STEINER -Main theme-" by 阿保剛 (credited
      // in kanji) exists in Plex as a various-artists soundtrack compilation
      // crediting the same composer romanized ("Takeshi Abo"). Title is an
      // exact match; there is no artist evidence at all connecting the two
      // scripts. isCompilation's own -40 and allowTitleOnlyMatch's own -20
      // already penalize exactly that "no artist evidence" fact once each -
      // a third, preferNonCompilation-driven -30 on top used to crush a 91%
      // exact-title base down to 1%, which reads as "barely related" for a
      // track that is, in fact, an exact title match.
      const result = await matchAgainst(
        { title: 'GATE OF STEINER -Main theme-', artist: '阿保剛' },
        {
          title: 'GATE OF STEINER -Main theme-',
          grandparentTitle: 'Various Artists',
          parentTitle: 'STEINS;GATE Original Soundtrack',
          originalTitle: 'Takeshi Abo',
          ratingKey: '1',
        }
      );
      // Not high enough to auto-match with no real artist confirmation
      // (correctly still below a realistic minimum threshold) - just no
      // longer absurdly low for what is an exact title match.
      expect(result.score).toBeGreaterThan(25);
      expect(result.score).toBeLessThan(50);
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

  // Both cases below are real matches a user reported as failing/mis-ranked in
  // production. Traced to two separate bugs, both fixed generally (not as
  // per-track special cases):
  //
  // 1. Plex's own convention for a multi-artist originalTitle is to join
  //    credits with ';' (e.g. "Artist A;Artist B;Artist C"), which wasn't
  //    recognized as a separator anywhere. normalizeForComparison() then
  //    stripped the semicolons to nothing rather than a space, gluing
  //    adjacent credits into one unmatchable word (e.g.
  //    "...Gaita;Mauro..." -> "...gaitamauro..."), so a real match only
  //    "worked" by accident, via a prefix/suffix fallback, whenever the
  //    matching name happened to be first or last in the list.
  //
  // 2. scorePlexCandidate() gave a flat +10 bonus whenever a candidate's
  //    album name equals its track title (true for many single-track
  //    "cover version" releases), even when the artist was a confirmed,
  //    confident mismatch - not just unknown/ambiguous. Combined with the
  //    baseline non-match artist score, that let a completely wrong-artist
  //    single outscore the actual correct, artist-matched candidate.
  describe('real-world regressions: semicolon-joined multi-artist originalTitle', () => {
    it('scores a full match when the source lists the same artists Plex credits with semicolons ("We Don\'t Talk About Bruno")', () => {
      const scored = scorePlexCandidate(
        "We Don't Talk About Bruno",
        'Carolina Gaitán - La Gaita',
        {
          title: 'We Don\'t Talk About Bruno (From "Encanto"/Soundtrack Version)',
          grandparentTitle: 'Disney Peaceful Guitar',
          parentTitle: 'Disney Summer Songs , Vol. 2',
          originalTitle: 'Carolina Gaitán - La Gaita;Mauro Castillo;Adassa;Rhenzy Feliz;Diane Guerrero;Stephanie Beatriz;Encanto - Cast',
        },
        DEFAULT_MATCHING_SETTINGS
      );

      expect(scored.score).toBe(100);
    });

    it('scores a full match when the source lists the same artists Plex credits with semicolons ("I Just Can\'t Wait to Be King")', () => {
      const scored = scorePlexCandidate(
        "I Just Can't Wait to Be King",
        'Jason Weaver, Rowan Atkinson, Laura Williams',
        {
          title: "I Just Can't Wait to Be King",
          grandparentTitle: 'The Lion King',
          parentTitle: 'The Lion King',
          originalTitle: 'Jason Weaver;Rowan Atkinson;Laura Williams',
        },
        DEFAULT_MATCHING_SETTINGS
      );

      expect(scored.score).toBe(100);
    });

    it('does not let a title-only "album name equals track title" coincidence outscore the real artist-matched candidate', () => {
      const wrongArtistSingle = scorePlexCandidate(
        "I Just Can't Wait to Be King",
        'Jason Weaver, Rowan Atkinson, Laura Williams',
        {
          // A generic cover single whose one-track album happens to share the
          // track's own title - real Deezer/Plex-library shape - with an
          // artist that has zero relation to the source credits.
          title: "I Just Can't Wait To Be King",
          grandparentTitle: 'Sergio Rodriguez',
          parentTitle: "I Just Can't Wait To Be King",
          originalTitle: '',
        },
        DEFAULT_MATCHING_SETTINGS
      );
      const correctCastRecording = scorePlexCandidate(
        "I Just Can't Wait to Be King",
        'Jason Weaver, Rowan Atkinson, Laura Williams',
        {
          title: "I Just Can't Wait to Be King",
          grandparentTitle: 'The Lion King',
          parentTitle: 'The Lion King',
          originalTitle: 'Jason Weaver;Rowan Atkinson;Laura Williams',
        },
        DEFAULT_MATCHING_SETTINGS
      );

      expect(correctCastRecording.score).toBeGreaterThan(wrongArtistSingle.score);
      expect(correctCastRecording.score).toBe(100);
    });

    it('picks the correct cast-recording candidate over a same-titled wrong-artist single when both are found', async () => {
      const mockPlexClient = {
        searchTrack: jest.fn().mockResolvedValue([
          {
            ratingKey: 'wrong-single',
            title: "I Just Can't Wait To Be King",
            grandparentTitle: 'Sergio Rodriguez',
            parentTitle: "I Just Can't Wait To Be King",
            originalTitle: '',
            Media: [{ audioCodec: 'mp3', bitrate: 320 }],
          },
          {
            ratingKey: 'correct-cast-recording',
            title: "I Just Can't Wait to Be King",
            grandparentTitle: 'The Lion King',
            parentTitle: 'The Lion King',
            originalTitle: 'Jason Weaver;Rowan Atkinson;Laura Williams',
            Media: [{ audioCodec: 'flac', bitrate: 900 }],
          },
        ]),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const [result] = await matchPlaylist(
        [{ title: "I Just Can't Wait to Be King", artist: 'Jason Weaver, Rowan Atkinson, Laura Williams' }],
        'http://localhost:32400',
        'test-token',
        undefined,
        { ...DEFAULT_MATCHING_SETTINGS }
      );

      expect(result.matched).toBe(true);
      expect(result.plexRatingKey).toBe('correct-cast-recording');
    });
  });

  // TODO.md: "Improve multiple artist handling." calculateMatchScore()'s
  // artist half used to always compare just the first-listed name on each
  // side (cleanArtistName() truncated to it unconditionally, ignoring the
  // useFirstArtistOnly setting entirely) - so a source crediting several
  // artists in a different order, or a different one of the same artists,
  // than Plex scored as a near-total artist mismatch (70%) even when a real
  // match existed among the other credits and the artist *gate* correctly
  // passed via anyArtistCreditMatches. That dragged the overall score down
  // for no reason, and with useFirstArtistOnly literally unable to change
  // this scoring behavior despite being an exposed Settings toggle.
  describe('real-world regression: multi-artist scoring compares every credit, not just the first-listed one', () => {
    it('scores a full match when the matching artist is not first on either side', () => {
      const scored = scorePlexCandidate(
        'Song Title',
        'Rowan Atkinson, Jason Weaver, Laura Williams',
        {
          title: 'Song Title',
          grandparentTitle: 'Laura Williams, Diane Guerrero, Jason Weaver',
          parentTitle: 'Album',
          originalTitle: '',
        },
        DEFAULT_MATCHING_SETTINGS
      );

      expect(scored.score).toBe(100);
    });

    it('useFirstArtistOnly restores the old first-name-only comparison', () => {
      const scored = scorePlexCandidate(
        'Song Title',
        'Rowan Atkinson, Jason Weaver, Laura Williams',
        {
          title: 'Song Title',
          grandparentTitle: 'Laura Williams, Diane Guerrero, Jason Weaver',
          parentTitle: 'Album',
          originalTitle: '',
        },
        { ...DEFAULT_MATCHING_SETTINGS, useFirstArtistOnly: true }
      );

      // "Rowan Atkinson" vs "Laura Williams" - genuinely unrelated first names.
      expect(scored.score).toBeLessThan(100);
    });
  });

  // Real match a user found via manual search (100% score) that automatic
  // "Retry" nonetheless failed to find at all. Traced to normalizeSearch()
  // (used to build the literal text sent to Plex's track.title/artist.title
  // search filters, which do a plain substring match, not fuzzy text search)
  // stripping apostrophes entirely. Plex's own stored title for this track is
  // "I Wan'na Be Like You (2016)" (apostrophe kept); searching for "I Wanna
  // Be Like You" is not a substring of that, so it was never retrieved -
  // instead, two unrelated cover versions that happen to be tagged without
  // the apostrophe were the only things found and scored, at 0% actual
  // artist match.
  describe('real-world regression: Plex search filters do a literal substring match, not fuzzy search', () => {
    it('finds the correct apostrophe-preserving track instead of only the apostrophe-stripped decoys', async () => {
      const wrongDecoys = [
        {
          ratingKey: 'cover-1',
          title: 'I Wanna Be Like You',
          grandparentTitle: 'Jonathan Young',
          parentTitle: "Young's Old Covers (Nostalgic Movies)",
          originalTitle: '',
          Media: [{ audioCodec: 'mp3', bitrate: 320 }],
        },
        {
          ratingKey: 'cover-2',
          title: 'I Wanna Be Like You (feat. Insaneintherainmusic)',
          grandparentTitle: 'Jonathan Young;insaneintherainmusic',
          parentTitle: 'I Wanna Be Like You (Feat. Insaneintherainmusic)',
          originalTitle: '',
          Media: [{ audioCodec: 'mp3', bitrate: 320 }],
        },
      ];
      const correctTrack = {
        ratingKey: 'official-soundtrack',
        title: "I Wan'na Be Like You (2016)",
        grandparentTitle: 'Christopher Walken',
        parentTitle: 'The Jungle Book (Original Motion Picture Soundtrack)',
        originalTitle: '',
        Media: [{ audioCodec: 'flac', bitrate: 885 }],
      };

      // Mirrors Plex's real behavior: a literal substring search for a
      // title/artist that still has the apostrophe finds the real track;
      // the apostrophe-stripped variant only turns up the decoys that
      // happen to be tagged without one.
      const mockPlexClient = {
        searchTrack: jest.fn().mockImplementation((_query: string, _libraryId: string | undefined, artist?: string, title?: string) => {
          if (title?.includes("'") || artist?.includes("'")) {
            return Promise.resolve([correctTrack]);
          }
          return Promise.resolve(wrongDecoys);
        }),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const [result] = await matchPlaylist(
        [{ title: "I Wan'na Be Like You (2016)", artist: 'Christopher Walken' }],
        'http://localhost:32400',
        'test-token',
        undefined,
        { ...DEFAULT_MATCHING_SETTINGS }
      );

      expect(result.matched).toBe(true);
      expect(result.plexRatingKey).toBe('official-soundtrack');
    });
  });

  // findPlexCandidates()'s search tiers used to stop at the first tier that
  // returned ANY results, even a single irrelevant one - so a later, broader
  // tier that would have found the real track never even ran. Now a tier only
  // "counts" once it turns up a candidate that actually passes the title/artist
  // gate, so a wrong-artist cover found by the main filtered search no longer
  // blocks the hub-search fallback from finding the real track.
  describe('real-world regression: an early search tier finding only a wrong-artist candidate must not block later tiers', () => {
    it('keeps searching past a gate-failing filtered-search result and finds the real track via hub search', async () => {
      const wrongArtistCover = {
        ratingKey: 'wrong-cover-band',
        title: 'Perfect',
        grandparentTitle: 'Wedding Cover Band',
        parentTitle: 'Wedding Covers',
        originalTitle: '',
        Media: [{ audioCodec: 'mp3', bitrate: 320 }],
      };
      const correctTrack = {
        ratingKey: 'ed-sheeran-official',
        title: 'Perfect',
        grandparentTitle: 'Ed Sheeran',
        parentTitle: '÷ (Divide)',
        originalTitle: '',
        Media: [{ audioCodec: 'flac', bitrate: 900 }],
      };

      const mockPlexClient = {
        searchTrack: jest.fn().mockImplementation((_query: string, _libraryId: string | undefined, artist?: string, title?: string) => {
          // Hub search fallback calls searchTrack with no artist/title filters.
          if (artist === undefined && title === undefined) {
            return Promise.resolve([correctTrack]);
          }
          // Every filtered-search tier only ever turns up the wrong-artist cover.
          return Promise.resolve([wrongArtistCover]);
        }),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const [result] = await matchPlaylist(
        [{ title: 'Perfect', artist: 'Ed Sheeran' }],
        'http://localhost:32400',
        'test-token',
        undefined,
        { ...DEFAULT_MATCHING_SETTINGS }
      );

      expect(result.matched).toBe(true);
      expect(result.plexRatingKey).toBe('ed-sheeran-official');
    });
  });

  // containsWholeWord()'s \b regex correctly rejects "time" as a substring of
  // "sometime" (no word boundary between them), but its prefix/suffix fallback
  // - added so a *near-miss* like "believin"/"believing" still counts as the
  // same word - had no length check, so it matched "time" against "sometime"
  // anyway via plain endsWith(). That silently passed the title gate for two
  // different songs whenever the same artist also happened to have both.
  describe('real-world regression: short title must not match as a suffix/prefix of an unrelated longer title', () => {
    it('does not match "Time" against an unrelated "Sometime" by the same artist', async () => {
      const result = await matchAgainst(
        { title: 'Time', artist: 'Pink Floyd' },
        {
          title: 'Sometime',
          grandparentTitle: 'Pink Floyd',
          parentTitle: 'Some Other Album',
          ratingKey: '12345',
        }
      );

      expect(result.matched).toBe(false);
    });

    it('still matches a near-miss contraction like "believin" against "believing"', async () => {
      const result = await matchAgainst(
        // No apostrophe here - normalizeForComparison() collapses "-ing" to
        // "-in" so "believing" and "believin" meet in the middle.
        { title: 'Believin', artist: 'Journey' },
        {
          title: 'Believing',
          grandparentTitle: 'Journey',
          parentTitle: 'Album',
          ratingKey: '12345',
        }
      );

      expect(result.matched).toBe(true);
    });
  });

  // Reported against the live library: "Bon Jovi - Livin On A Prayer" sat in
  // Missing Tracks while Plex held the exact song as "Livin' On A Prayer".
  // The search found it - all 8 candidates came back - but every one was
  // rejected on title, because the source had dropped the apostrophe and
  // normalizeForComparison() only expanded contractions when the apostrophe
  // was actually present. That left "livin" vs "living", a 3-of-4 word
  // overlap worth 72%, under the 80% default.
  describe('real-world regression: a contraction spelled without its apostrophe still matches', () => {
    it.each([
      ["Livin On A Prayer", "Livin' On A Prayer"],
      ["Livin On A Prayer", 'Living On A Prayer'],
      ["Livin' On A Prayer", 'Living On A Prayer'],
    ])('scores "%s" against "%s" as an exact match', (sourceTitle, plexTitle) => {
      const scored = scorePlexCandidate(
        sourceTitle,
        'Bon Jovi',
        { title: plexTitle, grandparentTitle: 'Bon Jovi', parentTitle: 'Slippery When Wet', originalTitle: '' },
        DEFAULT_MATCHING_SETTINGS
      );

      expect(scored.score).toBe(100);
    });
  });

  // Found by auditing this app's own live missing-tracks list against its
  // Plex library directly (not a user report): "Dusty Springfield - Wishin'
  // And Hopin'" was sitting in Missing Tracks even though the exact song is
  // in the library, tagged "Wishin' & Hopin'". artistsMatch() already treats
  // "&" and "and" as equivalent for artist names (splitting multi-artist
  // strings on either), but titlesMatch() had no equivalent handling, so an
  // otherwise-perfect title match failed outright over one word being
  // spelled out vs symbolized.
  describe('real-world regression: "&" and "and" are the same word in a title, not just an artist name', () => {
    it('matches a title using "&" against a source title spelling it out as "and"', () => {
      const scored = scorePlexCandidate(
        "Wishin' And Hopin'",
        'Dusty Springfield',
        {
          title: 'Wishin’ & Hopin’',
          grandparentTitle: 'Dusty Springfield',
          parentTitle: 'The Magic of Dusty Springfield',
          originalTitle: '',
        },
        DEFAULT_MATCHING_SETTINGS
      );

      expect(scored.score).toBe(100);
    });
  });

  // A live album's individual track titles are normally just the plain song
  // name - "(Live)"/"Unplugged"/etc. is tagged on the ALBUM title instead
  // (e.g. "Live at Wembley Stadium"). scorePlexCandidate() used to only check
  // the track title for these indicators, so a live-album track scored
  // identically to the real studio version and could win the match outright.
  describe('real-world regression: a live/remaster tag on the album title, not the track title, still applies', () => {
    it('penalizes a track on an album whose title says Live, even though the track title itself does not', () => {
      const studio = scorePlexCandidate(
        'Thunderstruck', 'AC/DC',
        { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'The Razors Edge', originalTitle: '' },
        DEFAULT_MATCHING_SETTINGS
      );
      const live = scorePlexCandidate(
        'Thunderstruck', 'AC/DC',
        { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'Live at River Plate', originalTitle: '' },
        DEFAULT_MATCHING_SETTINGS
      );

      // .score is clamped to [0, 100] and both land above the ceiling here on
      // an otherwise-perfect title/artist match - .rankScore is the real,
      // unclamped value the penalty actually shows up in (and what
      // findBestMatch()/isPreferredCandidate() rank candidates by).
      expect(live.rankScore).toBeLessThan(studio.rankScore);
    });

    it('gives the remaster bonus for an album title that says Remastered, even without a title tag', () => {
      const plain = scorePlexCandidate(
        'Thunderstruck', 'AC/DC',
        { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'The Razors Edge', originalTitle: '' },
        DEFAULT_MATCHING_SETTINGS
      );
      const remastered = scorePlexCandidate(
        'Thunderstruck', 'AC/DC',
        { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'The Razors Edge (Remastered)', originalTitle: '' },
        DEFAULT_MATCHING_SETTINGS
      );

      expect(remastered.rankScore).toBeGreaterThan(plain.rankScore);
    });
  });

  // TODO.md: "make a priority order: 1. Remastered ... 2. Original version
  // i.e. the oldest version of the track" - for two candidates that score
  // identically (same title/artist, neither tagged in a way the score itself
  // notices), pick deliberately rather than whichever the search happened to
  // list first.
  describe('real-world regression: tie-break priority between identically-scored candidates', () => {
    async function matchAgainstMultiple(
      sourceTrack: ExternalTrack,
      plexResults: PlexSearchResult[]
    ): Promise<MatchedTrack> {
      const mockPlexClient = {
        searchTrack: jest.fn().mockResolvedValue(plexResults),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const [result] = await matchPlaylist(
        [sourceTrack], 'http://localhost:32400', 'test-token', undefined,
        { ...DEFAULT_MATCHING_SETTINGS }
      );
      return result;
    }

    it('prefers the Remastered edition over an otherwise identically-scored original', async () => {
      const result = await matchAgainstMultiple(
        { title: 'Thunderstruck', artist: 'AC/DC', album: '' },
        [
          { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'The Razors Edge', ratingKey: 'original' },
          { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'The Razors Edge (Remastered)', ratingKey: 'remastered' },
        ]
      );

      expect(result.plexRatingKey).toBe('remastered');
    });

    it('prefers the older release when neither candidate is Remastered', async () => {
      const result = await matchAgainstMultiple(
        { title: 'Thunderstruck', artist: 'AC/DC', album: '' },
        [
          { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'Greatest Hits', ratingKey: 'newer', parentYear: 2010 } as any,
          { title: 'Thunderstruck', grandparentTitle: 'AC/DC', parentTitle: 'The Razors Edge', ratingKey: 'original', parentYear: 1990 } as any,
        ]
      );

      expect(result.plexRatingKey).toBe('original');
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

  // A remembered manual match (buildRememberedMatchMap(), fed by
  // database.ts's getUserManualMatches()) is a human's explicit "use this
  // track" choice from a previous Rematch - matchPlaylist() should honor it
  // directly instead of re-running the fuzzy search, but must still fall
  // back to a normal search if the remembered track no longer exists in Plex.
  describe('remembered manual matches', () => {
    it('uses a remembered manual match directly instead of searching', async () => {
      const searchTrack = jest.fn().mockResolvedValue([
        {
          ratingKey: 'wrong-search-result',
          title: 'Song',
          grandparentTitle: 'Artist',
          parentTitle: 'Some Other Album',
        },
      ]);
      const getTrackDetails = jest.fn().mockResolvedValue({
        ratingKey: 'remembered-key',
        title: 'Song',
        grandparentTitle: 'Artist',
        parentTitle: 'Album',
        Media: [{ audioCodec: 'flac', bitrate: 900 }],
      });
      const mockPlexClient = { searchTrack, getTrackDetails } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const rememberedMatches = buildRememberedMatchMap([
        { title: 'Song', artist: 'Artist', album: 'Album', plex_rating_key: 'remembered-key' },
      ]);

      const [result] = await matchPlaylist(
        [{ title: 'Song', artist: 'Artist', album: 'Album' }],
        'http://localhost:32400',
        'test-token',
        undefined,
        { ...DEFAULT_MATCHING_SETTINGS },
        undefined,
        undefined,
        undefined,
        undefined,
        rememberedMatches
      );

      expect(result.matched).toBe(true);
      expect(result.plexRatingKey).toBe('remembered-key');
      expect(result.score).toBe(100);
      expect(getTrackDetails).toHaveBeenCalledWith('remembered-key');
      expect(searchTrack).not.toHaveBeenCalled();
    });

    it('falls back to a normal search when the remembered track no longer exists in Plex', async () => {
      const searchTrack = jest.fn().mockResolvedValue([
        {
          ratingKey: 'still-in-library',
          title: 'Song',
          grandparentTitle: 'Artist',
          parentTitle: 'Album',
        },
      ]);
      // Simulates a deleted/moved track - getTrackDetails() returns null on a 404.
      const getTrackDetails = jest.fn().mockResolvedValue(null);
      const mockPlexClient = { searchTrack, getTrackDetails } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      const rememberedMatches = buildRememberedMatchMap([
        { title: 'Song', artist: 'Artist', album: 'Album', plex_rating_key: 'deleted-key' },
      ]);

      const [result] = await matchPlaylist(
        [{ title: 'Song', artist: 'Artist', album: 'Album' }],
        'http://localhost:32400',
        'test-token',
        undefined,
        { ...DEFAULT_MATCHING_SETTINGS },
        undefined,
        undefined,
        undefined,
        undefined,
        rememberedMatches
      );

      expect(getTrackDetails).toHaveBeenCalledWith('deleted-key');
      expect(result.matched).toBe(true);
      expect(result.plexRatingKey).toBe('still-in-library');
    });
  });

  describe('Plex auth failures', () => {
    it('propagates an invalid Plex token instead of reporting every track unmatched', async () => {
      const mockPlexClient = {
        searchTrack: jest.fn().mockRejectedValue(new PlexAuthError('Invalid Plex token')),
        searchTracks: jest.fn().mockRejectedValue(new PlexAuthError('Invalid Plex token')),
      } as unknown as jest.Mocked<PlexClient>;
      (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(() => mockPlexClient);

      await expect(
        matchPlaylist(
          [
            { title: 'Chasing Cars', artist: 'Snow Patrol', album: '' },
            { title: 'Ruby', artist: 'Kaiser Chiefs', album: '' },
          ],
          'http://localhost:32400',
          'expired-token',
          undefined,
          { ...DEFAULT_MATCHING_SETTINGS }
        )
      ).rejects.toBeInstanceOf(PlexAuthError);
    });
  });
});

// The bug this pins: an automatic match was recomputed by the fuzzy search
// on every single rerun, with no memory of what it landed on last time - so
// a rerun could land somewhere different, and there was no record of what an
// automatic run had actually decided to let anyone review or improve the
// matching algorithm later. rememberMatches() persists a newly-resolved
// match the same way a human's manual pick is persisted, and separately logs
// it to matches.log (not the main app log, which is routinely turned down to
// error-only) specifically so it survives to be reviewed.
describe('rememberMatches', () => {
  const track = (overrides: Partial<MatchedTrack> = {}): MatchedTrack => ({
    title: 'Song', artist: 'Artist', album: 'Album',
    matched: true, plexRatingKey: 'rk-1', plexTitle: 'Song', plexArtist: 'Artist', plexAlbum: 'Album',
    score: 95,
    ...overrides,
  });

  const fakeDb = (existing: Array<{ title: string; artist: string; album?: string; plex_rating_key: string }> = []) => ({
    getUserManualMatches: jest.fn(() => existing),
    recordManualMatch: jest.fn(),
  });

  beforeEach(() => jest.restoreAllMocks());

  it('persists a newly-resolved match and logs it to matches.log for later review', () => {
    const db = fakeDb();
    const logSpy = jest.spyOn(matchLogger, 'info').mockImplementation(() => matchLogger);

    rememberMatches(db as any, 1, [track()]);

    expect(db.recordManualMatch).toHaveBeenCalledWith(
      1,
      { title: 'Song', artist: 'Artist', album: 'Album' },
      'rk-1'
    );
    expect(logSpy).toHaveBeenCalledWith('match learned', expect.objectContaining({
      userId: 1,
      source: { title: 'Song', artist: 'Artist', album: 'Album' },
    }));
  });

  it('does not re-persist or re-log a match that was already remembered identically', () => {
    const db = fakeDb([{ title: 'Song', artist: 'Artist', album: 'Album', plex_rating_key: 'rk-1' }]);
    const logSpy = jest.spyOn(matchLogger, 'info').mockImplementation(() => matchLogger);

    rememberMatches(db as any, 1, [track()]);

    expect(db.recordManualMatch).not.toHaveBeenCalled();
    expect(logSpy).not.toHaveBeenCalled();
  });

  it('re-persists and logs when a rerun resolves to a different track than what was remembered', () => {
    const db = fakeDb([{ title: 'Song', artist: 'Artist', album: 'Album', plex_rating_key: 'rk-OLD' }]);
    const logSpy = jest.spyOn(matchLogger, 'info').mockImplementation(() => matchLogger);

    rememberMatches(db as any, 1, [track({ plexRatingKey: 'rk-NEW' })]);

    expect(db.recordManualMatch).toHaveBeenCalledWith(1, expect.anything(), 'rk-NEW');
    expect(logSpy).toHaveBeenCalledWith('match learned', expect.objectContaining({ replacedRatingKey: 'rk-OLD' }));
  });

  it('ignores unmatched tracks entirely', () => {
    const db = fakeDb();
    const logSpy = jest.spyOn(matchLogger, 'info').mockImplementation(() => matchLogger);

    rememberMatches(db as any, 1, [track({ matched: false, plexRatingKey: undefined })]);

    expect(db.recordManualMatch).not.toHaveBeenCalled();
    expect(logSpy).not.toHaveBeenCalled();
  });
});
