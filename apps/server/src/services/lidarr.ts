import axios from 'axios';
import { configService } from '../config';
import { logger } from '../utils/logger';
import { updateNotification } from './job-notifications';
import { matchPlaylist, insertMatchedTrackIntoPlaylist } from './matching';
import { PlexClient } from './plex';
import type { DatabaseService } from '../database/database';

/** Lidarr manages artist/album *monitoring* + triggers indexer/download-client
 * searches - it has no "download this one track" endpoint like deemix-server
 * does. So the flow here is: find (or add-and-monitor) the artist, find (or
 * skip) the matching album, then trigger a search command and poll it -
 * Lidarr's own indexers/download client (configured by the user in Lidarr
 * itself, not by this app) do the actual downloading. */

function client() {
  const { lidarrUrl, lidarrApiKey } = configService.config;
  if (!lidarrUrl || !lidarrApiKey) {
    throw new Error('Lidarr is not configured - set its URL and API key in Settings > Admin > Lidarr');
  }
  return axios.create({
    baseURL: `${lidarrUrl}/api/v1`,
    headers: { 'X-Api-Key': lidarrApiKey },
    timeout: 15000,
  });
}

export interface LidarrArtistLookup {
  artistName: string;
  foreignArtistId: string;
  /** Only present/non-zero if this artist is already added to Lidarr. */
  id?: number;
}

/** Searches MusicBrainz (via Lidarr's own lookup proxy) for an artist by
 * name - the same source Lidarr's "Add New Artist" UI search uses. */
export async function findLidarrArtist(name: string): Promise<LidarrArtistLookup | null> {
  const { data } = await client().get<LidarrArtistLookup[]>('/artist/lookup', { params: { term: name } });
  return data?.[0] ?? null;
}

// ponytail: uses whichever root folder / quality profile / metadata profile
// Lidarr lists first, since this app has no per-artist configuration UI for
// them. Fine for a single-root-folder/single-profile Lidarr setup (the
// common case); add a settings picker if that stops being true.
async function firstOrThrow<T>(path: string, label: string): Promise<T> {
  const { data } = await client().get<T[]>(path);
  if (!data?.length) throw new Error(`Lidarr has no ${label} configured`);
  return data[0];
}

/** Adds and monitors an artist found via findLidarrArtist(), or returns the
 * existing Lidarr artist id if it's already there. */
export async function addAndMonitorArtist(lookup: LidarrArtistLookup): Promise<number> {
  if (lookup.id) return lookup.id;

  const [rootFolder, qualityProfile, metadataProfile] = await Promise.all([
    firstOrThrow<{ path: string }>('/rootfolder', 'root folder'),
    firstOrThrow<{ id: number }>('/qualityprofile', 'quality profile'),
    firstOrThrow<{ id: number }>('/metadataprofile', 'metadata profile'),
  ]);

  const { data } = await client().post('/artist', {
    artistName: lookup.artistName,
    foreignArtistId: lookup.foreignArtistId,
    qualityProfileId: qualityProfile.id,
    metadataProfileId: metadataProfile.id,
    rootFolderPath: rootFolder.path,
    monitored: true,
    addOptions: { monitor: 'all', searchForMissingAlbums: false },
  });
  return data.id;
}

interface LidarrAlbum {
  id: number;
  title: string;
}

/** Finds an already-monitored album by title (loose match), so a search can
 * target just that album instead of the artist's whole discography. */
export async function findAlbumForArtist(artistId: number, albumTitle?: string): Promise<LidarrAlbum | null> {
  if (!albumTitle) return null;
  const { data } = await client().get<LidarrAlbum[]>('/album', { params: { artistId } });
  const normalize = (s: string) => s.trim().toLowerCase();
  const target = normalize(albumTitle);
  return data.find(a => normalize(a.title) === target) || data.find(a => normalize(a.title).includes(target)) || null;
}

/** Triggers an AlbumSearch (if an album was resolved) or a broader
 * ArtistSearch (if not) across Lidarr's configured indexers, and returns the
 * command id to poll. */
export async function triggerLidarrSearch(artistId: number, albumId: number | null): Promise<number> {
  const body = albumId ? { name: 'AlbumSearch', albumIds: [albumId] } : { name: 'ArtistSearch', artistId };
  const { data } = await client().post('/command', body);
  return data.id;
}

interface LidarrCommand {
  status: string; // 'queued' | 'started' | 'completed' | 'failed'
  result?: string;
}

async function getLidarrCommand(commandId: number): Promise<LidarrCommand | null> {
  try {
    const { data } = await client().get<LidarrCommand>(`/command/${commandId}`);
    return data;
  } catch (error: any) {
    logger.warn('[Lidarr] Failed to poll command', { commandId, error: error.message });
    return null;
  }
}

const LIDARR_POLL_INTERVAL_MS = 10 * 1000;
const LIDARR_POLL_MAX_MS = 15 * 60 * 1000;

/** Context needed to reconcile a Lidarr-triggered download back to the
 * missing-track record it was searched for, once the file has actually
 * arrived and Plex has scanned it - same idea as deemix.ts's
 * DeemixReconcileContext/reconcileDownloadedTrack. */
export interface LidarrReconcileContext {
  db: DatabaseService;
  missingTrackId: number;
  serverUrl: string;
  plexToken: string;
  libraryId?: string | null;
  serverClientId?: string | null;
}

/**
 * Polls a Lidarr search command until it finishes (Lidarr itself only
 * reports "search command done", not "file downloaded" - the actual
 * grab/import happens asynchronously via Lidarr's download client), then
 * hands off to the same bounded "wait for Plex to find the file" reconcile
 * loop the Deemix path uses.
 */
export async function trackLidarrSearch(userId: number, notificationId: string, commandId: number, reconcile: LidarrReconcileContext): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < LIDARR_POLL_MAX_MS) {
    await new Promise(resolve => setTimeout(resolve, LIDARR_POLL_INTERVAL_MS));
    const command = await getLidarrCommand(commandId);
    if (!command) continue;

    if (command.status === 'completed') {
      updateNotification(userId, notificationId, { detail: 'Search complete - waiting for Lidarr to grab and import it' });
      await reconcileLidarrDownload(userId, notificationId, reconcile);
      return;
    }
    if (command.status === 'failed') {
      updateNotification(userId, notificationId, { status: 'error', detail: command.result || 'Lidarr search failed' });
      return;
    }
    updateNotification(userId, notificationId, { detail: `Searching (${command.status})...` });
  }
  logger.error('[Lidarr] Timed out waiting for search command to finish', { commandId, userId });
  updateNotification(userId, notificationId, { status: 'error', detail: 'Timed out waiting for Lidarr' });
}

const RECONCILE_POLL_INTERVAL_MS = 30 * 1000;
// ponytail: same fixed-attempt heuristic as deemix.ts's reconciliation -
// neither Lidarr's grab/import timing nor Plex's scan interval are known to
// this server. Revisit if misses turn out to be common.
const RECONCILE_MAX_ATTEMPTS = 10;

async function reconcileLidarrDownload(userId: number, notificationId: string, reconcile: LidarrReconcileContext): Promise<void> {
  const { db, missingTrackId } = reconcile;

  for (let attempt = 1; attempt <= RECONCILE_MAX_ATTEMPTS; attempt++) {
    const track = db.getUserMissingTracks(userId).find(t => t.id === missingTrackId);
    if (!track) return; // Resolved another way while this was searching.

    updateNotification(userId, notificationId, {
      status: 'success',
      progress: 100,
      detail: `Waiting for Lidarr to grab and Plex to find it (${attempt}/${RECONCILE_MAX_ATTEMPTS})`,
    });

    await new Promise(resolve => setTimeout(resolve, RECONCILE_POLL_INTERVAL_MS));

    try {
      const settings = db.getUserSettings(userId);
      const [matched] = await matchPlaylist(
        [{ title: track.title, artist: track.artist, album: track.album || '' }],
        reconcile.serverUrl,
        reconcile.plexToken,
        reconcile.libraryId || undefined,
        settings.matching_settings
      );

      if (matched?.matched && matched.plexRatingKey) {
        const plexClient = new PlexClient(reconcile.serverUrl, reconcile.plexToken);
        const inserted = await insertMatchedTrackIntoPlaylist(
          db,
          plexClient,
          { server_client_id: reconcile.serverClientId, library_id: reconcile.libraryId },
          track,
          matched.plexRatingKey
        );
        if (inserted) {
          updateNotification(userId, notificationId, { status: 'success', progress: 100, detail: 'Downloaded and matched via Lidarr' });
          return;
        }
      }
    } catch (error: any) {
      logger.warn('[Lidarr] Reconciliation attempt failed, retrying', { error: error.message, missingTrackId, attempt });
    }
  }

  updateNotification(userId, notificationId, {
    status: 'success',
    progress: 100,
    detail: 'Lidarr search sent - could not auto-match yet, use Retry',
  });
}
