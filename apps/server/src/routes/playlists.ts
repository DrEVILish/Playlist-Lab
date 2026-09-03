import { Router, Request, Response, NextFunction } from 'express';
import { requireAuth } from '../middleware/auth';
import { createValidationError, createInternalError, createNotFoundError, createForbiddenError } from '../middleware/error-handler';
import { logger } from '../utils/logger';
import { PlexService, PlexAuthError, resolvePlexToken } from '../services/plex';
import { reimportPlaylistNow } from '../services/import';
import { updateNotification } from '../services/job-notifications';
import { enqueueAction } from '../services/action-queue';
import { plexLimiter } from '../services/task-queues';
import multer from 'multer';
import FormData from 'form-data';
import axios from 'axios';

const upload = multer({ 
  storage: multer.memoryStorage(),
  limits: { fileSize: 5 * 1024 * 1024 } // 5MB limit
});

const router = Router();

/**
 * GET /api/playlists
 * Get user's playlists from Plex
 * Query params:
 *   - userId: (optional, admin only) Get playlists for a specific user
 */
router.get('/', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const currentUserId = req.session.userId!;
    const db = req.dbService!;
    
    // Check if requesting another user's playlists
    const requestedUserId = req.query.userId ? parseInt(req.query.userId as string, 10) : currentUserId;
    
    // Only admins can fetch other users' playlists
    if (requestedUserId !== currentUserId && !db.isAdmin(currentUserId)) {
      return next(createForbiddenError('You do not have permission to view other users\' playlists'));
    }
    
    // Get user and server info
    const user = db.getUserById(requestedUserId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(requestedUserId);
    if (!userServer) {
      logger.info('No server selected, returning empty playlists', { userId: requestedUserId });
      return res.json({ playlists: [] });
    }

    // Fetch playlists directly from Plex
    try {
      const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
      const plexPlaylists = await plexService.getPlaylists();
      
      // Filter for audio playlists only
      const audioPlaylists = plexPlaylists.filter(p => p.playlistType === 'audio');

      // Cross-reference against our own tracking table (by plex_playlist_id)
      // so callers get one merged list: live data straight from Plex (track
      // count, duration, cover) plus our own metadata for playlists that
      // were imported through this app (numeric id for looking up its
      // schedule/missing tracks, original source + link). Playlists that
      // exist in Plex but were never imported through this app (e.g.
      // created directly in Plex) simply have no `dbId`/`source`.
      const trackedByPlexId = new Map(
        db.getUserPlaylists(requestedUserId).map(p => [p.plex_playlist_id, p])
      );

      // Map to our format
      const playlists = audioPlaylists.map(p => {
        // Clean up duplicate prefixes in playlist names (e.g., "All out - All out 60s" -> "All out 60s")
        let cleanName = p.title;
        const parts = cleanName.split(' - ');
        if (parts.length === 2 && parts[0] === parts[1].split(' ')[0]) {
          // If the prefix before " - " matches the first word after " - ", remove the prefix
          cleanName = parts[1];
        }

        const tracked = trackedByPlexId.get(p.ratingKey);

        return {
          id: p.ratingKey,
          dbId: tracked?.id,
          plexPlaylistId: p.ratingKey,
          name: cleanName,
          source: tracked?.source || 'plex',
          sourceUrl: tracked?.source_url ?? undefined,
          trackCount: p.leafCount || 0,
          duration: p.duration || 0,
          smart: !!p.smart,
          composite: p.composite,
          thumb: p.composite,
          createdAt: p.addedAt ? p.addedAt * 1000 : Date.now(),
          updatedAt: p.updatedAt ? p.updatedAt * 1000 : Date.now(),
        };
      });
      
      logger.info('Fetched playlists from Plex', { userId: requestedUserId, count: playlists.length });
      res.json({ playlists });
    } catch (error) {
      // An expired token is not "you have no playlists" - swallowing it here
      // shows an empty library and hides the one action that fixes it, so it
      // goes back as a 401 (PlexAuthError carries the status) while every
      // other failure still degrades to an empty list rather than a broken UI.
      if (error instanceof PlexAuthError) return next(error);
      logger.error('Failed to fetch playlists from Plex', { error, userId: requestedUserId });
      // Return empty array instead of error to avoid breaking the UI
      res.json({ playlists: [] });
    }
  } catch (error) {
    logger.error('Failed to get playlists', { error, userId: req.session.userId });
    next(createInternalError('Failed to retrieve playlists'));
  }
});

/**
 * GET /api/playlists/shared-with-me
 * Get playlists that have been shared with the current user
 */
router.get('/shared-with-me', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const currentUserId = req.session.userId!;
    const db = req.dbService!;
    
    logger.info('[Shared Playlists] Fetching shared playlists', { userId: currentUserId });
    
    // Get shared playlists from database
    const sharedPlaylists = db.getPlaylistsSharedWithUser(currentUserId);
    
    logger.info('[Shared Playlists] Found shared playlists', { 
      userId: currentUserId, 
      count: sharedPlaylists.length 
    });
    
    res.json({ sharedPlaylists });
  } catch (error: any) {
    logger.error('[Shared Playlists] Failed to get shared playlists', { 
      error: error.message,
      stack: error.stack,
      userId: req.session.userId 
    });
    next(createInternalError('Failed to retrieve shared playlists'));
  }
});

/**
 * GET /api/playlists/:id
 * Get playlist details
 */
router.get('/:id', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = parseInt((req.params as Record<string, string>).id, 10);

    if (isNaN(playlistId)) {
      return next(createValidationError('Invalid playlist ID'));
    }

    const playlist = db.getPlaylistById(playlistId);
    
    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    // Verify ownership
    if (playlist.user_id !== userId) {
      return next(createForbiddenError('You do not have permission to access this playlist'));
    }

    res.json({ playlist });
  } catch (error) {
    logger.error('Failed to get playlist', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to retrieve playlist'));
  }
});

/**
 * POST /api/playlists
 * Create a new playlist in Plex
 */
router.post('/', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { name, source, sourceUrl, trackUris } = req.body;

    if (!name) {
      return next(createValidationError('name is required'));
    }

    if (!trackUris || !Array.isArray(trackUris) || trackUris.length === 0) {
      return next(createValidationError('trackUris must be a non-empty array'));
    }

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    if (!userServer.library_id) {
      return next(createValidationError('No library selected. Please select a library first.'));
    }

    // Create playlist in Plex
    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
    const plexPlaylist = await plexService.createPlaylist(name, libraryUri, trackUris);

    // Save playlist to database
    const playlist = db.createPlaylist(
      userId,
      plexPlaylist.ratingKey,
      name,
      source || 'manual',
      sourceUrl
    );

    logger.info('Playlist created', { userId, playlistId: playlist.id, name });

    res.status(201).json({ playlist });
  } catch (error) {
    logger.error('Failed to create playlist', { error, userId: req.session.userId });
    next(createInternalError('Failed to create playlist'));
  }
});

/**
 * PUT /api/playlists/:id
 * Update playlist
 */
router.put('/:id', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = parseInt((req.params as Record<string, string>).id, 10);
    const { name, sourceUrl } = req.body;

    if (isNaN(playlistId)) {
      return next(createValidationError('Invalid playlist ID'));
    }

    const playlist = db.getPlaylistById(playlistId);

    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    // Verify ownership
    if (playlist.user_id !== userId) {
      return next(createForbiddenError('You do not have permission to modify this playlist'));
    }

    const trimmedName = typeof name === 'string' ? name.trim() : undefined;
    if (trimmedName === '') {
      return next(createValidationError('Playlist name cannot be empty'));
    }

    // Rename in Plex itself too, not just our own record of it - otherwise
    // this app's name would drift from what every actual Plex client shows.
    if (trimmedName !== undefined && trimmedName !== playlist.name) {
      const user = db.getUserById(userId);
      const userServer = db.getUserServer(userId);
      if (!user || !userServer) {
        return next(createValidationError('Plex server not configured'));
      }
      const plexClient = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
      await plexClient.renamePlaylist(playlist.plex_playlist_id, trimmedName);
    }

    // Build update object
    const updates: any = { updated_at: Date.now() };
    if (trimmedName !== undefined) updates.name = trimmedName;
    if (sourceUrl !== undefined) updates.source_url = sourceUrl;

    // Update playlist
    db.updatePlaylist(playlistId, updates);

    // Retrieve updated playlist
    const updatedPlaylist = db.getPlaylistById(playlistId);

    logger.info('Playlist updated', { userId, playlistId });

    res.json({ playlist: updatedPlaylist });
  } catch (error: any) {
    logger.error('Failed to update playlist', { error: error.message, stack: error.stack, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError(`Failed to update playlist: ${error.message || 'Unknown error'}`));
  }
});

/**
 * DELETE /api/playlists/by-plex-id/:ratingKey
 * Delete a playlist identified by its Plex ratingKey rather than our
 * internal numeric id - works for any playlist visible in Plex, including
 * ones never imported through this app (so there's no numeric id for them;
 * see GET /api/playlists's dbId field). Cleans up our tracking row too, if
 * one happens to exist for it.
 */
router.delete('/by-plex-id/:ratingKey', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { ratingKey } = req.params as Record<string, string>;

    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    await plexService.deletePlaylist(ratingKey);

    const tracked = db.getPlaylistByPlexId(userId, ratingKey);
    if (tracked) {
      db.deletePlaylist(tracked.id);
    }

    logger.info('Playlist deleted by Plex ratingKey', { userId, ratingKey, hadDbRecord: !!tracked });

    res.json({ success: true });
  } catch (error: any) {
    logger.error('Failed to delete playlist by Plex ratingKey', { error: error.message, userId: req.session.userId });
    next(createInternalError('Failed to delete playlist'));
  }
});

/**
 * PUT /api/playlists/by-plex-id/:ratingKey
 * Rename a playlist identified by its Plex ratingKey rather than our
 * internal numeric id - works for any playlist visible in Plex, including
 * ones never imported through this app (so there's no numeric id for them;
 * see GET /api/playlists's dbId field). Updates our tracking row too, if one
 * happens to exist for it, so the two names don't drift apart.
 */
router.put('/by-plex-id/:ratingKey', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { ratingKey } = req.params as Record<string, string>;
    const { name } = req.body;

    const trimmedName = typeof name === 'string' ? name.trim() : undefined;
    if (!trimmedName) {
      return next(createValidationError('Playlist name cannot be empty'));
    }

    const user = db.getUserById(userId);
    const userServer = db.getUserServer(userId);
    if (!user || !userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    const plexClient = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    await plexClient.renamePlaylist(ratingKey, trimmedName);

    const tracked = db.getPlaylistByPlexId(userId, ratingKey);
    if (tracked) {
      db.updatePlaylist(tracked.id, { name: trimmedName, updated_at: Date.now() });
    }

    logger.info('Playlist renamed by Plex ratingKey', { userId, ratingKey, hadDbRecord: !!tracked });

    res.json({ success: true, name: trimmedName });
  } catch (error: any) {
    logger.error('Failed to rename playlist by Plex ratingKey', { error: error.message, userId: req.session.userId });
    next(createInternalError(`Failed to rename playlist: ${error.message || 'Unknown error'}`));
  }
});

const NON_REIMPORTABLE_SOURCES = ['plex', 'manual', 'template'];

/**
 * POST /api/playlists/:id/reimport
 * Re-scrape a playlist from its original online source and replace its
 * Plex tracks with the refreshed result. Fires the refresh in the
 * background and returns immediately, mirroring POST /api/schedules/:id/run.
 */
router.post('/:id/reimport', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = parseInt((req.params as Record<string, string>).id, 10);

    if (isNaN(playlistId)) {
      return next(createValidationError('Invalid playlist ID'));
    }

    const playlist = db.getPlaylistById(playlistId);
    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    if (playlist.user_id !== userId) {
      return next(createForbiddenError('You do not have permission to reimport this playlist'));
    }

    if (!playlist.source || NON_REIMPORTABLE_SOURCES.includes(playlist.source)) {
      return next(createValidationError('This playlist has no online source to reimport from'));
    }

    const user = db.getUserById(userId);
    const userServer = db.getUserServer(userId);
    if (!user || !userServer) {
      return next(createValidationError('No Plex server configured'));
    }

    logger.info('Manual reimport triggered', { playlistId, userId, source: playlist.source });

    reimportPlaylistNow(db, playlist, user, userServer).catch((error: any) => {
      logger.error('Manual reimport failed', { playlistId, error: error.message });
    });

    res.json({ success: true, message: 'Reimport started' });
  } catch (error: any) {
    logger.error('Failed to trigger reimport', { error: error.message, userId: req.session.userId });
    next(createInternalError('Failed to trigger reimport'));
  }
});

/**
 * DELETE /api/playlists/:id
 * Delete playlist
 */
router.delete('/:id', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = parseInt((req.params as Record<string, string>).id, 10);

    if (isNaN(playlistId)) {
      return next(createValidationError('Invalid playlist ID'));
    }

    const playlist = db.getPlaylistById(playlistId);
    
    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    // Verify ownership
    if (playlist.user_id !== userId) {
      return next(createForbiddenError('You do not have permission to delete this playlist'));
    }

    // Get user and server info to delete from Plex
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (userServer) {
      try {
        // Delete from Plex
        const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
        await plexService.deletePlaylist(playlist.plex_playlist_id);
      } catch (error) {
        logger.warn('Failed to delete playlist from Plex, continuing with database deletion', { error, playlistId });
      }
    }

    // Delete from database
    db.deletePlaylist(playlistId);

    logger.info('Playlist deleted', { userId, playlistId });

    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to delete playlist', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to delete playlist'));
  }
});

/**
 * GET /api/playlists/:id/tracks
 * Get playlist tracks from Plex
 */
router.get('/:id/tracks', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = (req.params as Record<string, string>).id;

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    // Get tracks from Plex using the playlist ID directly
    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    const plexTracks = await plexService.getPlaylistTracks(playlistId);

    // Map tracks to include codec and bitrate from Media array
    const tracks = plexTracks.map(track => {
      const albumArtist = track.grandparentTitle || '';
      const trackArtist = (track as any).originalTitle || '';

      // Album artist is just the library's folder/grouping artist and can be wrong
      // or unmatchable for this specific track (e.g. "Various Artists", a
      // soundtrack's composer) - whenever Plex has a distinct per-track artist,
      // show that instead.
      const displayArtist = trackArtist && trackArtist.toLowerCase() !== albumArtist.toLowerCase()
        ? trackArtist
        : albumArtist;
      
      return {
        ratingKey: track.ratingKey,
        playlistItemID: track.playlistItemID,
        title: track.title,
        artist: displayArtist,
        album: track.parentTitle || '',
        duration: track.duration || 0,
        codec: track.Media?.[0]?.audioCodec?.toUpperCase(),
        bitrate: track.Media?.[0]?.bitrate,
      };
    });

    res.json({ tracks });
  } catch (error) {
    logger.error('Failed to get playlist tracks', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to retrieve playlist tracks'));
  }
});

/**
 * POST /api/playlists/:id/tracks
 * Add tracks to playlist
 */
router.post('/:id/tracks', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = (req.params as Record<string, string>).id; // Keep as string for Plex ID
    const { trackUris } = req.body;

    if (!trackUris || !Array.isArray(trackUris) || trackUris.length === 0) {
      return next(createValidationError('trackUris must be a non-empty array'));
    }

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    // Add tracks to Plex playlist using Plex ID directly
    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    await plexService.addToPlaylist(playlistId, trackUris);

    // Try to update database record if it exists
    const playlistIdNum = parseInt(playlistId, 10);
    if (!isNaN(playlistIdNum)) {
      const playlist = db.getPlaylistById(playlistIdNum);
      if (playlist) {
        db.updatePlaylist(playlistIdNum, { updated_at: Date.now() });
      }
    }

    logger.info('Tracks added to playlist', { userId, playlistId, trackCount: trackUris.length });

    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to add tracks to playlist', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to add tracks to playlist'));
  }
});

/**
 * DELETE /api/playlists/:id/tracks/:trackId
 * Remove track from playlist
 */
router.delete('/:id/tracks/:trackId', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = (req.params as Record<string, string>).id; // Keep as string for Plex ID
    const { trackId } = req.params as Record<string, string>;

    if (!trackId) {
      return next(createValidationError('trackId is required'));
    }

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    // Remove track from Plex playlist using Plex ID directly
    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    await plexService.removeFromPlaylist(playlistId, trackId);

    // Try to update database record if it exists
    const playlistIdNum = parseInt(playlistId, 10);
    if (!isNaN(playlistIdNum)) {
      const playlist = db.getPlaylistById(playlistIdNum);
      if (playlist) {
        db.updatePlaylist(playlistIdNum, { updated_at: Date.now() });
      }
    }

    logger.info('Track removed from playlist', { userId, playlistId, trackId });

    res.json({ success: true });
  } catch (error) {
    const errorMessage = error instanceof Error ? error.message : String(error);
    logger.error('Failed to remove track from playlist', { 
      error: errorMessage,
      stack: error instanceof Error ? error.stack : undefined,
      userId: req.session.userId, 
      playlistId: (req.params as Record<string, string>).id,
      trackId: (req.params as Record<string, string>).trackId
    });
    
    // Provide more specific error messages
    if (errorMessage.includes('not found')) {
      return next(createNotFoundError('Track not found in playlist. The playlist may have been modified.'));
    }
    
    next(createInternalError('Failed to remove track from playlist'));
  }
});

/**
 * PUT /api/playlists/:id/tracks/:trackId/move
 * Reorder a track in the playlist
 */
router.put('/:id/tracks/:trackId/move', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = (req.params as Record<string, string>).id; // Keep as string for Plex
    const { trackId } = req.params as Record<string, string>;
    const { afterId } = req.body;

    if (!trackId) {
      return next(createValidationError('trackId is required'));
    }

    if (afterId === undefined) {
      return next(createValidationError('afterId is required'));
    }

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('No server selected. Please select a server first.'));
    }

    // Move track in Plex playlist
    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    await plexService.movePlaylistItem(playlistId, trackId, afterId);

    logger.info('Track moved in playlist', { userId, playlistId, trackId, afterId });

    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to move track in playlist', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to move track in playlist'));
  }
});

/** Builds `server://<clientId>/.../library/metadata/<ratingKey>` URIs for
 * createPlaylist()/addToPlaylist(), the same format every other playlist
 * mutation in this file uses. */
function buildTrackUris(serverClientId: string | null | undefined, ratingKeys: string[]): string[] {
  return ratingKeys.map(key => `server://${serverClientId}/com.plexapp.plugins.library/library/metadata/${key}`);
}

/**
 * POST /api/playlists/merge
 * Combine two or more playlists' tracks (deduped) into a new playlist, or
 * append them onto an existing one. Takes Plex ratingKeys rather than our
 * internal numeric ids - like /by-plex-id/:ratingKey and /:id/share below,
 * this works on any playlist the user's own Plex token can see, not just
 * ones tracked in our `playlists` table (which only has rows for playlists
 * imported through this app).
 */
router.post('/merge', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { sourceRatingKeys, targetName, existingTargetRatingKey } = req.body;

    if (!Array.isArray(sourceRatingKeys) || sourceRatingKeys.length < 2) {
      return next(createValidationError('sourceRatingKeys must be an array of at least 2 playlist ratingKeys'));
    }
    if (!existingTargetRatingKey && (typeof targetName !== 'string' || !targetName.trim())) {
      return next(createValidationError('targetName is required when not merging into an existing playlist'));
    }

    const user = db.getUserById(userId);
    if (!user) return next(createNotFoundError('User not found'));
    const userServer = db.getUserServer(userId);
    if (!userServer || !userServer.library_id) {
      return next(createValidationError('No server/library selected. Please select one first.'));
    }

    const { jobId, position } = enqueueAction(userId, 'Merge playlists', async (notificationId) => {
      try {
        const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));

        const seen = new Set<string>();
        const mergedRatingKeys: string[] = [];
        for (const ratingKey of sourceRatingKeys) {
          const tracks = await plexService.getPlaylistTracks(ratingKey);
          for (const track of tracks) {
            if (track.ratingKey && !seen.has(track.ratingKey)) {
              seen.add(track.ratingKey);
              mergedRatingKeys.push(track.ratingKey);
            }
          }
        }

        if (mergedRatingKeys.length === 0) {
          updateNotification(userId, notificationId, { status: 'error', detail: 'The selected playlists have no tracks to merge' });
          return;
        }

        let resultPlexId: string;
        let resultName: string;
        if (existingTargetRatingKey) {
          const existingTracks = await plexService.getPlaylistTracks(existingTargetRatingKey);
          const existingKeys = new Set(existingTracks.map(t => t.ratingKey));
          const newKeys = mergedRatingKeys.filter(key => !existingKeys.has(key));
          if (newKeys.length > 0) {
            await plexService.addToPlaylist(existingTargetRatingKey, buildTrackUris(userServer.server_client_id, newKeys));
          }
          resultPlexId = existingTargetRatingKey;
          resultName = targetName?.trim() || existingTargetRatingKey;
          const tracked = db.getPlaylistByPlexId(userId, existingTargetRatingKey);
          if (tracked) db.updatePlaylist(tracked.id, { updated_at: Date.now() });
        } else {
          const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
          const plexPlaylist = await plexService.createPlaylist(targetName.trim(), libraryUri, buildTrackUris(userServer.server_client_id, mergedRatingKeys));
          resultPlexId = plexPlaylist.ratingKey;
          resultName = targetName.trim();
          db.createPlaylist(userId, resultPlexId, resultName, 'manual', undefined);
        }

        logger.info('Playlists merged', { userId, sourceRatingKeys, trackCount: mergedRatingKeys.length, resultPlexId });
        updateNotification(userId, notificationId, {
          title: resultName,
          status: 'success',
          detail: `Merged ${mergedRatingKeys.length} tracks`,
        });
      } catch (error: any) {
        logger.error('Failed to merge playlists', { error: error.message, userId });
        updateNotification(userId, notificationId, { status: 'error', detail: error.message || 'Failed to merge playlists' });
      }
    });

    res.status(202).json({ queued: true, jobId, position });
  } catch (error: any) {
    logger.error('Failed to merge playlists', { error: error.message, userId: req.session.userId });
    next(createInternalError(`Failed to merge playlists: ${error.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/playlists/:id/clone
 * Duplicate a playlist's current tracks into a new playlist. `:id` is the
 * Plex ratingKey (see /:id/share below, which uses the same convention).
 */
router.post('/:id/clone', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const sourceRatingKey = (req.params as Record<string, string>).id;

    const name = typeof req.body?.name === 'string' ? req.body.name.trim() : '';
    if (!name) return next(createValidationError('name is required'));

    const user = db.getUserById(userId);
    if (!user) return next(createNotFoundError('User not found'));
    const userServer = db.getUserServer(userId);
    if (!userServer || !userServer.library_id) {
      return next(createValidationError('No server/library selected. Please select one first.'));
    }

    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    const tracks = await plexService.getPlaylistTracks(sourceRatingKey);
    const ratingKeys = tracks.map(t => t.ratingKey).filter((k): k is string => !!k);
    if (ratingKeys.length === 0) {
      return next(createValidationError('This playlist has no tracks to clone'));
    }

    const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;
    const plexPlaylist = await plexService.createPlaylist(name, libraryUri, buildTrackUris(userServer.server_client_id, ratingKeys));
    const clonedPlaylist = db.createPlaylist(userId, plexPlaylist.ratingKey, name, 'manual', undefined);

    logger.info('Playlist cloned', { userId, sourceRatingKey, clonedPlaylistId: clonedPlaylist.id, trackCount: ratingKeys.length });
    res.status(201).json({ playlist: clonedPlaylist });
  } catch (error: any) {
    logger.error('Failed to clone playlist', { error: error.message, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError(`Failed to clone playlist: ${error.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/playlists/:id/split
 * Copy subsets of a playlist's tracks (by ratingKey) out into one or more
 * new playlists. Non-destructive - the source playlist is left untouched.
 * `:id` is the Plex ratingKey, same convention as clone/share above.
 */
router.post('/:id/split', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;

    const { groups } = req.body as { groups?: Array<{ name: string; trackIds: string[] }> };
    if (!Array.isArray(groups) || groups.length === 0) {
      return next(createValidationError('groups must be a non-empty array of { name, trackIds }'));
    }
    for (const group of groups) {
      if (!group.name?.trim() || !Array.isArray(group.trackIds) || group.trackIds.length === 0) {
        return next(createValidationError('Each group needs a name and a non-empty trackIds array'));
      }
    }

    const user = db.getUserById(userId);
    if (!user) return next(createNotFoundError('User not found'));
    const userServer = db.getUserServer(userId);
    if (!userServer || !userServer.library_id) {
      return next(createValidationError('No server/library selected. Please select one first.'));
    }

    const sourceRatingKey = (req.params as Record<string, string>).id;
    const { jobId, position } = enqueueAction(userId, 'Split playlist', async (notificationId) => {
      try {
        const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
        const libraryUri = `server://${userServer.server_client_id}/com.plexapp.plugins.library/library/sections/${userServer.library_id}`;

        const createdPlaylists = [];
        for (const group of groups) {
          const plexPlaylist = await plexService.createPlaylist(
            group.name.trim(),
            libraryUri,
            buildTrackUris(userServer.server_client_id, group.trackIds)
          );
          createdPlaylists.push(db.createPlaylist(userId, plexPlaylist.ratingKey, group.name.trim(), 'manual', undefined));
        }

        logger.info('Playlist split', { userId, sourceRatingKey, groupCount: groups.length });
        updateNotification(userId, notificationId, {
          status: 'success',
          detail: `Split into ${createdPlaylists.length} playlist(s)`,
        });
      } catch (error: any) {
        logger.error('Failed to split playlist', { error: error.message, userId, playlistId: sourceRatingKey });
        updateNotification(userId, notificationId, { status: 'error', detail: error.message || 'Failed to split playlist' });
      }
    });

    res.status(202).json({ queued: true, jobId, position });
  } catch (error: any) {
    logger.error('Failed to split playlist', { error: error.message, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError(`Failed to split playlist: ${error.message || 'Unknown error'}`));
  }
});

/** Fisher-Yates shuffle - unbiased, unlike the common `sort(() => Math.random() - 0.5)`
 * one-liner. Kept as a local function rather than exporting MixService's
 * private equivalent, since that would mean restructuring a class for one
 * call site. */
function shuffleArray<T>(array: T[]): T[] {
  const result = [...array];
  for (let i = result.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [result[i], result[j]] = [result[j], result[i]];
  }
  return result;
}

/**
 * Rewrites a playlist's order to match `ordered` by moving each track after
 * the one before it. Shared by shuffle and sort so there is one description
 * of how a reorder is performed.
 *
 * ponytail: Plex's playlist API has no batch-reorder endpoint, so this is
 * O(n) sequential movePlaylistItem calls - fine for typical playlist sizes,
 * but slow (and non-atomic - a mid-run failure leaves a partial reorder) for
 * very large ones. Revisit if Plex ever adds a bulk-reorder endpoint.
 */
async function applyTrackOrder(plexService: any, ratingKey: string, ordered: any[]): Promise<void> {
  for (let i = 0; i < ordered.length; i++) {
    const afterId = i === 0 ? '0' : ordered[i - 1].playlistItemID!.toString();
    await plexService.movePlaylistItem(ratingKey, ordered[i].playlistItemID!.toString(), afterId);
  }
}

/** The fields a playlist can be sorted on, and how to read each one off a
 * Plex track. Strings are compared with localeCompare so accented titles sort
 * where a reader expects rather than after "z". */
const SORT_KEYS: Record<string, (t: any) => string | number> = {
  title: t => t.title || '',
  artist: t => t.grandparentTitle || t.originalTitle || '',
  album: t => t.parentTitle || '',
  year: t => t.year || t.parentYear || 0,
  duration: t => t.duration || 0,
};

/**
 * POST /api/playlists/:id/shuffle
 * Randomize a playlist's track order in place. `:id` is the Plex ratingKey,
 * same convention as clone/split/share above.
 *
 * ponytail: Plex's playlist API has no batch-reorder endpoint, so this is
 * O(n) sequential movePlaylistItem calls - fine for typical playlist sizes,
 * but slow (and non-atomic - a mid-shuffle failure leaves a partial reorder)
 * for very large ones. Revisit if Plex ever adds a bulk-reorder endpoint.
 */
router.post('/:id/shuffle', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const ratingKey = (req.params as Record<string, string>).id;

    const user = db.getUserById(userId);
    if (!user) return next(createNotFoundError('User not found'));
    const userServer = db.getUserServer(userId);
    if (!userServer) return next(createValidationError('No server selected. Please select a server first.'));

    const { jobId, position } = enqueueAction(userId, 'Shuffle playlist', async (notificationId) => {
      try {
        const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
        const tracks = await plexService.getPlaylistTracks(ratingKey);
        const shuffled = shuffleArray(tracks.filter(t => t.playlistItemID != null));
        await applyTrackOrder(plexService, ratingKey, shuffled);

        const tracked = db.getPlaylistByPlexId(userId, ratingKey);
        if (tracked) db.updatePlaylist(tracked.id, { updated_at: Date.now() });

        logger.info('Playlist shuffled', { userId, ratingKey, trackCount: shuffled.length });
        updateNotification(userId, notificationId, { status: 'success', detail: `Shuffled ${shuffled.length} tracks` });
      } catch (error: any) {
        logger.error('Failed to shuffle playlist', { error: error.message, userId, playlistId: ratingKey });
        updateNotification(userId, notificationId, { status: 'error', detail: error.message || 'Failed to shuffle playlist' });
      }
    });

    res.status(202).json({ queued: true, jobId, position });
  } catch (error: any) {
    logger.error('Failed to shuffle playlist', { error: error.message, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError(`Failed to shuffle playlist: ${error.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/playlists/:id/sort
 * Reorder a playlist by one of the fields in SORT_KEYS. Body: `{ by,
 * direction }`. `:id` is the Plex ratingKey, same convention as the other
 * playlist operations here.
 */
router.post('/:id/sort', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const ratingKey = (req.params as Record<string, string>).id;
    const { by, direction } = req.body as { by?: string; direction?: string };

    const readKey = SORT_KEYS[by || ''];
    if (!readKey) {
      return next(createValidationError(`by must be one of: ${Object.keys(SORT_KEYS).join(', ')}`));
    }
    const descending = direction === 'desc';

    const user = db.getUserById(userId);
    if (!user) return next(createNotFoundError('User not found'));
    const userServer = db.getUserServer(userId);
    if (!userServer) return next(createValidationError('No server selected. Please select a server first.'));

    const { jobId, position } = enqueueAction(userId, 'Sort playlist', async (notificationId) => {
      try {
        const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
        const tracks = (await plexService.getPlaylistTracks(ratingKey)).filter(t => t.playlistItemID != null);

        const sorted = [...tracks].sort((a, b) => {
          const left = readKey(a);
          const right = readKey(b);
          const comparison = typeof left === 'string' || typeof right === 'string'
            ? String(left).localeCompare(String(right), undefined, { sensitivity: 'base' })
            : Number(left) - Number(right);
          return descending ? -comparison : comparison;
        });

        await applyTrackOrder(plexService, ratingKey, sorted);

        const tracked = db.getPlaylistByPlexId(userId, ratingKey);
        if (tracked) db.updatePlaylist(tracked.id, { updated_at: Date.now() });

        logger.info('Playlist sorted', { userId, ratingKey, by, direction: descending ? 'desc' : 'asc', trackCount: sorted.length });
        updateNotification(userId, notificationId, { status: 'success', detail: `Sorted ${sorted.length} tracks by ${by}` });
      } catch (error: any) {
        logger.error('Failed to sort playlist', { error: error.message, userId, playlistId: ratingKey });
        updateNotification(userId, notificationId, { status: 'error', detail: error.message || 'Failed to sort playlist' });
      }
    });

    res.status(202).json({ queued: true, jobId, position });
  } catch (error: any) {
    logger.error('Failed to sort playlist', { error: error.message, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError(`Failed to sort playlist: ${error.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/playlists/:id/dedupe
 * Remove repeated tracks, keeping the first occurrence of each so the
 * playlist's order is otherwise untouched. Duplicates are judged by Plex
 * ratingKey - the same track added twice - not by title, so two genuinely
 * different recordings of a song are both kept.
 */
router.post('/:id/dedupe', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const ratingKey = (req.params as Record<string, string>).id;

    const user = db.getUserById(userId);
    if (!user) return next(createNotFoundError('User not found'));
    const userServer = db.getUserServer(userId);
    if (!userServer) return next(createValidationError('No server selected. Please select a server first.'));

    const { jobId, position } = enqueueAction(userId, 'Remove duplicates', async (notificationId) => {
      try {
        const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
        const tracks = await plexService.getPlaylistTracks(ratingKey);

        const seen = new Set<string>();
        const duplicates = tracks.filter(t => {
          if (t.playlistItemID == null || !t.ratingKey) return false;
          const key = String(t.ratingKey);
          if (seen.has(key)) return true;
          seen.add(key);
          return false;
        });

        await plexService.removeMultipleFromPlaylist(
          ratingKey,
          duplicates.map(d => d.playlistItemID!.toString())
        );

        const tracked = db.getPlaylistByPlexId(userId, ratingKey);
        if (tracked) db.updatePlaylist(tracked.id, { updated_at: Date.now() });

        logger.info('Playlist deduplicated', { userId, ratingKey, removed: duplicates.length, remaining: seen.size });
        updateNotification(userId, notificationId, { status: 'success', detail: `Removed ${duplicates.length} duplicate(s)` });
      } catch (error: any) {
        logger.error('Failed to remove duplicates', { error: error.message, userId, playlistId: ratingKey });
        updateNotification(userId, notificationId, { status: 'error', detail: error.message || 'Failed to remove duplicates' });
      }
    });

    res.status(202).json({ queued: true, jobId, position });
  } catch (error: any) {
    logger.error('Failed to remove duplicates', { error: error.message, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError(`Failed to remove duplicates: ${error.message || 'Unknown error'}`));
  }
});

/**
 * GET /api/playlists/share-targets
 * Other Playlist Lab users on this server that a playlist can be shared
 * with (i.e. everyone but yourself, with a Plex server configured).
 */
router.get('/share-targets', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    const currentUserId = req.session.userId!;
    const db = req.dbService!;

    const users = db.getAllUsers()
      .filter(u => u.id !== currentUserId && !!db.getUserServer(u.id))
      .map(u => ({
        id: u.id,
        username: u.plex_username,
        thumb: u.plex_thumb,
      }));

    res.json({ users });
  } catch (error) {
    logger.error('Failed to get share targets', { error, userId: req.session.userId });
    next(createInternalError('Failed to retrieve share targets'));
  }
});

/**
 * POST /api/playlists/:id/share
 * Share a playlist with another Playlist Lab user (copy playlist to their account)
 */
router.post('/:id/share', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const currentUserId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = (req.params as Record<string, string>).id;
    const { targetUserId } = req.body;

    if (!targetUserId) {
      return next(createValidationError('targetUserId is required'));
    }

    const targetUserIdNum = parseInt(targetUserId, 10);
    if (isNaN(targetUserIdNum)) {
      return next(createValidationError('Invalid target user ID'));
    }

    // Get source user (playlist owner) and target user
    const targetUser = db.getUserById(targetUserIdNum);
    if (!targetUser) {
      return next(createNotFoundError('Target user not found'));
    }

    const targetUserServer = db.getUserServer(targetUserIdNum);
    if (!targetUserServer) {
      return next(createValidationError('Target user has no server configured'));
    }

    if (!targetUserServer.library_id) {
      return next(createValidationError('Target user has no library selected'));
    }

    // Get the playlist details and tracks from the source
    // We need to determine who owns this playlist
    // For now, we'll fetch it using the current user's credentials
    const currentUser = db.getUserById(currentUserId);
    if (!currentUser) {
      return next(createNotFoundError('Current user not found'));
    }

    const currentUserServer = db.getUserServer(currentUserId);
    if (!currentUserServer) {
      return next(createValidationError('No server selected'));
    }

    // Get playlist details and tracks
    const plexService = new PlexService(currentUserServer.server_url, resolvePlexToken(currentUser, currentUserServer));
    const tracks = await plexService.getPlaylistTracks(playlistId);
    
    if (tracks.length === 0) {
      return next(createValidationError('Cannot share empty playlist'));
    }

    // Get playlist name from the first request
    const playlists = await plexService.getPlaylists();
    const sourcePlaylist = playlists.find(p => p.ratingKey === playlistId);
    
    if (!sourcePlaylist) {
      return next(createNotFoundError('Playlist not found'));
    }

    // Create the playlist in the target user's account
    const targetPlexService = new PlexService(targetUserServer.server_url, resolvePlexToken(targetUser, targetUserServer));
    const libraryUri = `server://${targetUserServer.server_client_id}/com.plexapp.plugins.library/library/sections/${targetUserServer.library_id}`;
    
    // Build track URIs
    const trackUris = tracks.map(t => t.key);
    
    // Create the new playlist
    const newPlaylist = await targetPlexService.createPlaylist(
      sourcePlaylist.title,
      libraryUri,
      trackUris
    );

    // Save to database
    db.createPlaylist(
      targetUserIdNum,
      newPlaylist.ratingKey,
      sourcePlaylist.title,
      'shared',
      undefined
    );

    // Record the share so it shows up in the recipient's "Shared With Me" list.
    const sourcePlaylistRecord = db.getPlaylistByPlexId(currentUserId, playlistId)
      ?? db.createPlaylist(currentUserId, playlistId, sourcePlaylist.title, 'plex', undefined);
    db.recordPlaylistShare(
      sourcePlaylistRecord.id,
      currentUserId,
      targetUserIdNum,
      playlistId,
      sourcePlaylist.title
    );

    logger.info('Playlist shared', {
      playlistId, 
      playlistName: sourcePlaylist.title,
      fromUserId: currentUserId, 
      toUserId: targetUserIdNum,
      trackCount: tracks.length 
    });

    res.json({ 
      success: true,
      playlistName: sourcePlaylist.title,
      trackCount: tracks.length
    });
  } catch (error) {
    logger.error('Failed to share playlist', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to share playlist'));
  }
});

/**
 * POST /api/playlists/:id/share-to-friend
 * Share a playlist with a Plex friend (user with library access)
 */
router.post('/:id/share-to-friend', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const playlistId = parseInt((req.params as Record<string, string>).id);
    const { friendUsername } = req.body;
    const currentUserId = req.session.userId!;

    if (!friendUsername) {
      return next(createValidationError('Friend username is required'));
    }

    // Get the playlist
    const playlist = db.getPlaylistById(playlistId);
    if (!playlist) {
      return next(createValidationError('Playlist not found'));
    }

    // Verify ownership
    if (playlist.user_id !== currentUserId) {
      return next(createValidationError('You can only share your own playlists'));
    }

    // Get user's server config
    const userServer = db.getUserServer(currentUserId);
    if (!userServer) {
      return next(createValidationError('Server configuration not found'));
    }

    // Initialize Plex service
    const plexService = new PlexService(
      userServer.server_url,
      resolvePlexToken({ plex_token: req.user!.plexToken }, userServer),
      userServer.library_id
    );

    // Share the playlist via Plex API (creates a copy in the friend's account)
    await plexService.sharePlaylist(playlist.plex_playlist_id, friendUsername);

    // Check if the friend is a Playlist Lab user and record the share
    try {
      // Try to find the friend in our users table
      const friendUser = db.getUserByPlexUsername(friendUsername);
      if (friendUser) {
        // Record the share in the database so it shows in their "Shared Playlists" tab
        db.recordPlaylistShare(
          playlist.id,
          currentUserId,
          friendUser.id,
          playlist.plex_playlist_id,
          playlist.name
        );
        logger.info('Recorded playlist share in database', { 
          playlistId, 
          friendUserId: friendUser.id 
        });
      }
    } catch (err) {
      // Friend is not a Playlist Lab user, that's okay - they still got the playlist in Plex
      logger.info('Friend is not a Playlist Lab user, share not recorded in database', { 
        friendUsername 
      });
    }

    logger.info('Playlist shared with friend', { 
      playlistId, 
      playlistName: playlist.name,
      friendUsername 
    });

    res.json({ 
      success: true,
      playlistName: playlist.name
    });
  } catch (error: any) {
    logger.error('Failed to share playlist with friend', { 
      error: error.message, 
      userId: req.session.userId, 
      playlistId: (req.params as Record<string, string>).id 
    });
    next(createInternalError(error.message || 'Failed to share playlist'));
  }
});

/**
 * POST /api/playlists/copy-to-managed-user
 * Copy a playlist from one managed user to another
 */
router.post('/copy-to-managed-user', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const { sourceUserId, targetUserId, playlistId } = req.body;

    if (!sourceUserId || !targetUserId || !playlistId) {
      return next(createValidationError('Source user ID, target user ID, and playlist ID are required'));
    }

    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    logger.info('Copying playlist between managed users', { 
      sourceUserId, 
      targetUserId, 
      playlistId 
    });

    // Get source user's token - direct axios (plex.tv, not the user's own
    // PlexClient instance), so route it through plexLimiter by hand like
    // every other Plex call in the app.
    const sourceTokenResponse = await plexLimiter.run(() => axios.post(
      `https://plex.tv/api/v2/home/users/${sourceUserId}/switch`,
      {},
      {
        headers: {
          'Accept': 'application/json',
          'X-Plex-Token': user.plex_token
        }
      }
    ));
    const sourceToken = sourceTokenResponse.data.authToken;

    // Get target user's token
    const targetTokenResponse = await plexLimiter.run(() => axios.post(
      `https://plex.tv/api/v2/home/users/${targetUserId}/switch`,
      {},
      {
        headers: {
          'Accept': 'application/json',
          'X-Plex-Token': user.plex_token
        }
      }
    ));
    const targetToken = targetTokenResponse.data.authToken;

    // Get user's server
    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createValidationError('Server configuration not found'));
    }

    // Get playlist details from source user
    const sourcePlexService = new PlexService(userServer.server_url, sourceToken);
    const playlist = await sourcePlexService.getPlaylistDetails(playlistId);
    
    if (!playlist) {
      return next(createNotFoundError('Playlist not found'));
    }

    // Get playlist items
    const items = await sourcePlexService.getPlaylistTracks(playlistId);

    logger.info('Retrieved playlist details', { 
      title: playlist.title, 
      itemCount: items.length 
    });

    // Create playlist in target user's account
    const targetPlexService = new PlexService(userServer.server_url, targetToken);
    const machineId = await sourcePlexService.getMachineIdentifier();
    
    // Build track URIs
    const trackUris = items.map((item: any) => 
      sourcePlexService.buildTrackUri(item.ratingKey, machineId)
    );
    
    // Get library URI
    const libraryUri = sourcePlexService.buildLibraryUri(userServer.library_id!, machineId);

    // Delete existing playlist with same name if it exists
    try {
      const existingPlaylists = await targetPlexService.getPlaylists();
      const existingPlaylist = existingPlaylists.find(p => p.title === playlist.title);
      if (existingPlaylist) {
        logger.info('Deleting existing playlist in target user account', { 
          title: playlist.title 
        });
        await targetPlexService.deletePlaylist(existingPlaylist.ratingKey);
      }
    } catch (err) {
      logger.warn('Could not check for existing playlist', { error: err });
    }

    // Create the new playlist
    const newPlaylist = await targetPlexService.createPlaylist(playlist.title, libraryUri, trackUris);

    logger.info('Playlist copied successfully', { 
      playlistId: newPlaylist.ratingKey,
      title: playlist.title,
      trackCount: items.length
    });

    res.json({
      success: true,
      playlistId: newPlaylist.ratingKey,
      playlistName: playlist.title,
      trackCount: items.length
    });
  } catch (error: any) {
    logger.error('Failed to copy playlist between managed users', { 
      error: error.message 
    });
    next(createInternalError(error.message || 'Failed to copy playlist'));
  }
});

/**
 * POST /api/playlists/:id/cover
 * Upload cover image for playlist
 */
router.post('/:id/cover', requireAuth, upload.single('cover'), async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = (req.params as Record<string, string>).id; // Keep as string for Plex

    if (!req.file) {
      return next(createValidationError('No file uploaded'));
    }

    // Get user and server info
    const user = db.getUserById(userId);
    if (!user) {
      return next(createNotFoundError('User not found'));
    }

    const userServer = db.getUserServer(userId);
    if (!userServer) {
      return next(createNotFoundError('No server configured'));
    }

    // Upload to Plex
    // Plex expects the image to be uploaded via POST to /library/metadata/{ratingKey}/posters
    const coverToken = resolvePlexToken(user, userServer);
    const uploadUrl = `${userServer.server_url}/library/metadata/${playlistId}/posters?X-Plex-Token=${coverToken}`;

    const formData = new FormData();
    formData.append('file', req.file.buffer, {
      filename: req.file.originalname,
      contentType: req.file.mimetype,
    });

    await plexLimiter.run(() => axios.post(uploadUrl, formData, {
      headers: {
        ...formData.getHeaders(),
        'X-Plex-Token': coverToken,
      },
      maxBodyLength: Infinity,
      maxContentLength: Infinity,
    }));

    logger.info('Playlist cover uploaded', { userId, playlistId });
    res.json({ success: true, message: 'Cover uploaded successfully' });
  } catch (error: any) {
    logger.error('Failed to upload playlist cover', { 
      error: error.message, 
      userId: req.session.userId, 
      playlistId: (req.params as Record<string, string>).id 
    });
    next(createInternalError('Failed to upload cover'));
  }
});

/**
 * PUT /api/playlists/:id/tracks/:trackId/move
 * Reorder a track in the playlist
 */
router.put('/:id/tracks/:trackId/move', requireAuth, async (req: Request, res: Response, next: NextFunction) => {
  try {
    const userId = req.session.userId!;
    const db = req.dbService!;
    const playlistId = (req.params as Record<string, string>).id;
    const { trackId } = req.params as Record<string, string>;
    const { afterId } = req.body;

    logger.info('Move track request received', { 
      playlistId, 
      trackId, 
      afterId, 
      body: req.body,
      userId 
    });

    if (afterId === undefined) {
      logger.error('afterId is undefined', { body: req.body });
      return next(createValidationError('afterId is required'));
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

    // Move track in Plex playlist
    const plexService = new PlexService(userServer.server_url, resolvePlexToken(user, userServer));
    await plexService.movePlaylistItem(playlistId, trackId, afterId);

    logger.info('Track moved in playlist', { userId, playlistId, trackId, afterId });

    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to move track in playlist', { error, userId: req.session.userId, playlistId: (req.params as Record<string, string>).id });
    next(createInternalError('Failed to move track'));
  }
});

export default router;
