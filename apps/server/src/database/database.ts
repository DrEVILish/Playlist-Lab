/**
 * Database Class
 * 
 * Provides a high-level interface for all database operations.
 * Handles CRUD operations for all tables with proper error handling.
 */

import Database from 'better-sqlite3';
import { logger } from '../utils/logger';
import type {
  User,
  UserServer,
  UserSettings,
  ParsedUserSettings,
  MatchingSettings,
  MixSettings,
  Playlist,
  Schedule,
  ScheduleInput,
  MissingTrack,
  MissingTrackInput,
  CachedPlaylist,
  ParsedCachedPlaylist,
  ExternalTrack,
  MissingTrackStat,
  MixTemplate,
  ParsedMixTemplate,
  ManualMatch,
  DeemixDownload
} from './types';

/**
 * Replaces any non-finite number (NaN, +/-Infinity - e.g. from a malformed
 * API payload) with the corresponding value from `fallback` before it's
 * JSON.stringify'd for storage. Without this, JSON.stringify silently turns
 * NaN/Infinity into `null`, corrupting the setting instead of rejecting or
 * ignoring the bad value.
 */
function sanitizeNumbers<T extends Record<string, any>>(incoming: T, fallback: T): T {
  const result = { ...incoming };
  for (const key of Object.keys(result)) {
    if (typeof result[key] === 'number' && !Number.isFinite(result[key])) {
      (result as any)[key] = fallback[key];
    }
  }
  return result;
}

/**
 * Default matching settings
 */
const DEFAULT_MATCHING_SETTINGS: MatchingSettings = {
  minMatchScore: 0.8,
  stripParentheses: true,
  stripBrackets: true,
  useFirstArtistOnly: false,
  ignoreFeaturedArtists: true,
  ignoreRemixInfo: true,
  ignoreVersionInfo: false,
  preferNonCompilation: true,
  penalizeMonoVersions: true,
  penalizeLiveVersions: true,
  preferHigherRated: true,
  minRatingForMatch: 0,
  autoCompleteOnPerfectMatch: true,
  playlistPrefixes: {
    enabled: true,
    spotify: '[Spotify] ',
    deezer: '[Deezer] ',
    apple: '[Apple Music] ',
    tidal: '[Tidal] ',
    youtube: '[YouTube Music] ',
    amazon: '[Amazon Music] ',
    qobuz: '[Qobuz] ',
    listenbrainz: '[ListenBrainz] ',
    file: '[Imported] ',
    ai: '[AI Generated] '
  },
  customStripPatterns: [],
  featuredArtistPatterns: ['feat.', 'ft.', 'featuring'],
  versionSuffixPatterns: ['- Remaster', '- Remix', '- Live'],
  remasterPatterns: ['remaster', 'remastered'],
  variousArtistsNames: ['Various Artists', 'Various', 'VA'],
  penaltyKeywords: ['mono', 'live'],
  priorityKeywords: ['remaster', 'deluxe']
};

/**
 * Default mix settings
 */
const DEFAULT_MIX_SETTINGS: MixSettings = {
  weeklyMix: {
    topArtists: 10,
    tracksPerArtist: 5
  },
  dailyMix: {
    recentTracks: 20,
    relatedTracks: 15,
    rediscoveryTracks: 15,
    rediscoveryDays: 90
  },
  timeCapsule: {
    trackCount: 50,
    daysAgo: 365,
    maxPerArtist: 3
  },
  newMusic: {
    albumCount: 10,
    tracksPerAlbum: 3
  }
};

export class DatabaseService {
  constructor(private db: Database.Database) {}

  // ==================== User Operations ====================

  /**
   * Create a new user
   */
  createUser(
    plexUserId: string,
    username: string,
    token: string,
    thumb?: string
  ): User {
    const now = Math.floor(Date.now() / 1000);
    
    const stmt = this.db.prepare(`
      INSERT INTO users (plex_user_id, plex_username, plex_token, plex_thumb, created_at, last_login)
      VALUES (?, ?, ?, ?, ?, ?)
    `);
    
    const result = stmt.run(plexUserId, username, token, thumb, now, now);
    
    return {
      id: result.lastInsertRowid as number,
      plex_user_id: plexUserId,
      plex_username: username,
      plex_token: token,
      plex_thumb: thumb,
      created_at: now,
      last_login: now,
      is_enabled: 1
    };
  }

  /**
   * Get user by Plex user ID
   */
  getUserByPlexId(plexUserId: string): User | null {
    const stmt = this.db.prepare('SELECT * FROM users WHERE plex_user_id = ?');
    const result = stmt.get(plexUserId) as User | undefined;
    return result ?? null;
  }

  /**
   * Get user by internal ID
   */
  getUserById(id: number): User | null {
    const stmt = this.db.prepare('SELECT * FROM users WHERE id = ?');
    const result = stmt.get(id) as User | undefined;
    return result ?? null;
  }

  /**
   * Get the first-created user (lowest id). Used by the DEV_NO_AUTH bypass
   * to pick who to log in as when reviewing the UI without a real session.
   */
  getFirstUser(): User | null {
    const stmt = this.db.prepare('SELECT * FROM users ORDER BY id ASC LIMIT 1');
    const result = stmt.get() as User | undefined;
    return result ?? null;
  }

  /**
   * Get user by Plex username
   */
  getUserByPlexUsername(username: string): User | null {
    const stmt = this.db.prepare('SELECT * FROM users WHERE plex_username = ?');
    const result = stmt.get(username) as User | undefined;
    return result ?? null;
  }

  /**
   * Update user's last login timestamp
   */
  updateUserLogin(userId: number, timestamp?: number): void {
    const now = timestamp ?? Math.floor(Date.now() / 1000);
    const stmt = this.db.prepare('UPDATE users SET last_login = ? WHERE id = ?');
    stmt.run(now, userId);
  }

  /**
   * Update user's Plex token
   */
  updateUserToken(userId: number, token: string): void {
    const stmt = this.db.prepare('UPDATE users SET plex_token = ? WHERE id = ?');
    stmt.run(token, userId);
  }

  /**
   * Update a user's display name and avatar.
   *
   * Called on every login so names captured before plex.tv had one for the
   * account (managed Plex Home users) stop showing as blank in the admin UI.
   */
  updateUserProfile(userId: number, username: string, thumb?: string): void {
    const stmt = this.db.prepare('UPDATE users SET plex_username = ?, plex_thumb = COALESCE(?, plex_thumb) WHERE id = ?');
    stmt.run(username, thumb ?? null, userId);
  }

  // ==================== Server Operations ====================

  /**
   * Save user's server configuration
   */
  saveUserServer(
    userId: number,
    serverName: string,
    serverClientId: string,
    serverUrl: string,
    libraryId?: string,
    libraryName?: string,
    accessToken?: string | null
  ): UserServer {
    const insertStmt = this.db.prepare(`
      INSERT INTO user_servers (user_id, server_name, server_client_id, server_url, library_id, library_name, access_token)
      VALUES (?, ?, ?, ?, ?, ?, ?)
    `);
    const deleteStmt = this.db.prepare('DELETE FROM user_servers WHERE user_id = ?');

    // Delete existing server and insert the new one atomically - if the
    // INSERT throws after the DELETE, the transaction rolls back instead of
    // leaving the user with zero server rows.
    const replaceServer = this.db.transaction(() => {
      deleteStmt.run(userId);
      return insertStmt.run(userId, serverName, serverClientId, serverUrl, libraryId, libraryName, accessToken ?? null);
    });

    const result = replaceServer();

    return {
      id: result.lastInsertRowid as number,
      user_id: userId,
      server_name: serverName,
      server_client_id: serverClientId,
      server_url: serverUrl,
      library_id: libraryId,
      library_name: libraryName,
      access_token: accessToken ?? null
    };
  }

  /**
   * Get user's server configuration
   */
  getUserServer(userId: number): UserServer | null {
    const stmt = this.db.prepare('SELECT * FROM user_servers WHERE user_id = ?');
    const result = stmt.get(userId) as UserServer | undefined;
    return result ?? null;
  }

  // ==================== Settings Operations ====================

  /**
   * Get user settings (with defaults if not exists)
   */
  getUserSettings(userId: number): ParsedUserSettings {
    const stmt = this.db.prepare('SELECT * FROM user_settings WHERE user_id = ?');
    const row = stmt.get(userId) as UserSettings | undefined;
    
    if (!row) {
      // Return defaults
      return {
        user_id: userId,
        country: 'global',
        matching_settings: DEFAULT_MATCHING_SETTINGS,
        mix_settings: DEFAULT_MIX_SETTINGS,
        gemini_api_key: undefined,
        grok_api_key: undefined,
        ai_provider: 'gemini'
      };
    }
    
    return {
      user_id: row.user_id,
      country: row.country,
      matching_settings: JSON.parse(row.matching_settings),
      mix_settings: JSON.parse(row.mix_settings),
      gemini_api_key: row.gemini_api_key,
      grok_api_key: row.grok_api_key,
      ai_provider: row.ai_provider || 'gemini'
    };
  }

  /**
   * Save user settings
   */
  saveUserSettings(
    userId: number,
    settings: Partial<{
      country: string;
      matching_settings: MatchingSettings;
      mix_settings: MixSettings;
      gemini_api_key: string | null;
      grok_api_key: string | null;
      ai_provider: string;
    }>
  ): void {
    const current = this.getUserSettings(userId);
    
    const country = settings.country ?? current.country;
    const matchingSettings = sanitizeNumbers(settings.matching_settings ?? current.matching_settings, current.matching_settings);
    const mixSettings = sanitizeNumbers(settings.mix_settings ?? current.mix_settings, current.mix_settings);
    const geminiApiKey = settings.gemini_api_key !== undefined ? settings.gemini_api_key : current.gemini_api_key;
    const grokApiKey = settings.grok_api_key !== undefined ? settings.grok_api_key : current.grok_api_key;
    const aiProvider = settings.ai_provider ?? current.ai_provider ?? 'gemini';
    
    const stmt = this.db.prepare(`
      INSERT INTO user_settings (user_id, country, matching_settings, mix_settings, gemini_api_key, grok_api_key, ai_provider)
      VALUES (?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(user_id) DO UPDATE SET
        country = excluded.country,
        matching_settings = excluded.matching_settings,
        mix_settings = excluded.mix_settings,
        gemini_api_key = excluded.gemini_api_key,
        grok_api_key = excluded.grok_api_key,
        ai_provider = excluded.ai_provider
    `);
    
    stmt.run(
      userId,
      country,
      JSON.stringify(matchingSettings),
      JSON.stringify(mixSettings),
      geminiApiKey,
      grokApiKey,
      aiProvider
    );
  }

  // ==================== Playlist Operations ====================

  /**
   * Create a new playlist
   */
  createPlaylist(
    userId: number,
    plexPlaylistId: string,
    name: string,
    source: string,
    sourceUrl?: string | null
  ): Playlist {
    const now = Math.floor(Date.now() / 1000);
    
    const stmt = this.db.prepare(`
      INSERT INTO playlists (user_id, plex_playlist_id, name, source, source_url, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?)
    `);
    
    const result = stmt.run(userId, plexPlaylistId, name, source, sourceUrl ?? undefined, now, now);
    
    return {
      id: result.lastInsertRowid as number,
      user_id: userId,
      plex_playlist_id: plexPlaylistId,
      name,
      source,
      source_url: sourceUrl ?? undefined,
      created_at: now,
      updated_at: now
    };
  }

  /**
   * Get all playlists for a user
   */
  getUserPlaylists(userId: number): Playlist[] {
    const stmt = this.db.prepare('SELECT * FROM playlists WHERE user_id = ? ORDER BY created_at DESC');
    return stmt.all(userId) as Playlist[];
  }

  /**
   * Get playlist by ID
   */
  getPlaylistById(id: number): Playlist | null {
    const stmt = this.db.prepare('SELECT * FROM playlists WHERE id = ?');
    const result = stmt.get(id) as Playlist | undefined;
    return result ?? null;
  }

  /**
   * Update playlist
   */
  updatePlaylist(id: number, updates: Partial<Playlist>): void {
    const fields: string[] = [];
    const values: any[] = [];
    
    if (updates.name !== undefined) {
      fields.push('name = ?');
      values.push(updates.name);
    }
    if (updates.source_url !== undefined) {
      fields.push('source_url = ?');
      values.push(updates.source_url);
    }
    if (updates.plex_playlist_id !== undefined) {
      fields.push('plex_playlist_id = ?');
      values.push(updates.plex_playlist_id);
    }
    
    fields.push('updated_at = ?');
    values.push(Math.floor(Date.now() / 1000));
    
    values.push(id);
    
    const stmt = this.db.prepare(`UPDATE playlists SET ${fields.join(', ')} WHERE id = ?`);
    stmt.run(...values);
  }

  /**
   * Delete playlist
   */
  deletePlaylist(id: number): void {
    const stmt = this.db.prepare('DELETE FROM playlists WHERE id = ?');
    stmt.run(id);
  }

  // ==================== Schedule Operations ====================

  /**
   * Create a new schedule
   */
  createSchedule(userId: number, schedule: ScheduleInput): Schedule {
    const now = Math.floor(Date.now() / 1000);
    const stmt = this.db.prepare(`
      INSERT INTO schedules (user_id, playlist_id, schedule_type, frequency, start_date, config, created_at)
      VALUES (?, ?, ?, ?, ?, ?, ?)
    `);

    const result = stmt.run(
      userId,
      schedule.playlist_id,
      schedule.schedule_type,
      schedule.frequency,
      schedule.start_date,
      schedule.config ? JSON.stringify(schedule.config) : null,
      now
    );

    return {
      id: result.lastInsertRowid as number,
      user_id: userId,
      playlist_id: schedule.playlist_id,
      schedule_type: schedule.schedule_type,
      frequency: schedule.frequency,
      start_date: schedule.start_date,
      config: schedule.config ? JSON.stringify(schedule.config) : undefined,
      created_at: now
    };
  }

  /**
   * Get all schedules for a user
   */
  getUserSchedules(userId: number): Schedule[] {
    const stmt = this.db.prepare('SELECT * FROM schedules WHERE user_id = ?');
    return stmt.all(userId) as Schedule[];
  }

  /**
   * All schedules across every user, with the owning username and playlist
   * name joined in - for the admin "all users' schedules in one place" view,
   * which otherwise only has each user's own scoped GET /api/schedules.
   * Returns raw snake_case rows (like getUserSchedules) plus `username` and
   * `playlist_name`, for the route to run through the same transformSchedule
   * used by GET /api/schedules.
   */
  getAllSchedules(): any[] {
    const stmt = this.db.prepare(`
      SELECT schedules.*, users.plex_username AS username, playlists.name AS playlist_name
      FROM schedules
      JOIN users ON users.id = schedules.user_id
      LEFT JOIN playlists ON playlists.id = schedules.playlist_id
      ORDER BY schedules.last_run DESC
    `);
    return stmt.all();
  }

  /**
   * Get schedule by ID
   */
  getScheduleById(id: number): Schedule | null {
    const stmt = this.db.prepare('SELECT * FROM schedules WHERE id = ?');
    const result = stmt.get(id) as Schedule | undefined;
    return result ?? null;
  }

  /**
   * Get due schedules (schedules that need to run)
   */
  getDueSchedules(): Schedule[] {
    const now = Math.floor(Date.now() / 1000);
    const currentDate = new Date();
    
    // Get all schedules belonging to enabled users only - a disabled user's
    // schedules must not keep firing in the background after an admin
    // disables them via POST /api/admin/users/:userId/disable.
    const stmt = this.db.prepare(`
      SELECT schedules.*
      FROM schedules
      JOIN users ON users.id = schedules.user_id
      WHERE users.is_enabled = 1
    `);
    const allSchedules = stmt.all() as Schedule[];
    
    return allSchedules.filter(schedule => {
      const config = schedule.config ? JSON.parse(schedule.config as any) : {};
      
      // If no last_run, check if we've passed the start date
      if (!schedule.last_run) {
        const startDate = new Date(schedule.start_date + 'T00:00:00');
        if (currentDate < startDate) {
          return false; // Not yet time to start
        }
        // First run - check if we're at or past the scheduled time
        if (config.run_time) {
          const [scheduleHour, scheduleMinute] = (config.run_time as string).split(':').map(Number);
          const scheduledTimeToday = new Date(currentDate);
          scheduledTimeToday.setHours(scheduleHour, scheduleMinute, 0, 0);
          
          if (currentDate >= scheduledTimeToday) {
            return true; // First run, and we're past the scheduled time today
          }
          return false; // First run, but not yet time today
        }
        return true; // First run, no specific time set
      }
      
      // Check if enough time has passed based on frequency
      const timeSinceLastRun = now - schedule.last_run;
      let frequencyMet = false;
      
      switch (schedule.frequency) {
        case 'daily':
          frequencyMet = timeSinceLastRun >= 86400; // 24 hours
          break;
        case 'weekly':
          frequencyMet = timeSinceLastRun >= 604800; // 7 days
          break;
        case 'fortnightly':
          frequencyMet = timeSinceLastRun >= 1209600; // 14 days
          break;
        case 'monthly':
          frequencyMet = timeSinceLastRun >= 2592000; // 30 days
          break;
        default:
          return false;
      }
      
      // If frequency requirement not met, don't run
      if (!frequencyMet) {
        return false;
      }
      
      // Frequency requirement is met, now check if we're at the scheduled time (if specified)
      if (config.run_time) {
        const [scheduleHour, scheduleMinute] = (config.run_time as string).split(':').map(Number);
        
        // Check if we're at or past the scheduled time today
        const scheduledTimeToday = new Date(currentDate);
        scheduledTimeToday.setHours(scheduleHour, scheduleMinute, 0, 0);
        
        // If we're past the scheduled time today, run it
        if (currentDate >= scheduledTimeToday) {
          return true;
        }
        
        return false; // Frequency met but not at scheduled time yet
      }
      
      // No specific time set, just run based on frequency
      return true;
    });
  }

  /**
   * Update schedule's last run timestamp
   */
  updateScheduleLastRun(id: number, timestamp?: number): void {
    const now = timestamp ?? Math.floor(Date.now() / 1000);
    const stmt = this.db.prepare('UPDATE schedules SET last_run = ? WHERE id = ?');
    stmt.run(now, id);
  }

  /**
   * Update schedule
   */
  updateSchedule(id: number, updates: Partial<ScheduleInput>): void {
    const fields: string[] = [];
    const values: any[] = [];
    
    if (updates.frequency !== undefined) {
      fields.push('frequency = ?');
      values.push(updates.frequency);
    }
    if (updates.start_date !== undefined) {
      fields.push('start_date = ?');
      values.push(updates.start_date);
    }
    if (updates.config !== undefined) {
      fields.push('config = ?');
      values.push(JSON.stringify(updates.config));
    }
    
    if (fields.length === 0) return;
    
    values.push(id);
    
    const stmt = this.db.prepare(`UPDATE schedules SET ${fields.join(', ')} WHERE id = ?`);
    stmt.run(...values);
  }

  /**
   * Delete schedule
   */
  deleteSchedule(id: number): void {
    const stmt = this.db.prepare('DELETE FROM schedules WHERE id = ?');
    stmt.run(id);
  }

  // ==================== Schedule Execution Operations ====================

  /**
   * Create a schedule execution record
   */
  createScheduleExecution(scheduleId: number, userId: number, playlistName?: string): number {
    const stmt = this.db.prepare(`
      INSERT INTO schedule_executions (schedule_id, user_id, status, started_at, playlist_name)
      VALUES (?, ?, 'running', ?, ?)
    `);
    const now = Math.floor(Date.now() / 1000);
    const result = stmt.run(scheduleId, userId, now, playlistName || null);
    return result.lastInsertRowid as number;
  }

  /**
   * Update schedule execution with results
   */
  updateScheduleExecution(
    executionId: number,
    status: 'success' | 'failed',
    tracksMatched: number = 0,
    tracksUnmatched: number = 0,
    errorMessage?: string
  ): void {
    const stmt = this.db.prepare(`
      UPDATE schedule_executions
      SET status = ?, completed_at = ?, tracks_matched = ?, tracks_unmatched = ?, error_message = ?
      WHERE id = ?
    `);
    const now = Math.floor(Date.now() / 1000);
    stmt.run(status, now, tracksMatched, tracksUnmatched, errorMessage || null, executionId);
  }

  /**
   * Get execution history for a schedule
   */
  getScheduleExecutions(scheduleId: number, limit: number = 10): any[] {
    const stmt = this.db.prepare(`
      SELECT * FROM schedule_executions
      WHERE schedule_id = ?
      ORDER BY started_at DESC
      LIMIT ?
    `);
    return stmt.all(scheduleId, limit);
  }

  /**
   * Get all recent executions for a user
   */
  getUserScheduleExecutions(userId: number, limit: number = 50): any[] {
    const stmt = this.db.prepare(`
      SELECT se.*, s.schedule_type, s.frequency
      FROM schedule_executions se
      JOIN schedules s ON se.schedule_id = s.id
      WHERE se.user_id = ?
      ORDER BY se.started_at DESC
      LIMIT ?
    `);
    return stmt.all(userId, limit);
  }

  /**
   * Get currently running executions for a user
   */
  getRunningExecutions(userId: number): any[] {
    const stmt = this.db.prepare(`
      SELECT se.*, s.schedule_type, s.frequency
      FROM schedule_executions se
      JOIN schedules s ON se.schedule_id = s.id
      WHERE se.user_id = ? AND se.status = 'running'
      ORDER BY se.started_at DESC
    `);
    return stmt.all(userId);
  }

  /**
   * Get a single execution by ID
   */
  getExecutionById(executionId: number): any {
    const stmt = this.db.prepare(`
      SELECT * FROM schedule_executions
      WHERE id = ?
    `);
    return stmt.get(executionId);
  }

  /**
   * Delete a single execution record
   */
  deleteExecution(executionId: number): void {
    const stmt = this.db.prepare(`
      DELETE FROM schedule_executions
      WHERE id = ?
    `);
    stmt.run(executionId);
  }

  /**
   * Reconcile schedule executions left stuck in the 'running' state by an
   * unclean shutdown/restart. Since this service is restarted periodically,
   * any execution that was in-flight at restart time would otherwise stay
   * 'running' forever - which also permanently disables that schedule's
   * "Run Now" button in the UI (the frontend derives isRunning from any
   * execution with status 'running'). Intended to be called once at startup.
   *
   * @returns Number of executions marked as failed
   */
  reconcileStuckExecutions(): number {
    const stmt = this.db.prepare(`
      UPDATE schedule_executions
      SET status = 'failed', completed_at = ?, error_message = ?
      WHERE status = 'running'
    `);
    const now = Math.floor(Date.now() / 1000);
    const result = stmt.run(now, 'Execution interrupted by server restart');
    return result.changes;
  }

  /**
   * Clear all execution history for a user
   */
  clearUserExecutions(userId: number): number {
    const stmt = this.db.prepare(`
      DELETE FROM schedule_executions
      WHERE user_id = ?
    `);
    const result = stmt.run(userId);
    return result.changes;
  }

  // ==================== Missing Tracks Operations ====================

  /**
   * Add missing tracks for a playlist.
   *
   * A playlist's missing-tracks entry is identified by playlist_id, and
   * each track within it is deduplicated by (title, artist, album). If a
   * track is still missing on a later run (e.g. a re-run schedule), its
   * existing row is refreshed (position/source/added_at) rather than a
   * new duplicate row being inserted. Tracks that are no longer reported
   * as missing are intentionally left in place - they're still a useful
   * record that the library was missing them - see issue #33.
   */
  addMissingTracks(userId: number, playlistId: number, tracks: MissingTrackInput[]): void {
    const findStmt = this.db.prepare(`
      SELECT id FROM missing_tracks
      WHERE playlist_id = ?
        AND LOWER(TRIM(title)) = LOWER(TRIM(?))
        AND LOWER(TRIM(artist)) = LOWER(TRIM(?))
        AND LOWER(TRIM(COALESCE(album, ''))) = LOWER(TRIM(COALESCE(?, '')))
    `);

    const updateStmt = this.db.prepare(`
      UPDATE missing_tracks
      SET position = ?, after_track_key = ?, added_at = ?, source = ?
      WHERE id = ?
    `);

    const insertStmt = this.db.prepare(`
      INSERT INTO missing_tracks (user_id, playlist_id, title, artist, album, position, after_track_key, added_at, source)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `);

    const now = Math.floor(Date.now() / 1000);

    const upsertMany = this.db.transaction((tracks: MissingTrackInput[]) => {
      for (const track of tracks) {
        const existing = findStmt.get(playlistId, track.title, track.artist, track.album ?? '') as
          | { id: number }
          | undefined;

        if (existing) {
          updateStmt.run(track.position, track.after_track_key, now, track.source, existing.id);
        } else {
          insertStmt.run(
            userId,
            playlistId,
            track.title,
            track.artist,
            track.album,
            track.position,
            track.after_track_key,
            now,
            track.source
          );
        }
      }
    });

    upsertMany(tracks);
  }

  /**
   * Get all missing tracks for a user
   */
  getUserMissingTracks(userId: number): MissingTrack[] {
    const stmt = this.db.prepare(`
      SELECT * FROM missing_tracks
      WHERE user_id = ?
      ORDER BY playlist_id, position
    `);
    return stmt.all(userId) as MissingTrack[];
  }

  /**
   * Get missing tracks for a specific playlist
   */
  getPlaylistMissingTracks(playlistId: number): MissingTrack[] {
    const stmt = this.db.prepare(`
      SELECT * FROM missing_tracks
      WHERE playlist_id = ?
      ORDER BY position
    `);
    return stmt.all(playlistId) as MissingTrack[];
  }

  /**
   * Get all missing tracks (admin only)
   */
  getAllMissingTracks(): MissingTrack[] {
    const stmt = this.db.prepare('SELECT * FROM missing_tracks ORDER BY added_at DESC');
    return stmt.all() as MissingTrack[];
  }

  /**
   * Remove a missing track
   */
  removeMissingTrack(id: number): void {
    const stmt = this.db.prepare('DELETE FROM missing_tracks WHERE id = ?');
    stmt.run(id);
  }

  /**
   * Clear all missing tracks for a playlist
   */
  clearPlaylistMissingTracks(playlistId: number): void {
    const stmt = this.db.prepare('DELETE FROM missing_tracks WHERE playlist_id = ?');
    stmt.run(playlistId);
  }

  // ==================== Manual Matches Operations ====================

  /**
   * Remember a user's explicit "use this Plex track" choice for a source
   * track, so future matching for the same (title, artist, album) reuses it
   * instead of re-running the fuzzy search. Upserts by that same key -
   * (title, artist) normalized the same way addMissingTracks() dedupes, so
   * picking a different track for the same source track later overwrites
   * the old choice rather than accumulating stale rows.
   */
  recordManualMatch(userId: number, track: { title: string; artist: string; album?: string }, plexRatingKey: string): void {
    const findStmt = this.db.prepare(`
      SELECT id FROM manual_matches
      WHERE user_id = ?
        AND LOWER(TRIM(title)) = LOWER(TRIM(?))
        AND LOWER(TRIM(artist)) = LOWER(TRIM(?))
        AND LOWER(TRIM(COALESCE(album, ''))) = LOWER(TRIM(COALESCE(?, '')))
    `);
    const existing = findStmt.get(userId, track.title, track.artist, track.album ?? '') as { id: number } | undefined;

    const now = Math.floor(Date.now() / 1000);

    if (existing) {
      this.db.prepare('UPDATE manual_matches SET plex_rating_key = ?, updated_at = ? WHERE id = ?')
        .run(plexRatingKey, now, existing.id);
    } else {
      this.db.prepare(`
        INSERT INTO manual_matches (user_id, title, artist, album, plex_rating_key, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
      `).run(userId, track.title, track.artist, track.album ?? null, plexRatingKey, now, now);
    }
  }

  /**
   * All of a user's remembered manual matches, for callers (matchPlaylist())
   * to look up against before falling back to the normal search - fetched
   * once per matching run rather than once per track.
   */
  getUserManualMatches(userId: number): ManualMatch[] {
    const stmt = this.db.prepare('SELECT * FROM manual_matches WHERE user_id = ?');
    return stmt.all(userId) as ManualMatch[];
  }

  // ==================== Cached Playlists Operations ====================

  /**
   * Get cached playlist
   */
  getCachedPlaylist(source: string, sourceId: string): ParsedCachedPlaylist | null {
    const stmt = this.db.prepare('SELECT * FROM cached_playlists WHERE source = ? AND source_id = ?');
    const row = stmt.get(source, sourceId) as CachedPlaylist | undefined;
    
    if (!row) return null;
    
    return {
      ...row,
      tracks: JSON.parse(row.tracks)
    };
  }

  /**
   * Save cached playlist
   */
  saveCachedPlaylist(
    source: string,
    sourceId: string,
    name: string,
    description: string | undefined,
    tracks: ExternalTrack[],
    coverUrl?: string
  ): void {
    const now = Math.floor(Date.now() / 1000);
    
    const stmt = this.db.prepare(`
      INSERT INTO cached_playlists (source, source_id, name, description, tracks, cover_url, scraped_at)
      VALUES (?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(source, source_id) DO UPDATE SET
        name = excluded.name,
        description = excluded.description,
        tracks = excluded.tracks,
        cover_url = excluded.cover_url,
        scraped_at = excluded.scraped_at
    `);
    
    stmt.run(source, sourceId, name, description, JSON.stringify(tracks), coverUrl, now);
  }

  /**
   * Get stale cached playlists (older than maxAgeHours)
   */
  getStaleCache(maxAgeHours: number): ParsedCachedPlaylist[] {
    const cutoff = Math.floor(Date.now() / 1000) - (maxAgeHours * 3600);
    
    const stmt = this.db.prepare('SELECT * FROM cached_playlists WHERE scraped_at < ?');
    const rows = stmt.all(cutoff) as CachedPlaylist[];
    
    return rows.map(row => ({
      ...row,
      tracks: JSON.parse(row.tracks)
    }));
  }

  /**
   * Get all cached playlists
   */
  getAllCachedPlaylists(): ParsedCachedPlaylist[] {
    const stmt = this.db.prepare('SELECT * FROM cached_playlists ORDER BY scraped_at DESC');
    const rows = stmt.all() as CachedPlaylist[];
    
    return rows.map(row => ({
      ...row,
      tracks: JSON.parse(row.tracks)
    }));
  }

  /**
   * Delete old cached playlists
   */
  deleteOldCache(maxAgeDays: number): number {
    const cutoff = Math.floor(Date.now() / 1000) - (maxAgeDays * 86400);
    const stmt = this.db.prepare('DELETE FROM cached_playlists WHERE scraped_at < ?');
    const result = stmt.run(cutoff);
    return result.changes;
  }

  // ==================== Admin Operations ====================

  /**
   * Get all users (admin only)
   */
  getAllUsers(): User[] {
    const stmt = this.db.prepare('SELECT * FROM users ORDER BY created_at DESC');
    return stmt.all() as User[];
  }

  /**
   * Get user count
   */
  getUserCount(): number {
    const stmt = this.db.prepare('SELECT COUNT(*) as count FROM users');
    const result = stmt.get() as { count: number };
    return result.count;
  }

  /**
   * Get playlist count
   */
  getPlaylistCount(): number {
    const stmt = this.db.prepare('SELECT COUNT(*) as count FROM playlists');
    const result = stmt.get() as { count: number };
    return result.count;
  }

  /**
   * Get missing track statistics (most commonly missing tracks)
   */
  getMissingTrackStats(): MissingTrackStat[] {
    // Group by normalized (case/whitespace-insensitive) title+artist, same
    // as addMissingTracks' dedup logic, so e.g. "Song" and "song " aggregate
    // into a single stat instead of being counted separately. MIN() picks a
    // consistent (if arbitrary) original-cased variant for display. addedAt
    // is the most recent added_at across every user missing this track, so
    // sorting by it surfaces recently-flagged tracks first.
    const stmt = this.db.prepare(`
      SELECT
        MIN(title) as title,
        MIN(artist) as artist,
        COUNT(*) as count,
        MAX(added_at) as addedAt
      FROM missing_tracks
      GROUP BY LOWER(TRIM(title)), LOWER(TRIM(artist))
      ORDER BY count DESC
      LIMIT 100
    `);
    return stmt.all() as MissingTrackStat[];
  }

  /**
   * Check if user is admin
   */
  isAdmin(userId: number): boolean {
    const stmt = this.db.prepare('SELECT user_id FROM admin_users WHERE user_id = ?');
    return stmt.get(userId) !== undefined;
  }

  /**
   * Add admin user
   */
  addAdmin(userId: number): void {
    const stmt = this.db.prepare('INSERT OR IGNORE INTO admin_users (user_id) VALUES (?)');
    stmt.run(userId);
  }

  /**
   * Remove admin user
   */
  removeAdmin(userId: number): void {
    const stmt = this.db.prepare('DELETE FROM admin_users WHERE user_id = ?');
    stmt.run(userId);
  }

  /**
   * Enable a user
   */
  enableUser(userId: number): void {
    const stmt = this.db.prepare('UPDATE users SET is_enabled = 1 WHERE id = ?');
    stmt.run(userId);
  }

  /**
   * Disable a user
   */
  disableUser(userId: number): void {
    const stmt = this.db.prepare('UPDATE users SET is_enabled = 0 WHERE id = ?');
    stmt.run(userId);
  }

  /**
   * Check if a user is enabled
   */
  isUserEnabled(userId: number): boolean {
    const stmt = this.db.prepare('SELECT is_enabled FROM users WHERE id = ?');
    const result = stmt.get(userId) as { is_enabled: number } | undefined;
    // Default to enabled if column doesn't exist yet (migration pending)
    return result ? result.is_enabled !== 0 : true;
  }

  /**
   * Delete a user and all their data
   */
  deleteUser(userId: number): void {
    // Foreign keys with CASCADE will handle related data
    const stmt = this.db.prepare('DELETE FROM users WHERE id = ?');
    stmt.run(userId);
  }

  /**
   * Copy server config from one user to another
   */
  copyServerConfig(fromUserId: number, toUserId: number): void {
    const sourceServer = this.getUserServer(fromUserId);
    if (sourceServer) {
      this.saveUserServer(
        toUserId,
        sourceServer.server_name,
        sourceServer.server_client_id,
        sourceServer.server_url,
        sourceServer.library_id,
        sourceServer.library_name
      );
    }
  }

  /**
   * Get the first admin user (server owner)
   */
  getFirstAdmin(): User | null {
    const stmt = this.db.prepare(`
      SELECT u.* FROM users u
      INNER JOIN admin_users a ON u.id = a.user_id
      ORDER BY u.created_at ASC
      LIMIT 1
    `);
    const result = stmt.get() as User | undefined;
    return result ?? null;
  }

  // ==================== Playlist Sharing Operations ====================

  /**
   * Record a single playlist share
   */
  recordPlaylistShare(
    playlistId: number,
    ownerUserId: number,
    sharedWithUserId: number,
    plexPlaylistId: string,
    playlistName: string
  ): void {
    const stmt = this.db.prepare(`
      INSERT OR REPLACE INTO playlist_shares 
      (playlist_id, owner_user_id, shared_with_user_id, plex_playlist_id, playlist_name, shared_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `);

    const now = Date.now();
    stmt.run(playlistId, ownerUserId, sharedWithUserId, plexPlaylistId, playlistName, now);
  }

  /**
   * Get playlists shared with a user
   */
  getPlaylistsSharedWithUser(userId: number): Array<{
    id: number;
    playlistName: string;
    sharedByUsername: string;
    plexPlaylistId: string;
    sharedAt: number;
    trackCount: number;
  }> {
    try {
      const stmt = this.db.prepare(`
        SELECT 
          ps.id,
          ps.playlist_name as playlistName,
          ps.plex_playlist_id as plexPlaylistId,
          u.plex_username as sharedByUsername,
          ps.shared_at as sharedAt,
          0 as trackCount
        FROM playlist_shares ps
        JOIN users u ON ps.owner_user_id = u.id
        WHERE ps.shared_with_user_id = ?
        ORDER BY ps.shared_at DESC
      `);

      return stmt.all(userId) as any[];
    } catch (error) {
      logger.error('[Database] Error getting shared playlists', { error, userId });
      // Return empty array instead of throwing to avoid breaking the UI
      return [];
    }
  }

  /**
   * Get playlist by Plex ID, scoped to a single user.
   *
   * This is a multi-user app where each user has their own independent Plex
   * server, so ratingKeys (small sequential integers) can collide across
   * users. Looking up by plex_playlist_id alone could silently return - and
   * then mutate - a different user's playlist record, so callers must
   * always scope this to the requesting user.
   */
  getPlaylistByPlexId(userId: number, plexPlaylistId: string): Playlist | null {
    const stmt = this.db.prepare(`
      SELECT * FROM playlists WHERE user_id = ? AND plex_playlist_id = ?
    `);

    return (stmt.get(userId, plexPlaylistId) as Playlist | undefined) ?? null;
  }

  /**
   * Get a user's playlist record by name (case-insensitive).
   *
   * Used to re-associate a scheduled/chart import with its existing
   * playlist record even when the underlying Plex playlist was deleted
   * and recreated (which gives it a brand new plex_playlist_id on every
   * run). Without this, lookups by plex_playlist_id alone would never
   * match and every scheduled run would create a duplicate playlist
   * record - see issue #33.
   */
  getPlaylistByUserAndName(userId: number, name: string): Playlist | null {
    const stmt = this.db.prepare(`
      SELECT * FROM playlists
      WHERE user_id = ? AND LOWER(TRIM(name)) = LOWER(TRIM(?))
      ORDER BY created_at ASC
      LIMIT 1
    `);

    return (stmt.get(userId, name) as Playlist | undefined) ?? null;
  }

  /**
   * Get a user's playlist record by its source URL (e.g. a chart/Spotify/
   * Deezer link). Unlike the name, this never changes when the user renames
   * the playlist, so it's the preferred way to re-associate an unlinked
   * chart-import schedule with its existing record - matching by name alone
   * missed a just-renamed playlist and spawned a duplicate instead of
   * reusing it.
   */
  getPlaylistByUserAndSourceUrl(userId: number, sourceUrl: string): Playlist | null {
    const stmt = this.db.prepare(`
      SELECT * FROM playlists
      WHERE user_id = ? AND source_url = ?
      ORDER BY created_at ASC
      LIMIT 1
    `);

    return (stmt.get(userId, sourceUrl) as Playlist | undefined) ?? null;
  }

  /**
   * Link a schedule to a playlist record so future scheduled runs reuse
   * the same playlist (and therefore the same missing-tracks entry)
   * instead of creating a new one each time.
   */
  linkSchedulePlaylist(scheduleId: number, playlistId: number): void {
    const stmt = this.db.prepare(`UPDATE schedules SET playlist_id = ? WHERE id = ?`);
    stmt.run(playlistId, scheduleId);
  }


    /**
     * Create a new mix template
     */
    createMixTemplate(
      userId: number,
      name: string,
      description: string | null,
      mixType: string,
      configuration: any
    ): ParsedMixTemplate {
      const now = Date.now();
      const stmt = this.db.prepare(`
        INSERT INTO mix_templates (user_id, name, description, mix_type, configuration, created_at, updated_at, use_count)
        VALUES (?, ?, ?, ?, ?, ?, ?, 0)
      `);

      const result = stmt.run(
        userId,
        name,
        description,
        mixType,
        JSON.stringify(configuration),
        now,
        now
      );

      return {
        id: result.lastInsertRowid as number,
        user_id: userId,
        name,
        description: description || undefined,
        mix_type: mixType,
        configuration,
        created_at: now,
        updated_at: now,
        use_count: 0
      };
    }

    /**
     * Get all mix templates for a user
     * 
     * Performance optimizations:
     * - Uses composite index on (user_id, last_used_at, updated_at)
     * - Prepared statement for efficient repeated queries
     * - Sorts by usage for better UX
     */
    getMixTemplates(userId: number): ParsedMixTemplate[] {
      const stmt = this.db.prepare(`
        SELECT * FROM mix_templates
        WHERE user_id = ?
        ORDER BY 
          CASE WHEN last_used_at IS NULL THEN 0 ELSE 1 END DESC,
          last_used_at DESC, 
          updated_at DESC
      `);

      const templates = stmt.all(userId) as MixTemplate[];

      const parsedTemplates: ParsedMixTemplate[] = [];
      
      for (const t of templates) {
        // Parse configuration JSON with error handling
        let configuration: any;
        try {
          configuration = JSON.parse(t.configuration);
        } catch (error: any) {
          logger.error('Failed to parse template configuration JSON', { 
            templateId: t.id, 
            userId,
            error: error.message 
          });
          // Skip corrupted templates
          continue;
        }

        parsedTemplates.push({
          ...t,
          description: t.description || undefined,
          configuration,
          last_used_at: t.last_used_at || undefined
        });
      }
      
      return parsedTemplates;
    }

    /**
     * Get a specific mix template by ID
     */
    getMixTemplateById(id: number): ParsedMixTemplate | null {
      const stmt = this.db.prepare('SELECT * FROM mix_templates WHERE id = ?');
      const template = stmt.get(id) as MixTemplate | undefined;

      if (!template) {
        return null;
      }

      // Parse configuration JSON with error handling
      let configuration: any;
      try {
        configuration = JSON.parse(template.configuration);
      } catch (error: any) {
        logger.error('Failed to parse template configuration JSON', { 
          templateId: id, 
          error: error.message 
        });
        // Return null to indicate corrupted template
        return null;
      }

      return {
        ...template,
        description: template.description || undefined,
        configuration,
        last_used_at: template.last_used_at || undefined
      };
    }

    /**
     * Update a mix template
     */
    updateMixTemplate(id: number, updates: {
      name?: string;
      description?: string | null;
      configuration?: any;
    }): void {
      const template = this.getMixTemplateById(id);
      if (!template) {
        throw new Error('Template not found');
      }

      const fields: string[] = [];
      const values: any[] = [];

      if (updates.name !== undefined) {
        fields.push('name = ?');
        values.push(updates.name);
      }

      if (updates.description !== undefined) {
        fields.push('description = ?');
        values.push(updates.description);
      }

      if (updates.configuration !== undefined) {
        fields.push('configuration = ?');
        values.push(JSON.stringify(updates.configuration));
      }

      fields.push('updated_at = ?');
      values.push(Date.now());

      values.push(id);

      const stmt = this.db.prepare(`
        UPDATE mix_templates
        SET ${fields.join(', ')}
        WHERE id = ?
      `);

      stmt.run(...values);
    }

    /**
     * Delete a mix template
     */
    deleteMixTemplate(id: number): void {
      const stmt = this.db.prepare('DELETE FROM mix_templates WHERE id = ?');
      stmt.run(id);
    }

    /**
     * Update template usage statistics
     */
    updateMixTemplateUsage(id: number): void {
      const now = Date.now();
      const stmt = this.db.prepare(`
        UPDATE mix_templates
        SET last_used_at = ?, use_count = use_count + 1
        WHERE id = ?
      `);
      stmt.run(now, id);
    }

  // ==================== Deemix Download Operations ====================

  /**
   * Records a deemix download that is still in flight, so its progress
   * poller (and the missing track it was queued for) can be picked back up
   * after a server restart. Not unique on uuid: two missing tracks from the
   * same album resolve to the same deemix queue item, and each still needs
   * its own row to reconcile back to.
   */
  addDeemixDownload(userId: number, uuid: string, title: string, detail: string | null, missingTrackId: number | null): number {
    const stmt = this.db.prepare(`
      INSERT INTO deemix_downloads (user_id, uuid, missing_track_id, title, detail, created_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `);
    return Number(stmt.run(userId, uuid, missingTrackId, title, detail, Date.now()).lastInsertRowid);
  }

  /** Called once a download reaches a terminal state - completed, failed, or
   * given up on - so only genuinely in-flight downloads are ever resumed. */
  deleteDeemixDownload(id: number): void {
    this.db.prepare('DELETE FROM deemix_downloads WHERE id = ?').run(id);
  }

  /**
   * Every download still recorded as in flight, newest first. Rows older
   * than `maxAgeMs` are deleted rather than returned: deemix drops its own
   * queue entry for a long-finished item, so resuming one would only poll
   * for something that can no longer be found.
   */
  getActiveDeemixDownloads(maxAgeMs: number): DeemixDownload[] {
    this.db.prepare('DELETE FROM deemix_downloads WHERE created_at < ?').run(Date.now() - maxAgeMs);
    return this.db.prepare('SELECT * FROM deemix_downloads ORDER BY created_at DESC').all() as DeemixDownload[];
  }

  /** Every admin's user id, for server-wide notifications (e.g. an expired
   * Deezer ARL) that aren't tied to whoever happens to be logged in. */
  getAdminUserIds(): number[] {
    const rows = this.db.prepare('SELECT user_id FROM admin_users').all() as Array<{ user_id: number }>;
    return rows.map(r => r.user_id);
  }
}
