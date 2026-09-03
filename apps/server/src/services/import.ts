/**
 * Import Service
 * 
 * Handles playlist import workflow with cache-first logic:
 * 1. Check cached_playlists table before scraping
 * 2. Use cached data if fresh (< 24 hours)
 * 3. Scrape if cache miss or stale
 * 4. Store scraped data in cache
 * 5. Match tracks using matching service
 * 6. Store unmatched tracks in missing_tracks table
 */

import { DatabaseService } from '../database';
import {
  scrapeDeezerPlaylist,
  scrapeSpotifyPlaylist,
  scrapeAppleMusicPlaylist,
  scrapeTidalPlaylist,
  scrapeYouTubeMusicPlaylist,
  scrapeAmazonMusicPlaylist,
  scrapeQobuzPlaylist,
  getListenBrainzPlaylists,
  parseM3UFile,
  parseCSVFile,
  parsePLSFile,
  parseXSPFFile,
  scrapeAriaPlaylist,
  scrapeBillboardPlaylist,
  scrapeLastfmPlaylist,
  ExternalPlaylist,
} from './scrapers';
import { matchPlaylist, MatchedTrack, dedupeByPlexRatingKey, buildRememberedMatchMap, rememberedMatchKey, rememberMatches } from './matching';
import { logger } from '../utils/logger';
import { logImportDebug } from '../utils/import-debug-logger';
import { EventEmitter } from 'events';
import { debugLog } from '../utils/debug-logger';
import { addNotification, updateNotification } from './job-notifications';
import { externalLimiter } from './task-queues';

export interface ImportResult {
  playlistId: string;
  playlistName: string;
  source: string;
  matched: MatchedTrack[];
  unmatched: MatchedTrack[];
  matchedCount: number;
  totalCount: number;
  usedCache: boolean;
  coverUrl?: string;
}

export interface ImportOptions {
  userId: number;
  serverUrl: string;
  plexToken: string;
  libraryId?: string;
  customName?: string;
  filename?: string; // For file imports
}

/**
 * Import a playlist from an external source
 */
export async function importPlaylist(
  source: 'spotify' | 'deezer' | 'apple' | 'tidal' | 'youtube' | 'amazon' | 'qobuz' | 'listenbrainz' | 'file' | 'aria' | 'billboard' | 'lastfm',
  sourceIdentifier: string,
  options: ImportOptions,
  db: DatabaseService,
  progressEmitter?: EventEmitter,
  sessionId?: string,
  cancelledSessions?: Set<string>
): Promise<ImportResult> {
  const importId = `${source}-${Date.now()}`;
  try {
    debugLog(`[Import ${importId}] ========== IMPORT STARTED ==========`);
    debugLog(`[Import ${importId}] Source:`, source);
    debugLog(`[Import ${importId}] Identifier:`, sourceIdentifier);
    debugLog(`[Import ${importId}] Has progressEmitter:`, !!progressEmitter);
    logger.info(`[Import ${importId}] Starting import from ${source}: ${sourceIdentifier}`);
  
  // Step 1: Always scrape fresh (no cache-first)
  debugLog(`[Import ${importId}] Starting fresh scrape`);
  logger.info(`[Import ${importId}] Scraping ${source}: ${sourceIdentifier}`);
  
  // Check cache for fallback only (if scraping fails)
  const cached = db.getCachedPlaylist(source, sourceIdentifier);
  
  let externalPlaylist: ExternalPlaylist;
  let usedCache = false;
  let vanishedFromSource: ExternalPlaylist['tracks'] = [];
  
  // Emit initial scraping progress
  if (progressEmitter) {
    progressEmitter.emit('progress', {
      type: 'progress',
      phase: 'scraping',
      current: 0,
      total: 0,
      currentTrackName: 'Fetching playlist from ' + source + '...',
    });
  }
  
  debugLog('[Import] ========== STARTING SCRAPE ==========');
  
  try {
    externalPlaylist = await externalLimiter.run(() =>
      scrapePlaylist(source, sourceIdentifier, progressEmitter, options.userId, db, options)
    );
    debugLog('[Import] scrapePlaylist returned successfully');
    
    logImportDebug('=== SCRAPING COMPLETE ===', {
      playlistName: externalPlaylist.name,
      trackCount: externalPlaylist.tracks.length,
      coverUrl: externalPlaylist.coverUrl,
      hasCoverUrl: !!externalPlaylist.coverUrl
    });
    
    logger.info(`[Import] Scraping complete. Playlist: ${externalPlaylist.name}, Tracks: ${externalPlaylist.tracks.length}, Cover: ${externalPlaylist.coverUrl || 'none'}`);
    
    // Emit progress with cover URL and playlist name after scraping completes
    if (progressEmitter) {
      const scrapingCompleteEvent = {
        type: 'progress',
        phase: 'scraping',
        current: externalPlaylist.tracks.length,
        total: externalPlaylist.tracks.length,
        currentTrackName: `Found ${externalPlaylist.tracks.length} tracks`,
        coverUrl: externalPlaylist.coverUrl,
        playlistName: externalPlaylist.name
      };
      
      progressEmitter.emit('progress', scrapingCompleteEvent);
      
      // Brief wait to ensure frontend receives update before matching starts
      await new Promise(resolve => setTimeout(resolve, 500));
      logger.info(`[Import] Starting matching phase...`);
    }
    
    // A previous cache row means this is a reimport/refresh, not a first-time
    // import - diff its tracks against the fresh scrape to find any that
    // dropped off the source playlist since last time, before the cache gets
    // overwritten below and that comparison is lost.
    if (cached) {
      const freshKeys = new Set(externalPlaylist.tracks.map(t => rememberedMatchKey(t.title, t.artist, t.album)));
      vanishedFromSource = cached.tracks.filter(t => !freshKeys.has(rememberedMatchKey(t.title, t.artist, t.album)));
    }

    // Store scraped data in cache (for fallback if future scrapes fail)
    try {
      db.saveCachedPlaylist(
        source,
        sourceIdentifier,
        externalPlaylist.name,
        externalPlaylist.description,
        externalPlaylist.tracks,
        externalPlaylist.coverUrl
      );
      logger.info(`[Import] Cached ${externalPlaylist.tracks.length} tracks for ${source}:${sourceIdentifier}`);
    } catch (cacheError: any) {
      logger.error(`[Import] Failed to save to cache`, { error: cacheError.message });
    }
  } catch (error: any) {
    debugLog('[Import] Scraping error:', error.message);
    
    // If scraping fails and we have stale cache, use it as fallback
    if (cached) {
      logger.warn(`[Import] Scraping failed, using stale cache for ${source}:${sourceIdentifier}`, { 
        error: error.message,
      });
      externalPlaylist = {
        id: cached.source_id,
        name: cached.name,
        description: cached.description || '',
        source: cached.source,
        tracks: cached.tracks,
      };
      usedCache = true;
    } else {
      logger.error(`[Import] Scraping failed and no cache available for ${source}:${sourceIdentifier}`, { 
        error: error.message,
      });
      throw error;
    }
  }
  
  // Step 5: Match tracks using matching service
  debugLog('[Import] ========== EXITED SCRAPING BLOCK ==========');
  debugLog('[Import] About to start matching phase');
  debugLog('[Import] ===============================================');
  
  debugLog('[Import] ========== STARTING MATCHING PHASE ==========');
  debugLog('[Import] Playlist name:', externalPlaylist.name);
  debugLog('[Import] Track count:', externalPlaylist.tracks.length);
  debugLog('[Import] Cover URL:', externalPlaylist.coverUrl || 'NONE');
  debugLog('[Import] ================================================');
  
  logger.info(`[Import] Matching ${externalPlaylist.tracks.length} tracks for user ${options.userId}`);
  
  try {
    debugLog('[Import] Getting user settings...');
    const settings = db.getUserSettings(options.userId);
    debugLog('[Import] User settings retrieved');
    logger.info(`[Import] Got user settings`, { 
      hasSettings: !!settings,
      hasMatchingSettings: !!settings.matching_settings,
      minMatchScore: settings.matching_settings?.minMatchScore 
    });
    
    debugLog('[Import] Calling matchPlaylist...');
    
    // Emit matching phase start event
    if (progressEmitter) {
      progressEmitter.emit('progress', {
        type: 'progress',
        phase: 'matching',
        current: 0,
        total: externalPlaylist.tracks.length,
        currentTrackName: 'Matching tracks with your Plex library...',
        coverUrl: externalPlaylist.coverUrl,
        playlistName: externalPlaylist.name
      });
    }
    
    const rememberedMatches = buildRememberedMatchMap(db.getUserManualMatches(options.userId));
    const vanishedFromPlex: typeof externalPlaylist.tracks = [];

    const matchedTracks = await matchPlaylist(
      externalPlaylist.tracks,
      options.serverUrl,
      options.plexToken,
      options.libraryId,
      settings.matching_settings,
      progressEmitter,
      externalPlaylist.coverUrl,
      externalPlaylist.name,
      () => cancelledSessions?.has(sessionId || '') ?? false,
      rememberedMatches,
      vanishedFromPlex
    );

    // Remember what this run resolved, so the next refresh of this playlist
    // reuses the same decisions instead of re-deriving them.
    rememberMatches(db, options.userId, matchedTracks);

    const matched = matchedTracks.filter((t: MatchedTrack) => t.matched);
    const unmatched = matchedTracks.filter((t: MatchedTrack) => !t.matched);

    logger.info(`[Import] Matched ${matched.length}/${matchedTracks.length} tracks`);

    // Use custom name if provided, otherwise use the scraped name
    const finalPlaylistName = options.customName || externalPlaylist.name;

    // Surface both "vanished" cases as one notification per import rather
    // than spamming per-track - this is informational (not a job), so it's
    // posted straight in 'success' status.
    if (vanishedFromSource.length > 0 || vanishedFromPlex.length > 0) {
      const details: string[] = [];
      if (vanishedFromSource.length > 0) {
        details.push(`${vanishedFromSource.length} track(s) no longer in the source playlist`);
      }
      if (vanishedFromPlex.length > 0) {
        details.push(`${vanishedFromPlex.length} previously-matched track(s) no longer found in your Plex library`);
      }
      addNotification(options.userId, {
        type: 'track-vanished',
        title: finalPlaylistName,
        detail: details.join('; '),
        status: 'success',
      });
    }

    return {
      playlistId: externalPlaylist.id,
      playlistName: finalPlaylistName,
      source: externalPlaylist.source,
      matched,
      unmatched,
      matchedCount: matched.length,
      totalCount: matchedTracks.length,
      usedCache,
      coverUrl: externalPlaylist.coverUrl,
    };
  } catch (error: any) {
    logger.error(`[Import] Error during matching`, { 
      error: error.message,
      stack: error.stack,
      trackCount: externalPlaylist.tracks.length 
    });
    throw error;
  }
  } catch (error: any) {
    debugLog('[Import] ========== FATAL ERROR ==========');
    debugLog('[Import] Error:', error.message);
    debugLog('[Import] Stack:', error.stack);
    debugLog('[Import] =====================================');
    logger.error(`[Import] Fatal error in importPlaylist`, { 
      error: error.message,
      stack: error.stack,
      source,
      sourceIdentifier
    });
    throw error;
  }
}

/**
 * Re-scrape and re-match a playlist that was originally imported from an
 * online source, then replace its Plex playlist with the refreshed tracks.
 * Used by the manual "Reimport" row action (as opposed to the scheduled
 * refresh job in schedule-checker-job.ts, which is driven by a schedule row).
 */
export async function reimportPlaylistNow(
  db: DatabaseService,
  playlist: { id: number; name: string; source: string; source_url?: string | null; plex_playlist_id: string },
  user: { id: number; plex_token: string },
  server: { server_url: string; server_client_id?: string; library_id?: string | null; access_token?: string | null }
): Promise<void> {
  const { PlexClient, resolvePlexToken } = await import('./plex');

  // Fire-and-forget from the caller (the "Reimport" row button) - without
  // this, the only feedback was a blind timeout-then-refresh on the
  // frontend. Mirrors the notification schedule-checker-job.ts's
  // runSingleSchedule() creates for a manual schedule run.
  const notification = addNotification(user.id, {
    type: 'schedule',
    title: playlist.name,
    detail: 'Starting...',
    status: 'in-progress',
    progress: 0,
  });

  try {
    const progressEmitter = new EventEmitter();
    progressEmitter.on('progress', (data: { phase?: string; current?: number; total?: number; currentTrackName?: string }) => {
      const detail = data.phase === 'matching' ? 'Matching tracks with your Plex library...' : (data.currentTrackName || 'Fetching tracks...');
      const progress = data.total ? Math.round(((data.current || 0) / data.total) * 100) : undefined;
      updateNotification(user.id, notification.id, { detail, progress });
    });

    const result = await importPlaylist(
      playlist.source as any,
      playlist.source_url || playlist.plex_playlist_id,
      {
        userId: user.id,
        serverUrl: server.server_url,
        plexToken: resolvePlexToken(user, server),
        libraryId: server.library_id || undefined,
      },
      db,
      progressEmitter
    );

    const plex = new PlexClient(server.server_url, resolvePlexToken(user, server));

    try {
      const plexPlaylists = await plex.getPlaylists();
      const existing = plexPlaylists.find((p: any) => p.ratingKey === playlist.plex_playlist_id || p.title === playlist.name);
      if (existing) {
        await plex.deletePlaylist(existing.ratingKey);
      }
    } catch (error: any) {
      logger.warn('Failed to check for existing playlist before reimport', { playlistId: playlist.id, error: error.message });
    }

    const trackUris = dedupeByPlexRatingKey(result.matched.filter((t: any) => t.matched && t.plexRatingKey))
      .map((t: any) => `server://${server.server_client_id || 'playlist-lab-server'}/com.plexapp.plugins.library/library/metadata/${t.plexRatingKey}`);

    const newPlaylist = await plex.createPlaylist(playlist.name, server.library_id || '', trackUris);

    if (result.coverUrl) {
      try {
        await plex.uploadPlaylistPoster(newPlaylist.ratingKey, result.coverUrl);
      } catch (error: any) {
        logger.warn('Failed to upload cover art during reimport', { playlistId: playlist.id, error: error.message });
      }
    }

    db.updatePlaylist(playlist.id, {
      plex_playlist_id: newPlaylist.ratingKey,
      updated_at: Math.floor(Date.now() / 1000),
    } as any);

    if (result.unmatched.length > 0) {
      const missingSource = `Manual reimport – ${new Date().toLocaleDateString('en-GB')}`;
      db.addMissingTracks(user.id, playlist.id, result.unmatched.map((t, i) => ({
        title: t.title || 'Unknown',
        artist: t.artist || 'Unknown',
        album: t.album,
        position: i + 1,
        source: missingSource,
      })));
    }

    updateNotification(user.id, notification.id, {
      status: 'success',
      progress: 100,
      detail: `Matched ${result.matchedCount} of ${result.totalCount}`,
    });
    logger.info('Manual reimport completed', { playlistId: playlist.id, matched: result.matchedCount, unmatched: result.unmatched.length });
  } catch (error: any) {
    updateNotification(user.id, notification.id, { status: 'error', detail: error.message || 'Reimport failed' });
    throw error;
  }
}

export interface FinalizeImportOpts {
  playlistName?: string;
  overwriteExisting?: boolean;
  keepExistingCover?: boolean;
}

/**
 * Creates the Plex playlist from a completed scrape+match's matched tracks,
 * saves the playlist row (with the actual source/sourceIdentifier this
 * import used, so a later scheduled refresh has a real link to re-scrape -
 * see runNewImportAndFinalize's docs) and any unmatched tracks as missing
 * tracks. Split out from runNewImportAndFinalize() so a caller that already
 * has a scrape+match result in hand (the import-queue job handler in
 * index.ts, which ran importPlaylist() itself for its own SSE progress
 * wiring) doesn't have to scrape a second time just to finalize it.
 */
export async function finalizeImportResult(
  db: DatabaseService,
  source: string,
  sourceIdentifier: string,
  user: { id: number; plex_token: string },
  server: { server_url: string; server_client_id?: string; library_id?: string | null; access_token?: string | null },
  result: { matched: MatchedTrack[]; unmatched: MatchedTrack[]; coverUrl?: string; playlistName: string; matchedCount: number; totalCount: number },
  opts: FinalizeImportOpts = {}
): Promise<{ dbPlaylistId: number; plexRatingKey: string }> {
  const { PlexClient, resolvePlexToken } = await import('./plex');
  const playlistName = opts.playlistName || result.playlistName;
  const plex = new PlexClient(server.server_url, resolvePlexToken(user, server));

  let existingCoverUrl: string | undefined;
  if (opts.overwriteExisting) {
    try {
      const normalize = (s: string) => s.trim().replace(/\s+/g, ' ').toLowerCase();
      const normalizedTarget = normalize(playlistName);
      const plexPlaylists = await plex.getPlaylists();
      const matches = plexPlaylists.filter((p: any) => p.playlistType === 'audio' && normalize(p.title || '') === normalizedTarget);
      if (opts.keepExistingCover && matches[0]?.composite) {
        existingCoverUrl = `${server.server_url}${matches[0].composite}?X-Plex-Token=${resolvePlexToken(user, server)}`;
      }
      for (const existing of matches) {
        await plex.deletePlaylist(existing.ratingKey);
      }
      for (const existingDb of db.getUserPlaylists(user.id).filter((p: any) => normalize(p.name || '') === normalizedTarget)) {
        db.deletePlaylist(existingDb.id);
      }
    } catch (error: any) {
      logger.warn('[Import] Failed to overwrite existing playlist during finalize', { playlistName, error: error.message });
    }
  }

  const trackUris = dedupeByPlexRatingKey(result.matched.filter((t: any) => t.matched && t.plexRatingKey))
    .map((t: any) => `server://${server.server_client_id || 'playlist-lab-server'}/com.plexapp.plugins.library/library/metadata/${t.plexRatingKey}`);

  if (trackUris.length === 0) {
    throw new Error('No tracks matched in your Plex library - nothing to add to a playlist');
  }

  const newPlaylist = await plex.createPlaylist(playlistName, server.library_id || '', trackUris);

  const finalCoverUrl = result.coverUrl || existingCoverUrl;
  if (finalCoverUrl) {
    try {
      await plex.uploadPlaylistPoster(newPlaylist.ratingKey, finalCoverUrl);
    } catch (error: any) {
      logger.warn('[Import] Failed to upload cover art', { playlistName, error: error.message });
    }
  }

  const dbPlaylist = db.createPlaylist(user.id, newPlaylist.ratingKey, playlistName, source, sourceIdentifier);

  if (result.unmatched.length > 0) {
    db.addMissingTracks(user.id, dbPlaylist.id, result.unmatched.map((t, i) => ({
      title: t.title || 'Unknown',
      artist: t.artist || 'Unknown',
      album: t.album,
      position: i,
      source,
    })));
  }

  return { dbPlaylistId: dbPlaylist.id, plexRatingKey: newPlaylist.ratingKey };
}

/**
 * Scrape+match a playlist and, once matching completes, immediately create
 * the Plex playlist and save it - no manual "review matches, then confirm"
 * step. Used for import triggers that don't already run their own
 * scrape+match (file upload) so an import is a fire-and-forget background
 * job tracked in the notification bell, the same way reimportPlaylistNow()
 * already works for the "Reimport" row action. The caller is expected NOT
 * to await this.
 */
export async function runNewImportAndFinalize(
  db: DatabaseService,
  source: 'spotify' | 'deezer' | 'apple' | 'tidal' | 'youtube' | 'amazon' | 'qobuz' | 'listenbrainz' | 'file' | 'aria' | 'billboard' | 'lastfm',
  sourceIdentifier: string,
  user: { id: number; plex_token: string },
  server: { server_url: string; server_client_id?: string; library_id?: string | null; access_token?: string | null },
  opts: FinalizeImportOpts & { notificationTitle?: string; filename?: string } = {}
): Promise<void> {
  const notification = addNotification(user.id, {
    type: 'import',
    title: opts.notificationTitle || opts.playlistName || `${source} import`,
    detail: 'Starting...',
    status: 'in-progress',
    progress: 0,
  });

  try {
    const { resolvePlexToken } = await import('./plex');
    const progressEmitter = new EventEmitter();
    progressEmitter.on('progress', (data: { phase?: string; current?: number; total?: number; currentTrackName?: string; playlistName?: string }) => {
      if (data.playlistName && notification.title !== data.playlistName) {
        updateNotification(user.id, notification.id, { title: data.playlistName });
      }
      const detail = data.phase === 'matching' ? 'Matching tracks with your Plex library...' : (data.currentTrackName || 'Fetching tracks...');
      const progress = data.total ? Math.round(((data.current || 0) / data.total) * 100) : undefined;
      updateNotification(user.id, notification.id, { detail, progress });
    });

    const result = await importPlaylist(
      source,
      sourceIdentifier,
      {
        userId: user.id,
        serverUrl: server.server_url,
        plexToken: resolvePlexToken(user, server),
        libraryId: server.library_id || undefined,
        filename: opts.filename,
      },
      db,
      progressEmitter
    );

    await finalizeImportResult(db, source, sourceIdentifier, user, server, result, opts);

    updateNotification(user.id, notification.id, {
      title: opts.playlistName || result.playlistName,
      status: 'success',
      progress: 100,
      detail: `Matched ${result.matchedCount} of ${result.totalCount}`,
    });
    logger.info('Import completed', { userId: user.id, source, playlistName: result.playlistName, matched: result.matchedCount, unmatched: result.unmatched.length });
  } catch (error: any) {
    updateNotification(user.id, notification.id, { status: 'error', detail: error.message || 'Import failed' });
    logger.error('[Import] Fatal error in runNewImportAndFinalize', { error: error.message, stack: error.stack, source, sourceIdentifier, userId: user.id });
  }
}

/**
 * Store unmatched tracks in the missing_tracks table
 */
export function storeMissingTracks(
  userId: number,
  playlistId: number,
  tracks: MatchedTrack[],
  source: string,
  db: DatabaseService
): void {
  const unmatchedTracks = tracks
    .map((track, index) => ({
      track,
      index,
      // Find the last matched track before this one to get the after_track_key
      afterTrackKey: tracks
        .slice(0, index)
        .reverse()
        .find(t => t.matched)?.plexRatingKey,
    }))
    .filter(({ track }) => !track.matched)
    .map(({ track, index, afterTrackKey }) => ({
      title: track.title,
      artist: track.artist,
      album: track.album,
      position: index,
      after_track_key: afterTrackKey,
      source,
    }));
  
  if (unmatchedTracks.length > 0) {
    db.addMissingTracks(userId, playlistId, unmatchedTracks);
    logger.info(`[Import] Stored ${unmatchedTracks.length} missing tracks for playlist ${playlistId}`);
  }
}

/**
 * Scrape a playlist from an external source
 */
async function scrapePlaylist(
  source: 'spotify' | 'deezer' | 'apple' | 'tidal' | 'youtube' | 'amazon' | 'qobuz' | 'listenbrainz' | 'file' | 'aria' | 'billboard' | 'lastfm',
  sourceIdentifier: string,
  progressEmitter?: EventEmitter,
  userId?: number,
  db?: DatabaseService,
  options?: ImportOptions
): Promise<ExternalPlaylist> {
  debugLog('[scrapePlaylist] ========== FUNCTION CALLED ==========');
  debugLog('[scrapePlaylist] Source:', source);
  debugLog('[scrapePlaylist] Identifier:', sourceIdentifier);
  debugLog('[scrapePlaylist] =====================================');
  
  try {
    switch (source) {
      case 'spotify':
        debugLog('[scrapePlaylist] Calling scrapeSpotifyPlaylist...');
        // Pass userId and db for authenticated API access
        return await scrapeSpotifyPlaylist(sourceIdentifier, progressEmitter, userId, (db as any)?.db);
      
      case 'deezer':
        debugLog('[scrapePlaylist] Calling scrapeDeezerPlaylist...');
        // Extract playlist ID from URL if needed
        let deezerId = sourceIdentifier;
        if (sourceIdentifier.includes('deezer.com')) {
          const match = sourceIdentifier.match(/\/playlist\/(\d+)/);
          deezerId = match ? match[1] : sourceIdentifier;
        }
        debugLog('[scrapePlaylist] Deezer ID extraction', { originalUrl: sourceIdentifier, extractedId: deezerId });
        return await scrapeDeezerPlaylist(deezerId, progressEmitter);
      
      case 'apple':
        debugLog('[scrapePlaylist] Calling scrapeAppleMusicPlaylist...');
        const result = await scrapeAppleMusicPlaylist(sourceIdentifier, progressEmitter);
        debugLog('[scrapePlaylist] scrapeAppleMusicPlaylist returned:', JSON.stringify({ name: result.name, trackCount: result.tracks.length, coverUrl: result.coverUrl }));
        return result;
      
      case 'tidal':
        debugLog('[scrapePlaylist] Calling scrapeTidalPlaylist...');
        return await scrapeTidalPlaylist(sourceIdentifier, progressEmitter);
      
      case 'youtube':
        debugLog('[scrapePlaylist] Calling scrapeYouTubeMusicPlaylist...');
        return await scrapeYouTubeMusicPlaylist(sourceIdentifier, progressEmitter);
      
      case 'amazon':
        debugLog('[scrapePlaylist] Calling scrapeAmazonMusicPlaylist...');
        return await scrapeAmazonMusicPlaylist(sourceIdentifier, progressEmitter);
      
      case 'qobuz':
        debugLog('[scrapePlaylist] Calling scrapeQobuzPlaylist...');
        return await scrapeQobuzPlaylist(sourceIdentifier, progressEmitter);
      
      case 'aria':
        debugLog('[scrapePlaylist] Calling scrapeAriaPlaylist...');
        return await scrapeAriaPlaylist(sourceIdentifier, progressEmitter);
      
      case 'billboard':
        debugLog('[scrapePlaylist] Calling scrapeBillboardPlaylist...');
        return await scrapeBillboardPlaylist(sourceIdentifier, progressEmitter);
      
      case 'lastfm':
        debugLog('[scrapePlaylist] Calling scrapeLastfmPlaylist...');
        return await scrapeLastfmPlaylist(sourceIdentifier, progressEmitter);
      
      case 'listenbrainz':
        debugLog('[scrapePlaylist] Calling getListenBrainzPlaylists...');
        // For ListenBrainz, sourceIdentifier is username
        const playlists = await getListenBrainzPlaylists(sourceIdentifier);
        if (playlists.length === 0) {
          throw new Error(`No playlists found for ListenBrainz user: ${sourceIdentifier}`);
        }
        // Return the first playlist (or implement selection logic)
        return playlists[0];
      
      case 'file': {
        // For file imports, sourceIdentifier is the file content. Dispatch on
        // extension so the same set of formats this app can export to
        // (M3U/M3U8, PLS, XSPF, CSV) can also be imported back in.
        const fileName = options?.filename || 'imported-playlist.m3u';
        const ext = fileName.toLowerCase().substring(fileName.lastIndexOf('.'));
        debugLog('[scrapePlaylist] Filename:', { fileName, ext });
        switch (ext) {
          case '.csv':
            debugLog('[scrapePlaylist] Calling parseCSVFile...');
            return parseCSVFile(sourceIdentifier, fileName);
          case '.pls':
            debugLog('[scrapePlaylist] Calling parsePLSFile...');
            return parsePLSFile(sourceIdentifier, fileName);
          case '.xspf':
            debugLog('[scrapePlaylist] Calling parseXSPFFile...');
            return parseXSPFFile(sourceIdentifier, fileName);
          default:
            debugLog('[scrapePlaylist] Calling parseM3UFile...');
            return parseM3UFile(sourceIdentifier, fileName);
        }
      }
      
      default:
        throw new Error(`Unsupported source: ${source}`);
    }
  } catch (error: any) {
    debugLog('[scrapePlaylist] ========== ERROR ==========');
    debugLog('[scrapePlaylist] Error:', error.message);
    debugLog('[scrapePlaylist] Stack:', error.stack);
    debugLog('[scrapePlaylist] ===========================');
    throw error;
  }
}

