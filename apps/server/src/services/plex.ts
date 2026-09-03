/**
 * Plex API Client
 * 
 * Provides methods for interacting with Plex Media Server API.
 * Handles track searching, library management, playlist operations,
 * and play history retrieval.
 */

import axios, { AxiosInstance } from 'axios';
import https from 'https';
import { logger } from '../utils/logger';
import { plexLimiter } from './task-queues';

/**
 * Thrown when Plex rejects the stored auth token (revoked, expired, or
 * otherwise invalid). Distinguishable from generic errors so route handlers
 * can respond with 401 (prompting re-login) instead of a generic 500.
 */
export class PlexAuthError extends Error {
  code = 'PLEX_AUTH_INVALID';
  // Read by the error-handler middleware, so any route that passes this
  // straight to next() answers 401 (re-login) rather than a generic 500.
  statusCode = 401;

  constructor(message: string = 'Invalid or expired Plex token') {
    super(message);
    this.name = 'PlexAuthError';
    // Maintain proper prototype chain when compiled down (extends Error across targets)
    Object.setPrototypeOf(this, PlexAuthError.prototype);
  }
}

/**
 * Picks the right token to authenticate directly against a user's selected
 * Plex Media Server. For servers a user owns, their plex.tv account token
 * works fine - but for servers merely *shared* with them (a friend's server,
 * or one they access as a Plex Home/managed user), Plex requires the
 * server-specific access token from the /api/resources listing instead; the
 * account token gets a 401 from that PMS even though it's perfectly valid.
 * Using the wrong one here is what used to send shared-server users into an
 * unbreakable re-login loop, since re-authenticating with Plex only ever
 * refreshes the account token, never the server-specific one.
 */
export function resolvePlexToken(
  user: { plex_token: string },
  userServer: { access_token?: string | null } | null | undefined
): string {
  return userServer?.access_token || user.plex_token;
}

/**
 * Plex library section
 */
export interface PlexLibrary {
  key: string;
  title: string;
  type: string;
  agent: string;
  scanner: string;
  language: string;
  uuid: string;
  updatedAt: number;
  createdAt: number;
  scannedAt: number;
  content: boolean;
  directory: boolean;
  contentChangedAt: number;
  hidden: number;
}

/**
 * Plex track metadata
 */
export interface PlexTrack {
  ratingKey: string;
  key: string;
  parentRatingKey: string;
  grandparentRatingKey: string;
  guid: string;
  parentGuid: string;
  grandparentGuid: string;
  type: string;
  title: string;
  originalTitle?: string; // Track-level artist (used in compilations/Various Artists albums)
  grandparentKey: string;
  parentKey: string;
  grandparentTitle: string;
  parentTitle: string;
  summary: string;
  index: number;
  parentIndex: number;
  ratingCount: number;
  thumb: string;
  art: string;
  parentThumb: string;
  grandparentThumb: string;
  grandparentArt: string;
  duration: number;
  addedAt: number;
  updatedAt: number;
  year?: number;
  parentYear?: number; // Album release year
  viewCount?: number;
  lastViewedAt?: number;
  userRating?: number;
  Genre?: Array<{ tag: string }>;
  Mood?: Array<{ tag: string }>;
  Style?: Array<{ tag: string }>;
  Collection?: Array<{ tag: string }>;
  Label?: Array<{ tag: string }>;
  playlistItemID?: number; // Present when track is part of a playlist
  librarySectionID?: number;
  librarySectionTitle?: string;
  librarySectionKey?: string;
  Media?: Array<{
    id: number;
    duration: number;
    bitrate: number;
    audioChannels: number;
    audioCodec: string;
    audioSampleRate?: number;
    container: string;
    Part: Array<{
      id: number;
      key: string;
      duration: number;
      file: string;
      size: number;
      container: string;
    }>;
  }>;
}

// Plex track objects carry a lot of fields matching.ts never reads (thumb/art
// URLs, summary text, view/rating history, Mood/Style/Collection/Label tag
// lists). Artist catalog fetches below can return thousands of these at
// once, so stripping the unused weight before anything gets held in memory
// (cached or just passed around a batch) is the cheapest way to cut that
// footprint - ratingKey/title/originalTitle/grandparentTitle/parentTitle are
// what matching.ts actually scores against, Media is kept because the
// winning match's codec/bitrate is read from it.
function slimTrack(t: PlexTrack): PlexTrack {
  return {
    ratingKey: t.ratingKey,
    key: t.key,
    parentRatingKey: t.parentRatingKey,
    grandparentRatingKey: t.grandparentRatingKey,
    guid: t.guid,
    parentGuid: '',
    grandparentGuid: '',
    type: t.type,
    title: t.title,
    originalTitle: t.originalTitle,
    grandparentKey: t.grandparentKey,
    parentKey: t.parentKey,
    grandparentTitle: t.grandparentTitle,
    parentTitle: t.parentTitle,
    summary: '',
    index: t.index,
    parentIndex: t.parentIndex,
    ratingCount: 0,
    thumb: '',
    art: '',
    parentThumb: '',
    grandparentThumb: '',
    grandparentArt: '',
    duration: t.duration,
    addedAt: t.addedAt,
    updatedAt: t.updatedAt,
    year: t.year,
    parentYear: t.parentYear,
    Genre: t.Genre,
    librarySectionID: t.librarySectionID,
    Media: t.Media,
  };
}

/**
 * Plex playlist
 */
export interface PlexPlaylist {
  ratingKey: string;
  key: string;
  guid: string;
  type: string;
  title: string;
  summary: string;
  smart: boolean;
  playlistType: string;
  composite: string;
  duration: number;
  leafCount: number;
  addedAt: number;
  updatedAt: number;
}

/**
 * Plex play history item
 */
export interface PlexHistoryItem {
  historyKey: string;
  key: string;
  ratingKey: string;
  title: string;
  type: string;
  thumb: string;
  parentThumb: string;
  grandparentThumb: string;
  grandparentTitle: string;
  parentTitle: string;
  index: number;
  parentIndex: number;
  viewedAt: number;
  accountID: number;
  deviceID: number;
}

/**
 * Plex API response container
 */
interface PlexMediaContainer<T = any> {
  MediaContainer: {
    size: number;
    totalSize?: number;
    offset?: number;
    allowSync?: boolean;
    identifier?: string;
    mediaTagPrefix?: string;
    [key: string]: any;
  } & T;
}

/**
 * Extract a direct IP URL from a .plex.direct URL.
 * e.g., "https://192-168-0-3.abc123.plex.direct:32400" -> "https://192.168.0.3:32400"
 * Returns null if not a .plex.direct URL.
 */
function extractDirectIpUrl(plexDirectUrl: string): string | null {
  try {
    const url = new URL(plexDirectUrl);
    const hostname = url.hostname;
    
    // Check if this is a .plex.direct URL
    if (!hostname.endsWith('.plex.direct')) {
      return null;
    }
    
    // The IP is encoded in the first subdomain part with dashes instead of dots
    // e.g., "192-168-0-3.abc123.plex.direct" -> IP is 192.168.0.3
    const firstPart = hostname.split('.')[0];
    const ipParts = firstPart.split('-');
    
    // Validate: should be exactly 4 parts, each a number 0-255
    if (ipParts.length !== 4 || !ipParts.every(p => /^\d{1,3}$/.test(p) && parseInt(p) <= 255)) {
      return null;
    }
    
    const ip = ipParts.join('.');
    const port = url.port || '443';
    const protocol = url.protocol; // https:
    
    return `${protocol}//${ip}:${port}`;
  } catch {
    return null;
  }
}

export class PlexClient {
  private serverUrl: string;
  private token: string;
  public client: AxiosInstance;
  private clientId: string;
  private productName: string;
  private searchCache: Map<string, { results: PlexTrack[]; timestamp: number }> = new Map();
  // Keyed by artistKey alone (not by track title like searchCache), so a
  // missing-tracks retry with many tracks from the same artist fetches that
  // artist's full catalog once instead of once per track - this was the
  // actual driver behind multi-GB memory spikes on large retry batches.
  private artistTracksCache: Map<string, { tracks: PlexTrack[]; timestamp: number }> = new Map();
  // Resolving an artist name to its library entity is the first step of every
  // artist-first search, and findPlexCandidates() runs several search tiers
  // per track that each differ only in punctuation - so the same lookup gets
  // repeated a handful of times per track. Caching it (negative results
  // included, which is the expensive case: an artist the library doesn't have
  // makes every tier pay for the same empty answer) removes those round
  // trips. Keyed by the searched name rather than the resolved entity,
  // because a miss has no entity to key on.
  private artistLookupCache: Map<string, { artists: any[]; timestamp: number }> = new Map();
  // When a Plex server stops answering, every request to it costs the full
  // 60s timeout - and the direct-IP retry below doubles that to 120s. A page
  // that makes a handful of Plex calls then hangs for minutes instead of
  // failing, which is what made the whole UI feel dead whenever Plex was
  // busy. After a connection-level failure the server is treated as down for
  // a short cooldown and further calls fail immediately, so one request pays
  // the timeout and the rest return at once with something the UI can show.
  // Static because a PlexClient is constructed per request in several routes,
  // so per-instance state would never survive long enough to help.
  // ponytail: a plain cooldown, not a half-open probe - the first call after
  // it expires is the probe. Add proper half-open state if a busy server ends
  // up flapping in and out of the cooldown.
  private static unreachableUntil: Map<string, number> = new Map();
  private static readonly UNREACHABLE_COOLDOWN_MS = 30000;
  // Covers any real artist comfortably (a prolific one runs to a few
  // thousand tracks) while keeping a pathological compilation artist from
  // being pulled in full on every search.
  private static readonly MAX_ARTIST_CATALOG = 3000;

  /** Thrown instead of waiting on a server known to be down. Carries a
   * distinct name so callers/routes can tell it from a genuine Plex error. */
  static isUnreachableError(error: any): boolean {
    return error?.code === 'PLEX_UNREACHABLE';
  }
  private readonly CACHE_TTL = 300000; // 5 minutes
  // Hard cap on cache entries so memory can't grow unbounded within one TTL
  // window even under heavy single-pass churn (the old code only swept
  // TTL-expired entries once size passed 1000, which never fires if every
  // entry is still fresh). FIFO eviction (oldest inserted, not oldest used) -
  // good enough here since entries are cheap to refetch; upgrade to real LRU
  // if hit rate ever matters more than this.
  private static readonly MAX_CACHE_ENTRIES = 500;

  private static capCache<V>(cache: Map<string, V>, max: number): void {
    while (cache.size > max) {
      const oldestKey = cache.keys().next().value;
      if (oldestKey === undefined) break;
      cache.delete(oldestKey);
    }
  }

  constructor(
    serverUrl: string,
    token: string,
    clientId: string = 'playlist-lab-server',
    productName: string = 'Playlist Lab'
  ) {
    this.serverUrl = serverUrl.replace(/\/$/, ''); // Remove trailing slash
    this.token = token;
    this.clientId = clientId;
    this.productName = productName;

    // If using a .plex.direct URL, also prepare a direct IP fallback
    const directIpUrl = extractDirectIpUrl(this.serverUrl);
    
    // Check if URL is a direct IP connection (cert won't match IP, need relaxed TLS)
    const isDirectIp = /^https:\/\/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/.test(this.serverUrl);
    
    // Use relaxed TLS for direct IP connections (cert won't match IP)
    const httpsAgent = (directIpUrl || isDirectIp) ? new https.Agent({ rejectUnauthorized: false }) : undefined;

    this.client = axios.create({
      baseURL: this.serverUrl,
      headers: {
        'Accept': 'application/json',
        'X-Plex-Token': this.token,
        'X-Plex-Product': this.productName,
        'X-Plex-Client-Identifier': this.clientId,
        'X-Plex-Platform': 'Node.js',
        'X-Plex-Container-Size': '50' // Default page size, prevents future 400 errors
      },
      timeout: 60000, // 60 second timeout for large libraries
      ...(httpsAgent ? { httpsAgent } : {})
    });

    // Registered first so it wraps outermost: axios runs request interceptors
    // in registration order and response interceptors in reverse, so this
    // acquires a global Plex-call slot before anything else touches the
    // request and releases it last, after every other interceptor below
    // (including the direct-IP retry, which issues its own fresh request and
    // so acquires/releases its own slot in turn). Bounds total concurrent
    // Plex HTTP calls app-wide, no matter which feature made them - see
    // task-queues.ts.
    this.client.interceptors.request.use(async (config) => {
      await plexLimiter.acquire();
      return config;
    });
    this.client.interceptors.response.use(
      (response) => {
        plexLimiter.release();
        return response;
      },
      (error) => {
        plexLimiter.release();
        return Promise.reject(error);
      }
    );

    // Interceptor: log outgoing URL and prevent axios from re-encoding server:// URIs
    this.client.interceptors.request.use((config) => {
      const downUntil = PlexClient.unreachableUntil.get(this.serverUrl) ?? 0;
      if (Date.now() < downUntil) {
        const error: any = new Error('Plex server is not responding');
        error.code = 'PLEX_UNREACHABLE';
        throw error;
      }
      // Build the full URL for logging
      const fullUrl = `${config.baseURL || ''}${config.url || ''}`;
      if (fullUrl.includes('playlist')) {
        logger.debug('Plex outgoing request', { method: config.method, url: fullUrl });
      }
      return config;
    });

    // Connection-level failures open the cooldown; any success closes it.
    this.client.interceptors.response.use(
      (response) => {
        if (PlexClient.unreachableUntil.delete(this.serverUrl)) {
          logger.info('[Plex] Server is responding again', { serverUrl: this.serverUrl });
        }
        return response;
      },
      (error) => {
        // Every Plex request routes through here, so an expired/invalid token
        // is translated once instead of in each of the ~15 methods that catch
        // it individually. getTracksWithAdvancedFilters was one that didn't,
        // and its raw axios error reached the user as a 500 reading "Request
        // failed with status code 401" - useless for the one action that
        // actually fixes it, reconnecting the Plex account.
        if (error.response?.status === 401) {
          return Promise.reject(new PlexAuthError('Invalid Plex token'));
        }

        const isConnectionFailure = !error.response && error.code !== 'PLEX_UNREACHABLE';
        if (isConnectionFailure && !PlexClient.unreachableUntil.has(this.serverUrl)) {
          logger.warn('[Plex] Server unreachable, failing fast for a cooldown instead of waiting on every call', {
            serverUrl: this.serverUrl,
            error: error.message,
            cooldownMs: PlexClient.UNREACHABLE_COOLDOWN_MS,
          });
        }
        if (isConnectionFailure) {
          PlexClient.unreachableUntil.set(this.serverUrl, Date.now() + PlexClient.UNREACHABLE_COOLDOWN_MS);
        }
        return Promise.reject(error);
      }
    );

    // Response interceptor: retry with direct IP if .plex.direct DNS fails or times out
    if (directIpUrl && httpsAgent) {
      this.client.interceptors.response.use(undefined, async (error) => {
        const isDnsError = error.code === 'ENOTFOUND' || 
                           error.message?.includes('ENOTFOUND') ||
                           error.message?.includes('getaddrinfo');
        const isTimeoutError = error.code === 'ETIMEDOUT' || 
                               error.code === 'ECONNREFUSED' ||
                               error.message?.includes('ETIMEDOUT') ||
                               error.message?.includes('ECONNREFUSED') ||
                               error.message?.includes('timeout');
        
        if ((isDnsError || isTimeoutError) && error.config && !error.config._retriedWithDirectIp) {
          logger.warn('[PlexService] Connection failed, retrying with direct IP', {
            reason: isDnsError ? 'DNS resolution failed' : 'Connection timeout/refused',
            originalUrl: error.config.baseURL,
            directIpUrl
          });
          
          error.config._retriedWithDirectIp = true;
          error.config.baseURL = directIpUrl;
          error.config.httpsAgent = httpsAgent;
          // This retry goes through bare axios, so it bypasses the
          // interceptors above - including the one that clears the
          // unreachable cooldown on success. Clear it here too, or a server
          // that is only ever reachable on its direct IP would be re-marked
          // as down by every first attempt and never recover.
          const retried = await axios(error.config);
          PlexClient.unreachableUntil.delete(this.serverUrl);
          return retried;
        }
        
        throw error;
      });
    }
  }

  /**
   * Search for a track in the Plex library
   * Returns matching tracks sorted by relevance
   */
  async searchTrack(query: string, libraryId?: string, artist?: string, title?: string): Promise<PlexTrack[]> {
      try {
        const cacheKey = `${query}|${libraryId || 'all'}|${artist || ''}|${title || ''}`;

        // Check cache first
        const cached = this.searchCache.get(cacheKey);
        if (cached && Date.now() - cached.timestamp < this.CACHE_TTL) {
          logger.debug(`[Plex] Cache hit for: ${cacheKey}`);
          return cached.results;
        }

        let tracks: PlexTrack[] = [];

        if (libraryId && artist && title) {
          // OPTIMIZED STRATEGY: Search by artist first, then filter tracks
          // This is much more efficient than searching for short track names
          logger.debug(`[Plex] Using artist-first search: library=${libraryId}, artist="${artist}", title="${title}"`);
          
          try {
            // Step 1: Find the artist
            const artistLookupKey = `${libraryId}|${artist.toLowerCase()}`;
            const cachedLookup = this.artistLookupCache.get(artistLookupKey);
            let artists: any[];
            if (cachedLookup && Date.now() - cachedLookup.timestamp < this.CACHE_TTL) {
              artists = cachedLookup.artists;
              logger.debug(`[Plex] Artist lookup cache hit for "${artist}": ${artists.length} matches`);
            } else {
              const artistResponse = await this.client.get<PlexMediaContainer>(
                `/library/sections/${libraryId}/all`,
                {
                  params: {
                    type: 8, // Artist type
                    'artist.title': artist
                  }
                }
              );
              artists = artistResponse.data.MediaContainer.Metadata || [];
              this.artistLookupCache.set(artistLookupKey, { artists, timestamp: Date.now() });
              PlexClient.capCache(this.artistLookupCache, PlexClient.MAX_CACHE_ENTRIES);
              logger.debug(`[Plex] Found ${artists.length} matching artists for "${artist}"`);
            }
            
            if (artists.length > 0) {
              // Step 2: Get tracks from the first matching artist
              const artistKey = artists[0].ratingKey;
              const artistName = artists[0].title;
              const artistCacheKey = `${libraryId}|${artistKey}`;

              // A retry batch commonly has several tracks from the same
              // artist - reuse that artist's full catalog instead of
              // refetching it (and holding a duplicate copy) per track.
              const cachedArtist = this.artistTracksCache.get(artistCacheKey);
              let artistTracks: PlexTrack[];
              if (cachedArtist && Date.now() - cachedArtist.timestamp < this.CACHE_TTL) {
                logger.debug(`[Plex] Artist catalog cache hit: ${artistName} (${artistKey})`);
                artistTracks = cachedArtist.tracks;
              } else {
                logger.debug(`[Plex] Fetching tracks from artist: ${artistName} (${artistKey})`);

                // Every track by this artist (grandchildren endpoint), because
                // the title filter below is done here rather than by Plex -
                // that's what lets it tolerate punctuation and spelling
                // differences a server-side substring filter would miss.
                // Bounded because the cost of this call scales with the
                // artist, not the query: an ordinary artist is a few hundred
                // tracks, but the "Various Artists" entity a big compilation
                // library accumulates can be tens of thousands, and pulling
                // that per search is what makes a huge library crawl.
                const tracksResponse = await this.client.get<PlexMediaContainer>(
                  `/library/metadata/${artistKey}/allLeaves`,
                  {
                    params: { type: 10 }, // Type 10 = tracks
                    // As a header, not a query param: the client sets a
                    // default X-Plex-Container-Size of 50 for every request,
                    // and that header wins over a param of the same name -
                    // passing it as a param silently capped this at 50 and
                    // cut most of an artist's catalog out of matching.
                    headers: {
                      'X-Plex-Container-Start': '0',
                      'X-Plex-Container-Size': String(PlexClient.MAX_ARTIST_CATALOG),
                    },
                  }
                );

                const container = tracksResponse.data.MediaContainer;
                artistTracks = (container.Metadata || []).map(slimTrack);
                const totalForArtist = (container as any).totalSize ?? artistTracks.length;
                if (totalForArtist > artistTracks.length) {
                  // Logged at warn because it silently caps what can be
                  // matched: a track past the cut-off looks identical to a
                  // track the library doesn't have, and this is the only
                  // place that difference is visible.
                  logger.warn('[Plex] Artist catalog truncated, later tracks are not searchable by this route', {
                    artist: artistName,
                    fetched: artistTracks.length,
                    total: totalForArtist,
                  });
                }
                logger.debug(`[Plex] Artist has ${artistTracks.length} tracks (of ${totalForArtist})`);
                this.artistTracksCache.set(artistCacheKey, { tracks: artistTracks, timestamp: Date.now() });
                PlexClient.capCache(this.artistTracksCache, PlexClient.MAX_CACHE_ENTRIES);
              }
              
              // Step 3: Filter tracks by title
              const normalizeTitle = (t: string) => t.toLowerCase().replace(/[^a-z0-9]/g, '');
              const normalizedSearchTitle = normalizeTitle(title);
              
              tracks = artistTracks.filter((track: PlexTrack) => {
                const trackTitle = track.title || '';
                const normalizedTrackTitle = normalizeTitle(trackTitle);
                // Match if title contains search term or search term contains title
                return normalizedTrackTitle.includes(normalizedSearchTitle) || 
                       normalizedSearchTitle.includes(normalizedTrackTitle);
              });
              
              logger.debug(`[Plex] Filtered to ${tracks.length} tracks matching title "${title}"`);
            }
            
            // If no results from artist-first search, fall back to direct filter
            if (tracks.length === 0) {
              logger.debug(`[Plex] Artist-first search found nothing, trying direct filter`);
              const response = await this.client.get<PlexMediaContainer>(
                `/library/sections/${libraryId}/all`,
                { 
                  params: { 
                    type: 10,
                    'artist.title': artist,
                    'track.title': title
                  } 
                }
              );
              tracks = response.data.MediaContainer.Metadata || [];
              logger.debug(`[Plex] Direct filter returned ${tracks.length} tracks`);
            }
            
            // If still no results, try searching by track title + track-level artist (originalTitle).
            // This handles cases where the source artist is the track artist, not the album artist
            // (e.g. soundtracks, compilations, singles where track artist ≠ album artist).
            if (tracks.length === 0) {
              logger.debug(`[Plex] Direct filter found nothing, trying track-level artist search`);
              try {
                const trackArtistResponse = await this.client.get<PlexMediaContainer>(
                  `/library/sections/${libraryId}/all`,
                  { 
                    params: { 
                      type: 10,
                      'track.title': title,
                      'track.originalTitle': artist
                    } 
                  }
                );
                tracks = trackArtistResponse.data.MediaContainer.Metadata || [];
                logger.debug(`[Plex] Track-level artist filter returned ${tracks.length} tracks`);
              } catch (err: any) {
                // track.originalTitle filter may not be supported in all Plex versions
                logger.debug(`[Plex] Track-level artist filter not supported: ${err.message}, falling back to title-only search`);
              }
            }
            
            // If still no results, fall back to title-only search and filter by any artist field
            if (tracks.length === 0) {
              logger.debug(`[Plex] Trying title-only search, filtering by track artist (originalTitle) and album artist (grandparentTitle)`);
              const trackSearchResponse = await this.client.get<PlexMediaContainer>(
                `/library/sections/${libraryId}/all`,
                { 
                  params: { 
                    type: 10,
                    'track.title': title
                  } 
                }
              );
              
              const allTracks = trackSearchResponse.data.MediaContainer.Metadata || [];
              logger.debug(`[Plex] Found ${allTracks.length} tracks with title "${title}"`);

              // Don't hard-filter these by artist. Compilation/soundtrack album
              // tagging is inconsistent in practice - e.g. album artist set to
              // the composer ("Lin-Manuel Miranda") with the true per-track
              // performer ("Stephanie Beatriz") missing or mistagged in
              // originalTitle. A strict artist filter here would silently
              // discard the correct track before matching.ts's compilation-aware
              // scoring (which allows an exact title match on a soundtrack/
              // compilation album even without an artist match) ever sees it.
              // Rank artist-matching candidates first, but keep the rest so
              // matching.ts can still consider them via title-only matching.
              // Cap defensively in case a very generic title returns a huge
              // result set.
              // ponytail: flat cap + naive rank, revisit with real pagination if a
              // title regularly returns more than this many same-named tracks.
              const normalizeArtist = (a: string) => a.toLowerCase().replace(/[^a-z0-9]/g, '');
              const normalizedSearchArtist = normalizeArtist(artist);
              const artistMatches = (track: PlexTrack) => {
                const trackArtist = normalizeArtist(track.originalTitle || '');
                const albumArtist = normalizeArtist(track.grandparentTitle || '');
                // Guard each side non-empty: "".includes('') / anything.includes('') is
                // always true, which would let tracks with no originalTitle/grandparentTitle
                // match every search artist.
                return (!!trackArtist && (trackArtist.includes(normalizedSearchArtist) || normalizedSearchArtist.includes(trackArtist))) ||
                       (!!albumArtist && (albumArtist.includes(normalizedSearchArtist) || normalizedSearchArtist.includes(albumArtist)));
              };

              const ranked = [...allTracks].sort((a: PlexTrack, b: PlexTrack) =>
                Number(artistMatches(b)) - Number(artistMatches(a))
              );
              tracks = ranked.slice(0, 50);

              logger.debug(`[Plex] Title-only search returning ${tracks.length} candidates (${allTracks.filter(artistMatches).length} artist-matching) for scoring`);
            }
          } catch (err: any) {
            // A 401 is the token, not this search strategy - the fallback
            // below uses the same client and would just 401 again.
            if (err.response?.status === 401) {
              throw new PlexAuthError('Invalid Plex token');
            }
            logger.warn(`[Plex] Artist-first search failed: ${err.message}, falling back to direct filter`);
            // Fallback to original filtered search
            const response = await this.client.get<PlexMediaContainer>(
              `/library/sections/${libraryId}/all`,
              { 
                params: { 
                  type: 10,
                  'artist.title': artist,
                  'track.title': title
                } 
              }
            );
            tracks = response.data.MediaContainer.Metadata || [];
            logger.debug(`[Plex] Fallback filtered search returned ${tracks.length} tracks`);
          }
        } else if (libraryId && title) {
          // Search by title only
          logger.debug(`[Plex] Using title-only search: library=${libraryId}, title="${title}"`);
          const response = await this.client.get<PlexMediaContainer>(
            `/library/sections/${libraryId}/all`,
            { params: { type: 10, 'track.title': title } }
          );
          tracks = response.data.MediaContainer.Metadata || [];
          logger.debug(`[Plex] Title-only search returned ${tracks.length} tracks`);
        } else {
          // Fall back to hub search for combined query
          logger.debug(`[Plex] Using hub search: query="${query}", library=${libraryId || 'all'}`);
          const response = await this.client.get<PlexMediaContainer>('/hubs/search', {
            params: { query: query, limit: 100 }
          });
          const hubs = response.data.MediaContainer.Hub || [];
          const trackHub = hubs.find((hub: any) => hub.type === 'track');
          const albumHub = hubs.find((hub: any) => hub.type === 'album');
          let allTracks = trackHub?.Metadata || [];
          logger.debug(`[Plex] Hub search returned ${allTracks.length} tracks before filtering`);
          
          // Also get tracks from matching albums
          if (albumHub?.Metadata && albumHub.Metadata.length > 0) {
            logger.debug(`[Plex] Found ${albumHub.Metadata.length} matching albums, fetching their tracks`);
            for (const album of albumHub.Metadata.slice(0, 5)) { // Limit to first 5 albums
              try {
                const albumTracksResponse = await this.client.get<PlexMediaContainer>(album.key);
                const albumTracks = albumTracksResponse.data.MediaContainer.Metadata || [];
                logger.debug(`[Plex] Album "${album.title}" has ${albumTracks.length} tracks`);
                allTracks = allTracks.concat(albumTracks);
              } catch (err) {
                logger.warn(`[Plex] Failed to fetch tracks for album ${album.title}`);
              }
            }
            logger.debug(`[Plex] After adding album tracks: ${allTracks.length} total tracks`);
          }
          
          // Filter by library if specified
          if (libraryId && allTracks.length > 0) {
            // Convert libraryId to number for comparison (Plex returns numbers, we receive strings)
            const libraryIdNum = parseInt(libraryId, 10);
            tracks = allTracks.filter((track: PlexTrack) => track.librarySectionID === libraryIdNum);
            logger.debug(`[Plex] After library filter (${libraryId}): ${tracks.length} tracks`);
          } else {
            tracks = allTracks;
          }
        }

        tracks = tracks.map(slimTrack);

        // Cache the results, but don't cache empty results - the track might be added to the library later
        if (tracks.length > 0) {
          this.searchCache.set(cacheKey, { results: tracks, timestamp: Date.now() });
          PlexClient.capCache(this.searchCache, PlexClient.MAX_CACHE_ENTRIES);
        }

        return tracks;
      } catch (error: any) {
        if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
          throw new Error('Plex server is unreachable');
        }
        if (error.response?.status === 401) {
          throw new PlexAuthError('Invalid Plex token');
        }
        if (error.isAxiosError) {
          throw new Error(`Failed to search tracks: ${error.message}`);
        }
        throw error;
      }
    }




  /**
   * Clear the search cache
   * Useful when library content has been updated
   */
  clearSearchCache(): void {
    this.searchCache.clear();
    this.artistTracksCache.clear();
    // Also cleared here, or a newly-added artist would keep resolving to the
    // cached "not in this library" answer for the rest of the TTL.
    this.artistLookupCache.clear();
  }

  /**
   * Search for albums in the Plex library
   */
  async searchAlbums(query: string, libraryId?: string): Promise<any[]> {
    try {
      const params: any = { query };
      const response = await this.client.get<PlexMediaContainer>('/hubs/search', { params });
      
      const hubs = response.data.MediaContainer.Hub || [];
      const albumHub = hubs.find((hub: any) => hub.type === 'album');
      let albums = albumHub?.Metadata || [];
      
      if (libraryId && albums.length > 0) {
        albums = albums.filter((album: any) => album.librarySectionID === libraryId);
      }
      
      return albums;
    } catch (error: any) {
      logger.error('Failed to search albums', { error });
      return [];
    }
  }

  /**
   * Get full metadata for a single track
   */
  async getTrackDetails(ratingKey: string): Promise<PlexTrack | null> {
    try {
      const response = await this.client.get<PlexMediaContainer>(`/library/metadata/${ratingKey}`);
      const metadata = response.data.MediaContainer.Metadata || [];
      return metadata[0] || null;
    } catch (error: any) {
      logger.error('Failed to get track details', { ratingKey, error: error.message });
      return null;
    }
  }

  /**
   * Get all tracks from an album
   */
  async getAlbumTracks(albumRatingKey: string): Promise<PlexTrack[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(`/library/metadata/${albumRatingKey}/children`);
      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      logger.error('Failed to get album tracks', { albumRatingKey, error });
      return [];
    }
  }

  /**
   * Get all music libraries from the server
   */
  async getLibraries(): Promise<Array<{ id: string; name: string; type: string }>> {
    try {
      logger.info('Fetching libraries from Plex', { serverUrl: this.serverUrl });
      
      const response = await this.client.get<PlexMediaContainer>('/library/sections');
      
      logger.info('Plex libraries response', { 
        hasDirectory: !!response.data.MediaContainer.Directory,
        directoryCount: response.data.MediaContainer.Directory?.length || 0
      });
      
      const sections = response.data.MediaContainer.Directory || [];
      
      // Map to simplified format with id, name, type
      const libraries = sections.map((section: any) => ({
        id: section.key,
        name: section.title,
        type: section.type
      }));
      
      logger.info('Mapped libraries', { 
        totalCount: libraries.length,
        libraries: libraries.map((l: any) => ({ id: l.id, name: l.name, type: l.type }))
      });
      
      return libraries;
    } catch (error: any) {
      logger.error('Failed to get libraries', { 
        error: error.message,
        code: error.code,
        status: error.response?.status
      });
      
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to get libraries: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Scan/refresh a library section
   * Triggers Plex to scan for new or changed files in the library
   */
  /**
     * Scan/refresh a library section
     * Triggers Plex to scan for new or changed files in the library
     * @param libraryId - The library section ID
     * @param path - Optional specific folder path to scan
     */
    async scanLibrary(libraryId: string, path?: string): Promise<void> {
      try {
        const url = path 
          ? `/library/sections/${libraryId}/refresh?path=${encodeURIComponent(path)}`
          : `/library/sections/${libraryId}/refresh`;

        logger.info('Triggering library scan', { 
          serverUrl: this.serverUrl, 
          libraryId,
          path: path || 'full library'
        });

        await this.client.get(url);

        logger.info('Library scan triggered successfully', { libraryId, path });
      } catch (error: any) {
        logger.error('Failed to scan library', { 
          error: error.message,
          code: error.code,
          status: error.response?.status,
          libraryId,
          path
        });

        if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
          throw new Error('Plex server is unreachable');
        }
        if (error.response?.status === 401) {
          throw new PlexAuthError('Invalid Plex token');
        }
        if (error.response?.status === 404) {
          throw new Error('Library not found');
        }
        if (error.isAxiosError) {
          throw new Error(`Failed to scan library: ${error.message}`);
        }
        throw error;
      }
    }

    /**
     * Get library folder structure
     * Returns the folder paths configured for a library section
     */
    /**
       * Get library folder structure
       * Returns the folder paths configured for a library section
       */
      /**
         * Get library folder structure
         * Returns the root folder paths for a library section
         */
        async getLibraryFolders(libraryId: string): Promise<Array<{ path: string; accessible: boolean }>> {
            try {
              logger.info('Fetching library folders', { serverUrl: this.serverUrl, libraryId });

              // Get library section details which includes Location array
              const response = await this.client.get<PlexMediaContainer>(
                `/library/sections/${libraryId}`
              );

              logger.info('Library section response', { 
                hasMediaContainer: !!response.data.MediaContainer,
                hasDirectory: !!response.data.MediaContainer.Directory,
                directoryLength: response.data.MediaContainer.Directory?.length
              });

              const section = response.data.MediaContainer.Directory?.[0];
              if (!section) {
                logger.warn('No section found in library response', { libraryId });
                return [];
              }

              // Extract Location array which contains the configured folder paths
              const locations = section.Location || [];
              if (locations.length === 0) {
                logger.warn('No locations found in library section', { libraryId });
                return [];
              }

              // Map locations to folder objects
              const folders = locations.map((loc: any) => ({
                path: loc.path,
                accessible: true // Plex only returns accessible folders
              }));

              logger.info('Library folders retrieved', { libraryId, folderCount: folders.length, folders });

              return folders;
            } catch (error: any) {
              logger.error('Failed to get library folders', { 
                error: error.message,
                code: error.code,
                status: error.response?.status,
                libraryId,
                responseData: error.response?.data
              });

              if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
                throw new Error('Plex server is unreachable');
              }
              if (error.response?.status === 401) {
                throw new PlexAuthError('Invalid Plex token');
              }
              if (error.response?.status === 404) {
                throw new Error('Library not found');
              }
              if (error.isAxiosError) {
                throw new Error(`Failed to get library folders: ${error.message}`);
              }
              throw error;
            }
          }


  /**
   * Get play history for a library section
   * Returns tracks sorted by most recently played
   */
  async getPlayHistory(libraryId: string, limit: number = 100): Promise<PlexHistoryItem[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        '/status/sessions/history/all',
        {
          params: {
            librarySectionID: libraryId,
            'X-Plex-Container-Size': limit
          }
        }
      );

      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to get play history: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Get all playlists from Plex
   * Returns all playlists (audio, video, photo)
   */
  async getPlaylists(): Promise<PlexPlaylist[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        '/playlists'
      );

      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to get playlists: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Create a new playlist
   * Returns the created playlist
   */
  async createPlaylist(
        name: string,
        libraryUri: string,
        trackUris: string[]
      ): Promise<PlexPlaylist> {
        try {
          logger.info('Creating playlist', { name, libraryUri, trackCount: trackUris.length });

          // URL-encode the URI value (matching python-plexapi's joinArgs behavior)
          const url = `/playlists?type=audio&title=${encodeURIComponent(name)}&smart=0&uri=${encodeURIComponent(libraryUri)}`;
          logger.info('createPlaylist URL', { url: url.substring(0, 300) });

          const createResponse = await this.client.post<PlexMediaContainer>(url, null);

          const playlist = createResponse.data.MediaContainer.Metadata?.[0];
          if (!playlist) {
            throw new Error('Failed to create playlist - no playlist returned');
          }

          logger.info('Playlist created in Plex', { ratingKey: playlist.ratingKey, title: playlist.title });

          // Add tracks to playlist if provided
          if (trackUris.length > 0) {
            await this.addToPlaylist(playlist.ratingKey, trackUris);
          }

          return playlist;
        } catch (error: any) {
          logger.error('Failed to create playlist', { name, error: error.message });
          if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
            throw new Error('Plex server is unreachable');
          }
          if (error.response?.status === 401) {
            throw new PlexAuthError('Invalid Plex token');
          }
          if (error.isAxiosError) {
            throw new Error(`Failed to create playlist: ${error.message}`);
          }
          throw error;
        }
      }

  /**
   * Get tracks from a playlist
   */
  async getPlaylistTracks(playlistId: string): Promise<PlexTrack[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/playlists/${playlistId}/items`
      );

      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.response?.status === 404) {
        throw new Error('Playlist not found');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to get playlist tracks: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Get playlist details (metadata)
   */
  async getPlaylistDetails(playlistId: string): Promise<any> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/playlists/${playlistId}`
      );

      return response.data.MediaContainer?.Metadata?.[0] || null;
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.response?.status === 404) {
        throw new Error('Playlist not found');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to get playlist details: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Rename a playlist in Plex itself, not just our own DB record of it -
   * matches Plex's standard REST pattern for updating a resource's fields
   * via query params on its own endpoint (the same shape createPlaylist()
   * uses to set the initial title).
   */
  async renamePlaylist(playlistId: string, title: string): Promise<void> {
    try {
      await this.client.put(`/playlists/${playlistId}?title=${encodeURIComponent(title)}`, null);
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.response?.status === 404) {
        throw new Error('Playlist not found');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to rename playlist: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Add tracks to a playlist
   * trackUris should be in format: server://libraryId/item/ratingKey
   */
  async addToPlaylist(playlistId: string, trackUris: string[]): Promise<void> {
        try {
          logger.info('Adding tracks to playlist', { playlistId, trackCount: trackUris.length, sampleUri: trackUris[0] });

          // Match python-plexapi approach: batch ratingKeys into a single URI, URL-encode the value
          // python-plexapi uses: uri = f'{server._uriRoot()}/library/metadata/{ratingKeys}'
          // where ratingKeys are comma-separated, and joinArgs URL-encodes the value
          const uriPrefix = trackUris[0]?.replace(/\/library\/metadata\/.*$/, '');

          const BATCH_SIZE = 50;
          for (let i = 0; i < trackUris.length; i += BATCH_SIZE) {
            const batch = trackUris.slice(i, i + BATCH_SIZE);
            const ratingKeys = batch.map(uri => uri.split('/library/metadata/')[1]);
            const batchUri = `${uriPrefix}/library/metadata/${ratingKeys.join(',')}`;
            const url = `/playlists/${playlistId}/items?uri=${encodeURIComponent(batchUri)}`;

            if (i === 0) {
              logger.info('First add-to-playlist request', { url: url.substring(0, 300), batchUri: batchUri.substring(0, 200) });
            }

            const response = await this.client.put(url, null);

            logger.info('Add to playlist batch response', { 
              playlistId, 
              batchIndex: Math.floor(i / BATCH_SIZE), 
              batchSize: batch.length,
              status: response.status,
              leafCountAdded: response.data?.MediaContainer?.leafCountAdded,
              leafCount: response.data?.MediaContainer?.Metadata?.[0]?.leafCount,
            });
          }

          logger.info('All tracks added to playlist', { playlistId, count: trackUris.length });
        } catch (error: any) {
          logger.error('Failed to add tracks to playlist', { 
            playlistId, 
            trackCount: trackUris.length,
            error: error.message,
            responseStatus: error.response?.status,
            responseData: JSON.stringify(error.response?.data)?.substring(0, 500)
          });
          if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
            throw new Error('Plex server is unreachable');
          }
          if (error.response?.status === 401) {
            throw new PlexAuthError('Invalid Plex token');
          }
          if (error.response?.status === 404) {
            throw new Error('Playlist not found');
          }
          if (error.isAxiosError) {
            throw new Error(`Failed to add tracks to playlist: ${error.message}`);
          }
          throw error;
        }
      }

  /**
   * Upload a poster image to a playlist from a URL
   */
  async uploadPlaylistPoster(playlistId: string, imageUrl: string): Promise<void> {
    try {
      // Download the image
      const imageResponse = await axios.get(imageUrl, {
        responseType: 'arraybuffer',
        timeout: 15000,
        headers: { 'User-Agent': 'Playlist Lab/1.0' },
      });

      const contentType = imageResponse.headers['content-type'] || 'image/jpeg';

      // Upload to Plex
      await this.client.post(
        `/library/metadata/${playlistId}/posters`,
        imageResponse.data,
        {
          headers: { 'Content-Type': contentType },
          maxBodyLength: 10 * 1024 * 1024,
        }
      );
    } catch (error: any) {
      logger.warn('Failed to upload playlist poster', { playlistId, error: error.message });
      // Non-fatal — playlist still works without a poster
    }
  }

  /**
   * Remove a track from a playlist
   */
  async removeFromPlaylist(playlistId: string, playlistItemId: string): Promise<void> {
    try {
      await this.client.delete(
        `/playlists/${playlistId}/items/${playlistItemId}`
      );
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.response?.status === 404) {
        throw new Error('Playlist or item not found');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to remove track from playlist: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Remove several items from a playlist in one pass. Plex reassigns the
   * remaining items' playlistItemID after each removal, so a caller that
   * collected all the IDs up front (the normal "clear and rebuild" pattern)
   * will reliably 404 on the second item onward if removals are just
   * awaited one after another and any failure is left to abort the rest -
   * that used to throw away the remaining removals and skip re-adding the
   * refreshed tracks entirely. A 404 here almost always just means Plex
   * already shifted that item out from under the ID we have, so it's
   * skipped rather than treated as a real failure.
   */
  async removeMultipleFromPlaylist(playlistId: string, playlistItemIds: string[]): Promise<void> {
    for (const playlistItemId of playlistItemIds) {
      try {
        await this.removeFromPlaylist(playlistId, playlistItemId);
      } catch (error: any) {
        if (error.message === 'Playlist or item not found') {
          logger.warn('[Plex] Item already gone while bulk-removing from playlist, skipping', { playlistId, playlistItemId });
          continue;
        }
        throw error;
      }
    }
  }

  /**
   * Move a playlist item to a new position
   * @param playlistId - The playlist ID
   * @param playlistItemId - The playlist item ID to move
   * @param afterItemId - The playlist item ID to place this item after (or '0' for first position)
   */
  async movePlaylistItem(playlistId: string, playlistItemId: string, afterItemId: string): Promise<void> {
    try {
      await this.client.put(
        `/playlists/${playlistId}/items/${playlistItemId}/move`,
        null,
        { params: { after: afterItemId } }
      );
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.response?.status === 404) {
        throw new Error('Playlist or item not found');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to move playlist item: ${error.message}`);
      }
      throw error;
    }
  }


  /**
   * Delete a playlist
   */
  async deletePlaylist(playlistId: string): Promise<void> {
    try {
      await this.client.delete(`/playlists/${playlistId}`);
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      if (error.response?.status === 404) {
        throw new Error('Playlist not found');
      }
      if (error.isAxiosError) {
        throw new Error(`Failed to delete playlist: ${error.message}`);
      }
      throw error;
    }
  }

  /**
   * Build a track URI for use in playlist operations
   */
  buildTrackUri(ratingKey: string, machineIdentifier?: string): string {
    const id = machineIdentifier || this.clientId;
    return `server://${id}/com.plexapp.plugins.library/library/metadata/${ratingKey}`;
  }
  
  /**
   * Get the machine identifier for this Plex server
   */
  async getMachineIdentifier(): Promise<string> {
    try {
      const response = await this.client.get('/');
      return response.data.MediaContainer.machineIdentifier;
    } catch (error: any) {
      logger.error('Failed to get machine identifier', { error: error.message });
      throw new Error('Failed to get machine identifier');
    }
  }
  
  /**
   * Get library URI for playlist creation
   */
  buildLibraryUri(libraryId: string, machineIdentifier?: string): string {
    const id = machineIdentifier || this.clientId;
    return `server://${id}/com.plexapp.plugins.library/library/sections/${libraryId}`;
  }

  /**
   * Get recently played tracks from a library
   * @param libraryId Library section ID
   * @param days Number of days to look back (7 or 30)
   * @param limit Maximum number of tracks to return
   */
  async getRecentTracks(libraryId: string, days: number = 7, limit: number = 200): Promise<PlexTrack[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/all`,
        {
          params: {
            type: 10, // Tracks
            sort: 'lastViewedAt:desc',
            'lastViewedAt>>': 0, // Has been played
            'X-Plex-Container-Size': limit
          }
        }
      );

      const tracks = response.data.MediaContainer.Metadata || [];
      
      // Filter to tracks played within the specified days
      const cutoff = Date.now() - days * 24 * 60 * 60 * 1000;
      return tracks.filter((t: PlexTrack) => {
        const lastViewed = (t as any).lastViewedAt || 0;
        return lastViewed * 1000 >= cutoff;
      });
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Search for an artist by name
   */
  async searchArtist(libraryId: string, name: string): Promise<PlexTrack | null> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/all`,
        {
          params: {
            type: 8, // Artist
            title: name,
            'X-Plex-Container-Size': 1
          }
        }
      );

      const artists = response.data.MediaContainer.Metadata || [];
      return artists[0] || null;
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  async searchArtists(libraryId: string, query: string): Promise<PlexTrack[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/all`,
        {
          params: {
            type: 8, // Artist
            title: query
          }
        }
      );

      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Get popular tracks from an artist using hubs (external popularity data)
   * Falls back to play count if hubs unavailable
   */
  async getArtistPopularTracks(libraryId: string, artistKey: string, limit: number = 10): Promise<PlexTrack[]> {
    try {
      // Try hubs first (external popularity from Last.fm etc)
      const hubsResponse = await this.client.get<PlexMediaContainer>(
        `/hubs/sections/${libraryId}`,
        {
          params: {
            metadataItemId: artistKey,
            count: limit
          }
        }
      );

      const hubs = hubsResponse.data.MediaContainer.Hub || [];
      const popularHub = hubs.find((h: any) => {
        const title = h.title?.toLowerCase() || '';
        return title === 'popular' || title === 'top tracks';
      });

      if (popularHub?.Metadata?.length > 0) {
        return popularHub.Metadata.slice(0, limit);
      }

      // Fallback: get artist tracks sorted by play count
      const response = await this.client.get<PlexMediaContainer>(
        `/library/metadata/${artistKey}/allLeaves`,
        {
          params: {
            sort: 'viewCount:desc',
            'X-Plex-Container-Size': limit
          }
        }
      );

      const tracks = response.data.MediaContainer.Metadata || [];
      return tracks.slice(0, limit);
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Get artist details including popularity data
   * Returns artist metadata with ratingCount (external popularity from Last.fm/MusicBrainz)
   */
  async getArtistDetails(artistKey: string): Promise<any> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/metadata/${artistKey}`
      );

      const artist = response.data.MediaContainer.Metadata?.[0];
      return artist || null;
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Get all albums from an artist
   */
  async getArtistAlbums(artistKey: string): Promise<PlexTrack[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/metadata/${artistKey}/children`
      );

      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Get similar/related tracks (sonically similar or from same artist/album)
   */
  async getSimilarTracks(trackKey: string, limit: number = 10): Promise<PlexTrack[]> {
    try {
      // Try nearest (sonically similar) first
      const response = await this.client.get<PlexMediaContainer>(
        `/library/metadata/${trackKey}/nearest`,
        {
          params: {
            'X-Plex-Container-Size': limit
          }
        }
      );

      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      // If nearest fails, try similar (metadata-based)
      try {
        const response = await this.client.get<PlexMediaContainer>(
          `/library/metadata/${trackKey}/similar`,
          {
            params: {
              count: limit
            }
          }
        );

        return response.data.MediaContainer.Metadata || [];
      } catch {
        return [];
      }
    }
  }

  /**
   * Get related content hubs for an artist or track
   * Returns organized hubs like "Fans Also Like", "Similar Artists", etc.
   */
  async getRelatedHubs(ratingKey: string): Promise<any[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/metadata/${ratingKey}/related`
      );

      return response.data.MediaContainer.Hub || [];
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      return [];
    }
  }

  /**
   * Get tracks not played in X days (for rediscoveries).
   *
   * @param includeNeverPlayed - Also mix in tracks with no play history at all.
   *   Off by default because "rediscoveries" (Daily Mix) means tracks the user
   *   actually played before and forgot about; Time Capsule wants this on, since
   *   a genuinely-forgotten, never-touched track is arguably the most on-theme
   *   result and would otherwise be excluded just for lacking a lastViewedAt.
   */
  async getStalePlayedTracks(libraryId: string, daysAgo: number, limit: number, includeNeverPlayed = false): Promise<PlexTrack[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/all`,
        {
          params: {
            type: 10, // Tracks
            sort: 'lastViewedAt:asc', // Oldest first
            'lastViewedAt>>': 0, // Has been played
            'X-Plex-Container-Size': limit * 2
          }
        }
      );

      const tracks = response.data.MediaContainer.Metadata || [];

      // Filter to tracks not played in X days
      const cutoff = Date.now() - daysAgo * 24 * 60 * 60 * 1000;
      const staleTracks = tracks.filter((t: PlexTrack) => {
        const lastViewed = (t as any).lastViewedAt || 0;
        return lastViewed * 1000 < cutoff;
      });

      if (!includeNeverPlayed) {
        return staleTracks.slice(0, limit);
      }

      const neverPlayedResponse = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/all`,
        {
          params: {
            type: 10,
            viewCount: 0,
            'X-Plex-Container-Size': limit * 2
          }
        }
      );
      const neverPlayed = neverPlayedResponse.data.MediaContainer.Metadata || [];

      // Interleave so never-played tracks aren't crowded out when the stale-play
      // pool alone is already bigger than `limit`.
      const merged: PlexTrack[] = [];
      const seenKeys = new Set<string>();
      const poolLength = Math.max(staleTracks.length, neverPlayed.length);
      for (let i = 0; i < poolLength && merged.length < limit; i++) {
        for (const track of [staleTracks[i], neverPlayed[i]]) {
          if (track && !seenKeys.has(track.ratingKey) && merged.length < limit) {
            merged.push(track);
            seenKeys.add(track.ratingKey);
          }
        }
      }

      return merged;
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Get recently added albums
   */
  async getRecentlyAddedAlbums(libraryId: string, limit: number = 10): Promise<PlexTrack[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/recentlyAdded`,
        {
          params: {
            type: 9, // Albums
            'X-Plex-Container-Size': limit
          }
        }
      );

      return response.data.MediaContainer.Metadata || [];
    } catch (error: any) {
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Get tracks with custom filters
   */
  async getTracksWithFilters(libraryId: string, filters: string[], limit: number = 1000): Promise<PlexTrack[]> {
    try {
      const filterQuery = filters.join('&');
      const url = `/library/sections/${libraryId}/all?type=10&${filterQuery}&X-Plex-Container-Size=${limit}`;

      const response = await this.client.get<PlexMediaContainer>(url);

      if (!response.data.MediaContainer?.Metadata) {
        return [];
      }

      return response.data.MediaContainer.Metadata as PlexTrack[];
    } catch (error) {
      logger.error('Failed to get tracks with filters', { error, libraryId, filters });
      return [];
    }
  }

  /**
   * Get tracks with advanced filters including sonic analysis
   * Supports all Plex filter fields and complex boolean logic
   */
  async getTracksWithAdvancedFilters(
    libraryId: string,
    options: {
      // Time filters
      playedInLastDays?: number;
      notPlayedInLastDays?: number;
      addedInLastDays?: number;
      
      // Release date filters
      releasedAfterYear?: number;
      releasedBeforeYear?: number;
      
      // Rating & popularity
      minRating?: number;
      maxRating?: number;
      minPlayCount?: number;
      maxPlayCount?: number;
      minSkipCount?: number;
      maxSkipCount?: number;
      
      // Track characteristics
      minDuration?: number; // in seconds
      maxDuration?: number; // in seconds
      minTrackNumber?: number;
      maxTrackNumber?: number;
      discNumber?: number;
      
      // Quality filters
      minBitrate?: number; // in kbps
      audioCodec?: string[]; // e.g., ['flac', 'mp3']
      minSampleRate?: number; // in Hz
      losslessOnly?: boolean;
      
      // Metadata filters
      genres?: string[];
      excludeGenres?: string[];
      moods?: string[];
      excludeMoods?: string[];
      styles?: string[];
      excludeStyles?: string[];
      collections?: string[];
      labels?: string[]; // record labels
      
      // Artist/Album filters
      artistNames?: string[];
      albumTitles?: string[];
      
      // Sorting
      sortBy?: 'random' | 'playCount' | 'lastPlayed' | 'dateAdded' | 'releaseDate' | 'rating' | 'duration' | 'title';
      sortDirection?: 'asc' | 'desc';
      
      // Limit
      limit?: number;
    }
  ): Promise<PlexTrack[]> {
    try {
      const filters: string[] = [];
      const now = Math.floor(Date.now() / 1000);

      // Time filters
      if (options.playedInLastDays) {
        const daysAgo = now - (options.playedInLastDays * 24 * 60 * 60);
        filters.push(`lastViewedAt>=${daysAgo}`);
      }

      if (options.notPlayedInLastDays) {
        const daysAgo = now - (options.notPlayedInLastDays * 24 * 60 * 60);
        filters.push(`lastViewedAt<${daysAgo}`);
      }

      if (options.addedInLastDays) {
        const daysAgo = now - (options.addedInLastDays * 24 * 60 * 60);
        filters.push(`addedAt>=${daysAgo}`);
      }

      // Release date filters
      // Note: Year is typically stored at the album level (parentYear) not track level
      // Use >>= and <<= operators to ensure the field exists before comparing
      if (options.releasedAfterYear) {
        filters.push(`parentYear>>=${options.releasedAfterYear}`);
      }

      if (options.releasedBeforeYear) {
        filters.push(`parentYear<<=${options.releasedBeforeYear}`);
      }

      // Rating filters
      if (options.minRating) {
        filters.push(`userRating>=${options.minRating}`);
      }

      if (options.maxRating) {
        filters.push(`userRating<=${options.maxRating}`);
      }

      // Play count filters
      if (options.minPlayCount !== undefined) {
        filters.push(`viewCount>=${options.minPlayCount}`);
      }

      if (options.maxPlayCount !== undefined) {
        filters.push(`viewCount<=${options.maxPlayCount}`);
      }

      // Duration filters (convert seconds to milliseconds)
      if (options.minDuration) {
        filters.push(`duration>=${options.minDuration * 1000}`);
      }

      if (options.maxDuration) {
        filters.push(`duration<=${options.maxDuration * 1000}`);
      }

      // Track number filters
      if (options.minTrackNumber) {
        filters.push(`index>=${options.minTrackNumber}`);
      }

      if (options.maxTrackNumber) {
        filters.push(`index<=${options.maxTrackNumber}`);
      }

      if (options.discNumber) {
        filters.push(`parentIndex=${options.discNumber}`);
      }

      // Quality filters
      if (options.minBitrate) {
        filters.push(`bitrate>=${options.minBitrate}`);
      }

      if (options.losslessOnly) {
        // Common lossless codecs
        filters.push('push=1');
        filters.push('audioCodec=flac');
        filters.push('or=1');
        filters.push('audioCodec=alac');
        filters.push('or=1');
        filters.push('audioCodec=ape');
        filters.push('or=1');
        filters.push('audioCodec=wav');
        filters.push('pop=1');
      } else if (options.audioCodec && options.audioCodec.length > 0) {
        if (options.audioCodec.length === 1) {
          filters.push(`audioCodec=${options.audioCodec[0]}`);
        } else {
          filters.push('push=1');
          filters.push(`audioCodec=${options.audioCodec[0]}`);
          for (let i = 1; i < options.audioCodec.length; i++) {
            filters.push('or=1');
            filters.push(`audioCodec=${options.audioCodec[i]}`);
          }
          filters.push('pop=1');
        }
      }

      if (options.minSampleRate) {
        filters.push(`sampleRate>=${options.minSampleRate}`);
      }

      // Build the query
      let url = `/library/sections/${libraryId}/all?type=10`;
      
      if (filters.length > 0) {
        url += '&' + filters.join('&');
      }

      // Add sorting
      if (options.sortBy && options.sortBy !== 'random') {
        const sortField = this.getSortField(options.sortBy);
        const direction = options.sortDirection || 'desc';
        url += `&sort=${sortField}:${direction}`;
      }

      // Add limit
      const limit = options.limit || 1000;
      url += `&X-Plex-Container-Size=${limit}`;

      logger.info('Advanced filter query', { url: url.substring(0, 500) });

      const response = await this.client.get<PlexMediaContainer>(url);

      let tracks = response.data.MediaContainer.Metadata || [];

      // Client-side filtering for complex fields
      // Genre filters (OR logic within includes, AND logic between includes/excludes)
      if (options.genres && options.genres.length > 0) {
        const genreLower = options.genres.map(g => g.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackGenres = track.Genre?.map((g: any) => g.tag.toLowerCase()) || [];
          return genreLower.some(genre => trackGenres.includes(genre));
        });
      }

      if (options.excludeGenres && options.excludeGenres.length > 0) {
        const excludeLower = options.excludeGenres.map(g => g.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackGenres = track.Genre?.map((g: any) => g.tag.toLowerCase()) || [];
          return !excludeLower.some(genre => trackGenres.includes(genre));
        });
      }

      // Mood filters
      if (options.moods && options.moods.length > 0) {
        const moodLower = options.moods.map(m => m.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackMoods = (track as any).Mood?.map((m: any) => m.tag.toLowerCase()) || [];
          return moodLower.some(mood => trackMoods.includes(mood));
        });
      }

      if (options.excludeMoods && options.excludeMoods.length > 0) {
        const excludeLower = options.excludeMoods.map(m => m.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackMoods = (track as any).Mood?.map((m: any) => m.tag.toLowerCase()) || [];
          return !excludeLower.some(mood => trackMoods.includes(mood));
        });
      }

      // Style filters
      if (options.styles && options.styles.length > 0) {
        const styleLower = options.styles.map(s => s.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackStyles = (track as any).Style?.map((s: any) => s.tag.toLowerCase()) || [];
          return styleLower.some(style => trackStyles.includes(style));
        });
      }

      if (options.excludeStyles && options.excludeStyles.length > 0) {
        const excludeLower = options.excludeStyles.map(s => s.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackStyles = (track as any).Style?.map((s: any) => s.tag.toLowerCase()) || [];
          return !excludeLower.some(style => trackStyles.includes(style));
        });
      }

      // Collection filters
      if (options.collections && options.collections.length > 0) {
        const collectionLower = options.collections.map(c => c.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackCollections = (track as any).Collection?.map((c: any) => c.tag.toLowerCase()) || [];
          return collectionLower.some(collection => trackCollections.includes(collection));
        });
      }

      // Label filters (record labels)
      if (options.labels && options.labels.length > 0) {
        const labelLower = options.labels.map(l => l.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const trackLabel = (track as any).parentStudio?.toLowerCase() || '';
          return labelLower.some(label => trackLabel.includes(label));
        });
      }

      // Artist name filters
      if (options.artistNames && options.artistNames.length > 0) {
        const artistLower = options.artistNames.map(a => a.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const artistName = track.grandparentTitle?.toLowerCase() || '';
          return artistLower.some(artist => artistName.includes(artist));
        });
      }

      // Album title filters
      if (options.albumTitles && options.albumTitles.length > 0) {
        const albumLower = options.albumTitles.map(a => a.toLowerCase());
        tracks = tracks.filter((track: PlexTrack) => {
          const albumTitle = track.parentTitle?.toLowerCase() || '';
          return albumLower.some(album => albumTitle.includes(album));
        });
      }

      // Random shuffle if requested
      if (options.sortBy === 'random') {
        tracks = this.shuffleArray(tracks);
      }

      return tracks;
    } catch (error: any) {
      logger.error('Failed to get tracks with advanced filters', { error: error.message, libraryId });
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw error;
    }
  }

  /**
   * Get sonically similar tracks using Plex's sonic analysis
   */
  async getSonicallySimilarTracks(
    seedTrackKey: string,
    _libraryId: string, // Unused but kept for API consistency
    options: {
      maxDistance?: number; // 0-1, lower = more similar (default 0.25)
      limit?: number;
      tempoRange?: { min: number; max: number }; // BPM
      energyRange?: { min: number; max: number }; // 0-1
      danceabilityRange?: { min: number; max: number }; // 0-1
    } = {}
  ): Promise<PlexTrack[]> {
    try {
      // First, get the seed track's music analysis
      const seedTrack = await this.getTrackDetails(seedTrackKey);
      if (!seedTrack) {
        throw new Error('Seed track not found');
      }

      // Try to get nearest tracks using Plex's sonic analysis
      try {
        const response = await this.client.get<PlexMediaContainer>(
          `/library/metadata/${seedTrackKey}/nearest`,
          {
            params: {
              limit: options.limit || 50
            }
          }
        );

        let tracks = response.data.MediaContainer.Metadata || [];

        // Apply additional filters if provided
        if (options.tempoRange || options.energyRange || options.danceabilityRange) {
          tracks = tracks.filter((track: any) => {
            const analysis = track.musicAnalysis;
            if (!analysis) return true; // Include if no analysis data

            if (options.tempoRange) {
              const tempo = analysis.tempo;
              if (tempo < options.tempoRange.min || tempo > options.tempoRange.max) {
                return false;
              }
            }

            if (options.energyRange) {
              const energy = analysis.energy;
              if (energy < options.energyRange.min || energy > options.energyRange.max) {
                return false;
              }
            }

            if (options.danceabilityRange) {
              const danceability = analysis.danceability;
              if (danceability < options.danceabilityRange.min || danceability > options.danceabilityRange.max) {
                return false;
              }
            }

            return true;
          });
        }

        return tracks;
      } catch (nearestError) {
        logger.warn('Sonic analysis not available, falling back to metadata similarity', { error: nearestError });
        
        // Fallback: use similar tracks endpoint (metadata-based)
        return await this.getSimilarTracks(seedTrackKey, options.limit || 50);
      }
    } catch (error: any) {
      logger.error('Failed to get sonically similar tracks', { error: error.message, seedTrackKey });
      throw error;
    }
  }

  /**
   * Get all available genres from a library
   */
  async getLibraryGenres(libraryId: string): Promise<string[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/genre`
      );

      const genres = response.data.MediaContainer.Directory || [];
      return genres.map((g: any) => g.title).sort();
    } catch (error: any) {
      logger.error('Failed to get library genres', { error: error.message, libraryId });
      return [];
    }
  }

  /**
   * Get all available moods from a library
   */
  async getLibraryMoods(libraryId: string): Promise<string[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/mood`
      );

      const moods = response.data.MediaContainer.Directory || [];
      return moods.map((m: any) => m.title).sort();
    } catch (error: any) {
      logger.error('Failed to get library moods', { error: error.message, libraryId });
      return [];
    }
  }

  /**
   * Get all available styles from a library
   */
  async getLibraryStyles(libraryId: string): Promise<string[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/style`
      );

      const styles = response.data.MediaContainer.Directory || [];
      return styles.map((s: any) => s.title).sort();
    } catch (error: any) {
      logger.error('Failed to get library styles', { error: error.message, libraryId });
      return [];
    }
  }

  /**
   * Get all available collections from a library
   */
  async getLibraryCollections(libraryId: string): Promise<string[]> {
    try {
      const response = await this.client.get<PlexMediaContainer>(
        `/library/sections/${libraryId}/collection`
      );

      const collections = response.data.MediaContainer.Directory || [];
      return collections.map((c: any) => c.title).sort();
    } catch (error: any) {
      logger.error('Failed to get library collections', { error: error.message, libraryId });
      return [];
    }
  }

  /**
   * Helper method to map sort field names to Plex API field names
   */
  private getSortField(sortBy: string): string {
    const sortFieldMap: Record<string, string> = {
      'playCount': 'viewCount',
      'lastPlayed': 'lastViewedAt',
      'dateAdded': 'addedAt',
      'releaseDate': 'year',
      'rating': 'userRating',
      'duration': 'duration',
      'title': 'titleSort'
    };

    return sortFieldMap[sortBy] || sortBy;
  }

  /**
   * Helper method to shuffle an array
   */
  private shuffleArray<T>(array: T[]): T[] {
    const result = [...array];
    for (let i = result.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [result[i], result[j]] = [result[j], result[i]];
    }
    return result;
  }


  /**
   * Get users who have access to the Plex server
   * Returns list of users with library access
   */
  async getServerUsers(): Promise<Array<{ username: string; email: string; thumb?: string }>> {
    try {
      const response = await this.client.get('/accounts');
      
      // Extract user information from the response
      const accounts = response.data.MediaContainer?.Account || [];
      
      return accounts.map((account: any) => ({
        username: account.name || account.title,
        email: account.email || '',
        thumb: account.thumb
      }));
    } catch (error: any) {
      logger.error('Failed to get Plex server users', { error: error.message });
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      throw new Error('Failed to retrieve server users');
    }
  }

  /**
   * Get list of Plex friends (users with library access)
   */
  async getFriends(): Promise<Array<{ username: string; email: string; thumb?: string; friendlyName?: string }>> {
    try {
      // plex.tv/api/v2/friends has been retired (now returns 410 Gone) - /api/users
      // is the same "users who share your Home/library" list this class already
      // relies on elsewhere (see getPlaylistForFriend's shared_servers lookup below).
      const response = await axios.get('https://plex.tv/api/users', {
        headers: {
          'Accept': 'application/json',
          'X-Plex-Token': this.token
        }
      });

      const friends = response.data?.MediaContainer?.User || [];

      logger.info('Friends API response', {
        count: friends.length,
        friends: friends.map((f: any) => ({
          id: f['@id'],
          username: f['@username'],
          title: f['@title'],
          email: f['@email']
        }))
      });

      return friends.map((friend: any) => ({
        username: friend['@username'] || friend['@title'],
        email: friend['@email'],
        thumb: friend['@thumb'],
        friendlyName: friend['@title'] || friend['@username']
      }));
    } catch (error: any) {
      logger.error('Failed to get Plex friends', { error: error.message });
      
      if (error.response?.status === 401) {
        throw new PlexAuthError('Invalid Plex token');
      }
      
      throw new Error('Failed to retrieve Plex friends');
    }
  }

  /**
   * Get playlists from a friend's account
   * Uses their access token to retrieve their playlists
   * 
   * @param friendUsername - Username of the friend
   * @returns Array of playlists from the friend's account
   */
  async getFriendPlaylists(friendUsername: string): Promise<Array<{ playlistId: string; playlistName: string; trackCount: number; duration: number }>> {
    try {
      logger.info('Getting friend playlists', { friendUsername });
      
      // First, get the friend's ID from the friends API. plex.tv/api/v2/friends
      // has been retired (now returns 410 Gone) - /api/users is the same list,
      // just XML-attribute-shaped JSON (see the shared_servers lookup below,
      // which already used this endpoint).
      const friendsResponse = await axios.get('https://plex.tv/api/users', {
        headers: {
          'Accept': 'application/json',
          'X-Plex-Token': this.token
        }
      });

      const friends = friendsResponse.data?.MediaContainer?.User || [];
      const friend = friends.find((f: any) =>
        f['@username'] === friendUsername ||
        f['@title'] === friendUsername ||
        f['@username']?.toLowerCase() === friendUsername.toLowerCase() ||
        f['@title']?.toLowerCase() === friendUsername.toLowerCase()
      );

      if (!friend) {
        const availableFriends = friends.map((f: any) => f['@username'] || f['@title']);
        logger.error('Friend not found', {
          friendUsername,
          availableFriends
        });
        throw new Error(`Friend "${friendUsername}" not found. Available friends: ${availableFriends.join(', ')}`);
      }

      logger.info('Found friend', {
        friendUsername,
        friendId: friend['@id'],
        friendTitle: friend['@title']
      });
      
      // Get the friend's access token from shared_servers
      // Note: This returns users who have access to YOUR server
      // To browse THEIR playlists, they need to have shared their server with YOU
      
      // First, try to find if this friend has shared their server with you
      const resourcesResponse = await axios.get('https://plex.tv/api/v2/resources', {
        headers: {
          'Accept': 'application/json',
          'X-Plex-Token': this.token
        },
        params: {
          includeHttps: 1,
          includeRelay: 1
        }
      });
      
      const resources = resourcesResponse.data || [];
      logger.info('Resources (servers shared with you)', { 
        count: resources.length,
        servers: resources.map((r: any) => ({
          name: r.name,
          owned: r.owned,
          ownerId: r.ownerId
        }))
      });
      
      // Find a server owned by this friend. /api/users gives friend['@id'] as a
      // string while /api/v2/resources gives ownerId as a number - compare as
      // strings so the id still matches.
      const friendServer = resources.find((r: any) =>
        String(r.ownerId) === String(friend['@id']) && r.provides?.includes('server')
      );

      if (!friendServer) {
        logger.error('Friend has not shared their server with you', {
          friendId: friend['@id'],
          friendUsername,
          note: 'This friend needs to share their Plex server with you to view their playlists'
        });
        throw new Error(`Friend "${friendUsername}" has not shared their Plex server with you. They need to share their library with you in Plex settings to view their playlists.`);
      }
      
      const friendToken = friendServer.accessToken;
      const friendServerUrl = friendServer.connections?.[0]?.uri || `https://${friendServer.address}:${friendServer.port}`;
      
      logger.info('Found friend server', { 
        friendUsername,
        serverName: friendServer.name,
        serverUrl: friendServerUrl
      });
      
      // Create a Plex client with the friend's server and token
      const friendClient = new PlexClient(
        friendServerUrl,
        friendToken,
        this.clientId,
        this.productName
      );
      
      // Get their playlists
      const playlists = await friendClient.getPlaylists();
      logger.info('Retrieved playlists from friend', { 
        friendUsername, 
        totalPlaylists: playlists.length 
      });
      
      // Filter to audio playlists and map to simplified format
      const audioPlaylists = playlists
        .filter(p => p.playlistType === 'audio')
        .map(p => ({
          playlistId: p.ratingKey,
          playlistName: p.title,
          trackCount: p.leafCount || 0,
          duration: p.duration || 0
        }));
      
      logger.info('Filtered to audio playlists', { 
        friendUsername, 
        audioPlaylistCount: audioPlaylists.length 
      });
      
      return audioPlaylists;
    } catch (error: any) {
      logger.error('Failed to get friend playlists', { 
        friendUsername, 
        error: error.message,
        stack: error.stack
      });
      
      if (error.response?.status === 401) {
        throw new PlexAuthError('Authentication failed - invalid token');
      }
      
      throw new Error(error.message || 'Failed to retrieve friend playlists');
    }
  }

  /**
   * Share a playlist with a Plex friend by copying it to their account
   * 
   * NOTE: Plex doesn't have a native "share" feature. This method creates a copy
   * of the playlist in the target user's account using their access token.
   * 
   * @param playlistId - The Plex playlist rating key
   * @param targetUsername - Username of the friend to share with
   */
  async sharePlaylist(playlistId: string, targetUsername: string): Promise<void> {
    try {
      logger.info('Sharing playlist (copying to user account)', { playlistId, targetUsername });
      
      // Step 1: Get the playlist details and items
      const playlistResponse = await this.client.get(`/playlists/${playlistId}`);
      const playlist = playlistResponse.data.MediaContainer?.Metadata?.[0];
      
      if (!playlist) {
        throw new Error('Playlist not found');
      }
      
      const itemsResponse = await this.client.get(`/playlists/${playlistId}/items`);
      const items = itemsResponse.data.MediaContainer?.Metadata || [];
      
      logger.info('Retrieved playlist details', { 
        title: playlist.title, 
        itemCount: items.length 
      });
      
      // Step 2: Get the target user's access token from plex.tv
      const machineId = await this.getMachineIdentifier();
      const sharedServersResponse = await axios.get(
        `https://plex.tv/api/servers/${machineId}/shared_servers`,
        {
          headers: {
            'Accept': 'application/json',
            'X-Plex-Token': this.token
          }
        }
      );
      
      const sharedServers = sharedServersResponse.data.MediaContainer?.SharedServer || [];
      
      // Also get user info to match by username
      const usersResponse = await axios.get('https://plex.tv/api/users', {
        headers: {
          'Accept': 'application/json',
          'X-Plex-Token': this.token
        }
      });
      
      const users = usersResponse.data.MediaContainer?.User || [];
      const userIdMap = new Map(
        users.map((u: any) => [u['@id'], u['@username'] || u['@title']])
      );
      
      // Find the target user's access token
      let targetUserToken: string | null = null;
      for (const server of sharedServers) {
        const userId = server['@userID'];
        const username = userIdMap.get(userId);
        if (username === targetUsername) {
          targetUserToken = server['@accessToken'];
          break;
        }
      }
      
      if (!targetUserToken) {
        throw new Error(`User "${targetUsername}" not found in shared users or doesn't have access to this server`);
      }
      
      logger.info('Found target user token', { targetUsername });
      
      // Step 3: Create a new Plex client with the target user's token
      const targetUserClient = new PlexClient(
        this.serverUrl,
        targetUserToken,
        this.clientId,
        this.productName
      );
      
      // Step 4: Delete existing playlist with same name (if it exists)
      try {
        const existingPlaylists = await targetUserClient.getPlaylists();
        const existingPlaylist = existingPlaylists.find(p => p.title === playlist.title);
        if (existingPlaylist) {
          logger.info('Deleting existing playlist in target user account', { 
            title: playlist.title 
          });
          await targetUserClient.deletePlaylist(existingPlaylist.ratingKey);
        }
      } catch (err) {
        // Ignore errors when checking for existing playlist
        logger.warn('Could not check for existing playlist', { error: err });
      }
      
      // Step 5: Create the playlist in the target user's account
      const trackUris = items.map((item: any) => 
        this.buildTrackUri(item.ratingKey, machineId)
      );
      
      // Get library URI from first track
      const libraryId = items[0]?.librarySectionID;
      if (!libraryId) {
        throw new Error('Could not determine library ID from playlist items');
      }
      
      const libraryUri = this.buildLibraryUri(libraryId.toString(), machineId);
      
      await targetUserClient.createPlaylist(
        playlist.title,
        libraryUri,
        trackUris
      );
      
      logger.info('Successfully shared playlist (created copy in user account)', { 
        playlistTitle: playlist.title,
        targetUsername,
        trackCount: items.length
      });
    } catch (error: any) {
      logger.error('Failed to share playlist', { 
        error: error.message, 
        playlistId, 
        targetUsername 
      });
      
      if (error.code === 'ECONNREFUSED' || error.code === 'ETIMEDOUT') {
        throw new Error('Plex server is unreachable');
      }
      
      if (error.response?.status === 404) {
        throw new Error('Playlist not found');
      }
      
      if (error.response?.status === 401) {
        throw new PlexAuthError('Authentication failed - invalid token');
      }
      
      throw new Error(error.message || 'Failed to share playlist');
    }
  }

  
}

// Export PlexService as an alias for PlexClient for backward compatibility
export const PlexService = PlexClient;