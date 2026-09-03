import { Router, Request, Response, NextFunction } from 'express';
import { AuthService } from '../services/auth';
import { requireAuth } from '../middleware/auth';
import { createValidationError, createInternalError } from '../middleware/error-handler';
import { logger } from '../utils/logger';

const router = Router();
const authService = new AuthService(
  process.env.PLEX_CLIENT_ID || 'playlist-lab-server',
  'Playlist Lab'
);

// Extend session type
declare module 'express-session' {
  interface SessionData {
    userId: number;
    plexUserId: string;
  }
}

/**
 * Pick a display name for a Plex account.
 *
 * Managed (restricted) Plex Home users have no username - plex.tv returns
 * null/"" for them and only fills in `title`. plex_username is NOT NULL, so
 * passing that straight through made those logins fail with a constraint
 * error, which is why home users could not sign in.
 */
function plexDisplayName(userInfo: any): string {
  return (
    userInfo.username ||
    userInfo.title ||
    userInfo.friendlyName ||
    userInfo.email ||
    `plex-${userInfo.id}`
  );
}

/**
 * Handle user login: create/update user, auto-admin first user,
 * check Plex Home / friend membership, auto-assign server config
 */
async function handleUserLogin(
  db: any,
  plexUserId: string,
  username: string,
  authToken: string,
  thumb?: string
) {
  // Create or update user in database
  let user = db.getUserByPlexId(plexUserId);
  const isNewUser = !user;

  if (!user) {
    user = db.createUser(plexUserId, username, authToken, thumb);
    logger.info('New user created', { plexUserId, username });
  } else {
    db.updateUserLogin(user.id);
    db.updateUserToken(user.id, authToken);
    // Refresh name/avatar on every login so accounts created before we had a
    // usable name (or renamed since) heal themselves instead of staying blank.
    db.updateUserProfile(user.id, username, thumb);
    logger.info('User logged in', { plexUserId, username });
  }

  // First user ever becomes admin automatically
  const userCount = db.getUserCount();
  if (userCount === 1) {
    db.addAdmin(user.id);
    logger.info('First user auto-promoted to admin', { userId: user.id, username });
  }

  // Re-verify access for every non-admin login, not just the first one.
  // Approved = a member of the admin's Plex Home (including managed users)
  // or one of the admin's Plex friends. Re-checking on each login means a
  // user removed from Plex Home is disabled on their next login rather than
  // retaining access indefinitely.
  if (!db.isAdmin(user.id)) {
    const admin = db.getFirstAdmin();
    if (admin) {
      try {
        // Two lists because neither covers everyone: /home/users has managed
        // users but no friends, /api/users has friends but no managed users.
        const [homeUsers, friends] = await Promise.all([
          authService.getHomeUsers(admin.plex_token),
          authService.getFriends(admin.plex_token).catch((err) => {
            logger.warn('Failed to fetch Plex friends, falling back to Plex Home only', {
              error: err instanceof Error ? err.message : err,
            });
            return [];
          }),
        ]);

        const approvedIds = new Set(
          [...(homeUsers ?? []), ...(friends ?? [])].map((u: any) => u.id.toString())
        );
        const isApproved = approvedIds.has(plexUserId);

        if (isApproved) {
          // Give them the admin's server config if they have none yet. Keyed
          // off "has no server" rather than "is new" so a user who was
          // created before the admin configured a server still gets one.
          if (!db.getUserServer(user.id)) {
            db.copyServerConfig(admin.id, user.id);
            logger.info('Approved Plex user auto-assigned server config', {
              userId: user.id,
              username,
              adminId: admin.id,
            });
          }
          // Re-enable in case they were previously disabled and have since
          // been (re-)added to Plex Home or friends.
          if (!db.isUserEnabled(user.id)) {
            db.enableUser(user.id);
            logger.info('User re-enabled after Plex membership re-verification', {
              userId: user.id,
              username,
            });
          }
        } else if (db.isUserEnabled(user.id)) {
          // Neither a Plex Home member nor a friend - disable (whether newly
          // created or previously enabled and since removed).
          db.disableUser(user.id);
          logger.info('User disabled: not in Plex Home and not a friend', {
            userId: user.id,
            username,
            isNewUser,
          });
        }
      } catch (err) {
        logger.error('Failed to verify Plex Home/friend membership', {
          error: err instanceof Error ? err.message : err,
          userId: user.id,
        });
        // A brand-new account can't be verified, so don't hand it access on
        // the strength of a failed lookup - leave it for the admin to
        // approve. Existing users keep whatever state they already had.
        if (isNewUser && db.isUserEnabled(user.id)) {
          db.disableUser(user.id);
        }
      }
    }
  }

  // Check if user is enabled
  if (!db.isUserEnabled(user.id) && !db.isAdmin(user.id)) {
    return { user, enabled: false };
  }

  return { user, enabled: true };
}

/**
 * POST /api/auth/start
 * Initiate Plex PIN-based OAuth flow
 */
router.post('/start', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const pin = await authService.startAuth();

    // Build a forwardUrl so Plex redirects the popup back to our callback page
    // after the user signs in. The callback notifies the parent and auto-closes.
    const origin = req.headers.origin || `${req.protocol}://${req.headers.host}`;
    const forwardUrl = `${origin}/auth/callback`;

    res.json({
      id: pin.id,
      code: pin.code,
      authUrl: authService.getAuthUrl(pin.code, forwardUrl),
    });
  } catch (error) {
    logger.error('Failed to start auth', { error });
    next(createInternalError('Failed to initiate authentication'));
  }
});

/**
 * POST /api/auth/poll
 * Poll for PIN authentication completion
 */
router.post('/poll', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { pinId, code } = req.body;

    if (!pinId || !code) {
      return next(createValidationError('pinId and code are required'));
    }

    const pin = await authService.pollAuth(pinId, code);

    if (!pin) {
      return res.json({ authenticated: false, expired: true });
    }

    if (!pin.authToken) {
      return res.json({ authenticated: false });
    }

    // Get user info from Plex
    const userInfo = await authService.getUserInfo(pin.authToken);
    const db = req.dbService!;

    const { user, enabled } = await handleUserLogin(
      db,
      userInfo.id.toString(),
      plexDisplayName(userInfo),
      pin.authToken,
      userInfo.thumb
    );

    if (!enabled) {
      // Logged at error level, not warn: this is a rejected access attempt an
      // admin needs to see, and the log level is routinely turned down to
      // error-only - at warn it was written nowhere at all.
      logger.error('Login denied: user not approved', { userId: user.id, plexUserId: user.plex_user_id, username: userInfo.username, displayName: plexDisplayName(userInfo), email: userInfo.email });
      return res.json({
        authenticated: false,
        denied: true,
        message: 'Your account has not been approved. Please contact the server admin.',
      });
    }

    // Create session
    req.session.userId = user.id;
    req.session.plexUserId = user.plex_user_id;

    logger.info('Creating session for user', { 
      userId: user.id, 
      plexUserId: user.plex_user_id,
      sessionId: req.sessionID,
      cookie: req.session.cookie
    });

    // Regenerate session to ensure clean state
    const oldSessionId = req.sessionID;
    req.session.regenerate((regenerateErr) => {
      if (regenerateErr) {
        logger.error('Failed to regenerate session:', regenerateErr);
        // Continue anyway with existing session
      } else {
        logger.info('Session regenerated', { oldId: oldSessionId, newId: req.sessionID });
      }
      
      // Set session data again after regeneration
      req.session.userId = user.id;
      req.session.plexUserId = user.plex_user_id;

      // Explicitly save session before responding
      req.session.save((err) => {
        if (err) {
          logger.error('Failed to save session:', err);
          return next(createInternalError('Failed to create session'));
        }

        logger.info('Session saved successfully', { 
          sessionId: req.sessionID,
          userId: user.id,
          cookieValue: req.session.cookie
        });

        // Verify session was actually saved by reloading it
        req.session.reload((reloadErr) => {
          if (reloadErr) {
            logger.error('Failed to reload session after save:', reloadErr);
            return next(createInternalError('Session verification failed'));
          }

          if (!req.session.userId) {
            logger.error('Session reload succeeded but userId is missing!');
            return next(createInternalError('Session data not persisted'));
          }

          logger.info('Session verified successfully', {
            sessionId: req.sessionID,
            userId: req.session.userId
          });

          res.json({
            authenticated: true,
            user: {
              id: user.id,
              plexUserId: user.plex_user_id,
              plexUsername: user.plex_username,
              plexThumb: user.plex_thumb,
              isAdmin: db.isAdmin(user.id),
            },
          });
        });
      });
    });
  } catch (error) {
    logger.error('Failed to poll auth', { error });
    next(createInternalError('Failed to complete authentication'));
  }
});

/**
 * POST /api/auth/token
 * Authenticate using a Plex token directly (for 2FA users)
 */
router.post('/token', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { token } = req.body;

    if (!token || typeof token !== 'string') {
      return next(createValidationError('Token is required'));
    }

    // Validate token by getting user info from Plex
    const userInfo = await authService.getUserInfo(token);
    const db = req.dbService!;

    const { user, enabled } = await handleUserLogin(
      db,
      userInfo.id.toString(),
      plexDisplayName(userInfo),
      token,
      userInfo.thumb
    );

    if (!enabled) {
      logger.error('Login denied: user not approved', { userId: user.id, plexUserId: user.plex_user_id, username: userInfo.username, displayName: plexDisplayName(userInfo), email: userInfo.email });
      return res.json({
        success: false,
        denied: true,
        message: 'Your account has not been approved. Please contact the server admin.',
      });
    }

    // Create session
    req.session.userId = user.id;
    req.session.plexUserId = user.plex_user_id;

    logger.info('Creating session for user (token auth)', { 
      userId: user.id, 
      plexUserId: user.plex_user_id,
      sessionId: req.sessionID 
    });

    // Explicitly save session before responding
    req.session.save((err) => {
      if (err) {
        logger.error('Failed to save session:', err);
        return next(createInternalError('Failed to create session'));
      }

      logger.info('Session saved successfully (token auth)', { 
        sessionId: req.sessionID,
        userId: user.id 
      });

      res.json({
        success: true,
        user: {
          id: user.id,
          plexUserId: user.plex_user_id,
          plexUsername: user.plex_username,
          plexThumb: user.plex_thumb,
          isAdmin: db.isAdmin(user.id),
        },
      });
    });
  } catch (error) {
    logger.error('Failed to authenticate with token', { error });
    if (error instanceof Error && error.message.includes('Invalid or expired')) {
      return next(createValidationError('Invalid or expired Plex token'));
    }
    next(createInternalError('Failed to authenticate with token'));
  }
});

/**
 * POST /api/auth/logout
 * Destroy user session
 */
router.post('/logout', (req: Request, res: Response, next: NextFunction) => {
  req.session.destroy((err) => {
    if (err) {
      logger.error('Failed to destroy session', { error: err });
      return next(createInternalError('Failed to logout'));
    }
    res.json({ success: true });
  });
});

/**
 * GET /api/auth/me
 * Get current authenticated user info
 */
router.get('/me', requireAuth, (req: Request, res: Response, next: NextFunction) => {
  try {
    logger.info('Auth check request', { 
      sessionId: req.sessionID,
      userId: req.session.userId,
      hasSession: !!req.session,
      hasCookie: !!req.headers.cookie
    });

    const userId = req.session.userId!;
    const db = req.dbService!;
    const user = db.getUserById(userId);

    if (!user) {
      return next(createInternalError('User not found'));
    }

    res.json({
      id: user.id,
      plexUserId: user.plex_user_id,
      plexUsername: user.plex_username,
      plexThumb: user.plex_thumb,
      isAdmin: db.isAdmin(user.id),
    });
  } catch (error) {
    logger.error('Failed to get user info', { error });
    next(createInternalError('Failed to retrieve user information'));
  }
});

export default router;
