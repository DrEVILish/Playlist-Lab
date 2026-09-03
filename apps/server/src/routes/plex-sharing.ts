import { Router, Request, Response, NextFunction } from 'express';
import { requireAuth } from '../middleware/auth';
import { PlexClient, resolvePlexToken } from '../services/plex';
import { createInternalError, createValidationError } from '../middleware/error-handler';
import { logger } from '../utils/logger';

const router = Router();

// All routes require authentication
router.use(requireAuth);

/**
 * GET /api/plex/friends
 * Get list of Plex friends (users with library access)
 */
router.get('/friends', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const userServer = db.getUserServer(req.user!.id);
    
    if (!userServer) {
      return res.json({ friends: [] });
    }

    const plexClient = new PlexClient(
      userServer.server_url,
      resolvePlexToken({ plex_token: req.user!.plexToken }, userServer)
    );

    // Get friends from Plex
    const friends = await plexClient.getFriends();
    
    return res.json({ friends });
  } catch (error) {
    logger.error('Failed to get Plex friends', { error });
    return next(createInternalError('Failed to retrieve Plex friends'));
  }
});

/**
 * GET /api/plex/server-users
 * Get list of users who have access to the Plex server
 */
router.get('/server-users', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const userServer = db.getUserServer(req.user!.id);
    
    if (!userServer) {
      return next(createValidationError('No Plex server configured'));
    }

    const plexClient = new PlexClient(
      userServer.server_url,
      resolvePlexToken({ plex_token: req.user!.plexToken }, userServer)
    );

    // Get users with library access from Plex
    const users = await plexClient.getServerUsers();
    
    res.json({ users });
  } catch (error) {
    logger.error('Failed to get Plex server users', { error });
    next(createInternalError('Failed to retrieve Plex server users'));
  }
});

/**
 * POST /api/plex/share-playlist
 * Share a playlist with a Plex server user
 */
router.post('/share-playlist', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { playlistId, targetUsername } = req.body;

    if (!playlistId || !targetUsername) {
      return next(createValidationError('Missing playlistId or targetUsername'));
    }

    const db = req.dbService!;
    const userServer = db.getUserServer(req.user!.id);
    
    if (!userServer) {
      return next(createValidationError('No Plex server configured'));
    }

    const plexClient = new PlexClient(
      userServer.server_url,
      resolvePlexToken({ plex_token: req.user!.plexToken }, userServer)
    );

    // Share the playlist on Plex server
    await plexClient.sharePlaylist(playlistId, targetUsername);
    
    logger.info('Playlist shared with Plex user', { 
      playlistId, 
      targetUsername, 
      userId: req.user!.id 
    });

    res.json({ 
      success: true,
      message: `Playlist shared with ${targetUsername}`
    });
  } catch (error) {
    logger.error('Failed to share playlist', { error });
    next(createInternalError('Failed to share playlist'));
  }
});

/**
 * GET /api/plex/friends/:username/playlists
 * Get playlists from a friend's account
 */
router.get('/friends/:username/playlists', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { username } = req.params as Record<string, string>;
    logger.info('Getting playlists for friend', { username, userId: req.user!.id });
    
    const db = req.dbService!;
    const userServer = db.getUserServer(req.user!.id);
    
    if (!userServer) {
      logger.warn('No Plex server configured for user', { userId: req.user!.id });
      return next(createValidationError('No Plex server configured'));
    }

    const plexClient = new PlexClient(
      userServer.server_url,
      resolvePlexToken({ plex_token: req.user!.plexToken }, userServer)
    );

    // Get friend's playlists
    const playlists = await plexClient.getFriendPlaylists(username);
    
    logger.info('Successfully retrieved friend playlists', { 
      username, 
      playlistCount: playlists.length 
    });
    
    res.json({ playlists });
  } catch (error: any) {
    logger.error('Failed to get friend playlists', { 
      username: req.params.username,
      error: error.message,
      stack: error.stack
    });
    next(createInternalError(error.message || 'Failed to retrieve friend playlists'));
  }
});

/**
 * GET /api/plex/shared-playlists
 * Get list of playlists that have been shared with friends
 * 
 * This actually retrieves playlists from friends' accounts to see what they have.
 * You can identify your shared playlists by looking for playlists with matching names.
 */
router.get('/shared-playlists', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const userServer = db.getUserServer(req.user!.id);
    
    if (!userServer) {
      return res.json({ friendPlaylists: [] });
    }

    const plexClient = new PlexClient(
      userServer.server_url,
      resolvePlexToken({ plex_token: req.user!.plexToken }, userServer)
    );

    // Get friends list
    const friends = await plexClient.getFriends();
    
    // Get playlists for each friend
    const friendPlaylists: Array<{
      username: string;
      friendlyName: string;
      playlists: Array<{
        playlistId: string;
        playlistName: string;
        trackCount: number;
        duration: number;
      }>;
    }> = [];
    
    // Each friend's playlist list is an independent Plex API call - fetch
    // them concurrently instead of one at a time.
    const results = await Promise.allSettled(
      friends.map(friend => plexClient.getFriendPlaylists(friend.username))
    );
    results.forEach((result, i) => {
      const friend = friends[i];
      if (result.status === 'fulfilled') {
        friendPlaylists.push({
          username: friend.username,
          friendlyName: friend.friendlyName || friend.username,
          playlists: result.value
        });
      } else {
        logger.warn('Failed to get playlists for friend', {
          username: friend.username,
          error: result.reason?.message
        });
        // Continue with other friends
      }
    });
    
    return res.json({ friendPlaylists });
  } catch (error) {
    logger.error('Failed to get friend playlists', { error });
    return next(createInternalError('Failed to retrieve friend playlists'));
  }
});

export default router;
