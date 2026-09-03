/**
 * Authentication Middleware
 * 
 * Provides middleware functions for protecting routes and checking authentication.
 * Integrates with express-session for session management.
 */

import { Request, Response, NextFunction } from 'express';
import { DatabaseService } from '../database/database';
import { logger } from '../utils/logger';

/**
 * Extend Express Request to include user and session data
 */
declare global {
  namespace Express {
    interface Request {
      user?: {
        id: number;
        plexUserId: string;
        plexUsername: string;
        plexToken: string;
        plexThumb?: string;
      };
      dbService?: DatabaseService;
    }

    interface SessionData {
      userId?: number;
      plexUserId?: string;
    }
  }
}

/**
 * Middleware to attach database service to request
 */
export function attachDatabase(dbService: DatabaseService) {
  return (req: Request, _res: Response, next: NextFunction) => {
    req.dbService = dbService;
    next();
  };
}

/**
 * Middleware to require authentication
 * Returns 401 if user is not authenticated
 */
export function requireAuth(req: Request, res: Response, next: NextFunction): void {
  // ponytail: dev-only bypass for reviewing UI without logging in, never
  // live in production (requires both an explicit opt-in flag and a
  // non-production NODE_ENV). Logs in as the first user in the DB.
  if (!req.session.userId && process.env.DEV_NO_AUTH === 'true' && process.env.NODE_ENV !== 'production' && req.dbService) {
    const firstUser = req.dbService.getFirstUser();
    if (firstUser) {
      req.session.userId = firstUser.id;
    }
  }

  if (!req.session.userId) {
    // Every request used to log four console.log lines here regardless of
    // outcome - outside winston, so no level could turn them off, and one of
    // them printed a prefix of the session id, which is the credential
    // itself. The only part with debugging value is why a request was
    // rejected, which is what's left: whether a cookie arrived at all
    // separates "not logged in" from "cookie sent but session gone".
    logger.debug('[Auth] Rejected unauthenticated request', {
      method: req.method,
      path: req.path,
      hadCookie: !!req.headers.cookie,
    });
    res.status(401).json({
      error: {
        code: 'AUTH_REQUIRED',
        message: 'Authentication required',
        statusCode: 401
      }
    });
    return;
  }

  // Load user from database
  if (!req.dbService) {
    console.error('[Auth] Database service not available');
    res.status(500).json({
      error: {
        code: 'INTERNAL_ERROR',
        message: 'Database service not available',
        statusCode: 500
      }
    });
    return;
  }

  const user = req.dbService.getUserById(req.session.userId);
  
  if (!user) {
    // Session references non-existent user, clear it
    logger.warn('[Auth] Session references a user that no longer exists, destroying it', { userId: req.session.userId });
    req.session.destroy((err) => {
      if (err) {
        logger.error('[Auth] Failed to destroy invalid session', { error: err.message });
      }
    });

    res.status(401).json({
      error: {
        code: 'AUTH_INVALID',
        message: 'Invalid session',
        statusCode: 401
      }
    });
    return;
  }

  // Attach user to request
  req.user = {
    id: user.id,
    plexUserId: user.plex_user_id,
    plexUsername: user.plex_username,
    plexToken: user.plex_token,
    plexThumb: user.plex_thumb
  };

  // Check if user is enabled (admins always pass)
  if (!req.dbService.isAdmin(user.id) && !req.dbService.isUserEnabled(user.id)) {
    logger.warn('[Auth] Rejected disabled user', { userId: user.id });
    res.status(403).json({
      error: {
        code: 'USER_DISABLED',
        message: 'Your account has been disabled. Contact the server admin.',
        statusCode: 403
      }
    });
    return;
  }

  next();
}

/**
 * Middleware to require admin privileges
 * Must be used after requireAuth
 */
export function requireAdmin(req: Request, res: Response, next: NextFunction): void {
  if (!req.user || !req.dbService) {
    res.status(401).json({
      error: {
        code: 'AUTH_REQUIRED',
        message: 'Authentication required',
        statusCode: 401
      }
    });
    return;
  }

  const isAdmin = req.dbService.isAdmin(req.user.id);

  if (!isAdmin) {
    res.status(403).json({
      error: {
        code: 'ADMIN_REQUIRED',
        message: 'Admin privileges required',
        statusCode: 403
      }
    });
    return;
  }

  next();
}

/**
 * Optional authentication middleware
 * Attaches user if authenticated, but doesn't require it
 */
export function optionalAuth(req: Request, _res: Response, next: NextFunction): void {
  if (!req.session.userId || !req.dbService) {
    next();
    return;
  }

  const user = req.dbService.getUserById(req.session.userId);
  
  if (user) {
    req.user = {
      id: user.id,
      plexUserId: user.plex_user_id,
      plexUsername: user.plex_username,
      plexToken: user.plex_token,
      plexThumb: user.plex_thumb
    };
  }

  next();
}

