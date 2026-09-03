import axios from 'axios';
import * as fs from 'fs';
import { exec } from 'child_process';
import { promisify } from 'util';
import { configService } from '../config';
import { logger } from '../utils/logger';
import { addNotification, updateNotification } from './job-notifications';
import { matchPlaylist, insertMatchedTrackIntoPlaylist, scorePlexCandidate, buildAllVariants, pickEffectiveVariant } from './matching';
import { PlexClient, resolvePlexToken } from './plex';
import type { DatabaseService } from '../database/database';
import type { MatchingSettings } from '../database/types';

const execAsync = promisify(exec);

const DEEMIX_URL = (process.env.DEEMIX_URL || 'http://127.0.0.1:6595').replace(/\/$/, '');
// This is our own local deemix-server install (see /opt/deemix-server), not
// a generic third-party API - these point at its config file and systemd
// unit so admin-edited settings can be written and applied directly.
const DEEMIX_CONFIG_PATH = process.env.DEEMIX_CONFIG_PATH || '/opt/deemix-server/config/config.json';
export const DEEMIX_SERVICE_NAME = process.env.DEEMIX_SERVICE_NAME || 'deemix-server.service';

interface DeemixSearchResult {
  id: number;
  title: string;
  link: string;
  artist: { name: string };
  album: { title: string; id?: number };
}

/** Deezer's own search relevance ranking is good enough for "artist title"
 * queries - deemix's own web UI doesn't re-rank results either, it just
 * lists them in this order. Returns the raw top results, caller picks one. */
export async function searchDeemixTrack(term: string): Promise<DeemixSearchResult[]> {
  const { data } = await axios.get(`${DEEMIX_URL}/api/search`, {
    params: { term, type: 'track', start: 0, nb: 5 },
    timeout: 10000,
  });
  return data?.data ?? [];
}

/**
 * Picks the best of a search's top hits, or returns nothing when none of
 * them is actually the track being looked for.
 *
 * Taking results[0] blind - the previous behaviour - is worst exactly here:
 * a track is missing precisely because it's hard to find, and when it isn't
 * in Deezer's catalogue at all the top hit is routinely a karaoke version,
 * a cover, or an unrelated song that merely shares a title. That then gets
 * downloaded into the library permanently. Candidates are instead scored
 * with scorePlexCandidate() - the very scorer used to match a track against
 * Plex, which only ever reads title/artist/album off its candidate, so a
 * Deezer hit shaped into those three fields scores on exactly the same
 * scale - and a download has to clear the user's own minMatchScore just
 * like any other match would.
 *
 * @param artist - Empty for a title-only search (the "Various Artists"
 *   compilation case, where there is no real artist to score against);
 *   scoring then uses each candidate's own artist, which amounts to scoring
 *   on title alone.
 */
export async function findBestDeemixMatches(
  title: string,
  artist: string,
  settings: MatchingSettings,
  limit = 1
): Promise<Array<{ match: DeemixSearchResult; score: number }>> {
  const results = await searchDeemixTrack(`${artist} ${title}`.trim());
  // Stored as a 0-1 fraction on older settings rows and as 0-100 on newer
  // ones - matchPlaylist() normalizes the same way before comparing.
  const minScore = settings.minMatchScore <= 1 ? settings.minMatchScore * 100 : settings.minMatchScore;

  const scored = await Promise.all(results.map(async match => {
    // With no artist given, each candidate is scored against its own
    // artist, which makes both the gate and the score title-only.
    const sourceArtist = artist || match.artist?.name || '';
    const source = { title, artist: sourceArtist, album: match.album?.title || '' };
    const candidate = {
      title: match.title,
      grandparentTitle: match.artist?.name || '',
      parentTitle: match.album?.title || '',
    };
    // Deezer's own search already copes well with a Japanese-script query -
    // it's a real relevance search, not the literal substring match Plex
    // does, and routinely finds the right track under its native kana/kanji
    // spelling. What it doesn't do is compare it back: Deezer's own tag for
    // that result is commonly romanized ("シリウス" found as "sirius"),
    // which title/artist comparison would otherwise reject on script alone.
    // Shared with findBestMatch's identical fallback for Plex matching.
    const variants = await buildAllVariants(source);
    const { track: effectiveSource, gate } = pickEffectiveVariant(variants, candidate, settings);
    // The score alone is not enough: artist scoring bottoms out at 70, so
    // the right title by the wrong artist still clears most thresholds.
    // The gate is what actually rejects those, exactly as it does during
    // an import.
    if (!gate.passes) return null;
    return { match, score: scorePlexCandidate(effectiveSource.title, effectiveSource.artist, candidate, settings).score };
  }));

  return scored
    .filter((candidate): candidate is { match: DeemixSearchResult; score: number } => candidate !== null && candidate.score >= minScore)
    .sort((a, b) => b.score - a.score)
    .slice(0, limit);
}

/** Deezer's public catalogue API (no auth needed, same one deemix itself
 * reads metadata from) - used only to check nb_tracks before deciding
 * whether a match's release is a real multi-track album/EP or a standalone
 * single. Returns null on any failure so the caller can fall back to
 * downloading just the one matched track rather than blocking on this. */
async function getAlbumTrackCount(albumId: number): Promise<number | null> {
  try {
    const { data } = await axios.get(`https://api.deezer.com/album/${albumId}`, { timeout: 8000 });
    return typeof data?.nb_tracks === 'number' ? data.nb_tracks : null;
  } catch (error: any) {
    logger.warn('[Deemix] Failed to fetch album track count, downloading matched track only', { albumId, error: error.message });
    return null;
  }
}

/** Picks what URL to queue for a search match: the track itself, unless it's
 * one of several tracks on a real album/EP (not a standalone single), in
 * which case the whole album is queued instead so the rest of the
 * collection downloads along with it. This is the one place that decides
 * this, so it applies equally to every caller (the single-track "Deemix"
 * button and the "Deemix All" loop both go through queueDeemixDownload()). */
export async function resolveDownloadUrl(match: DeemixSearchResult): Promise<string> {
  if (!match.album?.id) return match.link;
  const trackCount = await getAlbumTrackCount(match.album.id);
  if (trackCount && trackCount > 1) {
    return `https://www.deezer.com/album/${match.album.id}`;
  }
  return match.link;
}

// deemix-server's login is a browser-style session (express-session, kept in
// memory only) tied to whatever cookie the caller sends - logging in through
// its web UI doesn't help server-to-server calls like ours, which have no
// cookie of their own. So this holds one login session for the whole app,
// established from DEEMIX_ARL and reused across requests, and re-established
// if deemix-server restarts (its session store is in-memory, so a restart
// there silently logs everyone out).
let sessionCookie: string | null = null;

/** Forces the next queueDeemixDownload() call to log in again - call after
 * the admin sets a new ARL so it takes effect immediately. */
export function resetDeemixSession(): void {
  sessionCookie = null;
}

async function login(): Promise<void> {
  const arl = configService.config.deemixArl;
  if (!arl) {
    throw new Error('Deemix ARL is not configured - set it in Settings > Admin > Deemix');
  }
  const response = await axios.post(`${DEEMIX_URL}/api/loginArl`, { arl }, { timeout: 15000 });
  const setCookie = response.headers['set-cookie'];
  if (!setCookie?.length) {
    throw new Error('deemix login did not return a session cookie');
  }
  sessionCookie = setCookie.map((c: string) => c.split(';')[0]).join('; ');
  if (response.data?.status === 0) {
    sessionCookie = null;
    throw new Error('deemix rejected DEEMIX_ARL - it may have expired, get a fresh one from your Deezer account');
  }
}

export interface QueuedDeemixTrack {
  uuid: string;
  title: string;
  artist: string;
  /** true when this wasn't a fresh queue add - deemix-server already had
   * this exact track+bitrate queued (currently downloading, or a previous
   * attempt that finished or failed and was never cleared). */
  alreadyQueued?: boolean;
}

/** deemix-server's queue entry for a uuid returned by queueDeemixDownload().
 * status is 'inQueue' | 'downloading' | 'completed' | 'withErrors' | 'failed'.
 * size/downloaded are always present (1/0 or 1/1 for a single track, N/<= N
 * for an album) - deemix-server tracks progress by completed-file count, not
 * bytes, so an album's progress climbs in visible per-track steps rather
 * than smoothly. */
export interface DeemixQueueItem {
  status: string;
  progress: number;
  title: string;
  artist: string;
  size: number;
  downloaded: number;
  errors: Array<{ message: string }>;
}

export async function getDeemixSettings(): Promise<Record<string, any>> {
  const { data } = await axios.get(`${DEEMIX_URL}/api/getSettings`, { timeout: 10000 });
  return data?.settings ?? {};
}

/** deemix-server's own settings-save paths (the REST /api/saveSettings
 * route, and the websocket 'saveSettings' event the real web UI uses) don't
 * work for us: the REST route reads its payload from the query string and
 * 500s on any real save, and the websocket protocol is a bespoke raw-`ws`
 * JSON format the checked-in webui bundle - built for socket.io - doesn't
 * actually speak either. Writing the config file directly and restarting is
 * what actually works. settings.load() only reads this file at process
 * start, so the restart is what makes a save take effect. */
export async function updateDeemixSettings(settings: Record<string, any>): Promise<void> {
  fs.writeFileSync(DEEMIX_CONFIG_PATH, JSON.stringify(enforceLibraryLayout(settings), null, 2), 'utf-8');
  resetDeemixSession();
  cachedBitrate = null;
  await execAsync(`systemctl restart ${DEEMIX_SERVICE_NAME}`);
}

/**
 * deemix's default splits a release into `CD1`/`CD2` subfolders below the
 * album folder. Plex reads the deepest folder as the album, so every
 * download landed in the library as an album literally named "CD1" instead
 * of under `Artist/Album/`. Unlike the artist/album folder and filename
 * templates - which are genuine preferences, and are editable in Settings >
 * Admin > Deemix - there is no value of this that produces a readable
 * library, so it's forced rather than exposed. Applied on every save (not
 * once at install) so a hand-edited or upgrade-reset deemix config can't
 * quietly reintroduce it.
 */
function enforceLibraryLayout(settings: Record<string, any>): Record<string, any> {
  return { ...settings, createCDFolder: false };
}

/** deemix-server is supposed to fall back to the account's configured
 * bitrate when none is given, but its check only handles the literal string
 * "null" - an actually-omitted bitrate comes through as JS `undefined`,
 * which slips past that check and gets baked into the queue item's uuid as
 * literally "..._undefined", breaking bitrate matching during download. So
 * the configured bitrate is fetched and sent explicitly instead of relying
 * on that fallback. */
// Cached because this is read on every single queue add, and the only thing
// that changes it is an admin saving deemix's settings - which clears it.
// Without the cache, "Deemix All" over a few hundred tracks pulled deemix's
// entire settings blob once per track for a value that never moved.
let cachedBitrate: string | null = null;

async function getConfiguredBitrate(): Promise<string> {
  if (!cachedBitrate) {
    const settings = await getDeemixSettings();
    cachedBitrate = String(settings.maxBitrate || '3');
  }
  return cachedBitrate;
}

/**
 * @param fallbackUrl - Queued instead if `trackUrl` produces nothing. deemix
 *   can fail to build a download for a whole album while the individual
 *   track is perfectly fine (its own Deezer metadata mapper throws on
 *   releases with an unexpected track shape - "Cannot read properties of
 *   undefined (reading 'HREF')" in its journal), and since queueing the
 *   album at all is our optimisation rather than what the user asked for,
 *   falling back to the single track gets them the track they wanted
 *   instead of an error.
 */
export async function queueDeemixDownload(trackUrl: string, fallbackUrl?: string): Promise<QueuedDeemixTrack> {
  if (!sessionCookie) {
    await login();
  }

  const bitrate = await getConfiguredBitrate();
  const attempt = async () => axios.post(
    `${DEEMIX_URL}/api/addToQueue`,
    { url: trackUrl, bitrate },
    { timeout: 10000, headers: { Cookie: sessionCookie! } }
  );

  // deemix-server answers with HTTP 200 even when it rejects the job (e.g.
  // {"result":false,"errid":"NotLoggedIn"}) - the status code alone can't
  // tell success from failure.
  let { data } = await attempt();
  if (data?.result === false && data.errid === 'NotLoggedIn') {
    await login();
    ({ data } = await attempt());
  }

  if (data?.result === false) {
    throw new Error(`deemix rejected the download (${data.errid || 'unknown error'})`);
  }

  const queued = Array.isArray(data?.data?.obj) ? data.data.obj[0] : data?.data?.obj;
  if (queued?.uuid) {
    return { uuid: queued.uuid, title: queued.title, artist: queued.artist };
  }

  // An empty obj (rather than a rejection) means deemix-server treated this
  // as "already in queue" and silently dropped it instead of adding a
  // duplicate - not a real failure, but the response carries no info about
  // it. deemix's own uuid scheme is deterministic (`${type}_${id}_${bitrate}`,
  // where type is "track" or "album" depending on the URL), so it can be
  // looked up directly to report its actual current state instead of a bare
  // "did not return a queued item".
  const urlMatch = trackUrl.match(/\/(track|album)\/(\d+)/);
  if (urlMatch) {
    const [, type, id] = urlMatch;
    const uuid = `${type}_${id}_${bitrate}`;
    const existing = await getDeemixQueueItem(uuid);
    if (existing) {
      logger.info('[Deemix] Already in deemix queue, reporting its existing state', { uuid, status: existing.status });
      return { uuid, title: existing.title, artist: existing.artist, alreadyQueued: true };
    }
  }

  // deemix answers 200/result:true even when generateDownloadObject() threw
  // for every url it was given - the reason only goes to its own websocket
  // listener and journal, never into this response - so an empty obj with no
  // queue entry behind it is the only signal we get that it failed.
  if (fallbackUrl && fallbackUrl !== trackUrl) {
    logger.warn('[Deemix] deemix could not queue this release, retrying with the single track', { trackUrl, fallbackUrl });
    return queueDeemixDownload(fallbackUrl);
  }

  logger.error('[Deemix] addToQueue returned no queued item and no matching existing queue entry', { trackUrl, bitrate, responseData: data });
  throw new Error(`deemix could not queue ${trackUrl} - it is most likely unavailable on the configured Deezer account (check deemix-server's own log for the reason)`);
}

// deemix-server has no per-item status endpoint - the only way to read one
// download's progress is to GET its entire queue. With "Deemix All" on a big
// playlist that meant every one of N in-flight downloads independently
// re-fetching and re-parsing the same N-entry queue payload every poll
// interval, i.e. N*N JSON work per interval, which pegged the CPU and made
// the whole instance unresponsive. One shared snapshot per interval, reused
// by every poller, makes that N-independent: one request, one parse.
// Caching the promise rather than the resolved queue makes one field do
// both jobs: callers arriving mid-fetch await the same request instead of
// starting their own, and callers arriving after it resolves get the
// already-settled promise until it ages out.
let queueFetch: { at: number; promise: Promise<Record<string, DeemixQueueItem>> } | null = null;
const QUEUE_SNAPSHOT_TTL_MS = 2000;

// Deliberately sent without a session cookie: deemix-server's /api/getQueue
// handler just returns its global in-memory queue and performs no login
// check at all (unlike /api/addToQueue, which throws NotLoggedIn). Sending
// one made reads look session-dependent, and the matching `if
// (!sessionCookie) return null` guard below turned every admin ARL or
// settings save - both of which call resetDeemixSession() - into a null
// item for every in-flight poller, which trackDeemixDownload() then read as
// "vanished, so it must have completed" and reported as success.
async function getDeemixQueue(): Promise<Record<string, DeemixQueueItem>> {
  if (!queueFetch || Date.now() - queueFetch.at >= QUEUE_SNAPSHOT_TTL_MS) {
    const promise = axios
      .get(`${DEEMIX_URL}/api/getQueue`, { timeout: 10000 })
      .then(response => response.data?.queue ?? {});
    const entry = { at: Date.now(), promise };
    queueFetch = entry;
    // A failed fetch must not be cached for the rest of the TTL, or every
    // poller in that window inherits the same rejection - but only clear it
    // if it is still the current snapshot, since a fetch that fails after
    // its TTL expired would otherwise throw away a newer in-flight one.
    promise.catch(() => { if (queueFetch === entry) queueFetch = null; });
  }
  return queueFetch.promise;
}

/** Reads one item's live download status/progress out of the shared queue
 * snapshot (see getDeemixQueue) rather than fetching the queue itself. */
export async function getDeemixQueueItem(uuid: string): Promise<DeemixQueueItem | null> {
  const queue = await getDeemixQueue();
  return queue[uuid] ?? null;
}

const DEEMIX_POLL_INTERVAL_MS = 3000;
const DEEMIX_POLL_MAX_MS = 30 * 60 * 1000;

/** Context needed to reconcile a completed deemix download back to the
 * missing-track record it was downloaded for - resolving the newly-arrived
 * file against Plex and inserting it into its playlist, instead of leaving
 * the user to notice the file exists and hit "Retry" themselves. */
export interface DeemixReconcileContext {
  db: DatabaseService;
  missingTrackId: number;
  serverUrl: string;
  plexToken: string;
  libraryId?: string | null;
  serverClientId?: string | null;
}

export interface DeemixDownloadRequest {
  db: DatabaseService;
  userId: number;
  /** Notification title - the track the user actually asked for, which is
   * not necessarily what got queued (a track on an album queues the album). */
  title: string;
  detail: string;
  uuid: string;
  alreadyQueued?: boolean;
  /** Omitted by the admin bulk button, whose rows are aggregated across
   * users and so have no single missing_tracks row to resolve back to. */
  reconcile?: DeemixReconcileContext;
}

/**
 * The one entry point for starting to track a queued deemix download:
 * opens the notification the header shows, records the download so it
 * survives a restart of this server (see resumeDeemixDownloads), and starts
 * the progress poller. Every caller used to assemble those three steps
 * itself, minus the persistence - which is how a restart mid-download came
 * to strand a track as "missing" even after its file had arrived.
 */
export function startDeemixDownload(request: DeemixDownloadRequest): void {
  const { db, userId, title, detail, uuid, alreadyQueued, reconcile } = request;
  const notification = addNotification(userId, {
    type: 'deemix',
    title,
    detail,
    status: 'in-progress',
    progress: 0,
  });
  const downloadId = db.addDeemixDownload(userId, uuid, title, detail, reconcile?.missingTrackId ?? null);
  trackDeemixDownload({
    db,
    userId,
    notificationId: notification.id,
    uuid,
    checkImmediately: alreadyQueued,
    reconcile,
    downloadId,
  });
}

// deemix writes its queue to disk and restores it on startup, so a download
// that was in flight when this server went down is usually still there to
// be watched when it comes back. Rows older than this aren't worth
// resuming - by then deemix has long since dropped the queue entry, so
// polling for it would only ever find nothing.
const RESUME_MAX_AGE_MS = 24 * 60 * 60 * 1000;

/**
 * Restarts the progress poller for every download still recorded as in
 * flight. Called once at startup: without it, a restart left deemix happily
 * downloading files that nothing was waiting for, so the tracks they were
 * for stayed missing until someone manually hit Retry.
 */
export function resumeDeemixDownloads(db: DatabaseService): void {
  let rows;
  try {
    rows = db.getActiveDeemixDownloads(RESUME_MAX_AGE_MS);
  } catch (error: any) {
    logger.error('[Deemix] Failed to read in-flight downloads to resume', { error: error.message });
    return;
  }
  if (rows.length === 0) return;

  for (const row of rows) {
    const notification = addNotification(row.user_id, {
      type: 'deemix',
      title: row.title,
      detail: row.detail ?? undefined,
      status: 'in-progress',
      progress: 0,
    });
    trackDeemixDownload({
      db,
      userId: row.user_id,
      notificationId: notification.id,
      uuid: row.uuid,
      // It may well have finished while this server was down, so don't sit
      // through a poll interval before finding that out.
      checkImmediately: true,
      reconcile: rebuildReconcileContext(db, row.user_id, row.missing_track_id),
      downloadId: row.id,
    });
  }
  logger.info('[Deemix] Resumed in-flight downloads after restart', { count: rows.length });
}

/** The server/token half of a reconcile context isn't stored with the
 * download - it's re-read from the user's current server on resume, which is
 * also what makes a resumed download reconcile against wherever they point
 * now rather than wherever they pointed when it was queued. */
function rebuildReconcileContext(db: DatabaseService, userId: number, missingTrackId?: number | null): DeemixReconcileContext | undefined {
  if (!missingTrackId) return undefined;
  const user = db.getUserById(userId);
  const server = db.getUserServer(userId);
  if (!user || !server) return undefined;
  return {
    db,
    missingTrackId,
    serverUrl: server.server_url,
    plexToken: resolvePlexToken(user, server),
    libraryId: server.library_id,
    serverClientId: server.server_client_id,
  };
}

/**
 * Polls deemix-server's own queue for one download's real progress and
 * mirrors it into the notification the header's live status area reads,
 * until it finishes, fails, or this has been polling too long to be worth
 * continuing (deemix-server unreachable, item stuck, etc). Shared by every
 * caller that queues a deemix download (missing-track "Deemix"/"Deemix All"
 * and the admin "Most Common Missing Tracks" download button) so progress
 * reporting behaves identically everywhere.
 */
async function trackDeemixDownload(options: {
  db: DatabaseService;
  userId: number;
  notificationId: string;
  uuid: string;
  checkImmediately?: boolean;
  /** When provided (missing-track downloads only), a finished download
   * triggers reconcileDownloadedTrack() to auto-match and insert the track
   * once Plex has seen the new file. */
  reconcile?: DeemixReconcileContext;
  /** deemix_downloads row to clear once this reaches a terminal state. */
  downloadId: number;
}): Promise<void> {
  const { db, userId, notificationId, uuid, checkImmediately, reconcile, downloadId } = options;
  const start = Date.now();
  let first = true;
  try {
    while (Date.now() - start < DEEMIX_POLL_MAX_MS) {
      // An already-queued track (see queueDeemixDownload's alreadyQueued case)
      // may well be done already - check right away instead of leaving the
      // notification stuck on "in progress" for a full poll interval first.
      if (!(first && checkImmediately)) {
        await new Promise(resolve => setTimeout(resolve, DEEMIX_POLL_INTERVAL_MS));
      }
      first = false;

      let item;
      try {
        item = await getDeemixQueueItem(uuid);
      } catch (error: any) {
        logger.warn('[Deemix] Failed to poll queue item, retrying', { error: error.message, uuid });
        continue;
      }

      if (!item) {
        // Gone from the queue is not the same as downloaded - deemix also
        // drops an item when its queue is cleared, or when it restarts
        // without restoring it. Plex is the only real proof the file
        // exists, so hand it to reconciliation instead of declaring success
        // here (which is what this used to do, on nothing but the absence).
        logger.warn('[Deemix] Queue item disappeared before finishing', { uuid, userId });
        if (reconcile) await reconcileDownloadedTrack(userId, notificationId, reconcile, 'vanished');
        else updateNotification(userId, notificationId, { status: 'error', detail: 'Disappeared from the deemix queue before finishing' });
        return;
      }
      if (item.status === 'completed') {
        if (reconcile) await reconcileDownloadedTrack(userId, notificationId, reconcile, 'completed');
        else updateNotification(userId, notificationId, { status: 'success', progress: 100 });
        return;
      }
      if (item.status === 'failed' || item.status === 'withErrors') {
        const errorMessage = item.errors?.[0]?.message || 'Download failed';
        logger.error('[Deemix] Download failed', { uuid, userId, status: item.status, error: errorMessage });
        updateNotification(userId, notificationId, { status: 'error', detail: errorMessage });
        return;
      }
      // deemix-server's queue entry for a not-yet-started item ('inQueue', e.g.
      // still waiting behind others when queueConcurrency limits how many run
      // at once - common right after "Deemix All" queues a whole list at once)
      // is built from a slimmer dict that omits progress/downloaded entirely,
      // not zero. Blindly writing that straight into the notification set
      // progress to undefined, which hid the progress bar in the UI outright
      // (it's only rendered when progress is a number) and left just the
      // spinner icon with no indication anything was actually happening.
      const statusWord = item.status === 'inQueue' ? 'Queued' : 'Downloading';
      const progress = typeof item.progress === 'number' ? item.progress : 0;
      // Also surface completed-file count directly for a multi-track album -
      // "3/18 tracks" is a far more legible sign of life than a bare
      // percentage that may sit unchanged for a while between whole-file jumps.
      const detail = item.size > 1 ? `${item.downloaded ?? 0}/${item.size} tracks - ${statusWord.toLowerCase()}` : statusWord;
      updateNotification(userId, notificationId, { progress, detail });
    }
    logger.error('[Deemix] Timed out waiting for download to finish', { uuid, userId, timeoutMs: DEEMIX_POLL_MAX_MS });
    updateNotification(userId, notificationId, { status: 'error', detail: 'Timed out waiting for deemix' });
  } finally {
    // Terminal one way or another - nothing left for a restart to resume.
    db.deleteDeemixDownload(downloadId);
  }
}

const RECONCILE_POLL_INTERVAL_MS = 30 * 1000;
// Each reconcile attempt runs a real Plex search per track. "Deemix All" on
// a large playlist finishes hundreds of downloads within a few minutes of
// each other, so without a cap that becomes hundreds of concurrent Plex
// search loops - the second thing (after the queue-polling storm fixed
// above) that pegged the CPU. Reconciliation is background catch-up work,
// so making it wait its turn costs nothing the user can perceive.
// ponytail: fixed global cap, make it per-server if one user's big batch
// starts starving another's.
const RECONCILE_MAX_CONCURRENT = 3;
let reconcileActive = 0;
const reconcileWaiting: Array<() => void> = [];

async function acquireReconcileSlot(): Promise<void> {
  if (reconcileActive < RECONCILE_MAX_CONCURRENT) {
    reconcileActive++;
    return;
  }
  await new Promise<void>(resolve => reconcileWaiting.push(resolve));
  reconcileActive++;
}

function releaseReconcileSlot(): void {
  reconcileActive--;
  reconcileWaiting.shift()?.();
}
// ponytail: fixed attempt count as the "has Plex scanned the new file yet"
// bound - Plex's own library-scan interval is admin-configured and unknown
// to this server, so this is a heuristic, not a guarantee. If auto-match
// misses turn out to be common, make this configurable instead.
const RECONCILE_MAX_ATTEMPTS = 10;

// Plex only finds a newly-downloaded file once it scans the library, and
// its scan interval is admin-configured (often long, sometimes manual only),
// so waiting for one to happen is a coin flip. Triggering the scan directly
// - the same call the server settings page already exposes - turns that into
// a near-certainty. It is deliberately not done per completed download: mid
// batch, the tracks still downloading aren't on disk yet, so a scan each
// time would just make Plex re-walk the whole library over and over for no
// gain. Only the download that finds deemix's queue drained triggers one,
// and the pollers still looping for the earlier downloads pick their tracks
// up from that same scan.
const SCAN_DEDUPE_MS = 60 * 1000;
const lastScanAt = new Map<string, number>();

async function scanPlexIfQueueDrained(reconcile: DeemixReconcileContext): Promise<void> {
  if (!reconcile.libraryId) return;
  try {
    const queue = await getDeemixQueue();
    const stillWorking = Object.values(queue).some(item => item.status === 'inQueue' || item.status === 'downloading');
    if (stillWorking) return;

    // Several downloads can finish inside one snapshot window and all see a
    // drained queue - one scan covers every one of them.
    const key = `${reconcile.serverUrl}:${reconcile.libraryId}`;
    if (Date.now() - (lastScanAt.get(key) ?? 0) < SCAN_DEDUPE_MS) return;
    lastScanAt.set(key, Date.now());

    await new PlexClient(reconcile.serverUrl, reconcile.plexToken).scanLibrary(reconcile.libraryId);
    logger.info('[Deemix] deemix queue drained - triggered a Plex library scan', { libraryId: reconcile.libraryId });
  } catch (error: any) {
    // Best effort - without it reconciliation just falls back to waiting for
    // Plex's own scheduled scan, which is all it ever did before.
    logger.warn('[Deemix] Could not trigger a Plex library scan', { error: error.message });
  }
}

/**
 * After a deemix download finishes, the file still has to be found by
 * Plex's library scan before it's matchable - so this asks Plex to scan
 * (once the whole batch is done, see scanPlexIfQueueDrained), polls for a
 * match on the originally-missing track a few times, and inserts it into
 * its playlist (via the same helper the manual retry batch uses) as soon as
 * it resolves. If every attempt misses, the missing_tracks row is left
 * alone so the existing manual Retry/Rematch actions still work.
 *
 * @param outcome - 'completed' when deemix reported the download finished,
 *   'vanished' when its queue entry disappeared instead. Both are worth
 *   checking Plex for, but only the first justifies telling the user the
 *   download succeeded when no match turns up.
 */
async function reconcileDownloadedTrack(
  userId: number,
  notificationId: string,
  reconcile: DeemixReconcileContext,
  outcome: 'completed' | 'vanished'
): Promise<void> {
  const label = outcome === 'completed' ? 'Downloaded' : 'Left the deemix queue unfinished';
  updateNotification(userId, notificationId, { progress: 100, detail: `${label} - waiting to check Plex` });
  await scanPlexIfQueueDrained(reconcile);
  await acquireReconcileSlot();
  try {
    await runReconcileAttempts(userId, notificationId, reconcile, label, outcome);
  } finally {
    releaseReconcileSlot();
  }
}

async function runReconcileAttempts(
  userId: number,
  notificationId: string,
  reconcile: DeemixReconcileContext,
  label: string,
  outcome: 'completed' | 'vanished'
): Promise<void> {
  const { db, missingTrackId } = reconcile;

  for (let attempt = 1; attempt <= RECONCILE_MAX_ATTEMPTS; attempt++) {
    const track = db.getUserMissingTracks(userId).find(t => t.id === missingTrackId);
    if (!track) {
      // Resolved another way (manual rematch, etc.) while this was downloading.
      updateNotification(userId, notificationId, { status: 'success', progress: 100, detail: `${label} - already matched` });
      return;
    }

    updateNotification(userId, notificationId, {
      progress: 100,
      detail: `${label} - waiting for Plex to find it (${attempt}/${RECONCILE_MAX_ATTEMPTS})`,
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
          updateNotification(userId, notificationId, { status: 'success', progress: 100, detail: `${label} and matched` });
          return;
        }
      }
    } catch (error: any) {
      logger.warn('[Deemix] Reconciliation attempt failed, retrying', { error: error.message, missingTrackId, attempt });
    }
  }

  updateNotification(userId, notificationId, {
    // A download deemix said it finished is still a success even if Plex
    // hasn't caught up; one that vanished from the queue with nothing to
    // show for it in Plex is not.
    status: outcome === 'completed' ? 'success' : 'error',
    progress: 100,
    detail: `${label} - could not auto-match yet, use Retry`,
  });
}

/** Last result of checkDeemixArl(), so the admin UI can show the ARL's
 * health without forcing a fresh login on every page load. */
let lastArlCheck: { ok: boolean; error?: string; at: number } | null = null;

export function getLastDeemixArlCheck(): { ok: boolean; error?: string; at: number } | null {
  return lastArlCheck;
}

/**
 * Verifies the configured ARL still works. Deezer ARLs expire every few
 * months, and the only thing that used to surface an expired one was a user
 * clicking Deemix and getting an error - by which point downloads had been
 * failing for a while with nobody told.
 */
export async function checkDeemixArl(): Promise<{ ok: boolean; error?: string }> {
  try {
    await login();
    lastArlCheck = { ok: true, at: Date.now() };
  } catch (error: any) {
    lastArlCheck = { ok: false, error: error.message || 'Deemix login failed', at: Date.now() };
  }
  return { ok: lastArlCheck.ok, error: lastArlCheck.error };
}

/** Checks the ARL and, when it fails, tells every admin. The ARL is
 * server-wide configuration rather than any one user's, so notifying
 * whoever happens to be logged in would be the wrong person. */
export async function checkDeemixArlAndNotifyAdmins(db: DatabaseService): Promise<void> {
  const result = await checkDeemixArl();
  if (result.ok) return;

  logger.error('[Deemix] ARL check failed', { error: result.error });
  for (const adminId of db.getAdminUserIds()) {
    addNotification(adminId, {
      type: 'deemix',
      title: 'Deemix login failed',
      detail: `${result.error} - update it in Settings > Admin > Deemix.`,
      status: 'error',
    });
  }
}
