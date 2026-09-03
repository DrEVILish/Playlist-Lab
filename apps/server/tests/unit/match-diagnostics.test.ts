/**
 * Unit tests for the explanation attached to an unmatched track.
 *
 * The case that motivated this: a track fails to match automatically, the
 * user opens Manual Match, and the very first result reads 100%. That looks
 * like a plain bug, but it isn't - Manual Match only scores candidates,
 * while automatic matching also applies the artist gate. Nothing in the log
 * said so, which made it impossible to tell that apart from a genuinely
 * broken search. These tests pin the wording that distinguishes them.
 */

import { explainNoMatch, type MatchAttempt } from '../../src/services/matching';

const attempt = (over: Partial<MatchAttempt> = {}): MatchAttempt => ({
  tiers: [],
  candidateCount: 0,
  titleRejected: 0,
  artistRejected: 0,
  ...over,
});

const rejected = (score: number) => ({
  ratingKey: '1',
  plexTitle: "Livin' on a Prayer",
  albumArtist: 'Various Artists',
  trackArtist: 'Bon Jovi Tribute Band',
  album: 'Rock Anthems',
  score,
  reason: 'source artist "Bon Jovi" matched neither (albumArtist=false, trackArtist=false, anyCredit=false)',
});

describe('explainNoMatch', () => {
  it('says so plainly when no search tier found anything', () => {
    expect(explainNoMatch(attempt(), null, 80)).toMatch(/No candidates/);
  });

  it('calls out that Manual Match will disagree when the gate rejected a candidate scoring above the minimum', () => {
    const explanation = explainNoMatch(
      attempt({ candidateCount: 5, artistRejected: 5, bestRejected: rejected(100) }),
      null,
      80
    );

    // The whole point: the log has to pre-empt "but Manual Match says 100%".
    expect(explanation).toContain('Manual Match WILL show this as a match');
    expect(explanation).toContain('artist gate');
    expect(explanation).toContain('100%');
    expect(explanation).toContain(rejected(100).reason);
  });

  it('does not claim Manual Match will show a match when the rejected candidate also scored too low', () => {
    const explanation = explainNoMatch(
      attempt({ candidateCount: 5, artistRejected: 5, bestRejected: rejected(40) }),
      null,
      80
    );

    expect(explanation).not.toContain('Manual Match WILL show this as a match');
    expect(explanation).toContain('below the 80% minimum');
  });

  it('reports a score shortfall when a candidate passed the gate but scored under the minimum', () => {
    const explanation = explainNoMatch(
      attempt({ candidateCount: 3 }),
      { score: 61.4, plexTitle: "Livin' on a Prayer", plexArtist: 'Bon Jovi' },
      80
    );

    expect(explanation).toContain('61%');
    expect(explanation).toContain('below the 80% minimum');
  });

  it('distinguishes "found things, none had the right title" from finding nothing', () => {
    const explanation = explainNoMatch(attempt({ candidateCount: 7, titleRejected: 7 }), null, 80);
    expect(explanation).toBe('7 candidate(s) found but none had a matching title.');
  });
});
