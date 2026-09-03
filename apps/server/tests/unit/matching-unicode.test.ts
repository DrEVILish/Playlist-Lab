/**
 * Non-Latin and case/spelling-variant matching.
 *
 * The comparison and Plex-search normalizers used to keep only [a-z0-9] /
 * ASCII \w, which broke non-Latin catalogues twice over: a fully Japanese
 * (or Cyrillic, Korean, ...) title was stripped to an empty string, so it
 * both compared equal to every other such title - any two Japanese tracks
 * scored 100% against each other - and failed the "does this title have
 * enough characters to search for" guard, so those tracks were never even
 * looked up in Plex.
 */

import { evaluateMatchGate, scorePlexCandidate, matchPlaylist } from '../../src/services/matching';
import { PlexClient } from '../../src/services/plex';
import type { MatchingSettings } from '../../src/database/types';

jest.mock('../../src/services/plex');

// Real kuromoji parses its ~40MB bundled dictionary off disk to build a
// tokenizer - a few hundred ms of real I/O, and (per a jest
// --detectOpenHandles run) something in its dictionary loader doesn't
// release cleanly afterwards, which is exactly the kind of thing that has
// no business running per test. Mocking it also makes the kanji tests
// deterministic: they assert on findBestMatch's own romanization-fallback
// wiring, not on IPADIC's specific (and sometimes simply wrong - see
// kanjiToRomaji's own comment) reading for a given name.
const mockReadings: Record<string, string> = {};
const mockTokenize = jest.fn((text: string) => [{ surface_form: text, reading: mockReadings[text] }]);
jest.mock('kuromoji', () => ({
  builder: () => ({
    build: (callback: (err: Error | null, tokenizer: { tokenize: (text: string) => { surface_form: string; reading?: string }[] }) => void) => {
      callback(null, { tokenize: mockTokenize });
    },
  }),
}));

const settings = {
  minMatchScore: 80,
  stripParentheses: true,
  stripBrackets: true,
  ignoreFeaturedArtists: true,
  ignoreRemixInfo: true,
  preferNonCompilation: true,
  variousArtistsNames: ['Various Artists'],
  featuredArtistPatterns: ['feat.', 'ft.'],
  customStripPatterns: [],
  versionSuffixPatterns: [],
  remasterPatterns: ['remaster'],
  penaltyKeywords: [],
  priorityKeywords: [],
} as unknown as MatchingSettings;

const match = (sourceTitle: string, sourceArtist: string, plexTitle: string, plexArtist: string) => {
  const candidate = { title: plexTitle, grandparentTitle: plexArtist };
  const source = { title: sourceTitle, artist: sourceArtist, album: '' };
  const passes = evaluateMatchGate(source, candidate, settings).passes;
  const score = Math.round(scorePlexCandidate(sourceTitle, sourceArtist, candidate, settings).score);
  // A real match needs both, in this order - the artist/title gate first,
  // then the score threshold. Asserting on the score alone would miss that a
  // matching artist alone is worth +50, enough to drag a title that matches
  // nothing at all up to exactly the default 80% threshold.
  return { passes, score, matched: passes && score >= 80 };
};

describe('non-Latin titles', () => {
  it('matches the same Japanese track against itself', () => {
    expect(match('前前前世', 'RADWIMPS', '前前前世', 'RADWIMPS')).toEqual({ passes: true, score: 100, matched: true });
  });

  it('does NOT match two different Japanese tracks by the same artist', () => {
    // The bug: both titles normalized to "" and so compared exactly equal.
    expect(match('前前前世', 'RADWIMPS', 'なんでもないや', 'RADWIMPS').matched).toBe(false);
  });

  it('still matches when only a parenthesised qualifier differs', () => {
    expect(match('前前前世', 'RADWIMPS', '前前前世 (movie ver.)', 'RADWIMPS').passes).toBe(true);
  });

  it('matches Cyrillic titles against themselves but not against each other', () => {
    expect(match('Привет', 'Артист', 'Привет', 'Артист').passes).toBe(true);
    expect(match('Привет', 'Артист', 'Прощай', 'Артист').matched).toBe(false);
  });

  it('folds half-width katakana onto full-width', () => {
    expect(match('ﾊﾅﾐｽﾞｷ', '一青窈', 'ハナミズキ', '一青窈').passes).toBe(true);
  });
});

describe('case and spelling variants', () => {
  it('ignores capitalisation on both sides', () => {
    expect(match('SICKO MODE', 'Travis Scott', 'sicko mode', 'TRAVIS SCOTT')).toEqual({ passes: true, score: 100, matched: true });
  });

  it('treats eszett and ss as the same word', () => {
    // 'ß' lowercases to itself and has no NFD decomposition, so it used to be
    // stripped outright: "Straße" became "strae", which is not "strasse".
    expect(match('Straße', 'Künstler', 'Strasse', 'Kunstler')).toEqual({ passes: true, score: 100, matched: true });
  });

  it('still refuses a same-titled track by a different artist', () => {
    expect(match('Blue Monday', 'New Order', 'BLUE MONDAY', 'Karaoke All Stars').matched).toBe(false);
  });
});

// Voiced/semi-voiced kana (デ, バ, パ, ...) decompose under NFKD into a base
// kana plus a combining sound mark outside the Latin-accent range being
// stripped, which the catch-all filter then deleted as "not a letter" -
// silently turning "デ" (de) into "テ" (te) in both the Plex search query and
// the comparison normalizer. A self-comparison test wouldn't catch this: the
// same corruption happened identically on both sides, so it still compared
// equal to itself. What actually broke was retrieval - a query built from
// the corrupted spelling never matches Plex's own (uncorrupted) tag.
describe('voiced kana normalization', () => {
  it('sends Plex the uncorrupted spelling of a title with a voiced kana', async () => {
    const searchTrack = jest.fn().mockResolvedValue([]);
    (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(
      () => ({ searchTrack } as unknown as PlexClient)
    );

    await matchPlaylist(
      [{ title: 'アマデウス', artist: 'いとうかなこ' }],
      'http://localhost:32400',
      'test-token',
      undefined,
      settings
    );

    const queriedTitles = searchTrack.mock.calls.map(call => call[3]);
    expect(queriedTitles.some(title => title === 'アマデウス')).toBe(true);
    expect(queriedTitles.some(title => typeof title === 'string' && title.includes('アマテ'))).toBe(false);
  });
});

// Most Plex libraries store a Japanese loanword's title or artist in its
// romanized spelling, not the original katakana/hiragana - "シリウス" shares
// no character with Plex's "Sirius". These drive the fix end-to-end through
// matchPlaylist (rather than evaluateMatchGate/scorePlexCandidate directly)
// because the romanization fallback lives one level up, in findBestMatch.
describe('katakana loanword romanization fallback', () => {
  const matchAgainst = async (
    sourceTrack: { title: string; artist: string },
    plexResult: { title: string; grandparentTitle: string; parentTitle?: string; ratingKey?: string }
  ) => {
    const searchTrack = jest.fn().mockResolvedValue([{ ratingKey: '1', parentTitle: '', ...plexResult }]);
    (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(
      () => ({ searchTrack } as unknown as PlexClient)
    );
    const [result] = await matchPlaylist([sourceTrack], 'http://localhost:32400', 'test-token', undefined, settings);
    return result;
  };

  it('matches a katakana title against its Latin-spelled Plex equivalent', async () => {
    const result = await matchAgainst(
      { title: 'アマデウス', artist: 'Kanako Ito' },
      { title: 'Amadeus', grandparentTitle: 'Kanako Ito' }
    );
    expect(result.matched).toBe(true);
  });

  it('romanizes only the field that needs it, leaving an already-matching kana artist alone', () => {
    // The bug this guards: romanizing title AND artist together broke this
    // exact case - Plex kept the artist in its original kana ("いとうかなこ"),
    // so romanizing it to "itoukanako" made a previously-fine artist
    // comparison fail, even though romanizing the title alone was correct.
    const romanizedArtistOnly = evaluateMatchGate(
      { title: 'amadeusu', artist: 'itoukanako', album: '' },
      { title: 'Amadeus', grandparentTitle: 'いとうかなこ' },
      settings
    );
    const kanaArtistKept = evaluateMatchGate(
      { title: 'amadeusu', artist: 'いとうかなこ', album: '' },
      { title: 'Amadeus', grandparentTitle: 'いとうかなこ' },
      settings
    );
    expect(romanizedArtistOnly.passes).toBe(false);
    expect(kanaArtistKept.passes).toBe(true);
  });

  it('falls back to a kuromoji reading when the kana-only pass cannot handle kanji', async () => {
    mockReadings['一二三'] = 'ヒフミ';
    const result = await matchAgainst(
      { title: '一二三', artist: 'Test Artist' },
      { title: 'Hifumi', grandparentTitle: 'Test Artist' }
    );
    expect(result.matched).toBe(true);
  });

  it('never trusts a kuromoji reading blindly - it still goes through the normal gate/score', async () => {
    // kuromoji's dictionary reading is a plausible guess, not a guarantee
    // (real case: 阿保剛 reads as "Aho Tsuyoshi" in IPADIC, not the "Takeshi
    // Abo" its actual credited artist uses) - a reading that doesn't
    // actually match the candidate must still leave the track unmatched
    // rather than being trusted just because a fallback was tried.
    mockReadings['阿保剛'] = 'アホツヨシ';
    const result = await matchAgainst(
      { title: 'GATE OF STEINER -Main theme-', artist: '阿保剛' },
      { title: 'GATE OF STEINER -Main theme-', grandparentTitle: 'Takeshi Abo' }
    );
    expect(result.matched).toBe(false);
  });

  it('does not reach for kuromoji at all once the kana-only pass already found a match', async () => {
    // kuromoji's dictionary reading is a weaker, sometimes-wrong signal (see
    // the test above) and each variant costs a full extra Plex search, so
    // findBestMatch should only pay for it when the cheaper attempts failed.
    mockTokenize.mockClear();
    const searchTrack = jest.fn().mockResolvedValue([{ ratingKey: '1', parentTitle: '', title: 'シリウス', grandparentTitle: 'Test Artist' }]);
    (PlexClient as jest.MockedClass<typeof PlexClient>).mockImplementation(
      () => ({ searchTrack } as unknown as PlexClient)
    );
    const [result] = await matchPlaylist(
      [{ title: 'シリウス', artist: 'Test Artist' }],
      'http://localhost:32400', 'test-token', undefined, settings
    );
    expect(result.matched).toBe(true);
    expect(mockTokenize).not.toHaveBeenCalled();
  });
});
