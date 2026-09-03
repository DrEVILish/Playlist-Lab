// Copy this entire file content and paste it into apps/server/src/services/matching.ts

/**
 * Matching Service - Complete implementation ported from desktop app
 */

import * as path from 'path';
import * as wanakana from 'wanakana';
import * as kuromoji from 'kuromoji';
import { MatchingSettings } from '../database/types';
import { logger, matchLogger } from '../utils/logger';
import { ExternalTrack } from './scrapers';
import { PlexClient, PlexAuthError } from './plex';
import type { DatabaseService } from '../database/database';
import type { MissingTrack } from '../database/types';
import { matchingLimiter } from './task-queues';

// wanakana only knows the phonetic reading of hiragana/katakana - it passes
// kanji through untouched, since a kanji's reading depends on context and
// needs a dictionary/morphological analyzer to resolve, which kanjiToRomaji
// below handles separately. A leftover CJK ideograph in the output means the
// source wasn't purely kana, so romanizing it would just glue romaji onto
// untouched kanji and search Plex with garbage text - null tells the caller
// not to bother.
function romanizeKana(text: string): string | null {
  if (!text || !wanakana.isJapanese(text)) return null;
  const romanized = wanakana.toRomaji(text);
  return /\p{Script=Han}/u.test(romanized) ? null : romanized;
}

// kuromoji.builder().build() parses the bundled IPADIC dictionary off disk -
// a few hundred ms the first time, and this app matches thousands of tracks
// over its lifetime, so that cost is worth paying once and reusing rather
// than repeating it per track. A promise (not the resolved tokenizer) is
// cached so every caller arriving before the first build finishes awaits
// that same build instead of racing a second one.
type KuromojiTokenizer = kuromoji.Tokenizer<kuromoji.IpadicFeatures>;
let tokenizerPromise: Promise<KuromojiTokenizer> | null = null;

function getTokenizer(): Promise<KuromojiTokenizer> {
  if (!tokenizerPromise) {
    const dicPath = path.join(path.dirname(require.resolve('kuromoji/package.json')), 'dict');
    tokenizerPromise = new Promise((resolve, reject) => {
      kuromoji.builder({ dicPath }).build((err, tokenizer) => (err ? reject(err) : resolve(tokenizer)));
    });
  }
  return tokenizerPromise;
}

/**
 * Best-effort reading for text that still contains kanji after romanizeKana
 * gives up on it. IPADIC's dictionary reading is the common
 * on'yomi/kun'yomi reading, not necessarily the one a specific person or
 * work actually uses for their name - personal-name (nanori) readings are
 * notoriously irregular, so "阿保剛" comes back as the plausible but wrong
 * "Aho Tsuyoshi" here, not the "Takeshi Abo" its actual credited artist
 * uses. That makes this worth trying - it's often right for ordinary words,
 * places, and common surnames - but it's one more candidate reading fed
 * through the same score/gate as everything else in findBestMatch, never a
 * "this must be correct" shortcut.
 */
async function kanjiToRomaji(text: string): Promise<string | null> {
  if (!text || !/\p{Script=Han}/u.test(text)) return null;
  try {
    const tokenizer = await getTokenizer();
    const reading = tokenizer.tokenize(text).map(t => t.reading || t.surface_form).join('');
    return reading ? wanakana.toRomaji(reading) : null;
  } catch (error: any) {
    logger.warn('[Matching] kuromoji tokenization failed, skipping kanji reading fallback', { error: error.message });
    return null;
  }
}

// Most Plex libraries (and Deezer/Spotify/etc metadata) store a Japanese
// loanword title or artist in its romanized spelling, not the original
// katakana/hiragana - "シリウス" shares no character with Plex's "Sirius", so
// neither Plex's own literal-substring search nor this file's word-overlap
// scoring ever connects the two. Building extra, romanized versions of the
// track to search and score alongside the original catches exactly that
// case. A title/artist that's already Latin, or one whose Plex tag is also
// kept in Japanese script, is unaffected.
//
// Title and artist are romanized independently, and every combination is
// returned rather than one all-Latin version: a library can (and often does)
// keep one field in its original script while the other is romanized - e.g.
// Plex tagging a title "Amadeus" but keeping its artist "いとうかなこ" as
// written. Romanizing both together would fix the title comparison but break
// the artist comparison that already worked, so each combination is tried
// and findBestMatch keeps whichever one actually explains a given candidate.
function buildKanaVariants(track: ExternalTrack): ExternalTrack[] {
  const romanizedTitle = romanizeKana(track.title);
  const romanizedArtist = romanizeKana(track.artist);
  if (!romanizedTitle && !romanizedArtist) return [track];

  const variants = [track];
  if (romanizedTitle) variants.push({ ...track, title: romanizedTitle });
  if (romanizedArtist) variants.push({ ...track, artist: romanizedArtist });
  if (romanizedTitle && romanizedArtist) variants.push({ ...track, title: romanizedTitle, artist: romanizedArtist });
  return variants;
}

// Same shape as buildKanaVariants, for whichever field(s) still contain
// kanji - kuromoji's dictionary-lookup reading is a meaningfully weaker
// signal than the algorithmic kana romanization above (see kanjiToRomaji),
// so findBestMatch only reaches for this after the cheaper kana variants
// (and the original) have already failed to turn up a passing candidate.
async function buildKanjiVariants(track: ExternalTrack): Promise<ExternalTrack[]> {
  const [kanjiTitle, kanjiArtist] = await Promise.all([kanjiToRomaji(track.title), kanjiToRomaji(track.artist)]);
  if (!kanjiTitle && !kanjiArtist) return [];

  const variants: ExternalTrack[] = [];
  if (kanjiTitle) variants.push({ ...track, title: kanjiTitle });
  if (kanjiArtist) variants.push({ ...track, artist: kanjiArtist });
  if (kanjiTitle && kanjiArtist) variants.push({ ...track, title: kanjiTitle, artist: kanjiArtist });
  return variants;
}

/**
 * Every reading of `track` worth searching/scoring: the original, then kana
 * and (for whatever's left after that) kuromoji-derived Latin readings.
 * Unlike findBestMatch's own variant handling, this always computes the full
 * set rather than gating kanji variants behind "did the cheaper ones already
 * work" - a caller that already has its search results in hand (deemix's
 * search-once-score-many shape) is only paying CPU to re-score them here,
 * not another network round trip per variant.
 */
export async function buildAllVariants(track: ExternalTrack): Promise<ExternalTrack[]> {
  const kanaVariants = buildKanaVariants(track);
  const kanjiVariants = await buildKanjiVariants(track);
  return [...kanaVariants, ...kanjiVariants];
}

/**
 * Scores `result` against every one of `variants` and keeps whichever one
 * actually explains it - a gate-passing reading beats one that isn't, and
 * among several passing (or several failing) readings the highest-scoring
 * one wins. Shared by findBestMatch (Plex) and findBestDeemixMatches
 * (deemix/Deezer), so a Japanese loanword title or artist gets the same
 * romanized-reading fallback in both places rather than only when matching
 * against Plex. Costs nothing when `variants` has just the one (non-Japanese)
 * entry, and never makes an already-working native-script comparison worse:
 * variants[0] is always in the running, and a worse-scoring alternate
 * reading never displaces it.
 */
export function pickEffectiveVariant(
  variants: ExternalTrack[],
  result: any,
  settings: MatchingSettings
): { track: ExternalTrack; gate: MatchGateResult } {
  let effectiveTrack = variants[0];
  let effectiveGate = evaluateMatchGate(variants[0], result, settings);
  for (const variant of variants.slice(1)) {
    const candidateGate = evaluateMatchGate(variant, result, settings);
    const better = (!effectiveGate.passes && candidateGate.passes) || (
      effectiveGate.passes === candidateGate.passes &&
      scorePlexCandidate(variant.title, variant.artist, result, settings).score >
        scorePlexCandidate(effectiveTrack.title, effectiveTrack.artist, result, settings).score
    );
    if (better) {
      effectiveTrack = variant;
      effectiveGate = candidateGate;
    }
  }
  return { track: effectiveTrack, gate: effectiveGate };
}

export interface MatchedTrack {
  title: string;
  artist: string;
  album?: string;
  matched: boolean;
  plexRatingKey?: string;
  plexTitle?: string;
  plexArtist?: string;
  plexAlbum?: string;
  plexCodec?: string;
  plexBitrate?: number;
  score?: number;
}

// Mirrors database/types.ts's ManualMatch row shape (snake_case, as returned
// by DatabaseService.getUserManualMatches()) rather than a separately-named
// camelCase type, so callers can pass that result straight through with no
// mapping step to keep in sync.
export interface RememberedMatch {
  title: string;
  artist: string;
  album?: string;
  plex_rating_key: string;
}

// Same (title, artist, album) key addMissingTracks()/recordManualMatch() dedupe
// on - kept as one function so building the map and looking it up can never
// use two different notions of "the same track". Exported so callers outside
// this file (e.g. import.ts diffing a fresh scrape against the previous
// cached one) identify "the same track" the same way.
export function rememberedMatchKey(title: string, artist: string, album?: string): string {
  return `${title.trim().toLowerCase()}|||${artist.trim().toLowerCase()}|||${(album ?? '').trim().toLowerCase()}`;
}

// Builds matchPlaylist()'s remembered-match lookup from a user's saved manual
// matches (database.ts's getUserManualMatches()). Exported so every caller
// builds this the same way rather than re-deriving the key itself.
export function buildRememberedMatchMap(manualMatches: RememberedMatch[]): Map<string, string> {
  const map = new Map<string, string>();
  for (const m of manualMatches) {
    map.set(rememberedMatchKey(m.title, m.artist, m.album), m.plex_rating_key);
  }
  return map;
}

/**
 * Records every match a run resolved, so a later rerun of the same playlist
 * reuses the decision instead of re-running the fuzzy search and possibly
 * landing somewhere else. Rows go in the same table (and under the same key)
 * as a user's explicit manual picks, so a human correction simply overwrites
 * what was learned automatically.
 *
 * Each newly-learned match is written to its own log file (matches.log)
 * rather than the main log, which is routinely turned down to error-only -
 * the point of the record is to be reviewable afterwards to improve matching.
 */
export function rememberMatches(db: DatabaseService, userId: number, matched: MatchedTrack[]): void {
  // Read what's already remembered first, so a rerun doesn't re-log every
  // track it matched the same way last time - only genuinely new or changed
  // decisions are worth reviewing.
  let known: Map<string, string>;
  try {
    known = buildRememberedMatchMap(db.getUserManualMatches(userId));
  } catch (error: any) {
    logger.warn('[Matching] Could not read remembered matches, skipping learn step', { error: error.message, userId });
    return;
  }

  for (const track of matched) {
    if (!track.matched || !track.plexRatingKey) continue;
    const key = rememberedMatchKey(track.title, track.artist, track.album);
    if (known.get(key) === track.plexRatingKey) continue;

    try {
      db.recordManualMatch(userId, { title: track.title, artist: track.artist, album: track.album }, track.plexRatingKey);
      matchLogger.info('match learned', {
        userId,
        replacedRatingKey: known.get(key),
        source: { title: track.title, artist: track.artist, album: track.album },
        plex: { title: track.plexTitle, artist: track.plexArtist, album: track.plexAlbum, ratingKey: track.plexRatingKey },
        score: track.score,
      });
    } catch (error: any) {
      // Learning is an optimisation for next time - never fail a completed
      // import over it.
      logger.warn('[Matching] Failed to remember a match', { error: error.message, userId, track: `${track.artist} - ${track.title}` });
    }
  }
}

// A gate/scoring false-positive (see titlesMatch/artistsMatch above) can resolve two
// distinct source tracks to the same Plex track. Callers building a playlist's track
// URI list should run their matched tracks through this first so that doesn't turn
// into a silent duplicate in the playlist.
export function dedupeByPlexRatingKey<T extends { plexRatingKey?: string }>(tracks: T[]): T[] {
  const seen = new Set<string>();
  return tracks.filter(t => {
    if (!t.plexRatingKey) return true;
    if (seen.has(t.plexRatingKey)) return false;
    seen.add(t.plexRatingKey);
    return true;
  });
}

// Caps how many full matching runs are in flight at once app-wide (see
// task-queues.ts) - every caller (import, deemix, lidarr, missing-track
// retry, cross-import, ...) goes through this one function, so gating here
// once covers all of them instead of each caller capping itself locally.
export async function matchPlaylist(...args: Parameters<typeof matchPlaylistImpl>): Promise<MatchedTrack[]> {
  return matchingLimiter.run(() => matchPlaylistImpl(...args));
}

async function matchPlaylistImpl(
  tracks: ExternalTrack[],
  serverUrl: string,
  plexToken: string,
  libraryId: string | undefined,
  settings: MatchingSettings,
  progressEmitter?: any,
  coverUrl?: string,
  playlistName?: string,
  isCancelled?: () => boolean,
  rememberedMatches?: Map<string, string>,
  vanishedFromPlex?: ExternalTrack[]
): Promise<MatchedTrack[]> {
  logger.info('[Matching] Starting', {
    trackCount: tracks.length
  });

  // Copy rather than mutate the caller's settings object - it may be a shared
  // reference (e.g. a user's cached settings row) that other concurrent
  // requests are reading.
  const effectiveSettings: MatchingSettings = {
    ...settings,
    minMatchScore: settings.minMatchScore <= 1 ? settings.minMatchScore * 100 : settings.minMatchScore,
  };

  const plexClient = new PlexClient(serverUrl, plexToken);
  const matchedTracks: MatchedTrack[] = new Array(tracks.length);
  
  // Process tracks in parallel batches to speed up matching without overloading Plex
  // BATCH_SIZE of 5 is a good balance:
  // - Too low (1-2): Slow, doesn't utilize Plex's capacity
  // - Too high (10+): May overload Plex, cause timeouts, or hit rate limits
  // - 5: Fast enough while keeping Plex responsive for other clients
  const BATCH_SIZE = 5;
  const batches: ExternalTrack[][] = [];
  
  for (let i = 0; i < tracks.length; i += BATCH_SIZE) {
    batches.push(tracks.slice(i, i + BATCH_SIZE));
  }

  let processedCount = 0;

  for (const batch of batches) {
    // Check for cancellation
    if (isCancelled && isCancelled()) {
      logger.info(`[Matching] Cancelled at track ${processedCount + 1}/${tracks.length}`);
      throw new Error('Import cancelled by user');
    }

    // Process batch in parallel
    const batchPromises = batch.map(async (track, batchIndex) => {
      const trackIndex = processedCount + batchIndex;
      logger.debug(`[Matching] ${trackIndex + 1}/${tracks.length}: ${track.artist} - ${track.title}`);

      try {
        // A remembered manual match is a human's explicit "use this track"
        // decision, not a guess - honor it directly instead of re-running the
        // fuzzy search, as long as the chosen track still exists in Plex.
        const rememberedRatingKey = rememberedMatches?.get(rememberedMatchKey(track.title, track.artist, track.album));
        let match = rememberedRatingKey ? await resolveRememberedMatch(rememberedRatingKey, plexClient) : null;
        if (rememberedRatingKey && !match) {
          logger.warn(`[Matching] Remembered manual match no longer exists in Plex, falling back to search`, { track: `${track.artist} - ${track.title}`, plexRatingKey: rememberedRatingKey });
          vanishedFromPlex?.push(track);
        } else if (match) {
          logger.info(`[Matching] Using remembered manual match`, { track: `${track.artist} - ${track.title}`, plexRatingKey: rememberedRatingKey });
        }
        let attempt: MatchAttempt | null = null;
        if (!match) {
          ({ match, attempt } = await findBestMatch(track, plexClient, libraryId, effectiveSettings));
        }
        const passesMinScore = match !== null && match.score >= effectiveSettings.minMatchScore;

        if (passesMinScore) {
          logger.info(`[Matching] MATCHED: "${track.artist} - ${track.title}" -> "${match!.plexArtist} - ${match!.plexTitle}" (${Math.round(match!.score)}%)`);
        } else if (attempt) {
          logUnmatchedTrack(track, attempt, match, effectiveSettings.minMatchScore);
        }

        return {
          index: trackIndex,
          result: {
            title: track.title,
            artist: track.artist,
            album: track.album,
            matched: passesMinScore,
            plexRatingKey: passesMinScore ? match?.ratingKey : undefined,
            plexTitle: passesMinScore ? match?.plexTitle : undefined,
            plexArtist: passesMinScore ? match?.plexArtist : undefined,
            plexAlbum: passesMinScore ? match?.plexAlbum : undefined,
            plexCodec: passesMinScore ? match?.plexCodec : undefined,
            plexBitrate: passesMinScore ? match?.plexBitrate : undefined,
            score: match?.score,
          }
        };
      } catch (error: any) {
        if (error instanceof PlexAuthError) throw error;
        logger.error(`[Matching] Error`, { track: `${track.artist} - ${track.title}`, error: error.message });
        return {
          index: trackIndex,
          result: { title: track.title, artist: track.artist, album: track.album, matched: false }
        };
      }
    });

    const batchResults = await Promise.all(batchPromises);
    
    // Store results in correct order
    for (const { index, result } of batchResults) {
      matchedTracks[index] = result;
    }

    processedCount += batch.length;

    // Emit progress after each batch
    if (progressEmitter) {
      progressEmitter.emit('progress', {
        type: 'progress',
        phase: 'matching',
        current: processedCount,
        total: tracks.length,
        currentTrackName: `Processing batch...`,
        coverUrl,
        playlistName
      });
    }
  }

  const matchedCount = matchedTracks.filter(t => t.matched).length;
  logger.info('[Matching] Complete', { total: matchedTracks.length, matched: matchedCount });
  return matchedTracks;
}

// Resolves a remembered manual match's Plex ratingKey to fresh track details,
// so a stale entry (the track was deleted/moved since it was chosen) is
// caught and reported as null - the caller falls back to a normal search
// rather than emitting a match that won't actually resolve for the user.
async function resolveRememberedMatch(
  plexRatingKey: string,
  plexClient: PlexClient
): Promise<{ ratingKey: string; score: number; plexTitle: string; plexArtist: string; plexAlbum: string; plexCodec?: string; plexBitrate?: number } | null> {
  const details = await plexClient.getTrackDetails(plexRatingKey) as any;
  if (!details) return null;

  const media = details.Media?.[0];
  return {
    ratingKey: plexRatingKey,
    score: 100,
    plexTitle: details.title || '',
    plexArtist: details.originalTitle || details.grandparentTitle || '',
    plexAlbum: details.parentTitle || '',
    plexCodec: media?.audioCodec?.toUpperCase(),
    plexBitrate: media?.bitrate,
  };
}

/**
 * Runs the same multi-tier Plex candidate search findBestMatch() (automatic
 * import/Retry) uses, and returns the raw, unscored, unfiltered results.
 * Exported so other callers that need candidates for the *same* track - most
 * importantly the manual rematch search endpoint - retrieve them exactly the
 * same way, rather than maintaining a second, independently-drifting search
 * implementation. Callers that want a single best pick should still gate and
 * score these with the same logic findBestMatch() applies (via
 * scorePlexCandidate() and the same title/artist checks); callers presenting
 * a picklist to a human (manual rematch) can reasonably show all of them,
 * scored but ungated, since a person can judge a lower-confidence candidate
 * a machine shouldn't auto-accept.
 */
export async function findPlexCandidates(
  track: ExternalTrack,
  plexClient: PlexClient,
  libraryId: string | undefined,
  settings: MatchingSettings,
  /** Collects a one-line record of every search tier that ran and what it
   * returned, so a failed match can be explained afterwards rather than
   * having to be reproduced. */
  tierLog?: string[]
): Promise<any[]> {
  try {
    // Counts letters/digits in any script. Restricted to [a-zA-Z0-9], this
    // guard saw a fully Japanese (or Cyrillic, Korean, ...) title as having
    // no characters at all and returned early - so those tracks were never
    // searched for in Plex even once, and came back as "not in the library".
    const titleWithoutPunctuation = track.title.replace(/[^\p{L}\p{N}]/gu, '');
    if (titleWithoutPunctuation.length < 2) return [];

    const cleanedArtist = cleanArtistName(track.artist, settings);
    const coreTitle = getCoreTitle(track.title, settings);
    
    // \w below is ASCII-only, so the strip used to blank out every non-Latin
    // character and send Plex an empty or meaningless query.
    const normalizeSearch = (s: string) => s
      .normalize('NFKD').replace(/[\u0300-\u036f]/g, '').normalize('NFC')
      .replace(/[\u2018\u2019\u201A\u201B\u0027\u0060\u00B4'`�]/g, '')
      .replace(/\//g, ' ').replace(/\./g, '')
      .replace(/[^\p{L}\p{N}_\s\-]/gu, ' ').replace(/\s+/g, ' ').trim();
    
    // Plex's track.title/artist.title search filters do a literal substring
    // match, not fuzzy text search - stripping the apostrophe (which
    // normalizeSearch does above, to tolerate curly-vs-straight-quote and
    // with/without-apostrophe spelling differences during *scoring*) changes
    // the literal character sequence sent as the query. If the Plex library
    // kept the apostrophe in its own tagging (e.g. its stored title genuinely
    // reads "Wan'na"), searching for "Wanna" is not a substring of that and
    // finds nothing - while an unrelated cover version that happens to be
    // tagged without the apostrophe wrongly becomes the only result, and gets
    // matched instead since nothing better was ever even retrieved. Trying
    // once with the apostrophe preserved (just canonicalized to a plain '
    // character) as well keeps this a literal match for a library that also
    // kept it, without giving up the existing apostrophe-stripped attempt
    // for libraries that dropped it.
    const normalizeSearchKeepApostrophe = (s: string) => s
      .normalize('NFKD').replace(/[̀-ͯ]/g, '').normalize('NFC')
      .replace(/[‘’‚‛`´'`�]/g, "'")
      .replace(/\//g, ' ').replace(/\./g, '')
      .replace(/[^\p{L}\p{N}_\s\-']/gu, ' ').replace(/\s+/g, ' ').trim();

    const searchTitle = normalizeSearch(coreTitle);
    const searchArtist = normalizeSearch(cleanedArtist);
    const searchTitleWithApostrophe = normalizeSearchKeepApostrophe(coreTitle);
    const searchArtistWithApostrophe = normalizeSearchKeepApostrophe(cleanedArtist);
    const originalTitle = normalizeSearch(track.title);
    const searchArtistNoHyphen = searchArtist.replace(/-/g, ' ').replace(/\s+/g, ' ').trim();
    
    // Search with artist and title separately for better Plex filtering
    const allResults: any[] = [];

    // Every tier below is the same three steps - run a search, merge any
    // ratingKeys not already collected, record what it did - so they share
    // one runner instead of repeating that block six times. `tierLog` is
    // what makes a failed match explainable after the fact: it records which
    // searches actually ran and what each returned, which is the difference
    // between "Plex has no such track" and "we never asked Plex the right
    // question".
    const runTier = async (name: string, search: () => Promise<any[]>) => {
      try {
        const results = await search();
        const before = allResults.length;
        for (const r of results) {
          if (!allResults.some(existing => existing.ratingKey === r.ratingKey)) {
            allResults.push(r);
          }
        }
        const summary = `${name} -> ${results.length} found, ${allResults.length - before} new`;
        tierLog?.push(summary);
        logger.debug(`[Matching] Tier ${summary}`);
      } catch (err: any) {
        // An expired/invalid Plex token is not "this tier found nothing" -
        // every remaining tier and every remaining track fails identically,
        // and swallowing it here turns a re-login problem into a confident
        // "not in your library" verdict for the whole playlist. Let it out.
        if (err instanceof PlexAuthError) throw err;
        tierLog?.push(`${name} -> ERROR: ${err.message}`);
        logger.warn(`[Matching] Tier "${name}" failed: ${err.message}`);
      }
    };

    // Each tier after the first only runs if nothing gate-worthy has turned
    // up yet - see hasGateWorthyCandidate for why "found something" isn't
    // good enough to stop on.
    const stillSearching = () => !hasGateWorthyCandidate(track, allResults, settings);

    // Try with the apostrophe preserved first when there is one - see the
    // comment above normalizeSearchKeepApostrophe for why this has to be a
    // separate attempt from the apostrophe-stripped one below.
    if (searchTitleWithApostrophe.includes("'") || searchArtistWithApostrophe.includes("'")) {
      await runTier(
        `apostrophe-preserved artist="${searchArtistWithApostrophe}" title="${searchTitleWithApostrophe}"`,
        () => plexClient.searchTrack('', libraryId, searchArtistWithApostrophe, searchTitleWithApostrophe)
      );
    }

    if (stillSearching()) {
      await runTier(
        `filtered artist="${searchArtist}" title="${searchTitle}"`,
        () => plexClient.searchTrack('', libraryId, searchArtist, searchTitle)
      );
    }

    // Handles different formatting like parentheses vs dashes.
    if (stillSearching() && originalTitle !== searchTitle) {
      await runTier(
        `original-title artist="${searchArtist}" title="${originalTitle}"`,
        () => plexClient.searchTrack('', libraryId, searchArtist, originalTitle)
      );
    }

    if (stillSearching() && searchArtistNoHyphen !== searchArtist) {
      await runTier(
        `artist-no-hyphen artist="${searchArtistNoHyphen}" title="${searchTitle}"`,
        () => plexClient.searchTrack('', libraryId, searchArtistNoHyphen, searchTitle)
      );
    }

    // If the title has a parenthetical (e.g. "Song (feat. Someone)", "Song
    // (Live)", "Song (Original Mix)"), try again with it stripped entirely -
    // independent of the stripParentheses setting, which only ever applies
    // unconditionally and would also affect titles that matched fine with the
    // parenthetical kept. This only fires as a last-resort retry after a
    // parenthetical title failed to find anything.
    const titleWithoutParens = normalizeSearch(coreTitle.replace(/\([^)]*\)/g, '').replace(/\s+/g, ' ').trim());
    if (stillSearching() && titleWithoutParens && titleWithoutParens !== searchTitle) {
      await runTier(
        `parenthetical-stripped artist="${searchArtist}" title="${titleWithoutParens}"`,
        () => plexClient.searchTrack('', libraryId, searchArtist, titleWithoutParens)
      );
    }

    // Last resort: hub search across all fields, which handles artist
    // mismatches the filtered searches above can't.
    if (stillSearching()) {
      const hubQuery = `${cleanedArtist} ${cleanTrackTitle(track.title, settings)}`;
      await runTier(
        `hub-search query="${hubQuery}"`,
        () => plexClient.searchTrack(hubQuery, libraryId, undefined, undefined)
      );
    }

    logger.debug(`[Matching] Search for "${cleanedArtist} - ${track.title}": results=${allResults.length}`, {
      artist: searchArtist,
      title: searchTitle,
      originalArtist: track.artist,
      cleanedArtist,
      originalTitle: track.title,
      tiers: tierLog,
      topResults: allResults.slice(0, 5).map(r => ({
        title: r.title,
        artist: r.grandparentTitle,
        album: r.parentTitle,
        ratingKey: r.ratingKey
      }))
    });

    return allResults;
  } catch (error: any) {
    if (error instanceof PlexAuthError) throw error;
    logger.error('[Matching] Error in findPlexCandidates', { error: error.message });
    return [];
  }
}

interface MatchGateResult {
  passes: boolean;
  titleMatches: boolean;
  albumArtistMatches: boolean;
  trackArtistMatches: boolean;
  anyArtistMatches: boolean;
  hasDistinctTrackArtist: boolean;
  artistGateMatches: boolean;
  isCompilation: boolean;
  allowTitleOnlyMatch: boolean;
}

// The single title/artist gate a Plex search result must clear to be treated as
// a real candidate for a source track - shared by findBestMatch() (deciding
// which gate-passing candidate to keep) and findPlexCandidates() (deciding
// whether an earlier, cheaper search tier already turned up something real, or
// it's worth spending another Plex round-trip on a broader tier). Kept as one
// pure function, with no logging of its own, so those two call sites can't
// silently drift apart the way the title-cleaning patterns and the settings
// global used to before they were unified.
/**
 * The artist/title gate every automatic match has to clear before its score
 * is even considered - a high score alone is not enough, since a title match
 * against a completely wrong artist still scores well (artist scoring has a
 * floor of 70). Exported so acquisition paths that pick a candidate outside
 * matchPlaylist() - the deemix search in particular - reject the same
 * candidates an import would, rather than downloading a karaoke version or
 * an unrelated same-titled song into the library.
 */
export function evaluateMatchGate(track: ExternalTrack, result: any, settings: MatchingSettings): MatchGateResult {
  const plexTitle = result.title || '';
  const albumArtist = result.grandparentTitle || '';
  const albumName = result.parentTitle || '';
  const trackArtist = result.originalTitle || '';

  const titleMatches = titlesMatch(track.title, plexTitle, settings);

  const albumArtistMatches = !!albumArtist && artistsMatch(track.artist, albumArtist, settings);
  const trackArtistMatches = !!trackArtist && artistsMatch(track.artist, trackArtist, settings);

  // Catches every case albumArtistMatches/trackArtistMatches can miss:
  // a match between any individually-named credit on either side, not
  // just the first-listed one each of those two only ever compares.
  const anyArtistMatches = anyArtistCreditMatches(track.artist, albumArtist, trackArtist);

  // Check if this is a "Various Artists" compilation
  const albumArtistLower = albumArtist.toLowerCase();
  const albumNameLower = albumName.toLowerCase();
  const isVariousArtists = settings.variousArtistsNames?.some(
    (name: string) => albumArtistLower === name.toLowerCase() ||
                      albumArtistLower.includes(name.toLowerCase())
  ) || normalizeForComparison(albumArtist).includes('various') ||
     normalizeForComparison(albumArtist).includes('compilation');

  // Also check if album name suggests it's a soundtrack/compilation
  const isSoundtrackAlbum = albumNameLower.includes('soundtrack') ||
                            albumNameLower.includes('ost') ||
                            albumArtistLower.includes('cast') ||
                            albumArtistLower.includes('soundtrack');
  // Plex itself only populates a track's originalTitle when the track-level
  // artist differs from the album artist (e.g. a movie soundtrack credited
  // to its composer as album artist, with each song's actual performer set
  // per-track). That's true regardless of whether the album/artist name
  // happens to contain "soundtrack" or "Various Artists" - e.g. an album
  // artist of "Lin-Manuel Miranda" with a track originalTitle of
  // "Stephanie Beatriz" is exactly this case.
  const hasDistinctTrackArtist = !!trackArtist && normalizeForComparison(trackArtist) !== normalizeForComparison(albumArtist);
  const isCompilation = isVariousArtists || isSoundtrackAlbum || hasDistinctTrackArtist;

  // For compilations with exact title match, allow title-only matching
  const cleanSourceTitle = normalizeForComparison(cleanTrackTitle(track.title, settings));
  const cleanPlexTitle = normalizeForComparison(cleanTrackTitle(plexTitle, settings));
  const exactTitleMatch = cleanSourceTitle === cleanPlexTitle;
  const allowTitleOnlyMatch = isCompilation && exactTitleMatch;

  // When Plex gives us a real, distinct track artist, that's the only artist
  // signal we trust - an album artist match alone must never qualify a
  // candidate, since album artist can be "Various Artists", a soundtrack's
  // composer, or otherwise wrong/unmatchable for this specific track.
  // Without a distinct track artist (ordinary, non-compilation tagging),
  // album artist IS the track's artist, so it remains a valid signal.
  const artistGateMatches = hasDistinctTrackArtist
    ? (trackArtistMatches || anyArtistMatches)
    : (albumArtistMatches || trackArtistMatches || anyArtistMatches);

  // Allow a match if the trusted artist signal matches, or it's a compilation
  // with an exact title match - but only once the title itself matches at all.
  const passes = titleMatches && (artistGateMatches || allowTitleOnlyMatch);

  return {
    passes,
    titleMatches,
    albumArtistMatches,
    trackArtistMatches,
    anyArtistMatches,
    hasDistinctTrackArtist,
    artistGateMatches,
    isCompilation,
    allowTitleOnlyMatch,
  };
}

// findPlexCandidates()'s search tiers used to stop as soon as ANY tier
// returned results, even a single irrelevant one - which meant a later, more
// thorough tier that would have actually found the right track never even
// ran. This checks whether the candidates found so far include one that would
// really pass findBestMatch()'s gate, so a tier only "counts" as having found
// the track, not just having found *something*.
function hasGateWorthyCandidate(track: ExternalTrack, results: any[], settings: MatchingSettings): boolean {
  return results.some(r => evaluateMatchGate(track, r, settings).passes);
}

/**
 * Why a track ended up unmatched, captured while the decision is being made
 * so it can be reported afterwards instead of having to be reproduced by
 * re-running the import with debug logging on.
 */
export interface MatchAttempt {
  /** Each search tier that ran, and what it returned. */
  tiers: string[];
  candidateCount: number;
  /** Candidates whose title didn't match the source track at all. */
  titleRejected: number;
  /** Candidates whose title matched but which failed the artist gate. These
   * are the ones that look like a perfect match in Manual Match, which
   * scores candidates without applying the gate. */
  artistRejected: number;
  /** Highest-scoring candidate the artist gate turned away, if any. */
  bestRejected?: {
    ratingKey: string;
    plexTitle: string;
    albumArtist: string;
    trackArtist: string;
    album: string;
    score: number;
    reason: string;
  };
}

/** Spells out, in the terms the gate actually uses, why a title-matching
 * candidate was still rejected - so the log says which artist fields were
 * compared against what, rather than just "artist mismatch". */
function describeArtistGateFailure(track: ExternalTrack, result: any, gate: MatchGateResult): string {
  const albumArtist = result.grandparentTitle || '(none)';
  const trackArtist = result.originalTitle || '(none)';
  const trusted = gate.hasDistinctTrackArtist
    ? `Plex has a distinct track artist ("${trackArtist}"), so its album artist ("${albumArtist}") is not trusted for this track`
    : `no distinct track artist, so album artist ("${albumArtist}") is the track's artist`;
  return `source artist "${track.artist}" matched neither (albumArtist=${gate.albumArtistMatches}, trackArtist=${gate.trackArtistMatches}, anyCredit=${gate.anyArtistMatches}); ${trusted}` +
    (gate.isCompilation ? '; treated as a compilation but the title was not an exact match, so title-only matching did not apply' : '');
}

async function findBestMatch(
  track: ExternalTrack,
  plexClient: PlexClient,
  libraryId: string | undefined,
  settings: MatchingSettings
): Promise<{ match: { ratingKey: string; score: number; plexTitle: string; plexArtist: string; plexAlbum: string; plexCodec?: string; plexBitrate?: number } | null; attempt: MatchAttempt }> {
  const tiers: string[] = [];
  const attempt: MatchAttempt = { tiers, candidateCount: 0, titleRejected: 0, artistRejected: 0 };

  try {
    // See buildKanaVariants: a single-element array (just `track` itself)
    // for anything that isn't pure kana, so every step below collapses back
    // to today's original-script-only behaviour. Extra variants only appear
    // for a title/artist Plex's own search can't retrieve under the original
    // script - so each is worth its own independent search pass rather than
    // just re-scoring the first pass's (likely empty) results.
    const variants = buildKanaVariants(track);
    const allResults = await findPlexCandidates(variants[0], plexClient, libraryId, settings, tiers);
    for (const variant of variants.slice(1)) {
      const variantResults = await findPlexCandidates(variant, plexClient, libraryId, settings, tiers);
      for (const r of variantResults) {
        if (!allResults.some(existing => existing.ratingKey === r.ratingKey)) allResults.push(r);
      }
    }

    // kuromoji's dictionary reading is a weaker, sometimes-wrong signal (see
    // buildKanjiVariants) and each variant costs a full extra Plex search -
    // so it's only worth reaching for once the cheaper attempts above have
    // already failed to turn up anything gate-passing, and skipped entirely
    // for the (large majority of) tracks with no kanji at all.
    const alreadyPassing = allResults.some(r => variants.some(v => evaluateMatchGate(v, r, settings).passes));
    if (!alreadyPassing) {
      const kanjiVariants = await buildKanjiVariants(track);
      for (const variant of kanjiVariants) {
        const variantResults = await findPlexCandidates(variant, plexClient, libraryId, settings, tiers);
        for (const r of variantResults) {
          if (!allResults.some(existing => existing.ratingKey === r.ratingKey)) allResults.push(r);
        }
        variants.push(variant);
      }
    }

    attempt.candidateCount = allResults.length;
    if (!allResults.length) return { match: null, attempt };

    let bestMatch: { ratingKey: string; score: number; rankScore: number; plexTitle: string; plexArtist: string; plexAlbum: string; plexCodec?: string; plexBitrate?: number } | null = null;
    let bestMatchResult: any = null;

    for (const result of allResults) {
      const { track: effectiveTrack, gate: effectiveGate } = pickEffectiveVariant(variants, result, settings);

      if (!effectiveGate.titleMatches) {
        attempt.titleRejected++;
        logger.debug(`[Matching] Title mismatch, skipping`, { sourceTitle: effectiveTrack.title, plexTitle: result.title });
        continue;
      }

      if (!effectiveGate.passes) {
        attempt.artistRejected++;
        // Scored even though it's being rejected: this is the candidate a
        // user sees at a high percentage in Manual Match, so the score is
        // the whole point of the eventual explanation.
        const scored = scorePlexCandidate(effectiveTrack.title, effectiveTrack.artist, result, settings);
        if (!attempt.bestRejected || scored.score > attempt.bestRejected.score) {
          attempt.bestRejected = {
            ratingKey: result.ratingKey,
            plexTitle: result.title || '',
            albumArtist: result.grandparentTitle || '',
            trackArtist: result.originalTitle || '',
            album: result.parentTitle || '',
            score: scored.score,
            reason: describeArtistGateFailure(effectiveTrack, result, effectiveGate),
          };
        }
        logger.debug(`[Matching] Artist gate rejected candidate`, {
          plexTitle: result.title,
          score: scored.score,
          reason: attempt.bestRejected?.reason,
        });
        continue;
      }

      const scored = scorePlexCandidate(effectiveTrack.title, effectiveTrack.artist, result, settings);
      if (!bestMatch || isPreferredCandidate(result, scored, bestMatchResult, bestMatch)) {
        bestMatch = { ratingKey: result.ratingKey, ...scored };
        bestMatchResult = result;
        logger.debug(`[Matching] New best match`, { ratingKey: result.ratingKey, score: scored.rankScore, displayScore: scored.score });
      }
    }

    if (!bestMatch) return { match: null, attempt };
    return {
      match: {
        ratingKey: bestMatch.ratingKey,
        score: bestMatch.score,
        plexTitle: bestMatch.plexTitle,
        plexArtist: bestMatch.plexArtist,
        plexAlbum: bestMatch.plexAlbum,
        plexCodec: bestMatch.plexCodec,
        plexBitrate: bestMatch.plexBitrate,
      },
      attempt,
    };
  } catch (error: any) {
    if (error instanceof PlexAuthError) throw error;
    logger.error('[Matching] Error in findBestMatch', { error: error.message, track: `${track.artist} - ${track.title}` });
    attempt.tiers.push(`ABORTED: ${error.message}`);
    return { match: null, attempt };
  }
}

/**
 * The single log line that explains an unmatched track. Written at warn so
 * it survives in the log next to the noise-free record of what was tried,
 * rather than needing debug logging to have been on ahead of time.
 *
 * The case worth spelling out is the last one: automatic matching applies
 * the artist gate on top of the score, while Manual Match only scores. So a
 * candidate can legitimately read 100% in the Manual Match dialog and still
 * be refused automatically, which looks like a bug unless the log says
 * exactly which artist comparison failed.
 */
export function explainNoMatch(attempt: MatchAttempt, match: { score: number; plexTitle: string; plexArtist: string } | null, minScore: number): string {
  if (attempt.candidateCount === 0) {
    return 'No candidates: every search tier came back empty, so this track is probably not in the library under any searchable spelling.';
  }
  if (match) {
    return `Best gate-passing candidate "${match.plexArtist} - ${match.plexTitle}" scored ${Math.round(match.score)}%, below the ${minScore}% minimum.`;
  }
  if (attempt.bestRejected) {
    const r = attempt.bestRejected;
    return `Title matched but the artist gate rejected every candidate. Best was "${r.albumArtist} - ${r.plexTitle}" (album "${r.album}") scoring ${Math.round(r.score)}%` +
      (r.score >= minScore
        ? ` - at or above the ${minScore}% minimum, so Manual Match WILL show this as a match. Manual Match only scores; automatic matching also requires the artist gate, which failed: ${r.reason}.`
        : `, and also below the ${minScore}% minimum. Gate failure: ${r.reason}.`);
  }
  return `${attempt.candidateCount} candidate(s) found but none had a matching title.`;
}

function logUnmatchedTrack(track: ExternalTrack, attempt: MatchAttempt, match: { score: number; plexTitle: string; plexArtist: string } | null, minScore: number): void {
  const explanation = explainNoMatch(attempt, match, minScore);

  logger.warn(`[Matching] NO MATCH: "${track.artist} - ${track.title}"`, {
    sourceTitle: track.title,
    sourceArtist: track.artist,
    sourceAlbum: track.album,
    explanation,
    minMatchScore: minScore,
    candidatesFound: attempt.candidateCount,
    rejectedOnTitle: attempt.titleRejected,
    rejectedOnArtist: attempt.artistRejected,
    bestRejectedCandidate: attempt.bestRejected,
    searchTiers: attempt.tiers,
  });
}

export interface ScoredCandidate {
  score: number;
  rankScore: number;
  plexTitle: string;
  plexArtist: string;
  plexAlbum: string;
  plexCodec?: string;
  plexBitrate?: number;
}

/**
 * The same score a candidate would get during a real import: calculateMatchScore()
 * plus every version/compilation bonus and penalty findBestMatch() applies while
 * picking a track's best match. Exported so other call sites (e.g. the manual
 * rematch search) can show the real score instead of re-deriving their own.
 */
export function scorePlexCandidate(sourceTitle: string, sourceArtist: string, result: any, settings: MatchingSettings): ScoredCandidate {
  const plexTitle = result.title || '';
  const albumArtist = result.grandparentTitle || '';
  const albumName = result.parentTitle || '';
  const trackArtist = result.originalTitle || '';

  const albumArtistMatches = !!albumArtist && artistsMatch(sourceArtist, albumArtist, settings);
  const trackArtistMatches = !!trackArtist && artistsMatch(sourceArtist, trackArtist, settings);

  // Catches every case albumArtistMatches/trackArtistMatches can miss: a
  // match between any individually-named credit on either side, not just
  // the first-listed one each of those two only ever compares.
  const anyArtistMatches = anyArtistCreditMatches(sourceArtist, albumArtist, trackArtist);

  const albumArtistLower = albumArtist.toLowerCase();
  const albumNameLower = albumName.toLowerCase();
  const isVariousArtists = settings.variousArtistsNames?.some(
    (name: string) => albumArtistLower === name.toLowerCase() || albumArtistLower.includes(name.toLowerCase())
  ) || normalizeForComparison(albumArtist).includes('various') || normalizeForComparison(albumArtist).includes('compilation');
  const isSoundtrackAlbum = albumNameLower.includes('soundtrack') || albumNameLower.includes('ost') ||
    albumArtistLower.includes('cast') || albumArtistLower.includes('soundtrack');
  const hasDistinctTrackArtist = !!trackArtist && normalizeForComparison(trackArtist) !== normalizeForComparison(albumArtist);
  const isCompilation = isVariousArtists || isSoundtrackAlbum || hasDistinctTrackArtist;

  const cleanSourceTitle = normalizeForComparison(cleanTrackTitle(sourceTitle, settings));
  const cleanPlexTitle = normalizeForComparison(cleanTrackTitle(plexTitle, settings));
  const exactTitleMatch = cleanSourceTitle === cleanPlexTitle;
  const allowTitleOnlyMatch = isCompilation && exactTitleMatch;

  const plexArtist = isCompilation && trackArtist
    ? trackArtist
    : (albumArtistMatches ? albumArtist : (trackArtist || albumArtist));
  let score = calculateMatchScore(sourceTitle, sourceArtist, plexTitle, plexArtist, settings);

  // Mirrors findBestMatch()'s gate: once a distinct track artist exists, only it
  // (or an any-artist multi-credit match) counts as a real artist match - an
  // album artist match alone is never trustworthy here, since album artist can
  // be "Various Artists" or a soundtrack's composer rather than this track's artist.
  const artistTrustedMatch = hasDistinctTrackArtist
    ? (trackArtistMatches || anyArtistMatches)
    : (albumArtistMatches || trackArtistMatches || anyArtistMatches);

  if (isCompilation && !artistTrustedMatch) score -= 40;
  if (allowTitleOnlyMatch && !trackArtistMatches && !anyArtistMatches) score -= 20;
  if (!hasReRecordedIndicator(sourceTitle) && hasReRecordedIndicator(plexTitle)) score -= 50;
  if (!hasSpeedModifiedIndicator(sourceTitle) && hasSpeedModifiedIndicator(plexTitle)) score -= 50;
  // A live/session album's individual track titles are usually just the
  // plain song name - "(Live)" etc. is tagged on the ALBUM title instead
  // ("Live at Wembley Stadium"), so checking plexTitle alone missed every
  // one of those and let live-album tracks match as if they were studio
  // versions. Same reasoning for a remix compilation's album title.
  const plexIsAlternateVersion = hasAlternateVersionIndicator(plexTitle) || hasAlternateVersionIndicator(albumName);
  const plexIsRemix = hasRemixIndicator(plexTitle) || hasRemixIndicator(albumName);
  // REMIX_KEYWORDS and ALTERNATE_VERSION_KEYWORDS overlap (e.g. "acoustic", "live"),
  // so a title can trip both indicators at once - apply only the larger penalty
  // rather than stacking them.
  const remixPenalty = (!hasRemixIndicator(sourceTitle) && plexIsRemix) ? 30 : 0;
  const alternateVersionPenalty = (!hasAlternateVersionIndicator(sourceTitle) && plexIsAlternateVersion) ? 35 : 0;
  score -= Math.max(remixPenalty, alternateVersionPenalty);
  if (!hasDemoIndicator(sourceTitle) && hasDemoIndicator(plexTitle)) score -= 35;
  // Same album-title gap for the flip side: a remaster reissue often marks
  // "(Remastered 2011)" on the album rather than every track.
  if ((hasRemasterIndicator(plexTitle) || hasRemasterIndicator(albumName)) && !plexIsRemix) score += 5;

  // Single-release albums are often named after their one track (e.g. a
  // "cover version" single release), which is real corroborating evidence
  // when the artist also lines up - but without artistTrustedMatch backing
  // it up, this coincidence alone let a title-only match to a completely
  // different, wrong artist's single (e.g. a generic cover of "I Just Can't
  // Wait to Be King" by an unrelated artist) outscore the actually-correct,
  // properly artist-matched candidate.
  const normalizedAlbum = normalizeForComparison(albumName);
  const normalizedTrack = normalizeForComparison(plexTitle);
  if (normalizedAlbum && normalizedTrack && normalizedAlbum === normalizedTrack && artistTrustedMatch) score += 10;

  if (settings.preferNonCompilation) {
    if (hasDistinctTrackArtist) {
      // Not on top of allowTitleOnlyMatch: that branch above already applied
      // its own -20 for exactly this "no artist evidence at all" case, on
      // top of the -40 isCompilation already took a few lines up. Both of
      // those already fire from the identical signal (hasDistinctTrackArtist
      // true, trackArtistMatches/anyArtistMatches both false) this branch
      // checks, so stacking a third penalty on it doesn't add information -
      // it just triple-counts one fact. A soundtrack candidate with an exact
      // title match and a genuinely uncorroborated composer credit (a
      // romanized name that doesn't textually match a kanji source, for
      // instance) should read as "low confidence, needs a human to confirm",
      // not crushed to ~1% as if the title barely resembled it at all. This
      // penalty still applies at full strength to a *non-exact* title-only
      // guess (allowTitleOnlyMatch false), which is the weaker signal it was
      // meant to catch.
      if (!trackArtistMatches && !anyArtistMatches && !allowTitleOnlyMatch) score -= 30;
    } else if (albumArtistMatches) {
      score += 50;
    } else if (isCompilation) {
      score -= 30;
    }
  }

  const media = result.Media?.[0];
  return {
    score: Math.min(100, Math.max(0, score)),
    rankScore: score,
    plexTitle,
    plexArtist,
    plexAlbum: albumName,
    plexCodec: media?.audioCodec?.toUpperCase(),
    plexBitrate: media?.bitrate,
  };
}

/**
 * Breaks a tie between two gate-passing candidates that scored identically -
 * e.g. the same song present on both a "Greatest Hits" reissue and the
 * original album, where nothing in the score itself (title, artist, quality)
 * differs. Priority: a Remastered edition first (a deliberate improvement on
 * the original, and usually the best-mastered audio available), then whichever
 * release is older (the original version, preferred over a later live/reissue
 * that shares a score only because neither title carries a version tag).
 * Never overrides a genuine score difference - only applies when scores tie.
 */
function isPreferredCandidate(
  candidateResult: any,
  candidate: ScoredCandidate,
  currentResult: any,
  current: { rankScore: number }
): boolean {
  if (candidate.rankScore !== current.rankScore) return candidate.rankScore > current.rankScore;

  const candidateRemastered = hasRemasterIndicator(candidateResult.title || '') || hasRemasterIndicator(candidateResult.parentTitle || '');
  const currentRemastered = hasRemasterIndicator(currentResult.title || '') || hasRemasterIndicator(currentResult.parentTitle || '');
  if (candidateRemastered !== currentRemastered) return candidateRemastered;

  // Album release year over the track's own (which Plex sometimes leaves
  // unset even when the album has one) - falls back to "no year data", which
  // never wins a tie against a candidate that actually has one.
  const candidateYear = candidateResult.parentYear ?? candidateResult.year ?? Infinity;
  const currentYear = currentResult.parentYear ?? currentResult.year ?? Infinity;
  return candidateYear < currentYear;
}

// Matches a trailing "- From <Movie>" qualifier, e.g. Spotify's
// `Try Everything - From "Zootropolis"` (the UK release title for Zootopia).
// Storefronts commonly append this to movie/TV tie-in songs; Plex's own track
// title for the same song is normally just the bare song title.
const MOVIE_TIE_IN_PATTERN = /\s*-\s*from\s+.+$/gi;

// Shared by getCoreTitle() (drives the Plex search query) and cleanTrackTitle()
// (drives title comparison/scoring) - these used to be two independently
// copy-pasted pattern lists, which risked drifting apart (a pattern added to
// one and not the other silently breaks "found it in manual search but
// auto-match says no match"). Both now go through this single list.
const EDITION_QUALIFIER_PATTERNS = [
  // Remaster patterns with year
  /\s*-\s*remaster(?:ed)?\s*\d{4}/gi,
  /\s*-\s*\d{4}\s*remaster(?:ed)?/gi,
  // Remaster patterns without year (with dash, parentheses, or brackets)
  /\s*-\s*remaster(?:ed)?/gi,
  /\s*\(remaster(?:ed)?\s*\d{4}\)/gi,
  /\s*\(remaster(?:ed)?\)/gi,
  /\s*\[remaster(?:ed)?\s*\d{4}\]/gi,
  /\s*\[remaster(?:ed)?\]/gi,
  // Remaster at end of string (no dash, just space)
  /\s+remaster(?:ed)?$/gi,
  // Deluxe edition patterns
  /\s*-\s*deluxe\s*edition/gi,
  /\s*\(deluxe\s*edition\)/gi,
  /\s*\[deluxe\s*edition\]/gi,
  /\s*-\s*deluxe/gi,
  /\s*\(deluxe\)/gi,
  /\s*\[deluxe\]/gi,
  /\s+deluxe$/gi,
  // Year edition patterns
  /\s*-\s*\d{4}\s*edition/gi,
  /\s*\(\d{4}\s*edition\)/gi,
  /\s*\[\d{4}\s*edition\]/gi,
  // Anniversary edition patterns
  /\s*-\s*anniversary\s*edition/gi,
  /\s*\(anniversary\s*edition\)/gi,
  /\s*\[anniversary\s*edition\]/gi,
  // Expanded edition patterns
  /\s*-\s*expanded\s*edition/gi,
  /\s*\(expanded\s*edition\)/gi,
  /\s*\[expanded\s*edition\]/gi,
  // Special edition patterns
  /\s*-\s*special\s*edition/gi,
  /\s*\(special\s*edition\)/gi,
  /\s*\[special\s*edition\]/gi,
  // Bonus track patterns
  /\s*-\s*bonus\s*track/gi,
  /\s*\(bonus\s*track\)/gi,
  /\s*\[bonus\s*track\]/gi
];

function stripEditionQualifiers(title: string, settings: MatchingSettings): string {
  let cleaned = title;

  for (const pattern of EDITION_QUALIFIER_PATTERNS) {
    cleaned = cleaned.replace(pattern, '');
  }

  // Movie/TV tie-in qualifier, e.g. Spotify's `Try Everything - From "Zootropolis"`.
  // Plex's own track title for the same song is normally just the bare song
  // title, so leaving this in makes every search for the song fail outright.
  cleaned = cleaned.replace(MOVIE_TIE_IN_PATTERN, '');

  if (settings.stripParentheses) cleaned = cleaned.replace(/\([^)]*\)/g, '');
  if (settings.stripBrackets) cleaned = cleaned.replace(/\[[^\]]*\]/g, '');
  return cleaned.replace(/\s+/g, ' ').trim();
}

function getCoreTitle(title: string, settings: MatchingSettings): string {
  return stripEditionQualifiers(title, settings);
}

function cleanTrackTitle(title: string, settings: MatchingSettings): string {
  return stripEditionQualifiers(title, settings) || title;
}

// Shared by cleanArtistName() (which keeps only the first credited artist) and the
// multi-artist fallback checks in findBestMatch()/scorePlexCandidate() (which check
// every credited artist individually) - both need to agree on what counts as a
// separator between artists, including the word "and" and not just &/,//.
// ';' is included because it's Plex's own convention for a track's
// originalTitle field when multiple artists are credited (e.g. a compilation
// track tagged "Performer A;Performer B;Performer C") - without it, the full
// semicolon-joined blob never gets split down to individual names, and
// normalizeForComparison()'s later punctuation stripping fuses adjacent
// credits into one glued word (e.g. "...Gaita;Mauro..." -> "gaitamauro"),
// which breaks word-boundary matching for every credit except whichever one
// happens to be a literal string prefix of the blob.
const MULTI_ARTIST_SEPARATOR_PATTERN = /\s*(?:&|,|;|\/|\band\b)\s*/i;

// Strips a trailing "feat./featuring X" credit, if settings.ignoreFeaturedArtists
// is on. Split out of cleanArtistName() so bestArtistScore() below can apply the
// same cleanup to each individually-split credit, not just a first/only name.
function stripFeaturedArtists(artist: string, settings: MatchingSettings): string {
  if (!artist) return '';
  let cleaned = artist;
  if (settings.ignoreFeaturedArtists) {
    for (const pattern of settings.featuredArtistPatterns) {
      // More flexible regex: handles "feat", "feat.", "featuring" with or without spaces/periods
      // Matches: "feat Post Malone", "feat. Post Malone", "featuring Post Malone", etc.
      const escapedPattern = pattern.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/\\.$/,''); // Remove trailing period from pattern
      const regex = new RegExp(`\\s+${escapedPattern}\\.?\\s+.+$`, 'gi');
      cleaned = cleaned.replace(regex, '');
    }
  }
  return cleaned.replace(/\s+/g, ' ').trim() || artist;
}

function cleanArtistName(artist: string, settings: MatchingSettings): string {
  if (!artist) return '';
  let cleaned = artist;

  // settings.useFirstArtistOnly ("Use first artist only" in Settings) used to
  // be ignored here - every multi-artist credit was truncated to its first
  // name unconditionally, regardless of what the toggle was set to. Left off
  // (the default), the full credit list survives so bestArtistScore() below
  // can compare every individual name instead of just whichever happened to
  // be listed first on each side.
  if (settings.useFirstArtistOnly && MULTI_ARTIST_SEPARATOR_PATTERN.test(cleaned)) {
    cleaned = cleaned.split(MULTI_ARTIST_SEPARATOR_PATTERN)[0].trim();
  }

  return stripFeaturedArtists(cleaned, settings);
}

function normalizeForComparison(str: string): string {
  // NFKD rather than NFD: as well as splitting accents off for the strip
  // below, it folds compatibility forms that routinely differ between a
  // streaming service's metadata and a Plex library's tags - full-width
  // Latin, half-width katakana ("ﾊﾅﾐｽﾞｷ" vs "ハナミズキ"), ligatures and
  // superscripts - onto a single spelling.
  // 'ß' has no decomposition and lowercases to itself, so it needs saying
  // explicitly: without it "Straße" and "Strasse" are different tracks.
  // The trailing .normalize('NFC') undoes NFKD's other effect on Japanese: it
  // also splits a voiced/semi-voiced kana (デ, バ, パ, ...) into its base kana
  // plus a combining sound mark outside the U+0300-036F range being stripped
  // above, which the catch-all filter further down then deletes as "not a
  // letter" - silently turning "デ" (de) into "テ" (te). Recomposing here
  // puts any such kana back together; a Latin accent (already stripped) has
  // nothing left to recompose, so it is unaffected.
  return str.toLowerCase().replace(/\u00df/g, 'ss').normalize('NFKD').replace(/[\u0300-\u036f]/g, '').normalize('NFC')
    .replace(/[\u2018\u2019\u201A\u201B\u0027\u0060\u00B4'`�]/g, '')
    // Contractions have to survive being spelled three different ways for the
    // same track: Plex tags it "Livin' On A Prayer", one source strips the
    // apostrophe to "Livin On A Prayer", another expands it to "Living On A
    // Prayer". With apostrophes already gone above, collapsing -ing to -in
    // lands all three on "livin". Doing it here rather than in titlesMatch
    // matters because calculateMatchScore compares word-by-word: "livin" vs
    // "living" is a 3-of-4 word overlap, which scores 72% and fails an 80%
    // threshold even when the gate is made to let it through.
    // ponytail: the 2+ character stem keeps the shortest words out of the
    // collapse ("sing" and "king" are left as-is), but "thing" and "thin" do
    // end up identical here. Note that short words like "sin"/"sing" are
    // already related by titlesMatch's separate prefix/suffix fallback
    // regardless, so this rule is not what keeps them apart. Narrow the stem
    // rule if a real title ever collides this way.
    .replace(/\b(\w{2,})ing\b/g, '$1in')
    .replace(/[\u201C\u201D\u201E\u201F"]/g, '')
    .replace(/\$/g, 's').replace(/\//g, '').replace(/\./g, '')
    // "&" and "and" are the same word for comparison purposes (e.g. a title
    // stored as "Wishin' & Hopin'" vs a source's "Wishin' And Hopin'") - must
    // run before the catch-all below, which would otherwise delete "&"
    // outright rather than treat it as a word, breaking an exact-title-match
    // real match down to a weaker partial one for no real reason.
    .replace(/&/g, ' and ')
    // Letters and numbers from ANY script, not just ASCII. [^a-z0-9\s] threw
    // away every character of a Japanese, Chinese, Korean, Cyrillic or Greek
    // title, leaving an empty string - which then compared equal to every
    // other such title, so any two Japanese tracks scored 100% against each
    // other while the genuinely correct one was never distinguishable.
    .replace(/[^\p{L}\p{N}\s]/gu, '')
    .replace(/(\d)\s+([ap]m)\b/gi, '$1$2') // Normalize "9 PM" to "9pm", "3 AM" to "3am"
    .replace(/\s+/g, ' ').trim();
}

// normalizeForComparison() only ever leaves [a-z0-9\s] behind, so the containment
// checks below can safely use \b word-boundary regexes with no escaping needed.
// Plain .includes() would let a short title like "Time" match "Sometimes" (as a
// mid-word substring) whenever the artist also happened to match - a silent
// high-confidence wrong match, not just a low-scoring one.
// A needle within this many characters of the haystack's length is close enough
// that only a trailing letter or two differs (the prefix/suffix fallback's actual
// target cases below). Any bigger gap means the fallback would just be matching
// one whole different word against another - e.g. "time" is a real \b-anchored
// suffix match of "sometime" ONLY via startsWith/endsWith, not \b, and letting
// that through turns "Time" by an artist into a false match against that same
// artist's unrelated "Sometime" - exactly the silent wrong-match problem this
// function exists to prevent.
const WHOLE_WORD_FALLBACK_MAX_LENGTH_GAP = 2;

function containsWholeWord(haystack: string, needle: string): boolean {
  if (!haystack || !needle) return false;
  // Lookarounds rather than \b: \b is defined against ASCII [A-Za-z0-9_]
  // only, so it finds no boundary on either side of a Japanese or Cyrillic
  // word and the containment check silently never fires for them.
  if (new RegExp(`(?<![\\p{L}\\p{N}])${needle}(?![\\p{L}\\p{N}])`, 'u').test(haystack)) return true;
  // Falls back to a prefix/suffix check (still anchored, unlike plain .includes())
  // so a needle that's whole-word everywhere except at one edge - e.g. "believin"
  // vs "believing" (contraction expansion only fires when the source title has
  // the apostrophe Plex's copy dropped) or "ac" vs "acdc" (the multi-artist
  // splitter treats "AC/DC"'s slash as a separator) - still matches. Gated to a
  // small length gap so it can't also match two genuinely different words.
  if (Math.abs(haystack.length - needle.length) > WHOLE_WORD_FALLBACK_MAX_LENGTH_GAP) return false;
  return haystack.startsWith(needle) || haystack.endsWith(needle);
}

function titlesMatch(sourceTitle: string, plexTitle: string, settings: MatchingSettings): boolean {
  const cleanSource = normalizeForComparison(cleanTrackTitle(sourceTitle, settings));
  const cleanPlex = normalizeForComparison(cleanTrackTitle(plexTitle, settings));
  if (cleanSource === cleanPlex) return true;
  if (cleanSource.replace(/\s+/g, '') === cleanPlex.replace(/\s+/g, '')) return true;
  if (containsWholeWord(cleanSource, cleanPlex) || containsWholeWord(cleanPlex, cleanSource)) return true;
  return false;
}

function artistsMatch(sourceArtist: string, plexArtist: string, settings: MatchingSettings): boolean {
  const cleanSource = normalizeForComparison(cleanArtistName(sourceArtist, settings));
  const cleanPlex = normalizeForComparison(cleanArtistName(plexArtist, settings));
  if (cleanSource === cleanPlex) return true;
  // titlesMatch has had this check all along; artistsMatch never did. Real
  // case: Deezer tags a Japanese composer's credit with a full-width space
  // between surname and given name ("阿保　剛") where the source has none
  // ("阿保剛") - same three characters, just a separator normalizeForComparison
  // correctly keeps (it's a real word boundary in the general case) but which
  // has no business being the sole reason two otherwise-identical names miss.
  if (cleanSource.replace(/\s+/g, '') === cleanPlex.replace(/\s+/g, '')) return true;
  if (containsWholeWord(cleanSource, cleanPlex) || containsWholeWord(cleanPlex, cleanSource)) return true;
  return false;
}

// Splits a possibly multi-artist credit string (comma/&/semicolon/slash/"and"
// -joined - see MULTI_ARTIST_SEPARATOR_PATTERN) into individual artist names.
function splitArtists(artist: string): string[] {
  if (!artist) return [];
  return artist.split(MULTI_ARTIST_SEPARATOR_PATTERN).map(a => a.trim()).filter(Boolean);
}

// General N-vs-M multi-artist match: true if ANY individually-named credit on
// one side matches ANY individually-named credit on the other. Comparing two
// *whole* multi-artist blobs (or a blob against a single name) only works
// when the matching name happens to be first/last in the blob, because
// normalizeForComparison() strips the separator punctuation between names -
// e.g. Plex's originalTitle "Jason Weaver;Rowan Atkinson;Laura Williams"
// normalizes to "jason weaverrowan atkinsonlaura williams", where "rowan
// atkinson" is no longer word-bounded and can't be found by substring
// checks. Splitting both sides into names first and comparing pairwise has
// no such blind spot, and works the same whether the source or the Plex
// side (or neither) actually has multiple credited artists.
function anyArtistCreditMatches(sourceArtist: string, ...plexArtistFields: string[]): boolean {
  const sourceNames = splitArtists(sourceArtist);
  const plexNames = plexArtistFields.flatMap(splitArtists);
  return sourceNames.some(a => {
    const cleanA = normalizeForComparison(a);
    return plexNames.some(b => {
      const cleanB = normalizeForComparison(b);
      return !!cleanA && !!cleanB && (cleanA === cleanB || containsWholeWord(cleanB, cleanA) || containsWholeWord(cleanA, cleanB));
    });
  });
}

// NOTE: deliberately does not include the bare word "version" - it's too
// generic and false-positives on legitimate, non-remixed compilation/
// soundtrack qualifiers like "(Soundtrack Version)" or "(Movie Version)",
// which would otherwise take an unwarranted -30 remix penalty.
const REMIX_KEYWORDS = /\b(remix|remixed|edit|mix|acoustic|live|instrumental|radio edit|bootleg|dub|extended|vip|flip|rework|reimagined)\b/i;
const REMASTER_KEYWORDS = /\b(remaster(?:ed)?)\b/i;
const ALTERNATE_VERSION_KEYWORDS = /\b(unplugged|acoustic|live|instrumental|radio edit|session|performance|cover)\b/i;
const DEMO_KEYWORDS = /\b(demo)\b/i;
const RERECORDED_KEYWORDS = /\b(re[- ]?recorded|re[- ]?recording|re[- ]?record|taylor'?s? version)\b/i;
const SPEED_MODIFIED_KEYWORDS = /\b(sped up|speed up|slowed down|slow down|nightcore|slowed \+ reverb|sped \+ reverb|accelerated|decelerated)\b/i;

function hasRemixIndicator(title: string): boolean {
  return REMIX_KEYWORDS.test(title);
}

function hasRemasterIndicator(title: string): boolean {
  return REMASTER_KEYWORDS.test(title);
}

function hasAlternateVersionIndicator(title: string): boolean {
  return ALTERNATE_VERSION_KEYWORDS.test(title);
}

function hasDemoIndicator(title: string): boolean {
  return DEMO_KEYWORDS.test(title);
}

function hasReRecordedIndicator(title: string): boolean {
  return RERECORDED_KEYWORDS.test(title);
}

function hasSpeedModifiedIndicator(title: string): boolean {
  return SPEED_MODIFIED_KEYWORDS.test(title);
}

function calculateMatchScore(sourceTitle: string, sourceArtist: string, plexTitle: string, plexArtist: string, settings: MatchingSettings): number {
  const cleanSourceTitle = normalizeForComparison(cleanTrackTitle(sourceTitle, settings));
  const cleanPlexTitle = normalizeForComparison(cleanTrackTitle(plexTitle, settings));

  let titleScore = 0;
  // Two titles that both normalize away to nothing (punctuation-only titles,
  // or - before the Unicode fixes above - anything non-Latin) are not the
  // same track, they are two things we can't compare. Scoring them 100 is
  // how every Japanese track used to match every other one.
  if (!cleanSourceTitle || !cleanPlexTitle) {
    titleScore = 0;
  } else if (cleanSourceTitle === cleanPlexTitle) {
    titleScore = 100;
  } else if (containsWholeWord(cleanSourceTitle, cleanPlexTitle) || containsWholeWord(cleanPlexTitle, cleanSourceTitle)) {
    titleScore = 90;
  } else {
    const sourceWords = cleanSourceTitle.split(/\s+/);
    const plexWords = cleanPlexTitle.split(/\s+/);
    const matches = sourceWords.filter(w => plexWords.some(pw => pw === w)).length;
    titleScore = Math.round((matches / Math.max(sourceWords.length, plexWords.length)) * 80);
  }

  const artistScore = bestArtistScore(sourceArtist, plexArtist, settings);

  return Math.round(titleScore * 0.7 + artistScore * 0.3);
}

/**
 * The artist-half of calculateMatchScore(). With settings.useFirstArtistOnly
 * on, this is just a single first-vs-first comparison (unchanged from before
 * this function existed). Off (the default), a source or Plex credit can
 * legitimately list several artists in a different order, or Plex may credit
 * a compilation track by a different one of the same several names - so
 * every individually-split credit on one side is compared against every one
 * on the other (mirroring anyArtistCreditMatches()'s gate logic) and the best
 * pairwise bucket wins, instead of only ever grading whichever name happened
 * to be listed first on each side.
 */
function bestArtistScore(sourceArtist: string, plexArtist: string, settings: MatchingSettings): number {
  if (settings.useFirstArtistOnly) {
    const cleanSourceArtist = normalizeForComparison(cleanArtistName(sourceArtist, settings));
    const cleanPlexArtist = normalizeForComparison(cleanArtistName(plexArtist, settings));
    return artistPairScore(cleanSourceArtist, cleanPlexArtist);
  }

  const sourceNames = splitArtists(sourceArtist).map(a => normalizeForComparison(stripFeaturedArtists(a, settings)));
  const plexNames = splitArtists(plexArtist).map(a => normalizeForComparison(stripFeaturedArtists(a, settings)));
  if (!sourceNames.length || !plexNames.length) return artistPairScore('', '');

  let best = 0;
  for (const a of sourceNames) {
    for (const b of plexNames) {
      best = Math.max(best, artistPairScore(a, b));
      if (best === 100) return best;
    }
  }
  return best;
}

function artistPairScore(cleanSourceArtist: string, cleanPlexArtist: string): number {
  if (cleanSourceArtist === cleanPlexArtist) return 100;
  if (containsWholeWord(cleanSourceArtist, cleanPlexArtist) || containsWholeWord(cleanPlexArtist, cleanSourceArtist)) return 90;
  return 70;
}

/**
 * Inserts a resolved Plex match for a previously-missing track into its
 * playlist (creating the Plex playlist first if it was only a pending
 * placeholder, or recreating it if Plex says it's gone), restores its
 * original position when known, and clears its missing_tracks row. This is
 * the same sequence the missing-tracks manual retry batch already runs per
 * track (routes/missing.ts's runRetryInBackground) - pulled out here so the
 * post-download reconciliation paths (Deemix, Lidarr) that resolve a match
 * outside of matchPlaylist() can reuse it instead of duplicating it.
 */
export async function insertMatchedTrackIntoPlaylist(
  db: DatabaseService,
  plexService: PlexClient,
  userServer: { server_client_id?: string | null; library_id?: string | null },
  original: Pick<MissingTrack, 'id' | 'playlist_id' | 'after_track_key' | 'title'>,
  plexRatingKey: string
): Promise<boolean> {
  const playlist = db.getPlaylistById(original.playlist_id);
  if (!playlist) {
    logger.warn('[Matching] Playlist not found for missing track', { playlistId: original.playlist_id });
    return false;
  }

  const trackUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/metadata/${plexRatingKey}`;

  try {
    if (playlist.plex_playlist_id.startsWith('pending-')) {
      const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
      const newPlaylist = await plexService.createPlaylist(playlist.name, libraryUri, [trackUri]);
      db.updatePlaylist(playlist.id, { plex_playlist_id: newPlaylist.ratingKey });
      playlist.plex_playlist_id = newPlaylist.ratingKey;
    } else {
      try {
        await plexService.addToPlaylist(playlist.plex_playlist_id, [trackUri]);
      } catch (plexError: any) {
        if (plexError.message?.includes('not found') || plexError.message?.includes('404')) {
          logger.warn('[Matching] Plex playlist not found, creating new one', {
            playlistId: playlist.id,
            plexPlaylistId: playlist.plex_playlist_id,
          });
          const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
          const newPlaylist = await plexService.createPlaylist(playlist.name, libraryUri, [trackUri]);
          db.updatePlaylist(playlist.id, { plex_playlist_id: newPlaylist.ratingKey });
          playlist.plex_playlist_id = newPlaylist.ratingKey;
        } else {
          throw plexError;
        }
      }

      if (original.after_track_key) {
        try {
          const playlistTracks = await plexService.getPlaylistTracks(playlist.plex_playlist_id);
          const newTrack = playlistTracks.find((t: any) => t.ratingKey === plexRatingKey);
          if (newTrack?.playlistItemID) {
            await plexService.movePlaylistItem(
              playlist.plex_playlist_id,
              newTrack.playlistItemID.toString(),
              original.after_track_key
            );
          }
        } catch (moveErr: any) {
          // Non-fatal - track is still in playlist, just not at original position.
          logger.warn('[Matching] Failed to move track to original position', {
            error: moveErr.message,
            trackTitle: original.title,
            playlistId: playlist.plex_playlist_id
          });
        }
      }
    }

    db.removeMissingTrack(original.id);
    return true;
  } catch (error: any) {
    logger.error('[Matching] Failed to add matched track to playlist', {
      error: error.message,
      trackId: original.id,
      playlistId: original.playlist_id
    });
    return false;
  }
}
