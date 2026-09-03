import { Router, Request, Response, NextFunction } from 'express';
import fs from 'fs';
import path from 'path';
import { exec } from 'child_process';
import { promisify } from 'util';
import { requireAuth, requireAdmin } from '../middleware/auth';
import { AuthService } from '../services/auth';
import { createInternalError, createValidationError } from '../middleware/error-handler';
import { logger, currentCombinedLogPaths, LOG_DIR, LOG_LEVELS, isAtLeastAsSevere } from '../utils/logger';
import { configService } from '../config';
import { resetDeemixSession, getDeemixSettings, updateDeemixSettings, findBestDeemixMatches, queueDeemixDownload, resolveDownloadUrl, startDeemixDownload, checkDeemixArl, getLastDeemixArlCheck, DEEMIX_SERVICE_NAME } from '../services/deemix';
import { transformSchedule } from './schedules';
import { updateNotification } from '../services/job-notifications';
import { enqueueAction } from '../services/action-queue';
import type { DatabaseService } from '../database/database';

const execAsync = promisify(exec);
const router = Router();

// journalctl's numeric PRIORITY (syslog severity) collapsed to this app's
// error/warn/info/debug levels, for filtering deemix-server's journal
// entries the same way as our own winston-logged ones.
function priorityToLevel(priority: string): string {
  const n = parseInt(priority, 10);
  if (n <= 3) return 'error';
  if (n === 4) return 'warn';
  if (n >= 7) return 'debug';
  return 'info';
}
const authService = new AuthService(
  process.env.PLEX_CLIENT_ID || 'playlist-lab-server',
  'Playlist Lab'
);

// All admin routes require authentication and admin privileges
router.use(requireAuth);
router.use(requireAdmin);

/**
 * GET /api/admin/stats
 * Get system statistics
 */
router.get('/stats', (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    
    const userCount = db.getUserCount();
    const playlistCount = db.getPlaylistCount();
    
    // Get active users (logged in within last 30 days)
    const thirtyDaysAgo = Date.now() - (30 * 24 * 60 * 60 * 1000);
    const allUsers = db.getAllUsers();
    const activeUsers = allUsers.filter(u => u.last_login >= thirtyDaysAgo).length;
    
    // Get missing tracks count
    const allMissing = db.getAllMissingTracks();
    const missingTracksCount = allMissing.length;
    
    res.json({
      stats: {
        totalUsers: userCount,
        activeUsers,
        totalPlaylists: playlistCount,
        totalMissingTracks: missingTracksCount,
      },
    });
  } catch (error) {
    logger.error('Failed to get admin stats', { error });
    next(createInternalError('Failed to retrieve system statistics'));
  }
});

/**
 * GET /api/admin/users
 * Get all users with their status
 */
router.get('/users', (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    
    const users = db.getAllUsers();
    
    // Include admin status and enabled status
    const sanitizedUsers = users.map(u => ({
      id: u.id,
      plexUserId: u.plex_user_id,
      plexUsername: u.plex_username,
      plexThumb: u.plex_thumb,
      createdAt: u.created_at,
      lastLogin: u.last_login,
      isAdmin: db.isAdmin(u.id),
      isEnabled: db.isUserEnabled(u.id),
      hasServer: !!db.getUserServer(u.id),
    }));
    
    res.json({ users: sanitizedUsers });
  } catch (error) {
    logger.error('Failed to get users', { error });
    next(createInternalError('Failed to retrieve users'));
  }
});

/**
 * POST /api/admin/users/:userId/enable
 * Enable a user
 */
router.post('/users/:userId/enable', (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const userId = parseInt((req.params as Record<string, string>).userId);
    
    if (isNaN(userId)) {
      return next(createValidationError('Invalid user ID'));
    }
    
    const user = db.getUserById(userId);
    if (!user) {
      return next(createValidationError('User not found'));
    }
    
    db.enableUser(userId);
    
    // Auto-assign server config from admin if user doesn't have one
    if (!db.getUserServer(userId)) {
      db.copyServerConfig(req.user!.id, userId);
    }
    
    logger.info('User enabled by admin', { userId, adminId: req.user!.id });
    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to enable user', { error });
    next(createInternalError('Failed to enable user'));
  }
});

/**
 * POST /api/admin/users/:userId/disable
 * Disable a user
 */
router.post('/users/:userId/disable', (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const userId = parseInt((req.params as Record<string, string>).userId);
    
    if (isNaN(userId)) {
      return next(createValidationError('Invalid user ID'));
    }
    
    // Can't disable yourself
    if (userId === req.user!.id) {
      return next(createValidationError('Cannot disable your own account'));
    }
    
    const user = db.getUserById(userId);
    if (!user) {
      return next(createValidationError('User not found'));
    }
    
    db.disableUser(userId);
    logger.info('User disabled by admin', { userId, adminId: req.user!.id });
    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to disable user', { error });
    next(createInternalError('Failed to disable user'));
  }
});

/**
 * DELETE /api/admin/users/:userId
 * Delete a user and all their data
 */
router.delete('/users/:userId', (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const userId = parseInt((req.params as Record<string, string>).userId);
    
    if (isNaN(userId)) {
      return next(createValidationError('Invalid user ID'));
    }
    
    // Can't delete yourself
    if (userId === req.user!.id) {
      return next(createValidationError('Cannot delete your own account'));
    }
    
    const user = db.getUserById(userId);
    if (!user) {
      return next(createValidationError('User not found'));
    }
    
    db.deleteUser(userId);
    logger.info('User deleted by admin', { userId, username: user.plex_username, adminId: req.user!.id });
    res.json({ success: true });
  } catch (error) {
    logger.error('Failed to delete user', { error });
    next(createInternalError('Failed to delete user'));
  }
});

/**
 * GET /api/admin/home-users
 * Get Plex Home users from the admin's Plex account
 */
router.get('/home-users', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const homeUsers = await authService.getHomeUsers(req.user!.plexToken);
    
    res.json({
      homeUsers: homeUsers.map(u => ({
        id: u.id,
        title: u.title,
        username: u.username,
        thumb: u.thumb,
        admin: u.admin,
        restricted: u.restricted,
        guest: u.guest,
      })),
    });
  } catch (error) {
    logger.error('Failed to get Plex Home users', { error });
    next(createInternalError('Failed to retrieve Plex Home users'));
  }
});

/**
 * GET /api/admin/missing
 * Get all missing tracks across all users, aggregated by track
 */
router.get('/missing', (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    
    const missingTrackStats = db.getMissingTrackStats();
    
    res.json({ 
      missingTracks: missingTrackStats,
      totalCount: missingTrackStats.length,
    });
  } catch (error) {
    logger.error('Failed to get missing tracks', { error });
    next(createInternalError('Failed to retrieve missing tracks'));
  }
});

/**
 * Searches deemix for one aggregated "most common missing track" row and
 * queues everything that clears the match gate, returning how many
 * downloads were queued (0 = nothing good enough was found). Shared by the
 * single-row button and the bulk job below so both apply the same gate and
 * the same Various-Artists handling.
 *
 * "Various Artists" is a compilation placeholder, not a real performer -
 * searching for it by name would just find tracks credited to a (real,
 * unrelated) artist literally called "Various Artists". So those rows are
 * searched by title alone and the top few matches are all queued, since one
 * of the several artists who actually recorded a same-titled track is far
 * more likely to be right than a single guess.
 */
async function queueDeemixForStat(
  db: DatabaseService,
  adminId: number,
  title: string,
  artist: string,
  matchingSettings: any
): Promise<number> {
  const isVariousArtists = artist.trim().toLowerCase() === 'various artists';
  // Candidates are scored against the admin's own matching settings (the
  // same gate the per-user Deemix button applies) so a track that simply
  // isn't in Deezer's catalogue doesn't get the nearest unrelated hit
  // downloaded into the library instead.
  const candidates = await findBestDeemixMatches(
    title,
    isVariousArtists ? '' : artist,
    matchingSettings,
    isVariousArtists ? 5 : 1
  );
  if (candidates.length === 0) return 0;

  for (const { match, score } of candidates) {
    const downloadUrl = await resolveDownloadUrl(match);
    const isFullAlbum = downloadUrl !== match.link;
    const queued = await queueDeemixDownload(downloadUrl, match.link);

    startDeemixDownload({
      db,
      userId: adminId,
      title,
      detail: `${match.artist.name}${isFullAlbum ? ' · full album' : ''} · ${Math.round(score)}% match`,
      uuid: queued.uuid,
      alreadyQueued: queued.alreadyQueued,
    });
  }

  logger.info('[Admin] Queued deemix download(s) for missing track', {
    adminId, title, artist, queued: candidates.length, variousArtists: isVariousArtists,
  });
  return candidates.length;
}

/**
 * POST /api/admin/missing/deemix-download
 * Search deemix for a "most common missing track" row and queue it for
 * download. These rows are aggregated across users by title+artist (see
 * db.getMissingTrackStats()), not a single user's missing_track id, so this
 * takes the title/artist directly instead of reusing /api/missing/:id.
 *
 * "Various Artists" is a compilation placeholder, not a real performer -
 * searching for it by name would just search for tracks credited to a
 * (real, unrelated) artist literally named "Various Artists". Instead this
 * downloads the top 5 title-only search results, since one of the several
 * artists who actually recorded a same-titled track is far more likely to
 * be the right one than a single guess.
 */
router.post('/missing/deemix-download', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { title, artist } = req.body;
    if (!title || typeof title !== 'string') {
      return next(createValidationError('title is required and must be a string'));
    }

    const adminId = req.session.userId!;
    const db = req.dbService!;
    const matchingSettings = db.getUserSettings(adminId).matching_settings;

    // Search + queue-add is bounded work but does a few sequential HTTP
    // round trips before it can respond - routed through the shared action
    // queue instead of running immediately so a burst of these (e.g. the
    // "Most Common Missing Tracks" bulk button) doesn't fire them all at
    // once. Each actual download still gets its own 'deemix' notification
    // (tracked via trackDeemixDownload) same as before - this job's own
    // notification only covers the search-and-enqueue step.
    const { jobId, position } = enqueueAction(adminId, `Deemix search: ${title}`, async (notificationId) => {
      try {
        const queued = await queueDeemixForStat(db, adminId, title, artist || '', matchingSettings);
        if (queued === 0) {
          updateNotification(adminId, notificationId, { status: 'error', detail: `No deemix match good enough for "${artist ? `${artist} - ` : ''}${title}"` });
          return;
        }
        updateNotification(adminId, notificationId, { status: 'success', detail: `Queued ${queued} download(s)` });
      } catch (error: any) {
        logger.error('[Admin] Failed to queue deemix download', { error: error?.message, stack: error?.stack, adminId, title, artist });
        updateNotification(adminId, notificationId, { status: 'error', detail: `${artist ? `${artist} - ` : ''}${title}: ${error?.message || 'Failed to queue deemix download'}` });
      }
    });

    res.json({ success: true, queued: true, jobId, position });
  } catch (error: any) {
    logger.error('[Admin] Failed to queue deemix download', { error: error?.message, stack: error?.stack, adminId: req.session.userId });
    next(createInternalError(`Failed to queue deemix download: ${error?.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/admin/missing/deemix-all
 * Queues a whole list of "most common missing track" rows as ONE job.
 *
 * The browser used to loop this endpoint's single-track sibling once per
 * row and then report "Queued N track(s)" as a finished action - which read
 * as complete while every one of those N searches and downloads was still
 * to happen. One job here means one notification that stays in progress
 * until the searches really are done, with the per-download notifications
 * startDeemixDownload() opens underneath it as before.
 */
router.post('/missing/deemix-all', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const tracks = req.body?.tracks;
    if (!Array.isArray(tracks) || tracks.length === 0) {
      return next(createValidationError('tracks must be a non-empty array of { title, artist }'));
    }

    const adminId = req.session.userId!;
    const db = req.dbService!;
    const matchingSettings = db.getUserSettings(adminId).matching_settings;

    const { jobId, position } = enqueueAction(adminId, `Deemix search: ${tracks.length} track(s)`, async (notificationId) => {
      let queued = 0;
      let unmatched = 0;
      let failed = 0;

      for (const [index, track] of tracks.entries()) {
        const title = String(track?.title || '').trim();
        if (!title) { failed++; continue; }
        const artist = String(track?.artist || '').trim();

        updateNotification(adminId, notificationId, {
          progress: Math.round((index / tracks.length) * 100),
          detail: `Searching deemix - ${index + 1} of ${tracks.length}: ${artist ? `${artist} - ` : ''}${title}`,
        });

        try {
          const count = await queueDeemixForStat(db, adminId, title, artist, matchingSettings);
          if (count > 0) queued += count; else unmatched++;
        } catch (error: any) {
          failed++;
          logger.warn('[Admin] Deemix All: failed to queue a track', { error: error?.message, adminId, title, artist });
        }
      }

      const parts = [`Queued ${queued} download(s) from ${tracks.length} track(s)`];
      if (unmatched > 0) parts.push(`${unmatched} with no good enough match`);
      if (failed > 0) parts.push(`${failed} failed`);
      updateNotification(adminId, notificationId, {
        status: failed > 0 ? 'error' : 'success',
        progress: 100,
        detail: parts.join(' · '),
      });
    });

    res.json({ success: true, jobId, position, message: 'Queued - progress is shown in the notification bell.' });
  } catch (error: any) {
    logger.error('[Admin] Failed to start Deemix All', { error: error?.message, stack: error?.stack, adminId: req.session.userId });
    next(createInternalError(`Failed to start Deemix All: ${error?.message || 'Unknown error'}`));
  }
});

// Next run time for a standard 5-field cron expression (minute hour dom month dow),
// each field either "*" or a comma-separated list of numbers - covers the
// fixed schedules of this app's 3 background jobs (including env overrides,
// as long as they stick to that same simple syntax). Brute-forces forward
// minute-by-minute rather than pulling in a cron-parsing dependency for 3
// known, infrequently-checked jobs; capped at 8 days out, well beyond any of
// their real periods (the least frequent is weekly).
function nextCronRun(expr: string, from = new Date()): number | null {
  const fields = expr.trim().split(/\s+/);
  if (fields.length !== 5) return null;
  const matchers = fields.map(f => f === '*' ? null : new Set(f.split(',').map(Number)));
  const date = new Date(from.getTime());
  date.setSeconds(0, 0);
  date.setMinutes(date.getMinutes() + 1);
  for (let i = 0; i < 8 * 24 * 60; i++) {
    const [min, hour, dom, month, dow] = matchers;
    if ((!min || min.has(date.getMinutes())) &&
        (!hour || hour.has(date.getHours())) &&
        (!dom || dom.has(date.getDate())) &&
        (!month || month.has(date.getMonth() + 1)) &&
        (!dow || dow.has(date.getDay()))) {
      return date.getTime();
    }
    date.setMinutes(date.getMinutes() + 1);
  }
  return null;
}

/**
 * GET /api/admin/jobs
 * Get background job status
 */
router.get('/jobs', (_req: Request, res: Response, next: NextFunction) => {
  try {
    // NOTE: this is still a static/hard-coded summary, not live job-scheduler
    // state (e.g. actual lastRun times or whether a run is currently in
    // progress). Wiring in real-time status from JobScheduler.getStatus()
    // (src/services/jobs.ts) would require the JobScheduler instance
    // constructed in src/index.ts to be reachable from this route (e.g. via
    // app.locals or req), which it currently isn't - see index.ts.
    const jobs = [
      {
        name: 'daily-scraper',
        schedule: process.env.SCRAPER_SCHEDULE || '0 2 * * *',
        enabled: process.env.ENABLE_SCRAPER_JOB !== 'false',
        lastRun: null, // Would be tracked in a real implementation
        status: 'scheduled',
      },
      {
        name: 'schedule-checker',
        // Matches the cron registered in src/index.ts: runs at :00, :10,
        // :20, :30, :40, :50 of every hour (every 10 minutes), not hourly.
        schedule: '0,10,20,30,40,50 * * * *',
        enabled: process.env.ENABLE_SCHEDULE_CHECKER !== 'false',
        lastRun: null,
        status: 'scheduled',
      },
      {
        name: 'cache-cleanup',
        schedule: '0 3 * * 0',
        enabled: process.env.ENABLE_CACHE_CLEANUP !== 'false',
        lastRun: null,
        status: 'scheduled',
      },
    ].map(job => ({ ...job, nextRun: job.enabled ? nextCronRun(job.schedule) : null }));

    res.json({ jobs });
  } catch (error) {
    logger.error('Failed to get job status', { error });
    next(createInternalError('Failed to retrieve job status'));
  }
});

/**
 * GET /api/admin/schedules
 * Every user's playlist-refresh/mix-generation schedules, with username and
 * playlist name joined in - the per-user GET /api/schedules can't see across
 * users, so the admin "all scheduled tasks in one place" view needs its own.
 */
router.get('/schedules', (req: Request, res: Response, next: NextFunction) => {
  try {
    const db = req.dbService!;
    const rows = db.getAllSchedules();
    const schedules = rows.map((row: any) => ({ ...transformSchedule(row, db), username: row.username, playlistName: row.playlist_name }));
    res.json({ schedules });
  } catch (error: any) {
    logger.error('Failed to get all schedules', { error: error.message });
    next(createInternalError('Failed to retrieve schedules'));
  }
});

/**
 * GET /api/admin/logs
 * Tail of the server's combined.log merged with deemix-server's systemd
 * journal (most recent first), optionally filtered by level. combined.log is
 * the single shared logger every route writes through (with userId/adminId
 * in its metadata), so it already covers every user; deemix-server is a
 * separate systemd-managed process whose stdout/stderr only lives in the
 * journal, not in our log files, so it's fetched and merged in here instead.
 */
router.get('/logs', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const limit = Math.min(1000, Math.max(1, parseInt(req.query.limit as string, 10) || 200));
    const level = (req.query.level as string) || '';
    // The configured log level is a floor for this view as well as for what
    // gets written. Without it, setting the server to error-only still
    // showed info entries here: history already written to combined.log
    // before the change, and deemix-server's journal, which is a separate
    // process this setting has no say over at write time.
    const configuredLevel = configService.config.logLevel;
    const withinConfiguredLevel = (entryLevel: string) => isAtLeastAsSevere(entryLevel, configuredLevel);

    let entries: Array<{ level: string; message: string; timestamp: string | null; service?: string; ts: number }> = [];

    // Scan newest-line-first and stop as soon as there are enough matches,
    // rather than JSON.parsing all ~20,000 lines of every 5MB file for a
    // request that returns a couple of hundred of them. The level filter has
    // to be applied here rather than afterwards: errors are sparse, so a
    // fixed tail of recent lines can easily contain none at all while plenty
    // exist further back.
    let scannedAll = true;
    for (const logPath of currentCombinedLogPaths()) {
      if (entries.length >= limit) { scannedAll = false; break; }
      if (!fs.existsSync(logPath)) continue;
      const lines = fs.readFileSync(logPath, 'utf-8').split('\n');
      for (let i = lines.length - 1; i >= 0; i--) {
        if (entries.length >= limit) { scannedAll = false; break; }
        const line = lines[i].trim();
        if (!line) continue;
        let entry: any;
        try {
          entry = JSON.parse(line);
          entry.ts = entry.timestamp ? Date.parse(entry.timestamp.replace(' ', 'T')) : 0;
        } catch {
          // A line winston was still writing at the instant we read the file -
          // surface it as-is rather than silently dropping it.
          entry = { level: 'info', message: line, timestamp: null, ts: 0 };
        }
        if (level && entry.level !== level) continue;
        if (!withinConfiguredLevel(entry.level)) continue;
        entries.push(entry);
      }
    }

    try {
      const { stdout } = await execAsync(`journalctl -u ${DEEMIX_SERVICE_NAME} -n ${limit} -o json --no-pager`);
      const journalEntries = stdout.split('\n').filter(line => line.trim().length > 0).map(line => {
        const j = JSON.parse(line);
        const ts = Math.floor(parseInt(j.__REALTIME_TIMESTAMP, 10) / 1000);
        return {
          level: priorityToLevel(j.PRIORITY),
          message: j.MESSAGE,
          timestamp: new Date(ts).toISOString().replace('T', ' ').slice(0, 19),
          service: 'deemix-server',
          ts,
        };
      });
      entries = entries.concat(
        journalEntries.filter(e => withinConfiguredLevel(e.level) && (!level || e.level === level))
      );
    } catch (journalError) {
      // deemix-server's journal isn't essential to this endpoint - if
      // journalctl is unavailable (e.g. non-systemd dev environment), still
      // return this app's own combined.log entries rather than failing.
      logger.warn('Failed to read deemix-server journal', { error: (journalError as Error).message });
    }

    entries.sort((a, b) => b.ts - a.ts);

    const truncated = !scannedAll || entries.length > limit;
    entries = entries.slice(0, limit);

    res.json({ entries: entries.map(({ ts, ...e }) => e), truncated });
  } catch (error: any) {
    logger.error('Failed to read logs', { error: error.message });
    next(createInternalError('Failed to read log'));
  }
});

/**
 * DELETE /api/admin/logs
 * Truncates every log file (current + winston's rotated maxFiles copies) in
 * the log directory - a fresh log directory is one restart away from
 * regrowing them, so deleting is simpler than trying to keep winston's own
 * open file handles in sync with a truncate.
 */
router.delete('/logs', (req: Request, res: Response, next: NextFunction) => {
  try {
    const files = fs.readdirSync(LOG_DIR).filter(f => /^(combined|error)\d*\.log$/.test(f));
    for (const file of files) fs.writeFileSync(path.join(LOG_DIR, file), '');
    // deemix-server's entries live in the system-wide systemd journal
    // (shared with every other unit on the box), so we deliberately don't
    // vacuum it here - `journalctl --vacuum-*` isn't scoped to one unit and
    // clearing it would wipe every other service's history too.
    logger.info('Logs cleared by admin', { adminId: req.user!.id });
    res.json({ success: true });
  } catch (error: any) {
    logger.error('Failed to clear logs', { error: error.message });
    next(createInternalError('Failed to clear logs'));
  }
});

/**
 * GET /api/admin/log-level
 * Returns the currently configured winston log level (admin only)
 */
router.get('/log-level', (_req: Request, res: Response) => {
  res.json({ level: configService.config.logLevel, levels: LOG_LEVELS });
});

/**
 * PUT /api/admin/log-level
 * Changes how much gets written to combined.log/console going forward and
 * persists the choice. error.log is unaffected - it always keeps errors.
 */
router.put('/log-level', (req: Request, res: Response, next: NextFunction) => {
  try {
    const { level } = req.body;
    if (!LOG_LEVELS.includes(level)) {
      return next(createValidationError(`level must be one of: ${LOG_LEVELS.join(', ')}`));
    }
    configService.updateConfig({ logLevel: level });
    logger.info('Log level changed by admin', { adminId: req.user!.id, level });
    res.json({ success: true, level });
  } catch (error: any) {
    logger.error('Failed to update log level', { error: error.message });
    next(createInternalError('Failed to update log level'));
  }
});

/**
 * GET /api/admin/deemix-arl
 * Returns the currently configured Deezer ARL for deemix-server (admin only)
 */
router.get('/deemix-arl', (_req: Request, res: Response) => {
  // `status` is whatever the last scheduled (or manual) check found - ARLs
  // expire every few months, and until this existed the only thing that
  // surfaced an expired one was a user clicking Deemix and getting an error.
  res.json({ arl: configService.config.deemixArl, status: getLastDeemixArlCheck() });
});

/**
 * POST /api/admin/deemix-arl/check
 * Re-tests the configured ARL against deemix-server right now, rather than
 * waiting for the daily check.
 */
router.post('/deemix-arl/check', async (_req: Request, res: Response) => {
  res.json(await checkDeemixArl());
});

/**
 * PUT /api/admin/deemix-arl
 * Updates the Deezer ARL deemix-server logs in with and persists it
 */
router.put('/deemix-arl', (req: Request, res: Response, next: NextFunction) => {
  try {
    const { arl } = req.body;
    if (typeof arl !== 'string') {
      return next(createValidationError('arl is required and must be a string'));
    }

    configService.updateConfig({ deemixArl: arl.trim() });
    resetDeemixSession();
    // Test it straight away so a mistyped or already-expired ARL is caught
    // on the settings page instead of by the next user who clicks Deemix.
    checkDeemixArl().catch(() => { /* result is recorded for GET /deemix-arl either way */ });

    logger.info('[Admin] Deemix ARL updated', { adminId: req.user!.id });
    res.json({ success: true, arl: configService.config.deemixArl });
  } catch (err: any) {
    next(createInternalError(`Failed to update Deemix ARL: ${err?.message || 'Unknown error'}`));
  }
});

/**
 * GET /api/admin/lidarr-config
 * Returns the currently configured Lidarr URL/API key (admin only; the key
 * itself is returned since it's needed to prefill the settings form - same
 * tradeoff as GET /deemix-arl above).
 */
router.get('/lidarr-config', (_req: Request, res: Response) => {
  res.json({ url: configService.config.lidarrUrl, apiKey: configService.config.lidarrApiKey });
});

/**
 * PUT /api/admin/lidarr-config
 * Updates the Lidarr URL and API key and persists them
 */
router.put('/lidarr-config', (req: Request, res: Response, next: NextFunction) => {
  try {
    const { url, apiKey } = req.body;
    if (typeof url !== 'string' || typeof apiKey !== 'string') {
      return next(createValidationError('url and apiKey are required and must be strings'));
    }

    configService.updateConfig({ lidarrUrl: url.trim().replace(/\/$/, ''), lidarrApiKey: apiKey.trim() });

    logger.info('[Admin] Lidarr config updated', { adminId: req.user!.id });
    res.json({ success: true, url: configService.config.lidarrUrl });
  } catch (err: any) {
    next(createInternalError(`Failed to update Lidarr config: ${err?.message || 'Unknown error'}`));
  }
});

/**
 * GET /api/admin/deemix-settings
 * Returns deemix-server's own download/tagging settings (quality, folder
 * structure, naming templates, tags, ...)
 */
router.get('/deemix-settings', async (_req: Request, res: Response, next: NextFunction) => {
  try {
    const settings = await getDeemixSettings();
    res.json({ settings });
  } catch (err: any) {
    next(createInternalError(`Failed to load Deemix settings: ${err?.message || 'Unknown error'}`));
  }
});

/**
 * PUT /api/admin/deemix-settings
 * Saves deemix-server's settings and restarts it so they take effect
 */
router.put('/deemix-settings', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { settings } = req.body;
    if (!settings || typeof settings !== 'object') {
      return next(createValidationError('settings object is required'));
    }

    await updateDeemixSettings(settings);

    logger.info('[Admin] Deemix settings updated', { adminId: req.user!.id });
    res.json({ success: true });
  } catch (err: any) {
    logger.error('[Admin] Failed to update Deemix settings', { error: err?.message, adminId: req.user?.id });
    next(createInternalError(`Failed to update Deemix settings: ${err?.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/admin/shutdown
 * Gracefully shut down the server process (admin only)
 * Useful when the server needs to be restarted from the tray app
 */
router.post('/shutdown', (req: Request, res: Response) => {
  logger.info('Shutdown requested by admin', { adminId: req.user!.id });
  res.json({ success: true, message: 'Server shutting down' });
  // Give the response time to send before exiting
  setTimeout(() => process.exit(0), 500);
});

export default router;
