import { Router, Request, Response, NextFunction } from 'express';
import { EventEmitter } from 'events';
import { requireAuth } from '../middleware/auth';
import { createValidationError, createInternalError, createNotFoundError, createForbiddenError } from '../middleware/error-handler';
import { logger } from '../utils/logger';
import { PlexService, resolvePlexToken } from '../services/plex';
import { matchPlaylist, dedupeByPlexRatingKey, buildRememberedMatchMap, insertMatchedTrackIntoPlaylist, rememberMatches } from '../services/matching';
import { findBestDeemixMatches, queueDeemixDownload, resolveDownloadUrl, startDeemixDownload } from '../services/deemix';
import { findLidarrArtist, addAndMonitorArtist, findAlbumForArtist, triggerLidarrSearch, trackLidarrSearch } from '../services/lidarr';
import { addNotification, updateNotification } from '../services/job-notifications';
import { enqueueAction } from '../services/action-queue';
import type { DatabaseService } from '../database/database';
import type { MissingTrack } from '../database/types';

const router = Router();

// A full retry batch does a real Plex search (up to 4 HTTP round trips) per
// track, so retrying a large missing-tracks list (100+ tracks across many
// playlists) can take well over a minute. Awaiting that synchronously in the
// request handler held the connection open long enough to trip client/proxy
// timeouts, which surfaced to users as "An unknown error occurred" even
// though the retry was actually still working server-side. Track one
// in-flight retry per user so it can run in the background instead - the
// client polls GET /api/missing to watch the list shrink as tracks resolve,
// and GET /api/missing/retry-status for live current/total progress (shown
// as a progress pill in the header).
// `error` is set (and the entry left in place, not deleted) when a batch
// throws - e.g. a revoked Plex token or an unreachable server - so a client
// still polling retry-status finds out the batch actually failed instead of
// just seeing it vanish and assuming everything genuinely stayed missing.
// A user's next POST /retry treats an errored entry as idle and clears it.
interface RetryProgress { current: number; total: number; error?: string }
const activeRetries = new Map<number, RetryProgress>();

// A retry requested while one is already running for that user doesn't get
// rejected - its tracks are merged into a pending batch (deduped by track
// id, so repeated "Retry All" clicks don't pile up duplicate work) that
// starts automatically as soon as the current one finishes.
const pendingRetries = new Map<number, Map<number, MissingTrack>>();

/**
 * GET /api/missing
 * Get user's missing tracks grouped by playlist
 */
router.get('/', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    
    const missingTracks = db.getUserMissingTracks(userId);
    
    // Group by playlist
    const groupedByPlaylist: { [playlistId: number]: any } = {};
    
    for (const track of missingTracks) {
      if (!groupedByPlaylist[track.playlist_id]) {
        const playlist = db.getPlaylistById(track.playlist_id);
        groupedByPlaylist[track.playlist_id] = {
          playlistId: track.playlist_id,
          playlistName: playlist?.name || 'Unknown',
          source: track.source,
          tracks: []
        };
      }
      groupedByPlaylist[track.playlist_id].tracks.push({
        id: track.id,
        title: track.title,
        artist: track.artist,
        album: track.album || '',
        position: track.position,
        source: track.source,
        addedAt: track.added_at * 1000, // Convert Unix seconds to JS milliseconds
      });
    }
    
    const grouped = Object.values(groupedByPlaylist);
    
    res.json({ 
      missingTracks: grouped,
      totalCount: missingTracks.length
    });
  } catch (error) {
    logger.error('Failed to get missing tracks', { error, userId: req.session.userId });
    next(createInternalError('Failed to retrieve missing tracks'));
  }
});

/**
 * POST /api/missing/retry
 * Retry matching missing tracks
 */
router.post('/retry', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { playlistId, trackIds } = req.body;

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    // Get missing tracks to retry
    let tracksToRetry;
    if (playlistId) {
      // Retry all tracks for a specific playlist
      const allMissing = db.getUserMissingTracks(userId);
      tracksToRetry = allMissing.filter(t => t.playlist_id === playlistId);
    } else if (trackIds && Array.isArray(trackIds)) {
      // Retry specific tracks
      const allMissing = db.getUserMissingTracks(userId);
      tracksToRetry = allMissing.filter(t => trackIds.includes(t.id));
    } else {
      // Retry all missing tracks
      tracksToRetry = db.getUserMissingTracks(userId);
    }

    if (tracksToRetry.length === 0) {
      return res.json({
        started: false,
        totalTracks: 0,
        message: 'No missing tracks to retry'
      });
    }

    // Get user settings
    const settings = db.getUserSettings(userId);

    // An entry with `error` set is a finished (failed) batch nobody's polled
    // away yet, not a live one - treat it as idle so this request starts a
    // fresh attempt instead of queuing behind a batch that already ended.
    const existing = activeRetries.get(userId);
    if (existing?.error) activeRetries.delete(userId);

    if (activeRetries.has(userId)) {
      let queue = pendingRetries.get(userId);
      if (!queue) {
        queue = new Map<number, MissingTrack>();
        pendingRetries.set(userId, queue);
      }
      for (const track of tracksToRetry) queue.set(track.id, track);

      return res.json({
        started: true,
        queued: true,
        totalTracks: tracksToRetry.length,
        message: `A retry is already running - ${queue.size} track(s) are queued to run next as soon as it finishes.`
      });
    }

    res.json({
      started: true,
      totalTracks: tracksToRetry.length,
      message: `Retrying ${tracksToRetry.length} track(s) - this list will update as they resolve.`
    });

    startRetryChain(userId, db, user, userServer, tracksToRetry, settings);
  } catch (error) {
    logger.error('Failed to retry missing tracks', { error, userId: req.session.userId });
    next(createInternalError('Failed to retry matching missing tracks'));
  }
});

/**
 * GET /api/missing/retry-status
 * Current retry progress for the header's progress indicator, null when idle.
 */
router.get('/retry-status', requireAuth, (req: Request, res: Response) => {
  const userId = req.session.userId!;
  const progress = activeRetries.get(userId);
  res.json({ active: progress ? { ...progress } : null });
});

/**
 * Names a retry batch after the playlist it's actually matching, so the
 * notification says "Matching: Discover Weekly" rather than a generic
 * "Matching missing tracks" that's indistinguishable from every other
 * batch when several run in sequence. A batch usually covers one playlist
 * (the per-playlist "Retry All" button), but "Retry All" from the whole
 * missing list spans several - which is named by count instead, since
 * listing every playlist would overflow the notification's title line.
 */
function retryBatchTitle(db: DatabaseService, tracks: MissingTrack[]): string {
  const playlistIds = [...new Set(tracks.map(t => t.playlist_id))];
  if (playlistIds.length > 1) return `Matching ${playlistIds.length} playlists`;
  const name = playlistIds.length ? db.getPlaylistById(playlistIds[0])?.name : null;
  return name ? `Matching: ${name}` : 'Matching missing tracks';
}

/**
 * Runs one retry batch, then - if more tracks were queued while it ran -
 * immediately runs another batch for those, re-reading them fresh from the
 * DB first since some may have resolved via other means (e.g. a manual
 * rematch) while this chain was running. Loops instead of recursing so a
 * single try/catch wraps the whole chain and guarantees a thrown batch
 * (e.g. a revoked Plex token or an unreachable server) can't leave a stuck
 * activeRetries entry that blocks every future retry for this user until
 * the server restarts. On failure the entry is left in place with `error`
 * set (not deleted) so a client polling retry-status sees the failure
 * instead of the batch just vanishing.
 */
async function startRetryChain(
  userId: number,
  db: DatabaseService,
  user: { plex_token: string },
  userServer: { server_url: string; library_id?: string | null; server_client_id?: string | null; access_token?: string | null },
  tracksToRetry: MissingTrack[],
  settings: { matching_settings: any }
): Promise<void> {
  activeRetries.set(userId, { current: 0, total: tracksToRetry.length });
  const notification = addNotification(userId, {
    type: 'retry-match',
    title: retryBatchTitle(db, tracksToRetry),
    detail: `0 of ${tracksToRetry.length}`,
    status: 'in-progress',
    progress: 0,
  });

  try {
    let batch = tracksToRetry;
    let totalAttempted = 0;
    let totalMatched = 0;
    while (batch.length > 0) {
      totalAttempted += batch.length;
      totalMatched += await runRetryInBackground(userId, db, user, userServer, batch, settings, notification.id);

      const queue = pendingRetries.get(userId);
      pendingRetries.delete(userId);
      if (!queue || queue.size === 0) break;

      // Re-check against the current missing list so tracks already
      // resolved (by this run or anything else) don't get retried again.
      const stillMissingIds = new Set(db.getUserMissingTracks(userId).map(t => t.id));
      batch = Array.from(queue.values()).filter(t => stillMissingIds.has(t.id));
      if (batch.length > 0) {
        activeRetries.set(userId, { current: 0, total: batch.length });
        // The queued follow-up batch is usually a different playlist than
        // the one this notification was named after - rename it so the
        // title keeps identifying what's actually being matched right now.
        updateNotification(userId, notification.id, { title: retryBatchTitle(db, batch) });
      }
    }
    activeRetries.delete(userId);
    updateNotification(userId, notification.id, {
      status: 'success',
      progress: 100,
      detail: `Matched ${totalMatched} of ${totalAttempted}`,
    });
  } catch (error: any) {
    logger.error('Missing-track retry chain failed', { error: error.message, userId });
    const progress = activeRetries.get(userId);
    activeRetries.set(userId, {
      current: progress?.current ?? 0,
      total: progress?.total ?? tracksToRetry.length,
      error: error.message || 'Retry failed',
    });
    pendingRetries.delete(userId);
    updateNotification(userId, notification.id, { status: 'error', detail: error.message || 'Retry failed' });
  }
}

/**
 * Matches and re-adds a batch of missing tracks. Runs detached from the
 * request/response cycle (see the /retry handler above) so large batches
 * don't hold an HTTP connection open long enough to hit client/proxy
 * timeouts; progress is instead observable via GET /api/missing as each
 * track resolves and is removed from the missing list.
 */
async function runRetryInBackground(
  userId: number,
  db: DatabaseService,
  user: { plex_token: string },
  userServer: { server_url: string; library_id?: string | null; server_client_id?: string | null; access_token?: string | null },
  tracksToRetry: MissingTrack[],
  settings: { matching_settings: any },
  notificationId: string
): Promise<number> {
  try {
    // Convert missing tracks to external track format
    const externalTracks = tracksToRetry.map(t => ({
      title: t.title,
      artist: t.artist,
      album: t.album || ''
    }));

    // matchPlaylist() emits a 'progress' event after every batch of 5 tracks
    // it searches - forward those into activeRetries so GET /retry-status
    // (polled for the header's progress pill) reflects real progress instead
    // of just "running".
    const progressEmitter = new EventEmitter();
    progressEmitter.on('progress', (data: { current: number; total: number }) => {
      activeRetries.set(userId, { current: data.current, total: data.total });
      updateNotification(userId, notificationId, {
        progress: Math.round((data.current / data.total) * 100),
        detail: `${data.current} of ${data.total}`,
      });
    });

    const rememberedMatches = buildRememberedMatchMap(db.getUserManualMatches(userId));

    // Attempt to match tracks
    const matchedTracks = await matchPlaylist(
      externalTracks,
      userServer.server_url,
      resolvePlexToken(user, userServer),
      userServer.library_id || '',
      settings.matching_settings,
      progressEmitter,
      undefined,
      undefined,
      undefined,
      rememberedMatches
    );

    rememberMatches(db, userId, matchedTracks);

    // Process matched tracks
    let matchedCount = 0;
    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));

    for (let i = 0; i < matchedTracks.length; i++) {
      const matched = matchedTracks[i];
      const original = tracksToRetry[i];

      if (matched.matched && matched.plexRatingKey) {
        const inserted = await insertMatchedTrackIntoPlaylist(db, plexService, userServer, original, matched.plexRatingKey);
        if (inserted) {
          matchedCount++;
          logger.info('Missing track matched and added', {
            userId,
            playlistId: original.playlist_id,
            trackId: original.id,
            title: original.title,
            artist: original.artist
          });
        }
      }
    }

    const stillMissing = tracksToRetry.length - matchedCount;
    logger.info('[Missing Retry] Background retry complete', {
      userId,
      totalTracks: tracksToRetry.length,
      matchedCount,
      stillMissing,
    });
    return matchedCount;
  } catch (error: any) {
    // Per-track failures above (e.g. one Plex add-to-playlist call failing)
    // are caught individually and don't reach here - this only catches a
    // failure that aborts the whole batch (matchPlaylist() throwing on a
    // revoked token or unreachable server). Rethrown so startRetryChain can
    // record it as this batch's error instead of it just going quiet.
    logger.error('[Missing Retry] Background retry failed', { error: error.message, userId });
    throw error;
  }
}

/**
 * POST /api/missing/add
 * Add tracks to missing tracks list
 */
router.post('/add', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { playlistId, tracks, source } = req.body;

    if (!playlistId || !tracks || !Array.isArray(tracks)) {
      return next(createValidationError('playlistId and tracks array are required'));
    }

    // Verify playlist ownership
    const playlist = db.getPlaylistById(playlistId);
    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    if (playlist.user_id !== userId) {
      return next(createForbiddenError('You do not have permission to modify this playlist'));
    }

    // Add tracks to missing tracks
    const missingTracksInput = tracks
      .filter(track => track.title && track.artist)
      .map((track, index) => ({
        title: track.title,
        artist: track.artist,
        album: track.album || '',
        position: track.position || index,
        source: source || 'manual'
      }));

    if (missingTracksInput.length > 0) {
      db.addMissingTracks(userId, playlistId, missingTracksInput);
    }

    const addedCount = missingTracksInput.length;

    logger.info('Missing tracks added', { userId, playlistId, count: addedCount });

    res.json({ 
      success: true,
      added: addedCount,
      message: `Added ${addedCount} track${addedCount !== 1 ? 's' : ''} to missing tracks`
    });
  } catch (error) {
    logger.error('Failed to add missing tracks', { error, userId: req.session.userId });
    next(createInternalError('Failed to add missing tracks'));
  }
});

/**
 * POST /api/missing/save
 * Save unmatched tracks to missing tracks list and create a Plex playlist with matched tracks
 */
router.post('/save', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { playlistName: rawPlaylistName, source, sourceUrl, tracks, matchedTracks, overwriteExisting, keepExistingCover, coverUrl } = req.body;

    if (!rawPlaylistName || typeof rawPlaylistName !== 'string') {
      return next(createValidationError('playlistName is required'));
    }

    const playlistName = rawPlaylistName.replace(/\s+/g, ' ').trim();

    if (!tracks || !Array.isArray(tracks) || tracks.length === 0) {
      return next(createValidationError('tracks array is required and must not be empty'));
    }

    // Get user's Plex token and server info (needed for overwrite and playlist creation)
    const userRow = (db as any).db.prepare('SELECT plex_token FROM users WHERE id = ?').get(userId);
    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, server_client_id, access_token FROM user_servers WHERE user_id = ?').get(userId);
    const hasPlexConfig = userRow?.plex_token && serverRow?.server_url && serverRow?.server_client_id;

    let existingCoverUrl: string | null = null;

    // Handle overwrite: delete existing playlist with same name
    if (overwriteExisting && hasPlexConfig) {
      try {
        const { PlexClient } = await import('../services/plex');
        const plexClient = new PlexClient(serverRow.server_url, resolvePlexToken(userRow, serverRow));
        const plexPlaylists = await plexClient.getPlaylists();
        const normalize = (s: string) => s.trim().replace(/\s+/g, ' ').toLowerCase();

        logger.info('Overwrite (missing save): searching for existing playlist', { playlistName, plexPlaylistCount: plexPlaylists.length });

        const matchingPlaylists = plexPlaylists.filter((p: any) =>
          normalize(p.title || '') === normalize(playlistName)
        );

        if (matchingPlaylists.length > 0) {
          const firstMatch = matchingPlaylists[0];
          if (keepExistingCover && firstMatch.composite) {
            existingCoverUrl = `${serverRow.server_url}${firstMatch.composite}?X-Plex-Token=${resolvePlexToken(userRow, serverRow)}`;
          }
          for (const existing of matchingPlaylists) {
            await plexClient.deletePlaylist(existing.ratingKey);
            logger.info('Deleted existing Plex playlist for overwrite', { playlistName, ratingKey: existing.ratingKey });
          }
        } else {
          logger.info('Overwrite (missing save): no existing Plex playlist found', { playlistName });
        }

        // Also delete from DB
        const userPlaylists = db.getUserPlaylists(userId);
        const matchingDbPlaylists = userPlaylists.filter((p: any) =>
          normalize(p.name || '') === normalize(playlistName)
        );
        for (const existingDb of matchingDbPlaylists) {
          db.deletePlaylist(existingDb.id);
          logger.info('Deleted existing DB playlist for overwrite', { playlistName, dbId: existingDb.id });
        }
      } catch (overwriteErr: any) {
        logger.error('Failed to delete existing playlist during overwrite (missing save)', { error: overwriteErr.message });
        return next(createInternalError(`Failed to overwrite existing playlist: ${overwriteErr.message}`));
      }
    }

    let plexPlaylistId = `pending-${Date.now()}`;

    // If matched tracks are provided, create a real Plex playlist
    if (matchedTracks && Array.isArray(matchedTracks) && matchedTracks.length > 0 && hasPlexConfig) {
      try {
        const { PlexClient } = await import('../services/plex');
        const plexClient = new PlexClient(serverRow.server_url, resolvePlexToken(userRow, serverRow));

        const trackUris = dedupeByPlexRatingKey(matchedTracks.filter((t: any) => t.plexRatingKey))
          .map((t: any) => `server://${serverRow.server_client_id}/com.plexapp.plugins.library/library/metadata/${t.plexRatingKey}`);

        if (trackUris.length > 0) {
          const libraryUri = `server://${serverRow.server_client_id}/com.plexapp.plugins.library/library/sections/${serverRow.library_id}`;
          const plexPlaylist = await plexClient.createPlaylist(playlistName, libraryUri, trackUris);
          plexPlaylistId = plexPlaylist.ratingKey;

          // Upload cover art
          const finalCoverUrl = (coverUrl && typeof coverUrl === 'string') ? coverUrl : existingCoverUrl;
          if (finalCoverUrl) {
            await plexClient.uploadPlaylistPoster(plexPlaylist.ratingKey, finalCoverUrl);
          }

          logger.info('Created Plex playlist for missing tracks save', {
            userId, playlistName, matchedCount: trackUris.length, plexPlaylistId
          });
        }
      } catch (plexError: any) {
        logger.error('Failed to create Plex playlist during missing tracks save', { error: plexError.message });
        // Continue without Plex playlist — tracks will still be saved as missing
      }
    }

    // Create DB playlist record
    const playlist = db.createPlaylist(
      userId,
      plexPlaylistId,
      playlistName,
      source || 'manual',
      sourceUrl || ''
    );

    // Add tracks as missing tracks
    const missingTracksInput = tracks
      .filter((t: any) => t.title && t.artist)
      .map((t: any, index: number) => ({
        title: t.title,
        artist: t.artist,
        album: t.album || '',
        position: index,
        source: source || 'manual',
      }));

    if (missingTracksInput.length > 0) {
      db.addMissingTracks(userId, playlist.id, missingTracksInput);
    }

    logger.info('Missing tracks saved', { userId, playlistId: playlist.id, count: missingTracksInput.length, hasPlexPlaylist: !plexPlaylistId.startsWith('pending-') });

    res.json({
      success: true,
      playlistId: playlist.id,
      added: missingTracksInput.length,
      message: `Saved ${missingTracksInput.length} missing track${missingTracksInput.length !== 1 ? 's' : ''}`,
    });
  } catch (error) {
    logger.error('Failed to save missing tracks', { error, userId: req.session.userId });
    next(createInternalError('Failed to save missing tracks'));
  }
});

/**
 * POST /api/missing/:id/rematch
 * Manually rematch a missing track to a specific Plex track
 */
router.post('/:id/rematch', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const trackId = parseInt((req.params as Record<string, string>).id, 10);
    const { ratingKey } = req.body;

    if (isNaN(trackId)) {
      return next(createValidationError('Invalid track ID'));
    }

    if (!ratingKey) {
      return next(createValidationError('ratingKey is required'));
    }

    // Verify ownership
    const allMissing = db.getUserMissingTracks(userId);
    const track = allMissing.find(t => t.id === trackId);

    if (!track) {
      return next(createNotFoundError('Missing track not found'));
    }

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected'));
    }

    // Get the playlist
    const playlist = db.getPlaylistById(track.playlist_id);
    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    const trackUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/metadata/${ratingKey}`;

    // If the playlist has a real Plex ID, add the track to it
    // Playlists created via /api/missing/save have placeholder IDs like "pending-..."
    if (playlist.plex_playlist_id && !playlist.plex_playlist_id.startsWith('pending-')) {
      try {
        await plexService.addToPlaylist(playlist.plex_playlist_id, [trackUri]);
      } catch (plexError: any) {
        // If the Plex playlist was deleted, try creating a new one
        if (plexError.message?.includes('not found') || plexError.message?.includes('404')) {
          logger.warn('Plex playlist not found, creating new one', {
            playlistId: playlist.id,
            plexPlaylistId: playlist.plex_playlist_id,
          });
          const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
          const newPlaylist = await plexService.createPlaylist(playlist.name, libraryUri, [trackUri]);
          db.updatePlaylist(playlist.id, { plex_playlist_id: newPlaylist.ratingKey });
        } else {
          throw plexError;
        }
      }
    } else if (playlist.plex_playlist_id?.startsWith('pending-')) {
      // Playlist has no real Plex ID yet — create one now with this first matched track
      try {
        const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
        const newPlaylist = await plexService.createPlaylist(playlist.name, libraryUri, [trackUri]);
        db.updatePlaylist(playlist.id, { plex_playlist_id: newPlaylist.ratingKey });
        logger.info('Created Plex playlist from pending during rematch', { playlistId: playlist.id, plexId: newPlaylist.ratingKey });
      } catch (createErr: any) {
        logger.error('Failed to create Plex playlist during rematch', { error: createErr.message });
      }
    }

    // Remember this choice so future matching for the same track (a re-import,
    // an auto-retry, another playlist with the same song) reuses it instead of
    // re-running the search that failed to find it automatically in the first place.
    db.recordManualMatch(userId, { title: track.title, artist: track.artist, album: track.album }, ratingKey);

    // Remove from missing tracks
    db.removeMissingTrack(trackId);

    logger.info('Missing track manually rematched', { userId, trackId, ratingKey });

    res.json({ success: true });
  } catch (error: any) {
    logger.error('Failed to rematch missing track', { 
      error: error?.message || error, 
      stack: error?.stack,
      userId: req.session.userId, 
      trackId: (req.params as Record<string, string>).id,
      ratingKey: req.body.ratingKey,
    });
    next(createInternalError(`Failed to rematch track: ${error?.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/missing/:id/replace-similar
 * Can't find this track anywhere? Seed off one of the artist's other tracks
 * already in the library and use Plex's own sonic-analysis "nearest" results
 * to pick a stand-in, instead of leaving the slot empty. Reuses the same
 * insert-and-clear-missing-row logic as /rematch and the retry batch.
 */
router.post('/:id/replace-similar', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const trackId = parseInt((req.params as Record<string, string>).id, 10);

    if (isNaN(trackId)) {
      return next(createValidationError('Invalid track ID'));
    }

    const allMissing = db.getUserMissingTracks(userId);
    const track = allMissing.find(t => t.id === trackId);
    if (!track) {
      return next(createNotFoundError('Missing track not found'));
    }

    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }
    const userServer = db.getUserServer(userId);
    if (!userServer || !userServer.library_id) {
      return next(createValidationError('No server selected'));
    }

    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));

    const artist = await plexService.searchArtist(userServer.library_id, track.artist);
    if (!artist?.ratingKey) {
      return next(createNotFoundError(`No artist matching "${track.artist}" found in your Plex library`));
    }

    const seedTracks = await plexService.getArtistPopularTracks(userServer.library_id, artist.ratingKey, 1);
    if (seedTracks.length === 0) {
      return next(createNotFoundError(`"${track.artist}" has no tracks in your Plex library to seed a similarity search from`));
    }

    const candidates = await plexService.getSonicallySimilarTracks(seedTracks[0].ratingKey, userServer.library_id, {
      maxDistance: 0.25,
      limit: 10,
    });

    const playlist = db.getPlaylistById(track.playlist_id);
    const existingKeys = new Set(
      playlist && !playlist.plex_playlist_id.startsWith('pending-')
        ? (await plexService.getPlaylistTracks(playlist.plex_playlist_id)).map((t: any) => t.ratingKey)
        : []
    );
    const replacement = candidates.find(c => c.ratingKey && !existingKeys.has(c.ratingKey));

    if (!replacement?.ratingKey) {
      return next(createNotFoundError('No sonically similar replacement found for this track'));
    }

    const inserted = await insertMatchedTrackIntoPlaylist(db, plexService, userServer, track, replacement.ratingKey);
    if (!inserted) {
      return next(createInternalError('Found a similar track but failed to add it to the playlist'));
    }

    logger.info('Replaced missing track with sonically similar match', {
      userId,
      trackId,
      original: `${track.artist} - ${track.title}`,
      replacement: `${(replacement as any).grandparentTitle || ''} - ${replacement.title}`,
    });

    res.json({ success: true, replacementTitle: replacement.title, replacementArtist: (replacement as any).grandparentTitle });
  } catch (error: any) {
    logger.error('Failed to replace missing track with similar match', {
      error: error?.message || error,
      stack: error?.stack,
      userId: req.session.userId,
      trackId: (req.params as Record<string, string>).id,
    });
    next(createInternalError(`Failed to find a similar replacement: ${error?.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/missing/:id/deemix-download
 * Search deemix for this track and queue the top match for download
 */
router.post('/:id/deemix-download', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  // Declared out here so the catch below can still name the track in its
  // message and log line - which is the whole point of naming it.
  let track: MissingTrack | undefined;
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const trackId = parseInt((req.params as Record<string, string>).id, 10);

    if (isNaN(trackId)) {
      return next(createValidationError('Invalid track ID'));
    }

    // Verify ownership
    const allMissing = db.getUserMissingTracks(userId);
    track = allMissing.find(t => t.id === trackId);
    if (!track) {
      return next(createNotFoundError('Missing track not found'));
    }

    const user = db.getUserById(userId);
    const userServer = db.getUserServer(userId);

    const settings = db.getUserSettings(userId);
    const result = await queueDeemixForMissingTrack(db, userId, user, userServer, track, settings.matching_settings);
    if (!result) {
      // Named, because this message is what ends up in the notification feed
      // and the error log - "No deemix match found for this track" on its own
      // says nothing about which track, and there are hundreds of them.
      return next(createNotFoundError(`No deemix match found for "${track.artist} - ${track.title}"`));
    }

    res.json({ success: true, matchedTitle: result.title, matchedArtist: result.artist, score: result.score });
  } catch (error: any) {
    logger.error('Failed to queue deemix download', {
      error: error?.message || error,
      stack: error?.stack,
      userId: req.session.userId,
      trackId: (req.params as Record<string, string>).id,
      track: track ? `${track.artist} - ${track.title}` : undefined,
    });
    next(createInternalError(`Failed to queue deemix download for "${track ? `${track.artist} - ${track.title}` : `track ${(req.params as Record<string, string>).id}`}": ${error?.message || 'Unknown error'}`));
  }
});

/**
 * Searches deemix for one missing track and queues its best match, if there
 * is one good enough. Shared by the single-track route above and the
 * "Deemix All" batch below so both apply the same match-score gate, the
 * same whole-album-vs-single-track decision, and the same reconciliation.
 *
 * Returns null when nothing in the search results clears the user's minimum
 * match score - a missing track is often missing because it isn't in
 * Deezer's catalogue either, and downloading the closest unrelated hit
 * pollutes the library permanently.
 */
async function queueDeemixForMissingTrack(
  db: DatabaseService,
  userId: number,
  user: { plex_token: string } | null,
  userServer: { server_url: string; library_id?: string | null; server_client_id?: string | null; access_token?: string | null } | null,
  track: MissingTrack,
  matchingSettings: any
): Promise<{ title: string; artist: string; score: number } | null> {
  const firstArtist = track.artist.split(/\s*[,&\/]\s*/)[0].trim();
  const [best] = await findBestDeemixMatches(track.title, firstArtist, matchingSettings);
  if (!best) return null;

  const { match, score } = best;
  // If this track is part of a real multi-track album/EP rather than a
  // standalone single, queue the whole album so the rest of the collection
  // downloads along with it instead of just this one track.
  const downloadUrl = await resolveDownloadUrl(match);
  const isFullAlbum = downloadUrl !== match.link;
  const queued = await queueDeemixDownload(downloadUrl, match.link);

  startDeemixDownload({
    db,
    userId,
    title: track.title,
    detail: `${match.artist.name}${isFullAlbum ? ' · full album' : ''} · ${Math.round(score)}% match`,
    uuid: queued.uuid,
    alreadyQueued: queued.alreadyQueued,
    // Once the download completes, auto-match and insert it back into the
    // playlist this track is missing from. Degrades to "no reconciliation"
    // only if user or server info is somehow missing (e.g. a stale session).
    reconcile: (user && userServer) ? {
      db,
      missingTrackId: track.id,
      serverUrl: userServer.server_url,
      plexToken: resolvePlexToken(user, userServer),
      libraryId: userServer.library_id,
      serverClientId: userServer.server_client_id,
    } : undefined,
  });

  logger.info('Queued deemix download for missing track', {
    userId,
    trackId: track.id,
    deemixTrackId: match.id,
    score,
    alreadyQueued: !!queued.alreadyQueued,
  });
  return { title: match.title, artist: match.artist.name, score };
}

/**
 * POST /api/missing/deemix-all
 * Retry matching against Plex, then queue every still-missing track (of one
 * playlist, or the whole library) for download.
 *
 * This loop used to run in the browser - one request per track, from two
 * separate copies of the same code - so navigating away partway through
 * silently abandoned the rest of the list, and a large library fired
 * hundreds of requests back to back. It now runs through the shared action
 * queue like every other long server-side job, and reports progress into
 * the notification the header already shows.
 */
router.post('/deemix-all', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId: number | undefined = req.body?.playlistId;

    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }
    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    const settings = db.getUserSettings(userId);
    const missingFor = () => {
      const all = db.getUserMissingTracks(userId);
      return playlistId ? all.filter(t => t.playlist_id === playlistId) : all;
    };
    const playlistName = playlistId ? db.getPlaylistById(playlistId)?.name : null;

    const { jobId, position } = enqueueAction(userId, playlistName ? `Deemix All: ${playlistName}` : 'Deemix All', async (notificationId) => {
      // Match against Plex first: a track can easily have been added to the
      // library since it was marked missing, and matching it there is
      // strictly better than downloading a redundant second copy.
      const toRetry = missingFor();
      if (toRetry.length > 0) {
        updateNotification(userId, notificationId, { detail: `Matching ${toRetry.length} track(s) against Plex first...` });
        await startRetryChain(userId, db, user, userServer, toRetry, settings);
      }

      const stillMissing = missingFor();
      if (stillMissing.length === 0) {
        updateNotification(userId, notificationId, { status: 'success', progress: 100, detail: 'Everything matched in Plex - nothing left to download' });
        return;
      }

      let queued = 0;
      let unmatched = 0;
      let failed = 0;
      for (const [index, track] of stillMissing.entries()) {
        updateNotification(userId, notificationId, {
          progress: Math.round((index / stillMissing.length) * 100),
          detail: `Searching deemix - ${index + 1} of ${stillMissing.length}`,
        });
        try {
          if (await queueDeemixForMissingTrack(db, userId, user, userServer, track, settings.matching_settings)) queued++;
          else unmatched++;
        } catch (error: any) {
          // One track failing (a rejected download, a search timeout) says
          // nothing about the rest - keep going and summarise at the end.
          failed++;
          logger.warn('Deemix All: failed to queue a track', { error: error?.message, userId, trackId: track.id });
        }
      }

      const parts = [`Queued ${queued} of ${stillMissing.length}`];
      if (unmatched > 0) parts.push(`${unmatched} with no good enough match`);
      if (failed > 0) parts.push(`${failed} failed`);
      updateNotification(userId, notificationId, {
        status: failed > 0 ? 'error' : 'success',
        progress: 100,
        detail: parts.join(' · '),
      });
    });

    res.json({ success: true, jobId, position, message: 'Queued - progress is shown in the notification bell.' });
  } catch (error: any) {
    logger.error('Failed to start Deemix All', { error: error?.message, stack: error?.stack, userId: req.session.userId });
    next(createInternalError(`Failed to start Deemix All: ${error?.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/missing/:id/lidarr-download
 * Find (or add-and-monitor) this track's artist in Lidarr, find its album if
 * known, and trigger a search across Lidarr's configured indexers. A second
 * acquisition path alongside /:id/deemix-download.
 */
router.post('/:id/lidarr-download', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const trackId = parseInt((req.params as Record<string, string>).id, 10);

    if (isNaN(trackId)) {
      return next(createValidationError('Invalid track ID'));
    }

    const allMissing = db.getUserMissingTracks(userId);
    const track = allMissing.find(t => t.id === trackId);
    if (!track) {
      return next(createNotFoundError('Missing track not found'));
    }

    const user = db.getUserById(userId);
    const userServer = db.getUserServer(userId);

    const firstArtist = track.artist.split(/\s*[,&\/]\s*/)[0].trim();
    const artistLookup = await findLidarrArtist(firstArtist);
    if (!artistLookup) {
      return next(createNotFoundError(`No Lidarr match found for artist "${firstArtist}"`));
    }

    const artistId = await addAndMonitorArtist(artistLookup);
    const album = await findAlbumForArtist(artistId, track.album);
    const commandId = await triggerLidarrSearch(artistId, album?.id ?? null);

    const notification = addNotification(userId, {
      type: 'lidarr',
      title: track.title,
      detail: album ? `${artistLookup.artistName} · ${album.title}` : `${artistLookup.artistName} · full discography`,
      status: 'in-progress',
      progress: 0,
    });

    const reconcile = (user && userServer) ? {
      db,
      missingTrackId: trackId,
      serverUrl: userServer.server_url,
      plexToken: resolvePlexToken(user, userServer),
      libraryId: userServer.library_id,
      serverClientId: userServer.server_client_id,
    } : undefined;
    if (reconcile) {
      trackLidarrSearch(userId, notification.id, commandId, reconcile);
    } else {
      updateNotification(userId, notification.id, { status: 'error', detail: 'No Plex server configured to reconcile against' });
    }

    logger.info('Triggered Lidarr search for missing track', { userId, trackId, artistId, albumId: album?.id, commandId });

    res.json({ success: true, matchedArtist: artistLookup.artistName, matchedAlbum: album?.title });
  } catch (error: any) {
    logger.error('Failed to trigger Lidarr search', {
      error: error?.message || error,
      stack: error?.stack,
      userId: req.session.userId,
      trackId: (req.params as Record<string, string>).id,
    });
    next(createInternalError(`Failed to trigger Lidarr search: ${error?.message || 'Unknown error'}`));
  }
});

/**
 * DELETE /api/missing/:id
 * Remove a specific missing track
 */
router.delete('/:id', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const trackId = parseInt((req.params as Record<string, string>).id, 10);

    if (isNaN(trackId)) {
      return next(createValidationError('Invalid track ID'));
    }

    // Get the missing track to verify ownership
    const allMissing = db.getUserMissingTracks(userId);
    const track = allMissing.find(t => t.id === trackId);

    if (!track) {
      return next(createNotFoundError('Missing track not found'));
    }

    // Remove the track
    db.removeMissingTrack(trackId);

    logger.info('Missing track removed', { userId, trackId });

    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to remove missing track', { error, userId: req.session.userId, trackId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to remove missing track'));
  }
});

/**
 * DELETE /api/missing/playlist/:playlistId
 * Clear all missing tracks for a playlist
 */
router.delete('/playlist/:playlistId', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = parseInt((req.params as Record<string, string>).playlistId, 10);

    if (isNaN(playlistId)) {
      return next(createValidationError('Invalid playlist ID'));
    }

    // Verify playlist ownership
    const playlist = db.getPlaylistById(playlistId);
    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    if (playlist.user_id !== userId) {
      return next(createForbiddenError('You do not have permission to modify this playlist'));
    }

    // Clear missing tracks
    db.clearPlaylistMissingTracks(playlistId);

    logger.info('Playlist missing tracks cleared', { userId, playlistId });

    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to clear playlist missing tracks', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).playlistId });
    next(createInternalError('Failed to clear playlist missing tracks'));
  }
});

export default router;
