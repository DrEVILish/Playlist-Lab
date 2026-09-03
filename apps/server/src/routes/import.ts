import { Router, Request, Response, NextFunction } from 'express';
import { requireAuth } from '../middleware/auth';
import { createValidationError, createInternalError } from '../middleware/error-handler';
import { logger } from '../utils/logger';
import { importPlaylist, ImportOptions, runNewImportAndFinalize } from '../services/import';
import { matchPlaylist, dedupeByPlexRatingKey, buildRememberedMatchMap } from '../services/matching';
import { resolvePlexToken } from '../services/plex';
import { EventEmitter } from 'events';
import { debugLog } from '../utils/debug-logger';
import multer from 'multer';
import { importQueue } from '../services/import-queue';

const upload = multer({ 
  storage: multer.memoryStorage(),
  limits: { fileSize: 10 * 1024 * 1024 }, // 10MB limit for playlist files
  fileFilter: (_req, file, cb) => {
    // Accept every format this app can also export a playlist to (M3U/M3U8,
    // PLS, XSPF, CSV), plus TXT (some programs export playlists as .txt).
    const allowedExtensions = ['.m3u', '.m3u8', '.pls', '.xspf', '.csv', '.txt'];
    const ext = file.originalname.toLowerCase().substring(file.originalname.lastIndexOf('.'));

    if (allowedExtensions.includes(ext)) {
      cb(null, true);
    } else {
      cb(new Error(`Invalid file type. Please upload an M3U, M3U8, PLS, XSPF, CSV, or TXT playlist file. Got: ${ext}`));
    }
  }
});

const router = Router();

// Store active import sessions
// NOTE: These are exported and shared with index.ts's import-queue job handler.
// Both the queue-based import path (index.ts) and the SSE/polling endpoints below
// (and the direct, non-queue import path further down this file) must read/write
// the SAME Map/Set instances, or progress and cancellation silently go nowhere.
export const importSessions = new Map<string, EventEmitter>();
export const cancelledSessions = new Set<string>();

// Store progress state for polling
export const progressState = new Map<string, any>();

// All import routes require authentication
router.use(requireAuth);

/**
 * Helper function to handle import requests
 */
async function handleImport(
  req: Request,
  res: Response,
  next: NextFunction,
  source: 'spotify' | 'deezer' | 'apple' | 'tidal' | 'youtube' | 'amazon' | 'qobuz' | 'listenbrainz' | 'aria' | 'billboard' | 'lastfm'
) {
  debugLog('========== IMPORT REQUEST RECEIVED ==========');
  debugLog('Source:', source);
  debugLog('Body:', JSON.stringify(req.body, null, 2));
  debugLog('============================================');

  try {
    const { url, sessionId, customName, skipQueue } = req.body;

    debugLog('[Import Route] URL:', url);
    debugLog('[Import Route] SessionId:', sessionId);
    debugLog('[Import Route] Custom Name:', customName);
    debugLog('[Import Route] Skip Queue:', skipQueue);

    if (!url || typeof url !== 'string') {
      debugLog('[Import Route] ERROR: URL validation failed');
      return next(createValidationError('url is required and must be a string'));
    }

    const userId = req.session.userId!;
    const db = req.dbService!;

    debugLog('[Import Route] UserId:', userId);

    // Get user's Plex token from users table
    const userRow = (db as any).db.prepare('SELECT plex_token FROM users WHERE id = ?').get(userId);

    if (!userRow) {
      debugLog('[Import Route] ERROR: User not found');
      return next(createValidationError('User not found'));
    }

    const { plex_token: accountToken } = userRow;

    if (!accountToken || typeof accountToken !== 'string') {
      debugLog('[Import Route] ERROR: No Plex token');
      return next(createValidationError('No Plex token found. Please log in again.'));
    }

    // Get user's server configuration from user_servers table
    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, access_token FROM user_servers WHERE user_id = ? LIMIT 1').get(userId);

    if (!serverRow) {
      debugLog('[Import Route] ERROR: No server configured');
      return next(createValidationError('No Plex server configured. Please go to Settings and select a server.'));
    }

    const { server_url: serverUrl, library_id: libraryId } = serverRow;
    const plexToken = resolvePlexToken({ plex_token: accountToken }, serverRow);

    if (!serverUrl || typeof serverUrl !== 'string') {
      debugLog('[Import Route] ERROR: No server URL');
      return next(createValidationError('No Plex server URL configured. Please go to Settings and select a server.'));
    }

    debugLog('[Import Route] Server URL:', serverUrl);
    debugLog('[Import Route] Library ID:', libraryId);

    const options: ImportOptions = {
      userId,
      serverUrl,
      plexToken,
      libraryId,
      customName: customName?.trim() || undefined,
    };

    // Check if we should use the queue (default: yes, unless skipQueue is explicitly true)
    const useQueue = skipQueue !== true;

    if (useQueue) {
      // Add to queue
      const job = importQueue.enqueue({
        id: sessionId,
        userId,
        source,
        url,
        playlistName: customName || undefined,
        sessionId,
      });

      logger.info('Import job added to queue', {
        jobId: job.id,
        userId,
        source,
        playlistName: job.playlistName,
        queuePosition: importQueue.getUserQueueStatus(userId).position,
      });

      // Return immediately with queue status
      const queueStatus = importQueue.getUserQueueStatus(userId);
      return res.json({
        success: true,
        queued: true,
        position: queueStatus.position,
        message: queueStatus.position === 0 
          ? 'Import started' 
          : `Import queued (position ${queueStatus.position})`,
      });
    }

    // Legacy path: run immediately without queue (for backward compatibility)
    // Get progress emitter if sessionId provided
    let progressEmitter = sessionId ? importSessions.get(sessionId) : undefined;

    // If sessionId was provided but emitter not found (race condition with SSE),
    // create a fallback emitter that stores progress for polling
    if (sessionId && !progressEmitter) {
      debugLog('[Import Route] No SSE emitter found, creating fallback for polling');
      progressEmitter = new EventEmitter();
      importSessions.set(sessionId, progressEmitter);
      progressEmitter.on('progress', (data: any) => {
        progressState.set(sessionId, data);
      });
      progressEmitter.on('complete', (data: any) => {
        progressState.set(sessionId, { type: 'complete', ...data });
      });
      progressEmitter.on('error', (data: any) => {
        progressState.set(sessionId, { type: 'error', ...data });
      });
    }

    debugLog('[Import Route] Has progressEmitter:', !!progressEmitter);
    debugLog('[Import Route] Calling importPlaylist...');

    if (!progressEmitter) {
      // No SSE connection and no sessionId, run synchronously
      try {
        const result = await importPlaylist(source, url, options, db, progressEmitter, sessionId, cancelledSessions);
        debugLog('[Import Route] Import complete, sending response');
        res.json(result);
      } catch (importError: any) {
        debugLog('[Import Route] ========== IMPORT ERROR ==========');
        debugLog('[Import Route] Import error:', importError.message);
        debugLog('[Import Route] Import stack:', importError.stack);
        debugLog('[Import Route] ====================================');
        throw importError;
      }
    } else {
      // SSE connection exists, run asynchronously
      debugLog('[Import Route] Running import asynchronously with SSE');
      
      // Return immediately to prevent timeout
      res.json({ success: true, message: 'Import started' });
      
      // Run import in background
      importPlaylist(source, url, options, db, progressEmitter, sessionId, cancelledSessions)
        .then((result) => {
          debugLog('[Import Route] Import complete, emitting complete event');
          progressEmitter.emit('complete', result);
        })
        .catch((importError: any) => {
          debugLog('[Import Route] ========== IMPORT ERROR ==========');
          debugLog('[Import Route] Import error:', importError.message);
          debugLog('[Import Route] Import stack:', importError.stack);
          debugLog('[Import Route] ====================================');
          
          logger.error(`Failed to import ${source} playlist`, {
            error: importError.message || importError,
            stack: importError.stack,
            url
          });
          
          progressEmitter.emit('error', { 
            message: importError.message || 'Import failed' 
          });
        });
    }
  } catch (error: any) {
    debugLog('[Import Route] ========== ERROR ==========');
    debugLog('[Import Route] Error:', error.message);
    debugLog('[Import Route] Stack:', error.stack);
    debugLog('[Import Route] ============================');

    logger.error(`Failed to import ${source} playlist`, {
      error: error.message || error,
      stack: error.stack,
      url: req.body.url
    });
    next(createInternalError(`Failed to import ${source} playlist: ${error.message || 'Unknown error'}`));
  }
}


/**
 * POST /api/import/spotify
 * Import a Spotify playlist
 */
router.post('/spotify', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'spotify');
});

/**
 * POST /api/import/deezer
 * Import a Deezer playlist
 */
router.post('/deezer', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'deezer');
});

/**
 * POST /api/import/apple
 * Import an Apple Music playlist
 */
router.post('/apple', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'apple');
});

/**
 * POST /api/import/tidal
 * Import a Tidal playlist
 */
router.post('/tidal', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'tidal');
});

/**
 * POST /api/import/youtube
 * Import a YouTube Music playlist
 */
router.post('/youtube', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'youtube');
});

/**
 * POST /api/import/amazon
 * Import an Amazon Music playlist
 */
router.post('/amazon', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'amazon');
});

/**
 * POST /api/import/qobuz
 * Import a Qobuz playlist
 */
router.post('/qobuz', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'qobuz');
});

/**
 * POST /api/import/aria
 * Import an ARIA chart
 */
router.post('/aria', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'aria');
});

/**
 * POST /api/import/billboard
 * Import a Billboard chart
 */
router.post('/billboard', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'billboard');
});

/**
 * POST /api/import/lastfm
 * Import a Last.fm chart
 */
router.post('/lastfm', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'lastfm');
});

/**
 * POST /api/import/listenbrainz
 * Import a ListenBrainz playlist
 */
router.post('/listenbrainz', async (req: Request, res: Response, next: NextFunction) => {
  await handleImport(req, res, next, 'listenbrainz');
});

/**
 * POST /api/import/file
 * Import a playlist from a file (M3U, CSV, etc.)
 */
router.post('/file', requireAuth, upload.single('file'), async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { sessionId } = req.body;
    const file = req.file;

    if (!file) {
      return next(createValidationError('No file uploaded'));
    }

    // Read file content with encoding detection
    let content: string;
    try {
      // Try UTF-8 first
      content = file.buffer.toString('utf-8');
      
      // Check for invalid UTF-8 sequences
      if (content.includes('\uFFFD')) {
        // Try other encodings if UTF-8 fails
        content = file.buffer.toString('latin1');
      }
    } catch (error) {
      return next(createValidationError('Unable to read file. Please ensure it is a text-based playlist file.'));
    }
    
    const filename = file.originalname;

    debugLog('[File Import] ========== FILE UPLOAD ==========');
    debugLog('[File Import] Filename: ' + filename);
    debugLog('[File Import] Size: ' + file.size + ' bytes');
    debugLog('[File Import] Content length: ' + content.length + ' chars');
    debugLog('[File Import] SessionId: ' + sessionId);
    debugLog('[File Import] ==========================================');

    if (!content || content.trim().length === 0) {
      return next(createValidationError('File is empty. Please upload a valid playlist file.'));
    }
    
    // Basic validation - check if it looks like a playlist file. PLS/XSPF/CSV
    // are trusted by extension (they have their own dedicated parsers), since
    // sniffing for M3U-specific markers would reject all of them.
    const ext = filename.toLowerCase().substring(filename.lastIndexOf('.'));
    const isStructuredFormat = ['.pls', '.xspf', '.csv'].includes(ext);
    const hasExtinf = content.includes('#EXTINF');
    const hasM3UHeader = content.includes('#EXTM3U');
    const hasFilePaths = /\.(mp3|m4a|flac|wav|ogg|aac|wma)/i.test(content);

    if (!isStructuredFormat && !hasExtinf && !hasM3UHeader && !hasFilePaths) {
      debugLog('[File Import] File validation failed - no playlist markers found');
      return next(createValidationError('File does not appear to be a valid playlist. Please check the file format.'));
    }

    const userId = req.session.userId!;
    const db = req.dbService!;

    // Get user's Plex token from users table
    const userRow = (db as any).db.prepare('SELECT plex_token FROM users WHERE id = ?').get(userId);

    if (!userRow) {
      return next(createValidationError('User not found'));
    }

    const { plex_token: plexToken } = userRow;

    if (!plexToken || typeof plexToken !== 'string') {
      return next(createValidationError('No Plex token found. Please log in again.'));
    }

    // Get user's server configuration from user_servers table
    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, server_client_id, access_token FROM user_servers WHERE user_id = ? LIMIT 1').get(userId);

    if (!serverRow) {
      return next(createValidationError('No Plex server configured. Please go to Settings and select a server.'));
    }

    const { server_url: serverUrl, library_id: libraryId, server_client_id: serverClientId, access_token: accessToken } = serverRow;

    if (!serverUrl || typeof serverUrl !== 'string') {
      return next(createValidationError('No Plex server URL configured. Please go to Settings and select a server.'));
    }

    // Fire-and-forget: scrape, match, and create the Plex playlist
    // automatically, tracked via the notification bell - no manual
    // review-and-confirm step (see runNewImportAndFinalize's docs).
    res.json({ success: true, message: 'Import started' });

    runNewImportAndFinalize(
      db,
      'file',
      content,
      { id: userId, plex_token: plexToken },
      { server_url: serverUrl, server_client_id: serverClientId, library_id: libraryId, access_token: accessToken },
      { filename, notificationTitle: filename }
    );
  } catch (error: any) {
    logger.error('Failed to import playlist from file', { error, filename: req.file?.originalname });
    next(createInternalError(`Failed to import playlist: ${error.message || 'Unknown error'}`));
  }
});

/**
 * Search for playlists on a platform
 * POST /api/import/search
 */
router.post('/search', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { source, query } = req.body;

    if (!source || typeof source !== 'string') {
      return next(createValidationError('source is required and must be a string'));
    }

    if (!query || typeof query !== 'string') {
      return next(createValidationError('query is required and must be a string'));
    }

    // Spotify doesn't support search via scraping
    if (source === 'spotify') {
      return next(createValidationError('Search is not supported for Spotify'));
    }

    // Import the scraper functions
    const scrapers = await import('../services/scrapers');
    
    let playlists: any[] = [];
    
    try {
      // Call the appropriate search function based on source
      switch (source) {
        case 'deezer':
          // Deezer search - returns array of playlists
          playlists = await scrapers.searchDeezerPlaylists(query);
          break;
        case 'youtube':
          // YouTube Music search
          playlists = await scrapers.searchYouTubeMusicPlaylists(query);
          break;
        case 'apple':
          // Apple Music search not implemented yet
          return res.json({ playlists: [], message: 'Search not yet implemented for Apple Music' });
        case 'tidal':
          // Tidal search not implemented yet
          return res.json({ playlists: [], message: 'Search not yet implemented for Tidal' });
        case 'amazon':
          // Amazon Music search not implemented yet
          return res.json({ playlists: [], message: 'Search not yet implemented for Amazon Music' });
        case 'qobuz':
          // Qobuz search not implemented yet
          return res.json({ playlists: [], message: 'Search not yet implemented for Qobuz' });
        case 'listenbrainz':
          // ListenBrainz uses username, not search
          return next(createValidationError('Use username field for ListenBrainz'));
        default:
          return next(createValidationError(`Unsupported source: ${source}`));
      }
    } catch (scraperError: unknown) {
      const errorData = scraperError as any;
      logger.error('Search failed', { error: errorData.message, source, query });
      return res.status(400).json({
        error: {
          message: `Search failed: ${errorData.message}`,
          code: 'SEARCH_FAILED',
          source,
        },
      });
    }

    res.json({ playlists });
  } catch (error) {
    logger.error('Failed to search playlists', { error, source: req.body.source, query: req.body.query });
    next(createInternalError('Failed to search playlists'));
  }
});

/**
 * Search for tracks in Plex library
 * POST /api/plex/search
 */
router.post('/plex/search', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { query, libraryId, originalTitle, originalArtist, artist, title } = req.body;

    // artist/title are the separated form the Manual Match dialog sends.
    // `query` remains supported for the older single-box callers, but a
    // caller that knows which part is which should say so - see the parsing
    // fallback below for what guessing costs.
    const hasSplitSearch = (typeof artist === 'string' && artist.trim()) || (typeof title === 'string' && title.trim());
    if (!hasSplitSearch && (!query || typeof query !== 'string')) {
      return next(createValidationError('query is required and must be a string (or provide artist/title)'));
    }
    const effectiveQuery: string = (typeof query === 'string' && query) || [artist, title].filter(Boolean).join(' ').trim();

    const userId = req.session.userId!;
    const db = req.dbService!;

    // Get user's Plex token from users table
    const userRow = (db as any).db.prepare('SELECT plex_token FROM users WHERE id = ?').get(userId);
    
    if (!userRow) {
      return next(createValidationError('User not found'));
    }

    const { plex_token: accountToken } = userRow;

    if (!accountToken || typeof accountToken !== 'string') {
      return next(createValidationError('No Plex token found. Please log in again.'));
    }

    // Get user's server configuration from user_servers table
    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, access_token FROM user_servers WHERE user_id = ? LIMIT 1').get(userId);

    if (!serverRow) {
      return next(createValidationError('No Plex server configured. Please go to Settings and select a server.'));
    }

    const { server_url: serverUrl, library_id: defaultLibraryId } = serverRow;

    if (!serverUrl || typeof serverUrl !== 'string') {
      return next(createValidationError('No Plex server URL configured. Please go to Settings and select a server.'));
    }

    // Use provided libraryId or fall back to user's default
    const searchLibraryId = libraryId || defaultLibraryId;

    // Import Plex client
    const { PlexClient } = await import('../services/plex');
    const plexToken = resolvePlexToken({ plex_token: accountToken }, serverRow);
    const plexClient = new PlexClient(serverUrl, plexToken);
    const matchingSettings = db.getUserSettings(userId).matching_settings;

    let rawTracks: any[] = [];

    let parsedArtist: string | undefined;
    let parsedTitle: string | undefined;

    if (hasSplitSearch) {
      // Nothing to guess - the caller told us which is which.
      parsedArtist = typeof artist === 'string' ? artist.trim() : undefined;
      parsedTitle = typeof title === 'string' ? title.trim() : undefined;
      logger.info(`[Plex Search] Separated search: artist="${parsedArtist || ''}", title="${parsedTitle || ''}"`);
    } else if (effectiveQuery.includes(' - ')) {
      const parts = effectiveQuery.split(' - ');
      if (parts.length === 2) {
        parsedArtist = parts[0].trim();
        parsedTitle = parts[1].trim();
        logger.debug(`[Plex Search] Parsed query as artist-title: "${parsedArtist}" - "${parsedTitle}"`);
      }
    }

    // Last resort for a single-box query with no separator: guess where the
    // artist ends by word position. This is wrong for any artist whose name
    // isn't two words ("The Rolling Stones Paint It Black" splits as artist
    // "The Rolling"), which is exactly why the Manual Match dialog now sends
    // artist and title as separate fields instead.
    if (!parsedArtist && !parsedTitle) {
      const words = effectiveQuery.split(/\s+/);
      if (words.length >= 3) {
        parsedArtist = words.slice(0, 2).join(' ');
        parsedTitle = words.slice(2).join(' ');
        logger.debug(`[Plex Search] Guessing split of unstructured query: artist="${parsedArtist}", title="${parsedTitle}"`);
      } else if (words.length === 2) {
        parsedArtist = words[0];
        parsedTitle = words[1];
        logger.debug(`[Plex Search] Guessing split of 2-word query: artist="${parsedArtist}", title="${parsedTitle}"`);
      }
    }

    // Retrieve candidates with the exact same multi-tier search findBestMatch()
    // uses for automatic import/Retry (findPlexCandidates()), so Manual
    // Rematch is never working from a weaker or differently-behaved search.
    // Driven by what the user actually typed into the search box,
    // NOT originalTitle/originalArtist - those are the already-failed
    // original track and are only used below to score results against it;
    // searching with them instead would make editing the search
    // box do nothing, since every search would just repeat the search that
    // already failed automatically.
    try {
      const { findPlexCandidates } = await import('../services/matching');
      rawTracks = parsedArtist && !parsedTitle ? [] : await findPlexCandidates(
        { title: parsedTitle || effectiveQuery, artist: parsedArtist || '' },
        plexClient,
        searchLibraryId,
        matchingSettings
      );
      logger.info(`[Plex Search] Shared candidate search for "${parsedArtist || ''} ${parsedTitle || effectiveQuery}": ${rawTracks.length} tracks`);
    } catch (err: any) {
      logger.warn(`[Plex Search] Shared candidate search failed: ${err.message}`);
    }

    // Everything that used to sit here - an artist-first search with the
    // parsed values, and a plain hub search of the whole query - is what
    // findPlexCandidates() above already does, via the very same
    // PlexClient.searchTrack() cascade. Running them again only repeated
    // searches that had just returned nothing, which is most of why a Manual
    // Match for a track that isn't in the library took ~13 seconds to come
    // back empty. The genuinely different title-fragment fallback below is
    // kept, as is browsing by artist alone:

    // Artist with no track name is a browse, not a search - list what that
    // artist has rather than looking for a track named after them. Worth
    // keeping as its own branch now that the dialog has separate fields: an
    // empty title is something the user can actually express, where the old
    // single box could only ever be guessed at.
    if (rawTracks.length === 0 && parsedArtist && !parsedTitle && searchLibraryId) {
      try {
        const artistEntity = await plexClient.searchArtist(searchLibraryId, parsedArtist);
        if (artistEntity?.ratingKey) {
          rawTracks = await plexClient.getArtistPopularTracks(searchLibraryId, artistEntity.ratingKey, 100);
          logger.info(`[Plex Search] Artist browse "${parsedArtist}": ${rawTracks.length} tracks`);
        }
      } catch (err: any) {
        logger.warn(`[Plex Search] Artist browse failed: ${err.message}`);
      }
    }

    // FALLBACK: Library search by track title for compilation tracks
    // Hub search only searches by album artist (grandparentTitle), so compilation tracks
    // where album artist is "Various Artists" won't be found. This searches the library
    // directly by track title, returning tracks regardless of album artist.
    if (rawTracks.length === 0 && searchLibraryId) {
      logger.debug(`[Plex Search] Regular search found nothing, trying library title search fallback`);
      try {
        // Extract potential title candidates from the query
        const titleCandidates: string[] = [];
        
        // If the query has " - " separator, the part after is the title
        if (effectiveQuery.includes(' - ')) {
          const parts = effectiveQuery.split(' - ');
          if (parts.length >= 2) {
            const titlePart = parts.slice(1).join(' - ').trim();
            if (titlePart.length >= 2) titleCandidates.push(titlePart);
          }
        }
        
        // Try first few words and last few words as potential title
        const queryWords = effectiveQuery.split(/\s+/).filter((w: string) => w.length > 1);
        if (queryWords.length >= 3) {
          titleCandidates.push(queryWords.slice(0, 4).join(' '));
          titleCandidates.push(queryWords.slice(0, 3).join(' '));
        }
        
        // Also try the full query as-is
        if (effectiveQuery.trim().length >= 2) {
          titleCandidates.push(effectiveQuery.trim());
        }
        
        const uniqueCandidates = [...new Set(titleCandidates)];
        
        for (const candidate of uniqueCandidates) {
          const trackSearchResponse = await plexClient.client.get(
            `/library/sections/${searchLibraryId}/all`,
            {
              params: {
                type: 10,
                'track.title': candidate,
              }
            }
          );
          const libTracks = trackSearchResponse.data.MediaContainer?.Metadata || [];
          logger.debug(`[Plex Search] Library title search for "${candidate}": ${libTracks.length} tracks`);
          
          for (const t of libTracks) {
            if (!rawTracks.some((existing: any) => existing.ratingKey === t.ratingKey)) {
              rawTracks.push(t);
            }
          }
          
          // If we found tracks, stop trying other candidates
          if (rawTracks.length > 0) break;
        }
        
        if (rawTracks.length > 0) {
          logger.debug(`[Plex Search] Library title search fallback found ${rawTracks.length} tracks`);
        }
      } catch (err: any) {
        logger.warn(`[Plex Search] Library title search fallback failed: ${err.message}`);
      }
    }

    logger.debug(`[Plex Search] Found ${rawTracks.length} raw tracks for query: ${effectiveQuery}`);

    // Filter to ensure we only have tracks (type 10), not albums or artists
    const trackTypeOnly = rawTracks.filter((track: any) => {
      // Plex track type is 'track' (string) or 10 (number)
      const isTrack = track.type === 'track' || track.type === 10;
      if (!isTrack) {
        logger.warn(`[Plex Search] Filtering out non-track item: ${track.title} (type: ${track.type})`);
      }
      return isTrack;
    });

    logger.debug(`[Plex Search] After type filter: ${trackTypeOnly.length} tracks`);

    // Enrich tracks missing artist/album/media by fetching full metadata
    // Use concurrency limit to avoid hammering Plex with 50 simultaneous requests
    const CONCURRENCY = 5;
    const enrichedTracks: any[] = [];
    for (let i = 0; i < trackTypeOnly.length; i += CONCURRENCY) {
      const batch = trackTypeOnly.slice(i, i + CONCURRENCY);
      const results = await Promise.all(
        batch.map(async (track: any) => {
          // Check if track has all required metadata
          const hasMetadata = track.grandparentTitle && track.parentTitle && track.Media?.length;
          
          if (hasMetadata) {
            return track;
          }
          
          // Fetch full metadata if missing
          try {
            logger.debug(`[Plex Search] Enriching track ${track.ratingKey}: ${track.title}`);
            const detailResp = await plexClient.getTrackDetails(track.ratingKey);
            return detailResp || track;
          } catch (err) {
            logger.warn(`[Plex Search] Failed to enrich track ${track.ratingKey}: ${err}`);
            return track;
          }
        })
      );
      enrichedTracks.push(...results);
    }

    logger.debug(`[Plex Search] Enriched ${enrichedTracks.length} tracks`);

    // Log a sample track to see what data we have
    if (enrichedTracks.length > 0) {
      const sample = enrichedTracks[0];
      logger.debug(`[Plex Search] Sample track data:`, {
        title: sample.title,
        hasMedia: !!sample.Media,
        mediaLength: sample.Media?.length,
        mediaData: sample.Media?.[0] ? {
          bitrate: sample.Media[0].bitrate,
          audioCodec: sample.Media[0].audioCodec,
          container: sample.Media[0].container,
        } : 'no media',
      });
    }

    // Calculate relevance score for each track
    const queryLower = effectiveQuery.toLowerCase();
    const queryWords = queryLower.split(/\s+/).filter(w => w.length >= 2);
    
    const tracksWithScores = enrichedTracks.map((track: any) => {
      const titleLower = (track.title || '').toLowerCase();
      // Check both album artist and track artist for better compilation support
      const albumArtistLower = (track.grandparentTitle || '').toLowerCase();
      const trackArtistLower = (track.originalTitle || '').toLowerCase();
      // Track artist (originalTitle) is the actual per-track performer; album artist
      // (grandparentTitle) is just the library's folder/grouping artist and can be
      // wrong or unmatchable (e.g. "Various Artists", a soundtrack's composer) -
      // prefer track artist whenever Plex has one.
      const artistLower = trackArtistLower || albumArtistLower;
      const albumLower = (track.parentTitle || '').toLowerCase();
      
      // Extract media info
      const media = track.Media?.[0];
      const codec = media?.audioCodec?.toUpperCase() || 'Unknown';
      const bitrate = media?.bitrate || 0;
      // Plex returns bitrate in kbps, not bps, so don't divide
      const bitrateKbps = bitrate || 0;
      
      logger.debug(`[Plex Search] Track "${track.title}": codec=${codec}, bitrate=${bitrate}, bitrateKbps=${bitrateKbps}`);
      
      let score = 0;
      
      // Check if the query matches artist name well (for artist-focused searches)
      const artistMatchesQuery = artistLower.includes(queryLower) || queryLower.includes(artistLower);
      
      if (artistMatchesQuery) {
        // If artist matches, prioritize it heavily
        score += 800;
        
        // Exact artist match gets even more
        if (artistLower === queryLower) {
          score += 200;
        }
      }
      
      // Exact title match (highest score for title-focused searches)
      if (titleLower === queryLower) {
        score += 1000;
      }
      
      // Title starts with the query
      if (titleLower.startsWith(queryLower)) {
        score += 500;
      }
      
      // Title contains the full query
      if (titleLower.includes(queryLower)) {
        score += 300;
      }
      
      // Count matching words in title
      for (const word of queryWords) {
        if (titleLower.includes(word)) {
          score += 100;
        }
      }
      
      // Artist word matches (if not already counted above)
      if (!artistMatchesQuery) {
        for (const word of queryWords) {
          if (artistLower.includes(word)) {
            score += 20;
          }
          // Also check track artist (originalTitle) for compilations
          // e.g. album artist = "Various Artists" but track artist = "Lindiwe Mkhize"
          if (trackArtistLower && trackArtistLower.includes(word) && !artistLower.includes(word)) {
            score += 30; // Slightly higher than album artist word match since it's more specific
          }
        }
      }
      
      // Album match (lowest priority)
      if (albumLower.includes(queryLower)) {
        score += 10;
      }
      
      // Prefer higher quality (small bonus)
      if (codec === 'FLAC') score += 5;
      else if (codec === 'ALAC') score += 4;
      else if (codec === 'AAC') score += 2;
      
      // Penalize re-recorded versions so originals appear first in search results
      if (/\b(re[- ]?recorded|re[- ]?recording|re[- ]?record|taylor'?s? version)\b/i.test(titleLower)) {
        score -= 200;
      }
      
      // Penalize sped up / slowed down versions so originals appear first in search results
      if (/\b(sped up|speed up|slowed down|slow down|nightcore|slowed \+ reverb|sped \+ reverb|accelerated|decelerated)\b/i.test(titleLower)) {
        score -= 200;
      }
      
      return {
        ratingKey: track.ratingKey,
        title: track.title,
        // Same track-artist-first priority as artistLower above - the manual
        // rematch UI must show the real per-track performer, not the album artist.
        artist: track.originalTitle || track.grandparentTitle || 'Unknown Artist',
        album: track.parentTitle || 'Unknown Album',
        codec: codec,
        bitrate: bitrateKbps,
        duration: track.duration || 0,
        score: score,
        plexRaw: track,
      };
    });

    // When the caller identifies which missing track this search is trying to
    // replace, score every result with the exact same function findBestMatch()
    // uses during a real import (services/matching.ts's scorePlexCandidate) -
    // not a re-derived approximation - so "Manual Rematch" shows the real
    // match score instead of nothing (the old free-text relevance score below
    // was never even sent to the frontend).
    let withMatchScore = tracksWithScores;
    let sortByMatchScore = false;
    if (typeof originalTitle === 'string' && typeof originalArtist === 'string') {
      const { scorePlexCandidate } = await import('../services/matching');
      const effectiveMinScore = matchingSettings.minMatchScore <= 1 ? matchingSettings.minMatchScore * 100 : matchingSettings.minMatchScore;
      withMatchScore = tracksWithScores.map((track) => {
        const scored = scorePlexCandidate(originalTitle, originalArtist, track.plexRaw, matchingSettings);
        // scored.plexArtist is the same track-artist-first resolution findBestMatch()
        // uses for a real import - use it here too so the artist shown matches the score.
        return { ...track, artist: scored.plexArtist, matchScore: scored.score, matched: scored.score >= effectiveMinScore };
      });
      sortByMatchScore = true;
    }

    // Sort by the real match score when we have one, otherwise by free-text relevance
    withMatchScore.sort((a: any, b: any) => sortByMatchScore ? b.matchScore - a.matchScore : b.score - a.score);

    // Remove internal-only fields from final output
    const tracks = withMatchScore.map(({ score, plexRaw, ...track }: any) => track);

    logger.info(`[Plex Search] Returning ${tracks.length} tracks sorted by ${sortByMatchScore ? 'match score' : 'relevance'}`);

    res.json({ tracks });
  } catch (error: any) {
    logger.error('Failed to search Plex tracks', { error: error.message, query: req.body.query });
    next(createInternalError(`Failed to search tracks: ${error.message || 'Unknown error'}`));
  }
});

/**
 * Retry matching a single track
 * POST /api/import/plex/retry-match
 */
router.post('/plex/retry-match', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { track } = req.body;

    if (!track || typeof track !== 'object') {
      return next(createValidationError('track is required and must be an object'));
    }

    if (!track.title || !track.artist) {
      return next(createValidationError('track must have title and artist'));
    }

    const userId = req.session.userId!;
    const db = req.dbService!;

    // Get user's Plex token from users table
    const userRow = (db as any).db.prepare('SELECT plex_token FROM users WHERE id = ?').get(userId);
    
    if (!userRow) {
      return next(createValidationError('User not found'));
    }

    const { plex_token: accountToken } = userRow;

    if (!accountToken || typeof accountToken !== 'string') {
      return next(createValidationError('No Plex token found. Please log in again.'));
    }

    // Get user's server configuration from user_servers table
    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, access_token FROM user_servers WHERE user_id = ? LIMIT 1').get(userId);

    if (!serverRow) {
      return next(createValidationError('No Plex server configured. Please go to Settings and select a server.'));
    }

    const { server_url: serverUrl, library_id: libraryId } = serverRow;
    const plexToken = resolvePlexToken({ plex_token: accountToken }, serverRow);

    if (!serverUrl || typeof serverUrl !== 'string') {
      return next(createValidationError('No Plex server URL configured. Please go to Settings and select a server.'));
    }

    // Get user's matching settings
    const settingsRow = (db as any).db.prepare('SELECT matching_settings FROM users WHERE id = ?').get(userId);
    const matchingSettings = settingsRow?.matching_settings 
      ? JSON.parse(settingsRow.matching_settings)
      : {
          minMatchScore: 70,
          preferHigherQuality: true,
          preferLossless: false,
          allowRemaster: true,
          allowLive: false,
          allowRemix: false,
          allowCover: false,
          allowKaraoke: false,
          allowInstrumental: false,
          allowAcoustic: true,
          allowExplicit: true,
          allowClean: true,
        };

    // Use matchPlaylist with a single track
    const externalTrack = {
      title: track.title,
      artist: track.artist,
      album: track.album || '',
    };

    const rememberedMatches = buildRememberedMatchMap(db.getUserManualMatches(userId));

    const matchResults = await matchPlaylist(
      [externalTrack],
      serverUrl,
      plexToken,
      libraryId,
      matchingSettings,
      undefined,
      undefined,
      undefined,
      undefined,
      rememberedMatches
    );

    const matchResult = matchResults[0];

    if (matchResult && matchResult.matched && matchResult.plexRatingKey) {
      // Return the matched track info
      res.json({
        matched: true,
        plexRatingKey: matchResult.plexRatingKey,
        plexTitle: matchResult.plexTitle,
        plexArtist: matchResult.plexArtist,
        plexAlbum: matchResult.plexAlbum,
      });
    } else {
      // No match found
      res.json({
        matched: false,
        message: 'Track not found in Plex library',
      });
    }
  } catch (error: any) {
    logger.error('Failed to retry track match', { error: error.message, track: req.body.track });
    next(createInternalError(`Failed to retry match: ${error.message || 'Unknown error'}`));
  }
});

/**
 * POST /api/import/playlist-name
 * Just the display name of a Spotify playlist, for the import modal's name
 * field.
 *
 * The modal used to ask /preview for this, which scrapes the entire
 * playlist - an embed-page fetch with a 15s budget, then slower fallbacks
 * behind it - so the name field sat on "Fetching name…" for 30s or more to
 * deliver one string. Spotify's public oEmbed endpoint returns the title
 * alone, unauthenticated, in about 50ms.
 */
router.post('/playlist-name', async (req: Request, res: Response, next: NextFunction) => {
  const { url } = req.body;
  if (!url || typeof url !== 'string') {
    return next(createValidationError('url is required and must be a string'));
  }

  // A playlist that has been imported or previewed before already has its
  // name on hand - no reason to ask Spotify again.
  const cached = req.dbService?.getCachedPlaylist('spotify', url);
  if (cached?.name) {
    return res.json({ name: cached.name });
  }

  try {
    const response = await fetch(`https://open.spotify.com/oembed?url=${encodeURIComponent(url)}`, {
      signal: AbortSignal.timeout(8000),
    });
    if (!response.ok) throw new Error(`oEmbed responded ${response.status}`);
    const data = await response.json() as { title?: string };
    res.json({ name: data?.title || '' });
  } catch (error: any) {
    // An empty name is a valid answer here: the field stays blank and the
    // real name is filled in from the scrape when the import actually runs.
    // Not worth failing the request and showing the user an error for.
    logger.warn('[Import] Could not resolve Spotify playlist name', { url, error: error.message });
    res.json({ name: '' });
  }
});

/**
 * Preview playlist without importing
 * POST /api/import/preview
 */
router.post('/preview', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { source, url } = req.body;

    if (!source || typeof source !== 'string') {
      return next(createValidationError('source is required and must be a string'));
    }

    if (!url || typeof url !== 'string') {
      return next(createValidationError('url is required and must be a string'));
    }

    // Always fetch fresh data for Spotify playlists - cache is only used as fallback if scraping fails
    // This ensures playlist changes (added/removed tracks) are always reflected
    const dbService = req.dbService!;

    // Import the scraper functions
    const scrapers = await import('../services/scrapers');
    const { getSpotifyToken } = await import('./spotify-auth');
    
    let playlistData;
    
    try {
      // Call the appropriate scraper based on source
      switch (source) {
        case 'spotify':
          // Try to use Spotify API with OAuth token first
          const userId = req.session.userId;
          const db = (dbService as any).db;
          const spotifyToken = userId ? await getSpotifyToken(userId, db) : null;
          
          if (spotifyToken) {
            // Use Spotify API
            const playlistId = url.split('/playlist/')[1]?.split('?')[0];
            if (!playlistId) {
              return next(createValidationError('Invalid Spotify playlist URL'));
            }
            
            try {
              logger.info('Attempting to fetch Spotify playlist via API', { playlistId, userId });
              
              // Get playlist details
              const playlistResponse = await fetch(`https://api.spotify.com/v1/playlists/${playlistId}`, {
                headers: {
                  'Authorization': `Bearer ${spotifyToken}`,
                },
              });
              
              if (!playlistResponse.ok) {
                const errorData = await playlistResponse.json();
                logger.error('Spotify API error', { 
                  error: errorData, 
                  status: playlistResponse.status,
                  playlistId,
                  userId,
                  tokenPrefix: spotifyToken.substring(0, 10) + '...'
                });
                throw new Error(`Spotify API error: ${errorData.error?.message || playlistResponse.statusText}`);
              }
              
              const playlist = await playlistResponse.json() as any;
              logger.info('Successfully fetched Spotify playlist via API', { 
                playlistId, 
                trackCount: playlist.tracks?.items?.length || 0 
              });
              
              playlistData = {
                name: playlist.name,
                tracks: playlist.tracks.items.map((item: any) => ({
                  title: item.track.name,
                  artist: item.track.artists.map((a: any) => a.name).join(', '),
                })),
              };
            } catch (apiError: any) {
              logger.error('Failed to fetch from Spotify API, falling back to scraping', { 
                error: apiError.message,
                stack: apiError.stack,
                playlistId,
                userId
              });
              // Fall back to scraping if API fails
              playlistData = await scrapers.scrapeSpotifyPlaylist(url);
            }
          } else {
            logger.info('No Spotify token available, falling back to scraping', { userId });
            // Fall back to scraping
            playlistData = await scrapers.scrapeSpotifyPlaylist(url);
          }
          break;
        case 'deezer':
          // Extract playlist ID from URL or use as-is
          let deezerId = url;
          if (url.includes('deezer.com')) {
            const match = url.match(/\/playlist\/(\d+)/);
            deezerId = match ? match[1] : url;
          }
          logger.info('[Import] Deezer ID extraction', { originalUrl: url, extractedId: deezerId });
          playlistData = await scrapers.scrapeDeezerPlaylist(deezerId);
          break;
        case 'apple':
          playlistData = await scrapers.scrapeAppleMusicPlaylist(url);
          break;
        case 'tidal':
          playlistData = await scrapers.scrapeTidalPlaylist(url);
          break;
        case 'youtube':
          playlistData = await scrapers.scrapeYouTubeMusicPlaylist(url);
          break;
        case 'amazon':
          playlistData = await scrapers.scrapeAmazonMusicPlaylist(url);
          break;
        case 'qobuz':
          playlistData = await scrapers.scrapeQobuzPlaylist(url);
          break;
        case 'aria':
          playlistData = await scrapers.scrapeAriaPlaylist(url);
          break;
        case 'billboard':
          playlistData = await scrapers.scrapeBillboardPlaylist(url);
          break;
        case 'lastfm':
          playlistData = await scrapers.scrapeLastfmPlaylist(url);
          break;
        case 'listenbrainz':
          const playlists = await scrapers.getListenBrainzPlaylists(url);
          playlistData = playlists[0] || { name: 'No playlists found', tracks: [] };
          break;
        default:
          return next(createValidationError(`Unsupported source: ${source}`));
      }

      // Cache the scraped data
      if (playlistData && playlistData.tracks) {
        dbService.saveCachedPlaylist(
          source,
          url,
          playlistData.name,
          '',
          playlistData.tracks
        );
        logger.info(`[Preview] Cached ${playlistData.tracks.length} tracks for ${source}:${url}`);
      }

      res.json({
        name: playlistData.name,
        tracks: playlistData.tracks,
      });
    } catch (scrapingError: any) {
      // Scraping failed, check for stale cache
      logger.warn(`[Preview] Scraping failed for ${source}:${url}, checking for stale cache`, { error: scrapingError.message });
      
      const staleCache = dbService.getCachedPlaylist(source, url);
      if (staleCache) {
        logger.info(`[Preview] Using stale cache for ${source}:${url}`);
        return res.json({
          name: staleCache.name,
          tracks: staleCache.tracks,
          fromCache: true,
        });
      }
      
      // No cache available - return empty with helpful message
      logger.info(`[Preview] No cache available for ${source}:${url}, returning empty preview`);
      return res.json({
        name: 'Preview Unavailable',
        tracks: [],
        error: scrapingError.message,
      });
    }
  } catch (error: any) {
    logger.error('Failed to preview playlist', { error: error.message, source: req.body.source, url: req.body.url });
    next(createInternalError(error.message || 'Failed to preview playlist'));
  }
});

/**
 * POST /api/import/match
 * Match a plain list of {title, artist, album} tracks against the user's
 * Plex library, without any source-specific scraping first. Used wherever
 * the caller already has track text from somewhere other than a scraped
 * playlist URL - e.g. restoring a backup JSON file - and just needs the
 * same matching engine every other import path uses (services/matching.ts's
 * matchPlaylist) before calling POST /confirm to actually create the
 * playlist from the result.
 */
router.post('/match', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { tracks } = req.body;

    if (!tracks || !Array.isArray(tracks) || tracks.length === 0) {
      return next(createValidationError('tracks is required and must be a non-empty array'));
    }

    const userId = req.session.userId!;
    const db = req.dbService!;

    const userRow = (db as any).db.prepare('SELECT plex_token FROM users WHERE id = ?').get(userId);
    if (!userRow) {
      return next(createValidationError('User not found'));
    }
    const { plex_token: accountToken } = userRow;
    if (!accountToken || typeof accountToken !== 'string') {
      return next(createValidationError('No Plex token found. Please log in again.'));
    }

    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, access_token FROM user_servers WHERE user_id = ?').get(userId);
    if (!serverRow) {
      return next(createValidationError('No Plex server configured. Please go to Settings and select a server.'));
    }
    const { server_url: serverUrl, library_id: libraryId } = serverRow;
    if (!serverUrl) {
      return next(createValidationError('No Plex server URL configured. Please go to Settings and select a server.'));
    }
    const plexToken = resolvePlexToken({ plex_token: accountToken }, serverRow);

    const settings = db.getUserSettings(userId);
    const externalTracks = tracks.map((t: any) => ({
      title: String(t.title || ''),
      artist: String(t.artist || ''),
      album: t.album ? String(t.album) : undefined,
    }));

    const rememberedMatches = buildRememberedMatchMap(db.getUserManualMatches(userId));
    const matched = await matchPlaylist(externalTracks, serverUrl, plexToken, libraryId, settings.matching_settings, undefined, undefined, undefined, undefined, rememberedMatches);

    res.json({ matched });
  } catch (error: any) {
    logger.error('Failed to match tracks', { error: error.message });
    next(createInternalError(error.message || 'Failed to match tracks'));
  }
});

/**
 * POST /api/import/confirm
 * Create playlist from matched tracks and optionally save missing tracks
 */
router.post('/confirm', async (req: Request, res: Response, next: NextFunction) => {
  try {
    const { playlistName: rawPlaylistName, source, sourceUrl, tracks, saveMissingTracks, missingTracks, overwriteExisting, keepExistingCover, coverUrl } = req.body;

    if (!rawPlaylistName || typeof rawPlaylistName !== 'string') {
      return next(createValidationError('playlistName is required and must be a string'));
    }

    // Normalize playlist name: collapse whitespace, trim
    const playlistName = rawPlaylistName.replace(/\s+/g, ' ').trim();

    if (!tracks || !Array.isArray(tracks)) {
      return next(createValidationError('tracks is required and must be an array'));
    }

    const userId = req.session.userId!;
    const db = req.dbService!;

    // Get user's Plex token and server info
    const userRow = (db as any).db.prepare('SELECT plex_token FROM users WHERE id = ?').get(userId);
    
    if (!userRow) {
      return next(createValidationError('User not found'));
    }

    const { plex_token: accountToken } = userRow;

    if (!accountToken) {
      return next(createValidationError('Plex token not configured. Please configure your Plex server in Settings.'));
    }

    // Get server URL and library ID
    const serverRow = (db as any).db.prepare('SELECT server_url, library_id, server_client_id, access_token FROM user_servers WHERE user_id = ?').get(userId);

    if (!serverRow) {
      return next(createValidationError('Plex server not configured. Please configure your Plex server in Settings.'));
    }

    const { server_url: serverUrl, library_id: libraryId, server_client_id: serverClientId } = serverRow;

    if (!serverUrl) {
      return next(createValidationError('Plex server URL not configured. Please configure your Plex server in Settings.'));
    }

    // Import PlexClient and create playlist
    const { PlexClient } = await import('../services/plex');
    const plexToken = resolvePlexToken({ plex_token: accountToken }, serverRow);
    const plexClient = new PlexClient(serverUrl, plexToken);
    
    // Handle overwrite: delete existing playlist with same name
    let existingCoverUrl: string | null = null;
    if (overwriteExisting) {
      try {
        const normalize = (s: string) => s.trim().replace(/\s+/g, ' ').toLowerCase();
        const normalizedTarget = normalize(playlistName);
        logger.info('[Overwrite] Starting overwrite flow', { playlistName, normalizedTarget });

        // Fetch playlists directly from Plex with audio filter
        const plexResponse = await plexClient.getPlaylists();
        logger.info('[Overwrite] Plex getPlaylists returned', { 
          count: plexResponse.length,
          titles: plexResponse.map((p: any) => p.title),
          types: plexResponse.map((p: any) => p.playlistType),
        });

        // Filter to audio playlists and find matches
        const audioPlaylists = plexResponse.filter((p: any) => p.playlistType === 'audio');
        const matchingPlaylists = audioPlaylists.filter((p: any) => {
          const normalizedTitle = normalize(p.title || '');
          const isMatch = normalizedTitle === normalizedTarget;
          if (isMatch) {
            logger.info('[Overwrite] Found matching playlist', { title: p.title, ratingKey: p.ratingKey });
          }
          return isMatch;
        });

        logger.info('[Overwrite] Match results', { 
          audioCount: audioPlaylists.length,
          matchCount: matchingPlaylists.length,
          audioTitles: audioPlaylists.map((p: any) => p.title),
        });

        if (matchingPlaylists.length > 0) {
          // Save existing cover URL from the first match if user wants to keep it
          const firstMatch = matchingPlaylists[0];
          if (keepExistingCover && firstMatch.composite) {
            existingCoverUrl = `${serverUrl}${firstMatch.composite}?X-Plex-Token=${plexToken}`;
            logger.info('[Overwrite] Saving existing cover', { playlistName });
          }

          // Delete ALL matching playlists (handles duplicates too)
          for (const existing of matchingPlaylists) {
            logger.info('[Overwrite] Deleting Plex playlist', { title: existing.title, ratingKey: existing.ratingKey });
            await plexClient.deletePlaylist(existing.ratingKey);
            logger.info('[Overwrite] Deleted Plex playlist', { title: existing.title, ratingKey: existing.ratingKey });
          }
        } else {
          logger.info('[Overwrite] No matching Plex playlist found', { 
            playlistName,
            normalizedTarget,
            audioTitles: audioPlaylists.map((p: any) => `"${p.title}" -> "${normalize(p.title || '')}"`),
          });
        }

        // Also delete from DB
        const userPlaylists = db.getUserPlaylists(userId);
        const matchingDbPlaylists = userPlaylists.filter((p: any) => 
          normalize(p.name || '') === normalizedTarget
        );
        for (const existingDb of matchingDbPlaylists) {
          db.deletePlaylist(existingDb.id);
          logger.info('[Overwrite] Deleted DB playlist', { playlistName, dbId: existingDb.id });
        }
      } catch (overwriteErr: any) {
        logger.error('[Overwrite] Failed', { error: overwriteErr.message, stack: overwriteErr.stack });
        return next(createInternalError(`Failed to overwrite existing playlist: ${overwriteErr.message}`));
      }
    }

    // Remember any tracks the user manually rematched (Rematch search, not
    // the automatic matcher) so future matching for the same track reuses
    // the choice instead of re-running the search.
    for (const t of tracks) {
      if (t.manuallyMatched && t.matched && t.plexRatingKey) {
        db.recordManualMatch(userId, { title: t.title, artist: t.artist, album: t.album }, t.plexRatingKey);
      }
    }

    // Create playlist in Plex
    const trackUris = dedupeByPlexRatingKey(tracks.filter((t: any) => t.matched && t.plexRatingKey))
      .map((t: any) => `server://${serverClientId}/com.plexapp.plugins.library/library/metadata/${t.plexRatingKey}`);

    logger.info('Confirm import - track URI details', {
      totalTracks: tracks.length,
      matchedWithKey: tracks.filter((t: any) => t.matched && t.plexRatingKey).length,
      matchedOnly: tracks.filter((t: any) => t.matched).length,
      withKeyOnly: tracks.filter((t: any) => t.plexRatingKey).length,
      uriCount: trackUris.length,
      serverClientId,
      libraryId,
      sampleTrack: tracks[0] ? { matched: tracks[0].matched, plexRatingKey: tracks[0].plexRatingKey, title: tracks[0].title } : null,
      sampleUri: trackUris[0],
    });
    
    if (trackUris.length === 0) {
      return next(createValidationError('No matched tracks to add to playlist'));
    }

    const libraryUri = `server://${serverClientId}/com.plexapp.plugins.library/library/sections/${libraryId}`;
    const plexPlaylist = await plexClient.createPlaylist(playlistName, libraryUri, trackUris);
    
    // Upload cover art: use new cover, or re-upload existing cover if kept
    const finalCoverUrl = (coverUrl && typeof coverUrl === 'string') ? coverUrl : existingCoverUrl;
    if (finalCoverUrl) {
      await plexClient.uploadPlaylistPoster(plexPlaylist.ratingKey, finalCoverUrl);
    }
    
    // Save playlist to database
    const playlist = db.createPlaylist(
      userId,
      plexPlaylist.ratingKey,
      playlistName,
      source || 'manual',
      sourceUrl || ''
    );

    logger.info('Playlist created from import', { 
      userId, 
      playlistId: playlist.id, 
      playlistName,
      trackCount: trackUris.length,
      hasMissingTracks: saveMissingTracks && missingTracks?.length > 0
    });

    // Save missing tracks if requested
    if (saveMissingTracks && missingTracks && Array.isArray(missingTracks) && missingTracks.length > 0) {
      const missingTracksInput = missingTracks.map((track: any, index: number) => ({
        title: track.title || '',
        artist: track.artist || '',
        album: track.album || '',
        position: index,
        source: source || 'manual',
      }));

      db.addMissingTracks(userId, playlist.id, missingTracksInput);

      logger.info('Missing tracks saved', { 
        userId, 
        playlistId: playlist.id, 
        missingCount: missingTracks.length 
      });
    }

    // Return playlist info
    res.json(playlist);
  } catch (error: any) {
    logger.error('Failed to confirm import', { error: error.message });
    next(createInternalError(error.message || 'Failed to create playlist'));
  }
});

/**
 * GET /api/import/spotify/user/:userId/playlists
 * Fetch public playlists from a Spotify user (unauthenticated)
 */
router.get('/spotify/user/:userId/playlists', async (req: Request, res: Response, next: NextFunction): Promise<void> => {
  try {
    const { userId: spotifyUserId } = req.params as Record<string, string>;
    const userId = req.session.userId!;
    const db = (req.dbService as any)?.db || (req as any).db;

    if (!spotifyUserId) {
      res.status(400).json({
        error: { message: 'Spotify user ID is required' }
      });
      return;
    }

    logger.info('[Spotify User Playlists] Fetching playlists', { spotifyUserId, userId });

    // Prevent browser caching so playlist list changes are always reflected
    res.set('Cache-Control', 'no-store, no-cache, must-revalidate, proxy-revalidate');
    res.set('Pragma', 'no-cache');

    // Import the adapter
    const { adapterRegistry, invalidateSpotifyUserPlaylistsCache } = await import('../adapters');

    // ?refresh=1 forces a fresh scrape. Without it the server-side result is
    // reused for a few minutes, because assembling it means scrolling a
    // headless browser and takes several seconds every time.
    if (req.query.refresh === '1') {
      invalidateSpotifyUserPlaylistsCache(spotifyUserId);
    }
    const adapter = adapterRegistry.getSource('spotify');

    if (!adapter || !adapter.searchPlaylists) {
      logger.error('[Spotify User Playlists] Spotify adapter not available or missing searchPlaylists');
      res.status(500).json({
        error: { message: 'Spotify adapter not available' }
      });
      return;
    }

    // Use searchPlaylists with the user ID - it will use unauthenticated method
    const result = await adapter.searchPlaylists(spotifyUserId, userId, db);
    
    // Check if result includes displayName (for user ID queries) or is just playlists array
    let displayName: string;
    let playlists: any[];
    
    if (Array.isArray(result)) {
      // Old format: just playlists array
      displayName = spotifyUserId;
      playlists = result;
    } else {
      // New format: { displayName, playlists }
      displayName = result.displayName;
      playlists = result.playlists;
    }

    // Update the display name in the database if this user is saved
    // But only if the display name looks valid (not a language dialog or generic title)
    const invalidNames = ['choose a language', 'spotify', 'spotify - web player', 'spotify – web player', 'web player', ''];
    const isValidDisplayName = displayName && 
      !invalidNames.includes(displayName.toLowerCase().trim()) &&
      !/choose|language|sprache|langue|idioma/i.test(displayName);
    
    if (isValidDisplayName) {
      try {
        db.prepare(
          `UPDATE saved_spotify_users 
           SET display_name = ? 
           WHERE user_id = ? AND spotify_user_id = ?`
        ).run(displayName, userId, spotifyUserId);
      } catch (updateError) {
        logger.warn('[Spotify User Playlists] Could not update display name', { 
          spotifyUserId, 
          error: updateError 
        });
      }
    } else {
      // Invalid display name from scraper — try to get the saved name from DB
      logger.info('[Spotify User Playlists] Invalid scraper display name, falling back to DB', { 
        spotifyUserId, 
        scraperName: displayName 
      });
      try {
        const savedUser = db.prepare(
          `SELECT display_name FROM saved_spotify_users WHERE user_id = ? AND spotify_user_id = ?`
        ).get(userId, spotifyUserId) as { display_name: string } | undefined;
        if (savedUser && !invalidNames.includes(savedUser.display_name.toLowerCase().trim()) && !/choose|language|sprache|langue|idioma/i.test(savedUser.display_name)) {
          displayName = savedUser.display_name;
        } else {
          displayName = spotifyUserId;
        }
      } catch {
        displayName = spotifyUserId;
      }
    }

    logger.info('[Spotify User Playlists] Found playlists', { 
      spotifyUserId,
      displayName,
      count: playlists.length 
    });

    res.json({ displayName, playlists });
  } catch (error: any) {
    logger.error('[Spotify User Playlists] Error', { error: error.message, stack: error.stack });
    next(createInternalError(error.message || 'Failed to fetch Spotify user playlists'));
  }
});

/**
 * GET /api/import/spotify/playlist/:playlistId/tracks
 * Fetch tracks from a Spotify playlist for preview
 */
router.get('/spotify/playlist/:playlistId/tracks', async (req: Request, res: Response, next: NextFunction): Promise<void> => {
  try {
    const { playlistId } = req.params as Record<string, string>;
    const userId = req.session.userId!;
    const db = (req.dbService as any).db; // Get the actual database instance

    logger.info('[Spotify Playlist Tracks] Fetching tracks', { playlistId, userId });

    // Prevent browser caching so playlist changes are always reflected
    res.set('Cache-Control', 'no-store, no-cache, must-revalidate, proxy-revalidate');
    res.set('Pragma', 'no-cache');

    // Import the adapter
    const { adapterRegistry } = await import('../adapters');
    const adapter = adapterRegistry.getSource('spotify');

    if (!adapter || !adapter.fetchTracks) {
      logger.error('[Spotify Playlist Tracks] Spotify adapter not available or missing fetchTracks');
      res.status(500).json({
        error: { message: 'Spotify adapter not available' }
      });
      return;
    }

    // Fetch tracks from the playlist
    const { tracks } = await adapter.fetchTracks(playlistId, userId, db);

    logger.info('[Spotify Playlist Tracks] Found tracks', { 
      playlistId, 
      count: tracks.length 
    });

    res.json({ tracks });
  } catch (error: any) {
    logger.error('[Spotify Playlist Tracks] Error', { error: error.message, stack: error.stack });
    next(createInternalError(error.message || 'Failed to fetch Spotify playlist tracks'));
  }
});

export default router;
