// Copy this entire file content and paste it into apps/server/src/services/matching.ts

/**
 * Matching Service - Complete implementation ported from desktop app
 */

import { MatchingSettings } from '../database/types';
import { logger } from '../utils/logger';
import { ExternalTrack } from './scrapers';
import { PlexClient } from './plex';

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

let currentMatchingSettings: MatchingSettings;

export async function matchPlaylist(
  tracks: ExternalTrack[],
  serverUrl: string,
  plexToken: string,
  libraryId: string | undefined,
  settings: MatchingSettings,
  progressEmitter?: any,
  coverUrl?: string,
  playlistName?: string,
  isCancelled?: () => boolean
): Promise<MatchedTrack[]> {
  logger.info('[Matching] Starting', {
    trackCount: tracks.length
  });
  currentMatchingSettings = settings;

  if (currentMatchingSettings.minMatchScore <= 1) {
    currentMatchingSettings.minMatchScore = currentMatchingSettings.minMatchScore * 100;
  }

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
      logger.info(`[Matching] ${trackIndex + 1}/${tracks.length}: ${track.artist} - ${track.title}`);

      try {
        const match = await findBestMatch(track, plexClient, libraryId);
        const passesMinScore = match !== null && match.score >= currentMatchingSettings.minMatchScore;

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

async function findBestMatch(
  track: ExternalTrack,
  plexClient: PlexClient,
  libraryId: string | undefined
): Promise<{ ratingKey: string; score: number; plexTitle: string; plexArtist: string; plexAlbum: string; plexCodec?: string; plexBitrate?: number } | null> {
  try {
    const titleWithoutPunctuation = track.title.replace(/[^a-zA-Z0-9]/g, '');
    if (titleWithoutPunctuation.length < 2) return null;
    
    const cleanedArtist = cleanArtistName(track.artist);
    const coreTitle = getCoreTitle(track.title);
    
    const normalizeSearch = (s: string) => s
      .normalize('NFD').replace(/[\u0300-\u036f]/g, '')
      .replace(/[\u2018\u2019\u201A\u201B\u0027\u0060\u00B4'`�]/g, '')
      .replace(/\//g, ' ').replace(/\./g, '')
      .replace(/[^\w\s\-]/g, ' ').replace(/\s+/g, ' ').trim();
    
    const searchTitle = normalizeSearch(coreTitle);
    const searchArtist = normalizeSearch(cleanedArtist);
    const originalTitle = normalizeSearch(track.title);
    const searchArtistNoHyphen = searchArtist.replace(/-/g, ' ').replace(/\s+/g, ' ').trim();
    
    // Search with artist and title separately for better Plex filtering
    const allResults: any[] = [];
    
    // Try with artist and title filters
    try {
      logger.info(`[Matching] Trying filtered search: artist="${searchArtist}", title="${searchTitle}"`);
      const results = await plexClient.searchTrack('', libraryId, searchArtist, searchTitle);
      logger.info(`[Matching] Filtered search returned ${results.length} results`);
      for (const r of results) {
        if (!allResults.some(existing => existing.ratingKey === r.ratingKey)) {
          allResults.push(r);
        }
      }
    } catch (err: any) {
      logger.error(`[Matching] Filtered search error: ${err.message}`);
    }
    
    // If no results, try with original title (handles different formatting like parentheses vs dashes)
    if (allResults.length === 0 && originalTitle !== searchTitle) {
      try {
        logger.info(`[Matching] Trying with original title: "${originalTitle}"`);
        const results = await plexClient.searchTrack('', libraryId, searchArtist, originalTitle);
        logger.info(`[Matching] Original title search returned ${results.length} results`);
        for (const r of results) {
          if (!allResults.some(existing => existing.ratingKey === r.ratingKey)) {
            allResults.push(r);
          }
        }
      } catch (err: any) {
        logger.error(`[Matching] Original title search error: ${err.message}`);
      }
    }
    
    // If still no results, try with artist variant (no hyphen)
    if (allResults.length === 0 && searchArtistNoHyphen !== searchArtist) {
      try {
        logger.info(`[Matching] Trying artist variant: "${searchArtistNoHyphen}"`);
        const results = await plexClient.searchTrack('', libraryId, searchArtistNoHyphen, searchTitle);
        logger.info(`[Matching] Artist variant search returned ${results.length} results`);
        for (const r of results) {
          if (!allResults.some(existing => existing.ratingKey === r.ratingKey)) {
            allResults.push(r);
          }
        }
      } catch (err: any) {
        logger.error(`[Matching] Artist variant search error: ${err.message}`);
      }
    }
    
    // If still no results, fall back to hub search (searches all fields, handles artist mismatches)
    if (allResults.length === 0) {
      try {
        // Use cleaned artist and cleaned title for hub search
        const cleanedTitle = cleanTrackTitle(track.title);
        const hubQuery = `${cleanedArtist} ${cleanedTitle}`;
        logger.info(`[Matching] Trying hub search fallback: query="${hubQuery}" (cleaned from "${track.artist} - ${track.title}")`);
        // Don't pass artist/title params to force hub search
        const results = await plexClient.searchTrack(hubQuery, libraryId, undefined, undefined);
        logger.info(`[Matching] Hub search returned ${results.length} results`);
        for (const r of results) {
          if (!allResults.some(existing => existing.ratingKey === r.ratingKey)) {
            allResults.push(r);
          }
        }
      } catch (err: any) {
        logger.error(`[Matching] Hub search error: ${err.message}`);
      }
    }
    
    logger.info(`[Matching] Search for "${cleanedArtist} - ${track.title}": results=${allResults.length}`, {
      artist: searchArtist,
      title: searchTitle,
      originalArtist: track.artist,
      cleanedArtist: cleanedArtist,
      originalTitle: track.title,
      topResults: allResults.slice(0, 5).map(r => ({ 
        title: r.title, 
        artist: r.grandparentTitle, 
        album: r.parentTitle,
        ratingKey: r.ratingKey 
      }))
    });
    
    if (!allResults.length) return null;
    
    let bestMatch: { ratingKey: string; score: number; rankScore: number; plexTitle: string; plexArtist: string; plexAlbum: string; plexCodec?: string; plexBitrate?: number } | null = null;
    
    for (const result of allResults) {
      const plexTitle = result.title || '';
      const albumArtist = result.grandparentTitle || '';
      const albumName = result.parentTitle || '';
      const trackArtist = result.originalTitle || '';
      
      logger.info(`[Matching] Checking result: "${plexTitle}" by "${albumArtist}"`, {
        plexTitle,
        albumArtist,
        trackArtist,
        albumName,
        ratingKey: result.ratingKey
      });
      
      if (!titlesMatch(track.title, plexTitle)) {
        logger.info(`[Matching] Title mismatch, skipping`, { sourceTitle: track.title, plexTitle });
        continue;
      }
      
      const albumArtistMatches = albumArtist && artistsMatch(track.artist, albumArtist);
      const trackArtistMatches = trackArtist && artistsMatch(track.artist, trackArtist);
      
      // Also check if any individual artist from a multi-artist string matches
      const sourceArtists = track.artist.split(MULTI_ARTIST_SEPARATOR_PATTERN).map(a => a.trim()).filter(Boolean);
      const anyArtistMatches = sourceArtists.length > 1 && sourceArtists.some(a => {
        const cleanA = normalizeForComparison(a);
        const cleanAlbum = normalizeForComparison(albumArtist);
        const cleanTrack = normalizeForComparison(trackArtist);
        return (cleanAlbum && (cleanA === cleanAlbum || containsWholeWord(cleanAlbum, cleanA) || containsWholeWord(cleanA, cleanAlbum))) ||
               (cleanTrack && (cleanA === cleanTrack || containsWholeWord(cleanTrack, cleanA) || containsWholeWord(cleanA, cleanTrack)));
      });

      // Check if this is a "Various Artists" compilation
      const albumArtistLower = albumArtist.toLowerCase();
      const albumNameLower = albumName.toLowerCase();
      const isVariousArtists = currentMatchingSettings.variousArtistsNames?.some(
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
      const cleanSourceTitle = normalizeForComparison(cleanTrackTitle(track.title));
      const cleanPlexTitle = normalizeForComparison(cleanTrackTitle(plexTitle));
      const exactTitleMatch = cleanSourceTitle === cleanPlexTitle;
      const allowTitleOnlyMatch = isCompilation && exactTitleMatch;
      
      logger.info(`[Matching] Artist check results`, {
        albumArtistMatches,
        trackArtistMatches,
        anyArtistMatches,
        isCompilation,
        allowTitleOnlyMatch,
        sourceArtist: track.artist,
        albumArtist,
        trackArtist
      });
      
      // Allow match if artist matches, or it's a compilation with exact title match
      if (!albumArtistMatches && !trackArtistMatches && !anyArtistMatches && !allowTitleOnlyMatch) {
        logger.info(`[Matching] Artist mismatch, skipping`);
        continue;
      }
      
      const scored = scorePlexCandidate(track.title, track.artist, result, currentMatchingSettings);
      if (!bestMatch || scored.rankScore > bestMatch.rankScore) {
        bestMatch = { ratingKey: result.ratingKey, ...scored };
        logger.info(`[Matching] New best match!`, { ratingKey: result.ratingKey, score: scored.rankScore, displayScore: scored.score });
      }
    }
    
    if (bestMatch) {
      return { 
        ratingKey: bestMatch.ratingKey, 
        score: bestMatch.score, 
        plexTitle: bestMatch.plexTitle, 
        plexArtist: bestMatch.plexArtist, 
        plexAlbum: bestMatch.plexAlbum, 
        plexCodec: bestMatch.plexCodec, 
        plexBitrate: bestMatch.plexBitrate 
      };
    }
    return null;
  } catch (error: any) {
    logger.error('[Matching] Error in findBestMatch', { error: error.message });
    return null;
  }
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
  // calculateMatchScore() and the title/artist cleaners it calls all read the
  // module-level currentMatchingSettings rather than taking settings as a
  // parameter (matchPlaylist() sets it before its own matching loop runs) -
  // called standalone, outside that loop, it would otherwise still be
  // whatever a previous request left it as, or unset entirely.
  currentMatchingSettings = settings;

  const plexTitle = result.title || '';
  const albumArtist = result.grandparentTitle || '';
  const albumName = result.parentTitle || '';
  const trackArtist = result.originalTitle || '';

  const albumArtistMatches = !!albumArtist && artistsMatch(sourceArtist, albumArtist);
  const trackArtistMatches = !!trackArtist && artistsMatch(sourceArtist, trackArtist);
  const sourceArtists = sourceArtist.split(MULTI_ARTIST_SEPARATOR_PATTERN).map(a => a.trim()).filter(Boolean);
  const anyArtistMatches = sourceArtists.length > 1 && sourceArtists.some(a => {
    const cleanA = normalizeForComparison(a);
    const cleanAlbum = normalizeForComparison(albumArtist);
    const cleanTrack = normalizeForComparison(trackArtist);
    return (cleanAlbum && (cleanA === cleanAlbum || containsWholeWord(cleanAlbum, cleanA) || containsWholeWord(cleanA, cleanAlbum))) ||
           (cleanTrack && (cleanA === cleanTrack || containsWholeWord(cleanTrack, cleanA) || containsWholeWord(cleanA, cleanTrack)));
  });

  const albumArtistLower = albumArtist.toLowerCase();
  const albumNameLower = albumName.toLowerCase();
  const isVariousArtists = settings.variousArtistsNames?.some(
    (name: string) => albumArtistLower === name.toLowerCase() || albumArtistLower.includes(name.toLowerCase())
  ) || normalizeForComparison(albumArtist).includes('various') || normalizeForComparison(albumArtist).includes('compilation');
  const isSoundtrackAlbum = albumNameLower.includes('soundtrack') || albumNameLower.includes('ost') ||
    albumArtistLower.includes('cast') || albumArtistLower.includes('soundtrack');
  const hasDistinctTrackArtist = !!trackArtist && normalizeForComparison(trackArtist) !== normalizeForComparison(albumArtist);
  const isCompilation = isVariousArtists || isSoundtrackAlbum || hasDistinctTrackArtist;

  const cleanSourceTitle = normalizeForComparison(cleanTrackTitle(sourceTitle));
  const cleanPlexTitle = normalizeForComparison(cleanTrackTitle(plexTitle));
  const exactTitleMatch = cleanSourceTitle === cleanPlexTitle;
  const allowTitleOnlyMatch = isCompilation && exactTitleMatch;

  const plexArtist = isCompilation && trackArtist
    ? trackArtist
    : (albumArtistMatches ? albumArtist : (trackArtist || albumArtist));
  let score = calculateMatchScore(sourceTitle, sourceArtist, plexTitle, plexArtist);

  if (isCompilation && !albumArtistMatches && !trackArtistMatches && !anyArtistMatches) score -= 40;
  if (allowTitleOnlyMatch && !trackArtistMatches && !anyArtistMatches) score -= 20;
  if (!hasReRecordedIndicator(sourceTitle) && hasReRecordedIndicator(plexTitle)) score -= 50;
  if (!hasSpeedModifiedIndicator(sourceTitle) && hasSpeedModifiedIndicator(plexTitle)) score -= 50;
  // REMIX_KEYWORDS and ALTERNATE_VERSION_KEYWORDS overlap (e.g. "acoustic", "live"),
  // so a title can trip both indicators at once - apply only the larger penalty
  // rather than stacking them.
  const remixPenalty = (!hasRemixIndicator(sourceTitle) && hasRemixIndicator(plexTitle)) ? 30 : 0;
  const alternateVersionPenalty = (!hasAlternateVersionIndicator(sourceTitle) && hasAlternateVersionIndicator(plexTitle)) ? 35 : 0;
  score -= Math.max(remixPenalty, alternateVersionPenalty);
  if (!hasDemoIndicator(sourceTitle) && hasDemoIndicator(plexTitle)) score -= 35;
  if (hasRemasterIndicator(plexTitle) && !hasRemixIndicator(plexTitle)) score += 5;

  const normalizedAlbum = normalizeForComparison(albumName);
  const normalizedTrack = normalizeForComparison(plexTitle);
  if (normalizedAlbum && normalizedTrack && normalizedAlbum === normalizedTrack) score += 10;

  if (settings.preferNonCompilation) {
    if (albumArtistMatches) score += 50;
    else if (isCompilation && !trackArtistMatches && !anyArtistMatches) score -= 30;
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

// Matches a trailing "- From <Movie>" qualifier, e.g. Spotify's
// `Try Everything - From "Zootropolis"` (the UK release title for Zootopia).
// Storefronts commonly append this to movie/TV tie-in songs; Plex's own track
// title for the same song is normally just the bare song title.
const MOVIE_TIE_IN_PATTERN = /\s*-\s*from\s+.+$/gi;

function getCoreTitle(title: string): string {
  let core = title;
  
  // Remove remaster/deluxe/edition patterns BEFORE stripping parentheses/brackets
  const remasterPatterns = [
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
  
  for (const pattern of remasterPatterns) {
    core = core.replace(pattern, '');
  }

  // Movie/TV tie-in qualifier, e.g. Spotify's `Try Everything - From "Zootropolis"`.
  // Plex's own track title for the same song is normally just the bare song
  // title, so leaving this in makes every search for the song fail outright.
  // (Kept in sync with the identical pattern in cleanTrackTitle() below.)
  core = core.replace(MOVIE_TIE_IN_PATTERN, '');

  if (currentMatchingSettings.stripParentheses) core = core.replace(/\([^)]*\)/g, '');
  if (currentMatchingSettings.stripBrackets) core = core.replace(/\[[^\]]*\]/g, '');
  return core.replace(/\s+/g, ' ').trim();
}

function cleanTrackTitle(title: string): string {
  let cleaned = title;
  
  // Remove remaster/deluxe/edition patterns BEFORE stripping parentheses/brackets
  // This handles cases like "Song Title - Remastered" or "Song Title (Deluxe Edition)"
  const remasterPatterns = [
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
  
  for (const pattern of remasterPatterns) {
    cleaned = cleaned.replace(pattern, '');
  }

  cleaned = cleaned.replace(MOVIE_TIE_IN_PATTERN, '');

  if (currentMatchingSettings.stripParentheses) cleaned = cleaned.replace(/\([^)]*\)/g, '');
  if (currentMatchingSettings.stripBrackets) cleaned = cleaned.replace(/\[[^\]]*\]/g, '');
  return cleaned.replace(/\s+/g, ' ').trim() || title;
}

// Shared by cleanArtistName() (which keeps only the first credited artist) and the
// multi-artist fallback checks in findBestMatch()/scorePlexCandidate() (which check
// every credited artist individually) - both need to agree on what counts as a
// separator between artists, including the word "and" and not just &/,//.
const MULTI_ARTIST_SEPARATOR_PATTERN = /\s*(?:&|,|\/|\band\b)\s*/i;

function cleanArtistName(artist: string): string {
  if (!artist) return '';
  let cleaned = artist;

  // Always use first artist only when there are multiple artists
  // Handle various separators: &, and, ,, /
  if (MULTI_ARTIST_SEPARATOR_PATTERN.test(cleaned)) {
    cleaned = cleaned.split(MULTI_ARTIST_SEPARATOR_PATTERN)[0].trim();
  }
  
  if (currentMatchingSettings.ignoreFeaturedArtists) {
    for (const pattern of currentMatchingSettings.featuredArtistPatterns) {
      // More flexible regex: handles "feat", "feat.", "featuring" with or without spaces/periods
      // Matches: "feat Post Malone", "feat. Post Malone", "featuring Post Malone", etc.
      const escapedPattern = pattern.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/\\.$/,''); // Remove trailing period from pattern
      const regex = new RegExp(`\\s+${escapedPattern}\\.?\\s+.+$`, 'gi');
      cleaned = cleaned.replace(regex, '');
    }
  }
  return cleaned.replace(/\s+/g, ' ').trim() || artist;
}

function normalizeForComparison(str: string): string {
  return str.toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '')
    // Fix contractions like "comin'", "goin'", "doin'" -> "coming", "going", "doing".
    // Must run BEFORE apostrophes are stripped below, and requires the apostrophe
    // to actually be present — so real words like "rain", "twin", "chin" are untouched.
    .replace(/\b(\w+)in['\u2018\u2019]/g, '$1ing')
    .replace(/[\u2018\u2019\u201A\u201B\u0027\u0060\u00B4'`�]/g, '')
    .replace(/[\u201C\u201D\u201E\u201F"]/g, '')
    .replace(/\$/g, 's').replace(/\//g, '').replace(/\./g, '')
    .replace(/[^a-z0-9\s]/g, '')
    .replace(/(\d)\s+([ap]m)\b/gi, '$1$2') // Normalize "9 PM" to "9pm", "3 AM" to "3am"
    .replace(/\s+/g, ' ').trim();
}

// normalizeForComparison() only ever leaves [a-z0-9\s] behind, so the containment
// checks below can safely use \b word-boundary regexes with no escaping needed.
// Plain .includes() would let a short title like "Time" match "Sometimes" (as a
// mid-word substring) whenever the artist also happened to match - a silent
// high-confidence wrong match, not just a low-scoring one.
function containsWholeWord(haystack: string, needle: string): boolean {
  if (!haystack || !needle) return false;
  if (new RegExp(`\\b${needle}\\b`).test(haystack)) return true;
  // Falls back to a prefix/suffix check (still anchored, unlike plain .includes())
  // so a needle that's whole-word everywhere except at one edge - e.g. "believin"
  // vs "believing" (contraction expansion only fires when the source title has
  // the apostrophe Plex's copy dropped) or "ac" vs "acdc" (the multi-artist
  // splitter treats "AC/DC"'s slash as a separator) - still matches.
  return haystack.startsWith(needle) || haystack.endsWith(needle);
}

function titlesMatch(sourceTitle: string, plexTitle: string): boolean {
  const cleanSource = normalizeForComparison(cleanTrackTitle(sourceTitle));
  const cleanPlex = normalizeForComparison(cleanTrackTitle(plexTitle));
  if (cleanSource === cleanPlex) return true;
  if (cleanSource.replace(/\s+/g, '') === cleanPlex.replace(/\s+/g, '')) return true;
  if (containsWholeWord(cleanSource, cleanPlex) || containsWholeWord(cleanPlex, cleanSource)) return true;
  return false;
}

function artistsMatch(sourceArtist: string, plexArtist: string): boolean {
  const cleanSource = normalizeForComparison(cleanArtistName(sourceArtist));
  const cleanPlex = normalizeForComparison(cleanArtistName(plexArtist));
  if (cleanSource === cleanPlex) return true;
  if (containsWholeWord(cleanSource, cleanPlex) || containsWholeWord(cleanPlex, cleanSource)) return true;
  return false;
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

function calculateMatchScore(sourceTitle: string, sourceArtist: string, plexTitle: string, plexArtist: string): number {
  const cleanSourceTitle = normalizeForComparison(cleanTrackTitle(sourceTitle));
  const cleanPlexTitle = normalizeForComparison(cleanTrackTitle(plexTitle));
  const cleanSourceArtist = normalizeForComparison(cleanArtistName(sourceArtist));
  const cleanPlexArtist = normalizeForComparison(cleanArtistName(plexArtist));
  
  let titleScore = 0;
  if (cleanSourceTitle === cleanPlexTitle) {
    titleScore = 100;
  } else if (containsWholeWord(cleanSourceTitle, cleanPlexTitle) || containsWholeWord(cleanPlexTitle, cleanSourceTitle)) {
    titleScore = 90;
  } else {
    const sourceWords = cleanSourceTitle.split(/\s+/);
    const plexWords = cleanPlexTitle.split(/\s+/);
    const matches = sourceWords.filter(w => plexWords.some(pw => pw === w)).length;
    titleScore = Math.round((matches / Math.max(sourceWords.length, plexWords.length)) * 80);
  }

  let artistScore = 0;
  if (cleanSourceArtist === cleanPlexArtist) {
    artistScore = 100;
  } else if (containsWholeWord(cleanSourceArtist, cleanPlexArtist) || containsWholeWord(cleanPlexArtist, cleanSourceArtist)) {
    artistScore = 90;
  } else {
    artistScore = 70;
  }
  
  return Math.round(titleScore * 0.7 + artistScore * 0.3);
}
