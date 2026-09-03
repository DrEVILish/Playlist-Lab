/**
 * Schedule Checker Job
 * 
 * Checks for due schedules and executes them:
 * - Playlist refresh schedules
 * - Mix generation schedules
 */

import { EventEmitter } from 'events';
import { DatabaseService } from '../database/database';
import { logger } from '../utils/logger';
import { importPlaylist } from './import';
import { dedupeByPlexRatingKey } from './matching';
import { MixService } from './mixes';
import { PlexClient, resolvePlexToken } from './plex';
import { addNotification, updateNotification } from './job-notifications';
import type { Schedule } from '../database/types';

/**
 * Execute playlist refresh schedules that are due.
 *
 * @param schedulesOverride - When provided, these schedules are executed
 *   directly instead of querying `db.getDueSchedules()`. Used by
 *   runSingleSchedule() for manual "Run Now" triggers so it doesn't need to
 *   mutate any shared state on `db` (which is a process-wide singleton).
 * @param progressEmitter - Only meaningful together with `schedulesOverride`
 *   (which is always exactly one schedule for a manual trigger) - forwarded
 *   into importPlaylist() so the caller can mirror real scraping/matching
 *   progress into that schedule's notification.
 */
async function executePlaylistRefreshSchedules(
  db: DatabaseService,
  schedulesOverride?: Schedule[],
  progressEmitter?: EventEmitter
): Promise<{ executed: number; failed: number }> {
  const dueSchedules = (schedulesOverride ?? db.getDueSchedules()).filter(s => s.schedule_type === 'playlist_refresh');

  let executed = 0;
  let failed = 0;

  for (const schedule of dueSchedules) {
    let executionId: number | null = null;
    try {
      logger.info('Executing playlist refresh schedule', {
        scheduleId: schedule.id,
        userId: schedule.user_id,
        playlistId: schedule.playlist_id
      });

      // Get user info
      const user = db.getUserById(schedule.user_id);
      if (!user) {
        logger.error('User not found for schedule', { scheduleId: schedule.id, userId: schedule.user_id });
        failed++;
        continue;
      }

      // Get user's server info
      const server = db.getUserServer(schedule.user_id);
      if (!server) {
        logger.error('Server not found for user', { scheduleId: schedule.id, userId: schedule.user_id });
        failed++;
        continue;
      }

      // Parse schedule config
      const config = schedule.config ? JSON.parse(schedule.config) : {};

      // Check if this is a chart import schedule (has chartUrl or autoImport in config)
      const isChartImport = config.chartUrl || config.autoImport;

      let result;
      let playlistName;

      if (isChartImport) {
        playlistName = config.playlistName || config.chartName || 'Chart Import';
      } else {
        const playlist = schedule.playlist_id ? db.getPlaylistById(schedule.playlist_id) : null;
        if (!playlist) {
          logger.error('Playlist not found for schedule', { scheduleId: schedule.id, playlistId: schedule.playlist_id });
          failed++;
          continue;
        }
        playlistName = playlist.name;
      }

      // Create execution record
      executionId = db.createScheduleExecution(schedule.id, schedule.user_id, playlistName);

      if (isChartImport) {
        // Chart import schedule
        logger.info('Executing chart import schedule', {
          scheduleId: schedule.id,
          chartName: config.chartName,
          chartSource: config.chartSource,
          chartUrl: config.chartUrl
        });

        // Import from chart URL
        result = await importPlaylist(
          config.chartSource as any,
          config.chartUrl,
          {
            userId: schedule.user_id,
            serverUrl: server.server_url,
            plexToken: resolvePlexToken(user, server),
            libraryId: server.library_id || undefined,
          },
          db,
          progressEmitter
        );

        // Use custom playlist name from config, or fall back to chart name
        playlistName = config.playlistName || config.chartName || result.playlistName;
      } else {
        // Regular playlist refresh schedule
        const playlist = schedule.playlist_id ? db.getPlaylistById(schedule.playlist_id) : null;
        if (!playlist) {
          logger.error('Playlist not found for schedule', { scheduleId: schedule.id, playlistId: schedule.playlist_id });
          failed++;
          continue;
        }

        // Re-import the playlist
        result = await importPlaylist(
          playlist.source as any,
          playlist.source_url || playlist.plex_playlist_id,
          {
            userId: schedule.user_id,
            serverUrl: server.server_url,
            plexToken: resolvePlexToken(user, server),
            libraryId: server.library_id || undefined,
          },
          db,
          progressEmitter
        );

        playlistName = playlist.name;
      }

      // Update the playlist in Plex
      const plex = new PlexClient(server.server_url, resolvePlexToken(user, server));

      // Resolve the DB record this run should update, by the most stable
      // identifier available - the schedule's own link if it has one, else
      // (for chart imports) the chart's source URL, which - unlike the
      // playlist name - never changes when the user renames the playlist.
      // Matching on name alone here used to miss a just-renamed playlist and
      // create a duplicate instead of reusing it (see issue #33 and its
      // follow-up).
      let trackedPlaylist = schedule.playlist_id ? db.getPlaylistById(schedule.playlist_id) : null;
      if (!trackedPlaylist && isChartImport && config.chartUrl) {
        trackedPlaylist = db.getPlaylistByUserAndSourceUrl(schedule.user_id, config.chartUrl);
      }
      if (!trackedPlaylist && isChartImport) {
        trackedPlaylist = db.getPlaylistByUserAndName(schedule.user_id, playlistName);
      }

      // How this run reconciles the refreshed source against what's already
      // in the playlist:
      //   'replace'    - the playlist mirrors the source. Tracks that left
      //                  the source leave the playlist.
      //   'accumulate' - the playlist only ever grows. New tracks are added,
      //                  nothing is removed, so a weekly chart becomes a
      //                  running archive of everything that ever charted.
      // `overwriteExisting` is the older boolean this replaces: true always
      // meant replace. false used to create a second playlist of the same
      // name on every run, which just accumulated duplicates in Plex rather
      // than tracks in one playlist - accumulate is what that was reaching
      // for, so it maps there.
      const updateMode: 'replace' | 'accumulate' =
        config.updateMode === 'accumulate' || config.updateMode === 'replace'
          ? config.updateMode
          : (config.overwriteExisting === false ? 'accumulate' : 'replace');

      // A refresh run only ever writes into an existing Plex playlist - the
      // tracked one, or (first run / a stale tracking link) whatever
      // playlist already has this name in Plex. A new playlist is created
      // only when neither exists, so ratingKeys stay stable across runs.
      const targetPlaylistId = await resolveTargetPlaylistId(plex, trackedPlaylist, playlistName);

      // Filter out tracks without valid plexRatingKey and build proper URIs
      const matchedWithKeys = dedupeByPlexRatingKey(result.matched.filter((t: any) => t.matched && t.plexRatingKey));

      // Get server client ID for building track URIs
      const serverClientId = server.server_client_id || 'playlist-lab-server';
      const trackUris = matchedWithKeys.map((t: any) =>
        `server://${serverClientId}/com.plexapp.plugins.library/library/metadata/${t.plexRatingKey}`
      );

      logger.info('Updating playlist with matched tracks', {
        scheduleId: schedule.id,
        totalMatched: result.matched.filter((t: any) => t.matched).length,
        validTrackUris: trackUris.length,
        playlistName,
        targetPlaylistId,
        sampleUri: trackUris[0]
      });

      // In accumulate mode new tracks are appended, nothing removed. In
      // replace mode the playlist's contents are cleared and refilled in
      // the source's order. Either way, an existing playlist keeps its
      // ratingKey, its cover, and anything the user added by hand.
      let newPlaylist: { ratingKey: string };
      if (targetPlaylistId && updateMode === 'accumulate') {
        const existingTracks = await plex.getPlaylistTracks(targetPlaylistId);
        const alreadyPresent = new Set(existingTracks.map((t: any) => String(t.ratingKey)));
        const newUris = matchedWithKeys
          .filter((t: any) => !alreadyPresent.has(String(t.plexRatingKey)))
          .map((t: any) => `server://${serverClientId}/com.plexapp.plugins.library/library/metadata/${t.plexRatingKey}`);

        if (newUris.length > 0) {
          await plex.addToPlaylist(targetPlaylistId, newUris);
        }
        logger.info('Accumulated refreshed tracks into existing playlist', {
          scheduleId: schedule.id,
          playlistName,
          added: newUris.length,
          alreadyPresent: matchedWithKeys.length - newUris.length,
          totalAfter: alreadyPresent.size + newUris.length,
        });
        newPlaylist = { ratingKey: targetPlaylistId };
      } else if (targetPlaylistId) {
        await replacePlaylistTracks(plex, targetPlaylistId, trackUris);
        newPlaylist = { ratingKey: targetPlaylistId };
      } else {
        newPlaylist = await plex.createPlaylist(
          playlistName,
          server.library_id || '',
          trackUris
        );
      }

      // Upload cover art if available and overwriteCover is enabled
      const overwriteCover = config.overwriteCover !== undefined ? config.overwriteCover : true;
      if (result.coverUrl && overwriteCover) {
        try {
          logger.info('Uploading cover art for scheduled import', {
            scheduleId: schedule.id,
            playlistName,
            coverUrl: result.coverUrl
          });
          await plex.uploadPlaylistPoster(newPlaylist.ratingKey, result.coverUrl);
        } catch (coverError: any) {
          logger.warn('Failed to upload cover art for scheduled import', {
            scheduleId: schedule.id,
            playlistName,
            error: coverError.message
          });
          // Continue - cover upload failure shouldn't fail the whole import
        }
      }

      // Get or create playlist record in database, reusing the record
      // resolved above so a schedule with no direct link yet (chart imports)
      // still lands on its existing playlist instead of spawning a
      // duplicate.
      let playlistDbId = trackedPlaylist?.id ?? null;

      if (playlistDbId) {
        // Update existing playlist record - including the name, so a
        // rename that only reached the schedule's config (not this row)
        // doesn't keep drifting the two apart on every future run.
        db.updatePlaylist(playlistDbId, {
          plex_playlist_id: newPlaylist.ratingKey,
          name: playlistName,
          updated_at: Math.floor(Date.now() / 1000)
        });
      } else {
        // Create new playlist record
        const createdPlaylist = db.createPlaylist(
          schedule.user_id,
          newPlaylist.ratingKey,
          playlistName,
          isChartImport ? config.chartSource : 'plex',
          isChartImport ? config.chartUrl : undefined
        );
        playlistDbId = createdPlaylist.id;

        logger.info('Created playlist record for scheduled import', {
          scheduleId: schedule.id,
          playlistId: playlistDbId,
          playlistName,
          isChartImport
        });
      }

      if (schedule.playlist_id !== playlistDbId) {
        // Persist the resolved playlist record back onto the schedule so
        // future runs go straight to the "update existing record" branch
        // above instead of re-resolving (and risking a new record) each time.
        db.linkSchedulePlaylist(schedule.id, playlistDbId);
      }

      // Save any unmatched tracks to missing_tracks
      if (result.unmatched && result.unmatched.length > 0 && playlistDbId) {
        const runDate = new Date().toLocaleDateString('en-GB');
        const missingSource = `Scheduled import – ${runDate}`;

        // Merge into the playlist's existing missing-tracks entry rather
        // than wiping and re-adding: tracks still missing get refreshed,
        // newly-missing tracks get added, and tracks that dropped off this
        // run's unmatched list are left alone as a record for the future
        // (per issue #33).
        const missingTracks = result.unmatched.map((t: any, i: number) => ({
          title: t.title || 'Unknown',
          artist: t.artist || 'Unknown',
          album: t.album,
          position: i + 1,
          source: missingSource,
        }));

        db.addMissingTracks(schedule.user_id, playlistDbId, missingTracks);

        logger.info('Saved missing tracks from scheduled import', {
          scheduleId: schedule.id,
          playlistId: playlistDbId,
          missingCount: missingTracks.length,
        });
      }

      // Update schedule last_run
      db.updateScheduleLastRun(schedule.id);

      // Update execution record with success
      if (executionId) {
        const matchedCount = result.matched?.filter((t: any) => t.matched).length || 0;
        const unmatchedCount = result.unmatched?.length || 0;
        db.updateScheduleExecution(executionId, 'success', matchedCount, unmatchedCount);
      }

      executed++;
      logger.info('Playlist refresh schedule executed successfully', {
        scheduleId: schedule.id,
        playlistName,
        trackCount: trackUris.length
      });
    } catch (error: any) {
      logger.error('Failed to execute playlist refresh schedule', {
        scheduleId: schedule.id,
        error: error.message,
        stack: error.stack
      });
      
      // Update execution record with failure
      if (executionId) {
        db.updateScheduleExecution(executionId, 'failed', 0, 0, error.message);
      }
      
      failed++;
    }
  }

  return { executed, failed };
}


/**
 * Resolve which Plex playlist a refresh/mix-generation run should write
 * into - never a new one. Schedules always fully regenerate their track
 * list, so the previous run's playlist (tracked by DB record, or by name
 * for schedules that predate tracking) is reused; a fresh playlist is only
 * created when nothing to reuse exists yet (a genuine first run). This is
 * what keeps a playlist's Plex ratingKey stable across every scheduled run.
 */
async function resolveTargetPlaylistId(
  plex: PlexClient,
  trackedPlaylist: { plex_playlist_id: string } | null | undefined,
  playlistName: string
): Promise<string | null> {
  if (trackedPlaylist?.plex_playlist_id && !trackedPlaylist.plex_playlist_id.startsWith('pending-')) {
    return trackedPlaylist.plex_playlist_id;
  }

  // No tracked record yet - fall back to a name search (covers a genuinely
  // first run, or a schedule that predates DB tracking).
  try {
    const playlists = await plex.getPlaylists();
    const existing = playlists.find((p: any) => p.title === playlistName);
    return existing?.ratingKey ?? null;
  } catch (error: any) {
    logger.warn('Failed to check for existing playlist', { playlistName, error: error.message });
    return null;
  }
}

/**
 * Replace a Plex playlist's contents in place - same ratingKey throughout -
 * by clearing its current items and adding the refreshed set, in order.
 */
async function replacePlaylistTracks(plex: PlexClient, playlistId: string, trackUris: string[]): Promise<void> {
  const existingTracks = await plex.getPlaylistTracks(playlistId);
  const existingItemIds = existingTracks
    .filter(t => t.playlistItemID != null)
    .map(t => String(t.playlistItemID));
  await plex.removeMultipleFromPlaylist(playlistId, existingItemIds);
  if (trackUris.length > 0) {
    await plex.addToPlaylist(playlistId, trackUris);
  }
}

/**
 * Write a generated mix's tracks into Plex and keep the DB record pointed
 * at it, reusing the schedule's existing playlist (by link, else by name)
 * so a mix schedule's ratingKey never changes across runs.
 */
async function createOrUpdateMixPlaylist(
  db: DatabaseService,
  plex: PlexClient,
  schedule: Schedule,
  playlistName: string,
  trackUris: string[],
  libraryId: string
): Promise<string> {
  const linked = schedule.playlist_id ? db.getPlaylistById(schedule.playlist_id) : null;
  const targetPlaylistId = await resolveTargetPlaylistId(plex, linked, playlistName);

  let ratingKey: string;
  if (targetPlaylistId) {
    await replacePlaylistTracks(plex, targetPlaylistId, trackUris);
    ratingKey = targetPlaylistId;
  } else {
    const created = await plex.createPlaylist(playlistName, libraryId, trackUris);
    ratingKey = created.ratingKey;
  }

  upsertMixPlaylistRecord(db, schedule, ratingKey, playlistName);
  return ratingKey;
}

/**
 * After creating/recreating a mix's Plex playlist, keep the local
 * `playlists` record (and the schedule's link to it) pointed at the new
 * ratingKey so the next run can find and delete it again instead of
 * accumulating duplicates.
 */
function upsertMixPlaylistRecord(
  db: DatabaseService,
  schedule: Schedule,
  newPlaylistId: string,
  playlistName: string
): void {
  if (schedule.playlist_id) {
    db.updatePlaylist(schedule.playlist_id, {
      plex_playlist_id: newPlaylistId,
      updated_at: Math.floor(Date.now() / 1000)
    });
    return;
  }

  const existing = db.getPlaylistByPlexId(schedule.user_id, newPlaylistId)
    ?? db.getPlaylistByUserAndName(schedule.user_id, playlistName);

  const playlistDbId = existing
    ? existing.id
    : db.createPlaylist(schedule.user_id, newPlaylistId, playlistName, 'plex', undefined).id;

  if (existing) {
    db.updatePlaylist(playlistDbId, {
      plex_playlist_id: newPlaylistId,
      updated_at: Math.floor(Date.now() / 1000)
    });
  }

  db.linkSchedulePlaylist(schedule.id, playlistDbId);
}

/**
 * Execute mix generation schedules that are due.
 *
 * @param schedulesOverride - When provided, these schedules are executed
 *   directly instead of querying `db.getDueSchedules()`. Used by
 *   runSingleSchedule() for manual "Run Now" triggers so it doesn't need to
 *   mutate any shared state on `db` (which is a process-wide singleton).
 */
async function executeMixGenerationSchedules(
  db: DatabaseService,
  schedulesOverride?: Schedule[]
): Promise<{ executed: number; failed: number }> {
  const dueSchedules = (schedulesOverride ?? db.getDueSchedules()).filter(s => s.schedule_type === 'mix_generation');
  
  let executed = 0;
  let failed = 0;

  const mixService = new MixService();

  for (const schedule of dueSchedules) {
    let executionId: number | null = null;
    try {
      logger.info('Executing mix generation schedule', { 
        scheduleId: schedule.id,
        userId: schedule.user_id
      });

      // Get user info
      const user = db.getUserById(schedule.user_id);
      if (!user) {
        logger.error('User not found for schedule', { scheduleId: schedule.id, userId: schedule.user_id });
        failed++;
        continue;
      }

      // Get user's server info
      const server = db.getUserServer(schedule.user_id);
      if (!server) {
        logger.error('Server not found for user', { scheduleId: schedule.id, userId: schedule.user_id });
        failed++;
        continue;
      }

      // Get user settings
      const settings = db.getUserSettings(schedule.user_id);

      // Parse schedule config to determine which mixes to generate
      const config = schedule.config ? JSON.parse(schedule.config) : {};
      
      const plex = new PlexClient(server.server_url, resolvePlexToken(user, server));

      // Check if this is a template-based schedule
      if (config.templateId) {
        // Template-based mix generation
        const template = db.getMixTemplateById(config.templateId);
        if (!template) {
          logger.error('Template not found for schedule', { 
            scheduleId: schedule.id, 
            templateId: config.templateId 
          });
          failed++;
          continue;
        }

        const playlistName = config.templateName || template.name;
        
        // Create execution record
        executionId = db.createScheduleExecution(schedule.id, schedule.user_id, playlistName);

        logger.info('Generating mix from template', {
          scheduleId: schedule.id,
          templateId: template.id,
          templateName: template.name,
          mixType: template.mix_type
        });

        // Generate mix using the template configuration
        const templateConfig = typeof template.configuration === 'string' 
          ? JSON.parse(template.configuration) 
          : template.configuration;

        let result;
        
        // Handle different mix types from templates
        if (template.mix_type === 'custom' && templateConfig.customRules) {
          // Custom mix from template
          result = await mixService.generateCustomMix(
            server.server_url,
            resolvePlexToken(user, server),
            server.library_id || '',
            {
              ...templateConfig,
              playlistName,
            }
          );
        } else {
          // Other mix types (weekly, daily, etc.)
          const mixType = templateConfig.mixType || template.mix_type;
          result = await generateMixByType(
            mixService,
            mixType,
            server.server_url,
            resolvePlexToken(user, server),
            server.library_id || '',
            settings,
            templateConfig
          );
        }

        if (!result || result.trackKeys.length === 0) {
          logger.warn('Template mix generation returned no tracks', { 
            templateId: template.id,
            scheduleId: schedule.id 
          });
          if (executionId) {
            db.updateScheduleExecution(executionId, 'failed', 0, 0, 'No tracks generated');
          }
          failed++;
          continue;
        }

        // Create or update playlist
        const templateRatingKey = await createOrUpdateMixPlaylist(
          db, plex, schedule, playlistName, result.trackKeys, server.library_id || ''
        );

        logger.info('Template mix generated successfully', {
          templateId: template.id,
          playlistName,
          trackCount: result.trackCount,
          playlistId: templateRatingKey
        });

        // Update template usage
        db.updateMixTemplateUsage(template.id);

        // Update execution record with success
        if (executionId) {
          db.updateScheduleExecution(executionId, 'success', result.trackCount, 0);
        }

      } else if (config.mixType) {
        // Individual mix type schedule (from quick mix settings)
        const mixType = config.mixType;
        const playlistName = config.mixName || `${mixType} Mix`;
        
        // Create execution record
        executionId = db.createScheduleExecution(schedule.id, schedule.user_id, playlistName);

        logger.info('Generating individual mix', {
          scheduleId: schedule.id,
          mixType,
          playlistName
        });

        const result = await generateMixByType(
          mixService,
          mixType,
          server.server_url,
          resolvePlexToken(user, server),
          server.library_id || '',
          settings,
          config
        );

        if (!result || result.trackKeys.length === 0) {
          logger.warn('Mix generation returned no tracks', { mixType, scheduleId: schedule.id });
          if (executionId) {
            db.updateScheduleExecution(executionId, 'failed', 0, 0, 'No tracks generated');
          }
          failed++;
          continue;
        }

        // Create or update playlist
        const mixRatingKey = await createOrUpdateMixPlaylist(
          db, plex, schedule, playlistName, result.trackKeys, server.library_id || ''
        );

        logger.info('Mix generated successfully', {
          mixType,
          playlistName,
          trackCount: result.trackCount,
          playlistId: mixRatingKey
        });

        // Update execution record with success
        if (executionId) {
          db.updateScheduleExecution(executionId, 'success', result.trackCount, 0);
        }

      } else {
        // Legacy format: config.mixes array
        const mixTypes = config.mixes || ['weekly', 'daily', 'timecapsule', 'newmusic'];

        for (const mixType of mixTypes) {
          try {
            const playlistName = `${mixType.charAt(0).toUpperCase() + mixType.slice(1)} Mix`;
            
            const result = await generateMixByType(
              mixService,
              mixType,
              server.server_url,
              resolvePlexToken(user, server),
              server.library_id || '',
              settings,
              {}
            );

            if (!result || result.trackKeys.length === 0) {
              logger.warn('Mix generation returned no tracks', { mixType, scheduleId: schedule.id });
              continue;
            }

            // Create or update playlist by name. Legacy multi-mix schedules
            // generate several differently-named playlists per run, so
            // there's no single schedule.playlist_id to link them to (unlike
            // the template/quick-mix branches above) - matching this
            // branch's existing no-DB-record behavior.
            const legacyTargetId = await resolveTargetPlaylistId(plex, null, playlistName);
            let legacyRatingKey: string;
            if (legacyTargetId) {
              await replacePlaylistTracks(plex, legacyTargetId, result.trackKeys);
              legacyRatingKey = legacyTargetId;
            } else {
              const created = await plex.createPlaylist(playlistName, server.library_id || '', result.trackKeys);
              legacyRatingKey = created.ratingKey;
            }

            logger.info('Mix generated successfully', {
              mixType,
              playlistName,
              trackCount: result.trackCount,
              playlistId: legacyRatingKey
            });
          } catch (error: any) {
            logger.error('Failed to generate mix', { 
              mixType,
              scheduleId: schedule.id,
              error: error.message
            });
          }
        }
      }

      // Update schedule last_run
      db.updateScheduleLastRun(schedule.id);

      executed++;
      logger.info('Mix generation schedule executed successfully', { scheduleId: schedule.id });
    } catch (error: any) {
      logger.error('Failed to execute mix generation schedule', { 
        scheduleId: schedule.id,
        error: error.message,
        stack: error.stack
      });
      
      // Update execution record with failure
      if (executionId) {
        db.updateScheduleExecution(executionId, 'failed', 0, 0, error.message);
      }
      
      failed++;
    }
  }

  return { executed, failed };
}

/**
 * Helper function to generate a mix by type
 */
async function generateMixByType(
  mixService: MixService,
  mixType: string,
  serverUrl: string,
  plexToken: string,
  libraryId: string,
  settings: any,
  config: any
): Promise<any> {
  switch (mixType) {
    case 'weekly':
      return await mixService.generateWeeklyMix(
        serverUrl,
        plexToken,
        libraryId,
        settings.mix_settings.weeklyMix
      );

    case 'daily':
      return await mixService.generateDailyMix(
        serverUrl,
        plexToken,
        libraryId,
        settings.mix_settings.dailyMix
      );

    case 'timecapsule':
      return await mixService.generateTimeCapsule(
        serverUrl,
        plexToken,
        libraryId,
        settings.mix_settings.timeCapsule
      );

    case 'newmusic':
      return await mixService.generateNewMusicMix(
        serverUrl,
        plexToken,
        libraryId,
        settings.mix_settings.newMusic
      );

    case 'deepcuts':
      return await mixService.generateDeepCutsMix(
        serverUrl,
        plexToken,
        libraryId,
        config
      );

    case 'workout':
      return await mixService.generateWorkoutMix(
        serverUrl,
        plexToken,
        libraryId,
        config
      );

    case 'forgottenfavorites':
      return await mixService.generateForgottenFavoritesMix(
        serverUrl,
        plexToken,
        libraryId,
        config
      );

    default:
      throw new Error(`Unknown mix type: ${mixType}`);
  }
}

/**
 * Run schedule checker job (checks both playlist refresh and mix generation)
 */
export async function runScheduleCheckerJob(db: DatabaseService): Promise<void> {
  logger.debug('Starting schedule checker job');

  const playlistResults = await executePlaylistRefreshSchedules(db);
  const mixResults = await executeMixGenerationSchedules(db);

  // The checker wakes up on a timer and almost always finds nothing due, so
  // only a run that actually did something is worth an info line - otherwise
  // it emits a pair of lines every few minutes forever and drowns the log.
  const didSomething = playlistResults.executed + playlistResults.failed +
                       mixResults.executed + mixResults.failed > 0;
  // No `timestamp` in the meta - winston stamps every line itself, and this
  // one overwrote it with a different (ISO) format, so these lines sorted and
  // filtered differently from every other line in the log.
  logger[didSomething ? 'info' : 'debug']('Schedule checker job completed', {
    playlistRefresh: playlistResults,
    mixGeneration: mixResults,
  });
}

/**
 * Run a single schedule immediately (for manual "Run Now" and "Refresh All"
 * triggers) - executes the schedule logic without checking if it's due, and
 * reports live progress into a notification the header's bell reads, so a
 * manually-triggered run (unlike the automatic cron-driven checker) always
 * gives the user something to watch instead of just vanishing until it's done.
 */
export async function runSingleSchedule(db: DatabaseService, schedule: any): Promise<void> {
  logger.info('Manually running single schedule', {
    scheduleId: schedule.id,
    scheduleType: schedule.schedule_type,
    userId: schedule.user_id
  });

  const title = scheduleDisplayName(db, schedule);
  const notification = addNotification(schedule.user_id, {
    type: 'schedule',
    title,
    detail: schedule.schedule_type === 'mix_generation' ? 'Generating mix' : 'Starting...',
    status: 'in-progress',
    progress: 0,
  });

  try {
    if (schedule.schedule_type === 'playlist_refresh') {
      // Execute the playlist refresh logic for just this schedule, passing
      // it directly as an override instead of monkey-patching
      // db.getDueSchedules(). `db` is a process-wide singleton and this
      // function can be invoked fire-and-forget (not awaited) concurrently
      // for multiple schedules (e.g. "Run Now" and "Run All Schedules"), so
      // mutating shared state on it is not safe - overlapping calls would
      // race on saving/restoring the original method.
      const progressEmitter = new EventEmitter();
      progressEmitter.on('progress', (data: { phase?: string; current?: number; total?: number; currentTrackName?: string }) => {
        const detail = data.phase === 'matching' ? 'Matching tracks with your Plex library...' : (data.currentTrackName || 'Fetching tracks...');
        const progress = data.total ? Math.round(((data.current || 0) / data.total) * 100) : undefined;
        updateNotification(schedule.user_id, notification.id, { detail, progress });
      });

      // executePlaylistRefreshSchedules() catches its own per-schedule
      // errors (to keep a batch of many due schedules going) rather than
      // throwing, so success/failure has to be read from its returned
      // counts, not from whether this call rejects.
      const { failed } = await executePlaylistRefreshSchedules(db, [schedule], progressEmitter);
      if (failed > 0) throw new Error('Playlist refresh failed - check the logs for details');

      updateNotification(schedule.user_id, notification.id, { status: 'success', progress: 100, detail: 'Refreshed' });
      logger.info('Manual playlist refresh completed', {
        scheduleId: schedule.id
      });
    } else if (schedule.schedule_type === 'mix_generation') {
      // Same rationale as above - pass the schedule directly rather than
      // mutating shared db state. Mix generation is a single Plex-side
      // selection call with no meaningful sub-progress to report, so this
      // just tracks start/success/failure rather than a percentage.
      const { failed } = await executeMixGenerationSchedules(db, [schedule]);
      if (failed > 0) throw new Error('Mix generation failed - check the logs for details');

      updateNotification(schedule.user_id, notification.id, { status: 'success', progress: 100, detail: 'Generated' });
      logger.info('Manual mix generation completed', {
        scheduleId: schedule.id
      });
    } else {
      throw new Error(`Unknown schedule type: ${schedule.schedule_type}`);
    }
  } catch (error: any) {
    logger.error('Failed to manually run schedule', {
      scheduleId: schedule.id,
      error: error.message,
      stack: error.stack
    });
    updateNotification(schedule.user_id, notification.id, { status: 'error', detail: error.message || 'Failed' });
    throw error;
  }
}

/** Best-effort display name for a schedule's notification title - doesn't
 * need to match the executor's own playlistName resolution exactly, just be
 * recognizable to the user who triggered it. */
function scheduleDisplayName(db: DatabaseService, schedule: any): string {
  if (schedule.playlist_id) {
    const playlist = db.getPlaylistById(schedule.playlist_id);
    if (playlist) return playlist.name;
  }
  const config = schedule.config ? JSON.parse(schedule.config) : {};
  return config.playlistName || config.chartName || config.templateName || config.mixName
    || (schedule.schedule_type === 'mix_generation' ? 'Scheduled mix' : 'Scheduled playlist refresh');
}
