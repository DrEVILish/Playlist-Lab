import { Router, Request, Response, NextFunction } from 'express';
import { EventEmitter } from 'events';
import { requireAuth } from '../middleware/auth';
import { createValidationError, createInternalError, createNotFoundError, createForbiddenError } from '../middleware/error-handler';
import { logger } from '../utils/logger';
import { PlexService } from '../services/plex';
import { matchPlaylist, dedupeByPlexRatingKey } from '../services/matching';
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
  userServer: { server_url: string; library_id?: string | null; server_client_id?: string | null },
  tracksToRetry: MissingTrack[],
  settings: { matching_settings: any }
): Promise<void> {
  activeRetries.set(userId, { current: 0, total: tracksToRetry.length });

  try {
    let batch = tracksToRetry;
    while (batch.length > 0) {
      await runRetryInBackground(userId, db, user, userServer, batch, settings);

      const queue = pendingRetries.get(userId);
      pendingRetries.delete(userId);
      if (!queue || queue.size === 0) break;

      // Re-check against the current missing list so tracks already
      // resolved (by this run or anything else) don't get retried again.
      const stillMissingIds = new Set(db.getUserMissingTracks(userId).map(t => t.id));
      batch = Array.from(queue.values()).filter(t => stillMissingIds.has(t.id));
      if (batch.length > 0) activeRetries.set(userId, { current: 0, total: batch.length });
    }
    activeRetries.delete(userId);
  } catch (error: any) {
    logger.error('Missing-track retry chain failed', { error: error.message, userId });
    const progress = activeRetries.get(userId);
    activeRetries.set(userId, {
      current: progress?.current ?? 0,
      total: progress?.total ?? tracksToRetry.length,
      error: error.message || 'Retry failed',
    });
    pendingRetries.delete(userId);
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
  userServer: { server_url: string; library_id?: string | null; server_client_id?: string | null },
  tracksToRetry: MissingTrack[],
  settings: { matching_settings: any }
): Promise<void> {
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
    });

    // Attempt to match tracks
    const matchedTracks = await matchPlaylist(
      externalTracks,
      userServer.server_url,
      user.plex_token,
      userServer.library_id || '',
      settings.matching_settings,
      progressEmitter
    );

    // Process matched tracks
    let matchedCount = 0;
    const plexService = new PlexService(userServer.server_url, user.plex_token);

    for (let i = 0; i < matchedTracks.length; i++) {
      const matched = matchedTracks[i];
      const original = tracksToRetry[i];

      if (matched.matched && matched.plexRatingKey) {
        try {
          // Get the playlist
          const playlist = db.getPlaylistById(original.playlist_id);
          if (!playlist) {
            logger.warn('Playlist not found for missing track', { playlistId: original.playlist_id });
            continue;
          }

          // Add track to playlist at original position
          const trackUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/metadata/${matched.plexRatingKey}`;
          
          // If playlist has a pending ID (no Plex playlist yet), create one now
          if (playlist.plex_playlist_id.startsWith('pending-')) {
            try {
              const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
              const newPlaylist = await plexService.createPlaylist(playlist.name, libraryUri, [trackUri]);
              db.updatePlaylist(playlist.id, { plex_playlist_id: newPlaylist.ratingKey });
              playlist.plex_playlist_id = newPlaylist.ratingKey;
              logger.info('Created Plex playlist from pending during retry', { playlistId: playlist.id, plexId: newPlaylist.ratingKey });
            } catch (createErr: any) {
              logger.error('Failed to create Plex playlist during retry', { error: createErr.message });
              continue;
            }
          } else {
            // Add track to playlist (will be added at the end)
            try {
              await plexService.addToPlaylist(playlist.plex_playlist_id, [trackUri]);
            } catch (plexError: any) {
              // If the Plex playlist was deleted, try creating a new one
              if (plexError.message?.includes('not found') || plexError.message?.includes('404')) {
                logger.warn('Plex playlist not found during retry, creating new one', {
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
            
            // If we have position information, move the track to its original position
            if (original.after_track_key) {
              try {
                // Get the playlist tracks to find the newly added track's playlistItemID
                const playlistTracks = await plexService.getPlaylistTracks(playlist.plex_playlist_id);
                
                // Find the newly added track (it will be at the end)
                const newTrack = playlistTracks.find(t => t.ratingKey === matched.plexRatingKey);
                
                if (newTrack && newTrack.playlistItemID) {
                  // Move the track to its original position (after the specified track)
                  await plexService.movePlaylistItem(
                    playlist.plex_playlist_id,
                    newTrack.playlistItemID.toString(),
                    original.after_track_key
                  );
                  
                  logger.info('Moved track to original position', {
                    playlistId: playlist.plex_playlist_id,
                    trackTitle: original.title,
                    afterTrackKey: original.after_track_key
                  });
                }
              } catch (moveErr: any) {
                // Non-fatal - track is still in playlist, just not at original position
                logger.warn('Failed to move track to original position', {
                  error: moveErr.message,
                  trackTitle: original.title,
                  playlistId: playlist.plex_playlist_id
                });
              }
            }
          }

          // Remove from missing tracks
          db.removeMissingTrack(original.id);
          matchedCount++;

          logger.info('Missing track matched and added', { 
            userId, 
            playlistId: original.playlist_id, 
            trackId: original.id,
            title: original.title,
            artist: original.artist
          });
        } catch (error) {
          logger.error('Failed to add matched track to playlist', { 
            error, 
            trackId: original.id,
            playlistId: original.playlist_id
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
    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, server_client_id FROM user_servers WHERE user_id = ?').get(userId);
    const hasPlexConfig = userRow?.plex_token && serverRow?.server_url && serverRow?.server_client_id;

    let existingCoverUrl: string | null = null;

    // Handle overwrite: delete existing playlist with same name
    if (overwriteExisting && hasPlexConfig) {
      try {
        const { PlexClient } = await import('../services/plex');
        const plexClient = new PlexClient(serverRow.server_url, userRow.plex_token);
        const plexPlaylists = await plexClient.getPlaylists();
        const normalize = (s: string) => s.trim().replace(/\s+/g, ' ').toLowerCase();

        logger.info('Overwrite (missing save): searching for existing playlist', { playlistName, plexPlaylistCount: plexPlaylists.length });

        const matchingPlaylists = plexPlaylists.filter((p: any) =>
          normalize(p.title || '') === normalize(playlistName)
        );

        if (matchingPlaylists.length > 0) {
          const firstMatch = matchingPlaylists[0];
          if (keepExistingCover && firstMatch.composite) {
            existingCoverUrl = `${serverRow.server_url}${firstMatch.composite}?X-Plex-Token=${userRow.plex_token}`;
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
        const plexClient = new PlexClient(serverRow.server_url, userRow.plex_token);

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
    const trackId = parseInt(req.params.id, 10);
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

    const plexService = new PlexService(userServer.server_url, user.plex_token);
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

    // Remove from missing tracks
    db.removeMissingTrack(trackId);

    logger.info('Missing track manually rematched', { userId, trackId, ratingKey });

    res.json({ success: true });
  } catch (error: any) {
    logger.error('Failed to rematch missing track', { 
      error: error?.message || error, 
      stack: error?.stack,
      userId: req.session.userId, 
      trackId: req.params.id,
      ratingKey: req.body.ratingKey,
    });
    next(createInternalError(`Failed to rematch track: ${error?.message || 'Unknown error'}`));
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
    const trackId = parseInt(req.params.id, 10);

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
    logger.error('Failed to remove missing track', { error, userId: req.session.userId, trackId: req.params.id });
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
    const playlistId = parseInt(req.params.playlistId, 10);

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
    logger.error('Failed to clear playlist missing tracks', { error, userId: req.session.userId, playlistId: req.params.playlistId });
    next(createInternalError('Failed to clear playlist missing tracks'));
  }
});

export default router;
