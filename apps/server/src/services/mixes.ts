/**
 * Mix Generation Service
 * 
 * Generates personalized playlists based on user's listening history:
 * - Weekly Mix: Top tracks from most-played artists
 * - Daily Mix: Recent plays + related tracks + rediscoveries
 * - Time Capsule: Old tracks with artist diversity
 * - New Music Mix: Tracks from recently added albums
 */

import { PlexClient, PlexTrack } from './plex';
import { LastFmService } from './lastfm';
import { logger } from '../utils/logger';
import { mapBatched, PLEX_LOOKUP_BATCH_SIZE } from '../utils/batch';

export interface MixSettings {
  weeklyMix: {
    topArtists: number;
    tracksPerArtist: number;
  };
  dailyMix: {
    recentTracks: number;
    relatedTracks: number;
    rediscoveryTracks: number;
    rediscoveryDays: number;
  };
  timeCapsule: {
    trackCount: number;
    daysAgo: number;
    maxPerArtist: number;
  };
  newMusic: {
    albumCount: number;
    tracksPerAlbum: number;
  };
}

export interface MixResult {
  trackKeys: string[];
  trackCount: number;
}

export class MixService {
  /**
   * Shuffle array in place using Fisher-Yates algorithm
   */
  private shuffle<T>(array: T[]): T[] {
    const result = [...array];
    for (let i = result.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [result[i], result[j]] = [result[j], result[i]];
    }
    return result;
  }

  /**
   * Create a PlexClient instance for the given server and token
   */
  private createPlexClient(serverUrl: string, plexToken: string): PlexClient {
    return new PlexClient(serverUrl, plexToken);
  }

  /**
   * Generate Weekly Mix: Top tracks from user's most-played artists
   */
  async generateWeeklyMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: MixSettings['weeklyMix']
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get recently played tracks (last 7 days, fallback to 30 days)
    let recentTracks = await plex.getRecentTracks(libraryId, 7);
    
    if (recentTracks.length < 20) {
      recentTracks = await plex.getRecentTracks(libraryId, 30);
    }

    // Count plays per artist
    const artistCounts = new Map<string, number>();
    for (const track of recentTracks) {
      const artist = track.grandparentTitle || 'Unknown';
      // Skip generic artists
      if (artist === 'Various Artists' || artist === 'Unknown' || artist === 'Soundtrack') {
        continue;
      }
      artistCounts.set(artist, (artistCounts.get(artist) || 0) + 1);
    }

    // Get top N artists by play count
    const topArtists = Array.from(artistCounts.entries())
      .sort((a, b) => b[1] - a[1])
      .slice(0, settings.topArtists)
      .map(([name]) => name);

    if (topArtists.length === 0) {
      return { trackKeys: [], trackCount: 0 };
    }

    // Find these artists in the library and get their popular tracks. Each
    // artist's lookup is independent, so run them batched rather than one
    // at a time; merge in original (most-played-first) order afterward so
    // the result is identical to the old sequential version, just faster.
    const allTracks: string[] = [];
    const addedKeys = new Set<string>();

    const perArtistTracks = await mapBatched(topArtists, PLEX_LOOKUP_BATCH_SIZE, async (artistName) => {
      const artist = await plex.searchArtist(libraryId, artistName);
      if (!artist) return [];
      return plex.getArtistPopularTracks(libraryId, artist.ratingKey, settings.tracksPerArtist);
    });

    for (const tracks of perArtistTracks) {
      for (const track of tracks) {
        if (!addedKeys.has(track.ratingKey)) {
          allTracks.push(track.ratingKey);
          addedKeys.add(track.ratingKey);
        }
      }
    }

    return {
      trackKeys: this.shuffle(allTracks),
      trackCount: allTracks.length
    };
  }

  /**
   * Generate Daily Mix: Recent plays + related tracks + rediscoveries
   */
  async generateDailyMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: MixSettings['dailyMix']
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);
    const addedKeys = new Set<string>();
    const mixTracks: string[] = [];

    // 1. Get recent listened tracks
    const recentTracks = await plex.getRecentTracks(libraryId, 7);
    const seedTracks = recentTracks.slice(0, settings.recentTracks);

    // Add seed tracks
    for (const track of seedTracks) {
      if (!addedKeys.has(track.ratingKey)) {
        mixTracks.push(track.ratingKey);
        addedKeys.add(track.ratingKey);
      }
    }

    // 2. For each seed track, get related tracks (similar/same artist).
    // Each seed's lookup is independent, so batch them rather than going
    // one at a time - but still check the target-reached exit between
    // batches (instead of only per-item) so a small recentTracks/
    // relatedTracks setting doesn't pointlessly look up every seed.
    const relatedPerSeed = Math.ceil(settings.relatedTracks / Math.max(seedTracks.length, 1));
    const dailyMixTarget = settings.recentTracks + settings.relatedTracks;
    for (let i = 0; i < seedTracks.length && mixTracks.length < dailyMixTarget; i += PLEX_LOOKUP_BATCH_SIZE) {
      const batch = seedTracks.slice(i, i + PLEX_LOOKUP_BATCH_SIZE);
      const batchRelated = await Promise.all(batch.map(seed => plex.getSimilarTracks(seed.ratingKey, relatedPerSeed)));

      for (const related of batchRelated) {
        for (const track of related) {
          if (!addedKeys.has(track.ratingKey)) {
            mixTracks.push(track.ratingKey);
            addedKeys.add(track.ratingKey);
          }
        }
      }
    }

    // 3. Get tracks not played in X days (rediscoveries)
    const staleTracks = await plex.getStalePlayedTracks(
      libraryId,
      settings.rediscoveryDays,
      settings.rediscoveryTracks
    );

    for (const track of staleTracks) {
      if (!addedKeys.has(track.ratingKey)) {
        mixTracks.push(track.ratingKey);
        addedKeys.add(track.ratingKey);
      }
    }

    return {
      trackKeys: this.shuffle(mixTracks),
      trackCount: mixTracks.length
    };
  }

  /**
   * Generate Time Capsule: Old tracks with artist diversity
   */
  async generateTimeCapsule(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: MixSettings['timeCapsule']
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Fetch a larger pool of tracks not played in X days, including tracks that
    // were never played at all - a forgotten, untouched track is exactly what
    // this mix is about, not something to exclude.
    const poolSize = settings.trackCount * 10;
    const allTracks = await plex.getStalePlayedTracks(
      libraryId,
      settings.daysAgo,
      poolSize,
      true
    );

    if (allTracks.length === 0) {
      return { trackKeys: [], trackCount: 0 };
    }

    // Group tracks by artist
    const tracksByArtist = new Map<string, any[]>();
    for (const track of allTracks) {
      const artist = track.grandparentTitle || 'Unknown';
      if (!tracksByArtist.has(artist)) {
        tracksByArtist.set(artist, []);
      }
      tracksByArtist.get(artist)!.push(track);
    }

    // Select tracks with artist diversity using round-robin
    const selectedTracks: any[] = [];
    const artistQueues = Array.from(tracksByArtist.entries()).map(([artist, tracks]) => ({
      artist,
      tracks: [...tracks],
      selected: 0
    }));

    // Shuffle artist order for variety
    for (let i = artistQueues.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [artistQueues[i], artistQueues[j]] = [artistQueues[j], artistQueues[i]];
    }

    // Round-robin selection: take one track from each artist in rotation
    let hasMore = true;
    while (selectedTracks.length < settings.trackCount && hasMore) {
      hasMore = false;
      for (const queue of artistQueues) {
        if (selectedTracks.length >= settings.trackCount) break;
        if (queue.selected >= settings.maxPerArtist) continue;
        if (queue.tracks.length === 0) continue;

        // Pick a random track from this artist's remaining tracks
        const randomIndex = Math.floor(Math.random() * queue.tracks.length);
        const track = queue.tracks.splice(randomIndex, 1)[0];
        selectedTracks.push(track);
        queue.selected++;
        hasMore = true;
      }
    }

    return {
      trackKeys: this.shuffle(selectedTracks.map(t => t.ratingKey)),
      trackCount: selectedTracks.length
    };
  }

  /**
   * Generate New Music Mix: Tracks from recently added albums
   */
  async generateNewMusicMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: MixSettings['newMusic']
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get recently added albums
    const albums = await plex.getRecentlyAddedAlbums(libraryId, settings.albumCount);

    const trackKeys: string[] = [];
    const addedKeys = new Set<string>();

    // Get tracks from each album
    for (const album of albums) {
      const tracks = await plex.getAlbumTracks(album.ratingKey);
      
      // Shuffle and take N tracks per album
      const shuffledTracks = this.shuffle(tracks).slice(0, settings.tracksPerAlbum);
      
      for (const track of shuffledTracks) {
        if (!addedKeys.has(track.ratingKey)) {
          trackKeys.push(track.ratingKey);
          addedKeys.add(track.ratingKey);
        }
      }
    }

    return {
      trackKeys: this.shuffle(trackKeys),
      trackCount: trackKeys.length
    };
  }

  /**
   * Generate custom mix with advanced filters
   */
  async generateCustomMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      trackCount: number;
      
      // Time filters
      playedInLastDays?: number;
      notPlayedInLastDays?: number;
      addedInLastDays?: number;
      
      // Release date filters
      releasedAfterYear?: number;
      releasedBeforeYear?: number;
      // Disjoint year ranges (e.g. multiple selected decades). When present, a track must
      // fall within at least one of these ranges to be included. This is applied as a
      // post-fetch filter after tracks are retrieved via whichever strategy is used below,
      // so it works regardless of how the initial candidate pool was assembled.
      yearRanges?: Array<{ min?: number; max?: number }>;

      // Rating & popularity
      minRating?: number;
      maxRating?: number;
      minPlayCount?: number;
      maxPlayCount?: number;
      popularTracksOnly?: boolean; // Only include tracks from Plex's "Popular Tracks" section
      popularTracksPerArtist?: number; // Number of popular tracks to fetch per artist
      popularArtistsOnly?: boolean; // Only include tracks from popular/well-known artists
      maxPopularArtists?: number; // Maximum number of popular artists to include (default: 20)
      
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
      
      // Sonic Analysis filters
      sonicSeedTrackKey?: string;
      sonicSeedArtistKey?: string;
      sonicMaxDistance?: number;
      sonicIncludeSameArtist?: boolean;
      sonicIncludeSimilarArtists?: boolean;
      sonicUsePopularTracks?: boolean;
      
      // Sorting
      sortBy: 'random' | 'playCount' | 'lastPlayed' | 'dateAdded' | 'releaseDate' | 'rating' | 'duration' | 'title';
      sortDirection: 'asc' | 'desc';
    },
    progressEmitter?: any
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    let tracks: PlexTrack[] = [];

    // Auto-enable popular tracks optimization if year filter is present
    // This prevents timeouts on large libraries when filtering by year
    const hasYearFilter = settings.releasedAfterYear || settings.releasedBeforeYear ||
      (settings.yearRanges && settings.yearRanges.length > 0);
    const hasArtistFilter = settings.artistNames && settings.artistNames.length > 0;
    const shouldUsePopularTracksOptimization = settings.popularTracksOnly || (hasYearFilter && !settings.popularTracksOnly);
    
    if (shouldUsePopularTracksOptimization) {
      // Log if we're auto-enabling the optimization
      if (hasYearFilter && !settings.popularTracksOnly) {
        console.log('[Auto-Optimization] Year filter detected, using Last.fm + popular tracks approach to avoid timeout');
      }
    }

    // If user specified specific artists, use those instead of Last.fm popular artists
    // Artist filtering should always take precedence
    if (hasArtistFilter && shouldUsePopularTracksOptimization) {
      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'fetching_artists',
        message: `Fetching tracks from ${settings.artistNames!.length} specified artists...`,
        progress: 10
      });
      
      const allPopularTracks: PlexTrack[] = [];
      const popularTracksPerArtist = settings.popularTracksPerArtist || Math.max(3, Math.ceil(settings.trackCount / settings.artistNames!.length));

      // Each artist's lookup is independent - batch them instead of going
      // one at a time (with up to a few dozen artists this used to mean
      // dozens of sequential Plex round trips on an interactive request).
      // Progress is emitted per batch rather than per artist.
      const artistNames = settings.artistNames!;
      let processed = 0;
      for (let i = 0; i < artistNames.length; i += PLEX_LOOKUP_BATCH_SIZE) {
        const batch = artistNames.slice(i, i + PLEX_LOOKUP_BATCH_SIZE);
        const batchTracks = await Promise.all(batch.map(async (artistName) => {
          try {
            const plexArtist = await plex.searchArtist(libraryId, artistName);
            if (!plexArtist) {
              logger.warn('[Artist Filter] Artist not found in library', { artistName });
              return [];
            }
            return await plex.getArtistPopularTracks(libraryId, plexArtist.ratingKey, popularTracksPerArtist);
          } catch (error) {
            logger.warn('[Artist Filter] Failed to fetch tracks for artist', { artistName, error });
            return [];
          }
        }));
        for (const popularTracks of batchTracks) allPopularTracks.push(...popularTracks);

        processed += batch.length;
        progressEmitter?.emit('progress', {
          type: 'progress',
          stage: 'fetching_artists',
          message: `Processed ${processed}/${artistNames.length} artists...`,
          progress: 10 + (processed / artistNames.length) * 70
        });
      }

      tracks = allPopularTracks;
      logger.info('[Artist Filter] Fetched tracks from specified artists', {
        artistCount: settings.artistNames!.length,
        trackCount: tracks.length
      });
    }
    // If popularTracksOnly is requested OR year filter is present, use Last.fm to get popular artists
    // This is much faster than querying Plex directly
    else if (shouldUsePopularTracksOptimization) {
      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'fetching_artists',
        message: 'Fetching popular artists from Last.fm...',
        progress: 10
      });
      
      // Use Last.fm to get popular artists (much faster than Plex)
      const lastfm = new LastFmService();
      const maxArtists = settings.maxPopularArtists || 20;
      
      logger.info('[Last.fm] Fetching top artists', { maxArtists });
      const lastfmArtists = await lastfm.getTopArtists(maxArtists * 2); // Get more to account for missing matches
      
      if (lastfmArtists.length === 0) {
        logger.warn('[Last.fm] No artists returned from Last.fm API');
        throw new Error('Failed to fetch popular artists from Last.fm. Please try again.');
      }
      
      logger.info('[Last.fm] Found artists', { count: lastfmArtists.length });
      
      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'matching_artists',
        message: `Matching ${lastfmArtists.length} popular artists in your library...`,
        progress: 20
      });
      
      // Match Last.fm artists to Plex library. Each lookup is independent,
      // so batch them - but still check the maxArtists exit between
      // batches (not just per-item) so a small maxArtists setting doesn't
      // pointlessly look up every Last.fm artist returned.
      const matchedArtistKeys = new Set<string>();
      let matchedCount = 0;

      for (let i = 0; i < lastfmArtists.length && matchedArtistKeys.size < maxArtists; i += PLEX_LOOKUP_BATCH_SIZE) {
        const batch = lastfmArtists.slice(i, i + PLEX_LOOKUP_BATCH_SIZE);
        const batchArtists = await Promise.all(batch.map(async (lastfmArtist) => {
          try {
            return await plex.searchArtist(libraryId, lastfmArtist.name);
          } catch (error) {
            return null;
          }
        }));

        for (const plexArtist of batchArtists) {
          if (plexArtist) {
            matchedArtistKeys.add(plexArtist.ratingKey);
            matchedCount++;
          }
        }
        progressEmitter?.emit('progress', {
          type: 'progress',
          stage: 'matching_artists',
          message: `Matched ${matchedCount}/${lastfmArtists.length} artists...`,
          progress: 20 + (matchedCount / lastfmArtists.length) * 20
        });
      }
      
      logger.info('[Last.fm] Matched artists in library', { 
        requested: maxArtists,
        matched: matchedArtistKeys.size,
        total: lastfmArtists.length 
      });
      
      if (matchedArtistKeys.size === 0) {
        throw new Error('None of the popular artists from Last.fm were found in your library. Try expanding your library or adjusting filters.');
      }

      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'fetching_popular',
        message: `Fetching popular tracks from ${matchedArtistKeys.size} artists...`,
        progress: 40
      });

      // Get popular tracks from each matched artist - independent lookups, batched.
      const popularTracksPerArtist = settings.popularTracksPerArtist || Math.max(3, Math.ceil(settings.trackCount / matchedArtistKeys.size));
      const allPopularTracks: PlexTrack[] = [];

      const artistKeys = Array.from(matchedArtistKeys);
      let processed = 0;
      for (let i = 0; i < artistKeys.length; i += PLEX_LOOKUP_BATCH_SIZE) {
        const batch = artistKeys.slice(i, i + PLEX_LOOKUP_BATCH_SIZE);
        const batchTracks = await Promise.all(batch.map(async (artistKey) => {
          try {
            return await plex.getArtistPopularTracks(libraryId, artistKey, popularTracksPerArtist);
          } catch (error) {
            return [];
          }
        }));
        for (const popularTracks of batchTracks) allPopularTracks.push(...popularTracks);

        processed += batch.length;
        progressEmitter?.emit('progress', {
          type: 'progress',
          stage: 'fetching_popular',
          message: `Processed ${processed}/${matchedArtistKeys.size} artists...`,
          progress: 40 + (processed / matchedArtistKeys.size) * 40
        });
      }

      tracks = allPopularTracks;
      
      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'applying_filters',
        message: 'Applying additional filters...',
        progress: 80
      });
      
      // Apply additional filters to the popular tracks
      // We can't use getTracksWithAdvancedFilters here because it would query the entire library
      // which is what we're trying to avoid with the popular tracks optimization
      
      const beforeFilters = tracks.length;
      
      // Apply filters that aren't already handled
      if (settings.releasedAfterYear || settings.releasedBeforeYear ||
          settings.playedInLastDays || settings.notPlayedInLastDays || settings.addedInLastDays ||
          settings.minDuration || settings.maxDuration || settings.minTrackNumber || settings.maxTrackNumber ||
          settings.discNumber || settings.minRating || settings.maxRating || settings.minPlayCount || 
          settings.maxPlayCount || settings.genres || settings.excludeGenres || settings.moods || 
          settings.excludeMoods || settings.styles || settings.excludeStyles || settings.collections || 
          settings.labels || settings.minBitrate || settings.audioCodec || settings.minSampleRate || 
          settings.losslessOnly) {
        
        const now = Math.floor(Date.now() / 1000);
        
        tracks = tracks.filter(track => {
          // Year filters - check both track year and album year (parentYear)
          const trackYear = track.year || (track as any).parentYear;
          if (settings.releasedAfterYear && (!trackYear || trackYear < settings.releasedAfterYear)) return false;
          if (settings.releasedBeforeYear && (!trackYear || trackYear > settings.releasedBeforeYear)) return false;
          
          // Time filters
          if (settings.playedInLastDays) {
            const daysAgo = now - (settings.playedInLastDays * 24 * 60 * 60);
            if (!track.lastViewedAt || track.lastViewedAt < daysAgo) return false;
          }
          
          if (settings.notPlayedInLastDays) {
            const daysAgo = now - (settings.notPlayedInLastDays * 24 * 60 * 60);
            if (track.lastViewedAt && track.lastViewedAt >= daysAgo) return false;
          }
          
          if (settings.addedInLastDays) {
            const daysAgo = now - (settings.addedInLastDays * 24 * 60 * 60);
            if (!track.addedAt || track.addedAt < daysAgo) return false;
          }
          
          // Duration filters
          if (settings.minDuration && track.duration < settings.minDuration * 1000) return false;
          if (settings.maxDuration && track.duration > settings.maxDuration * 1000) return false;
          
          // Track number filters
          if (settings.minTrackNumber && track.index < settings.minTrackNumber) return false;
          if (settings.maxTrackNumber && track.index > settings.maxTrackNumber) return false;
          if (settings.discNumber && track.parentIndex !== settings.discNumber) return false;
          
          // Rating filters
          if (settings.minRating && (!track.userRating || track.userRating < settings.minRating)) return false;
          if (settings.maxRating && track.userRating && track.userRating > settings.maxRating) return false;
          
          // Play count filters
          if (settings.minPlayCount && (!track.viewCount || track.viewCount < settings.minPlayCount)) return false;
          if (settings.maxPlayCount && track.viewCount && track.viewCount > settings.maxPlayCount) return false;
          
          // Genre filters
          if (settings.genres && settings.genres.length > 0) {
            const trackGenres = track.Genre?.map((g: any) => g.tag.toLowerCase()) || [];
            if (!settings.genres.some(g => trackGenres.includes(g.toLowerCase()))) return false;
          }
          
          if (settings.excludeGenres && settings.excludeGenres.length > 0) {
            const trackGenres = track.Genre?.map((g: any) => g.tag.toLowerCase()) || [];
            if (settings.excludeGenres.some(g => trackGenres.includes(g.toLowerCase()))) return false;
          }
          
          // Mood filters
          if (settings.moods && settings.moods.length > 0) {
            const trackMoods = (track as any).Mood?.map((m: any) => m.tag.toLowerCase()) || [];
            if (!settings.moods.some(m => trackMoods.includes(m.toLowerCase()))) return false;
          }
          
          if (settings.excludeMoods && settings.excludeMoods.length > 0) {
            const trackMoods = (track as any).Mood?.map((m: any) => m.tag.toLowerCase()) || [];
            if (settings.excludeMoods.some(m => trackMoods.includes(m.toLowerCase()))) return false;
          }
          
          // Style filters
          if (settings.styles && settings.styles.length > 0) {
            const trackStyles = (track as any).Style?.map((s: any) => s.tag.toLowerCase()) || [];
            if (!settings.styles.some(s => trackStyles.includes(s.toLowerCase()))) return false;
          }
          
          if (settings.excludeStyles && settings.excludeStyles.length > 0) {
            const trackStyles = (track as any).Style?.map((s: any) => s.tag.toLowerCase()) || [];
            if (settings.excludeStyles.some(s => trackStyles.includes(s.toLowerCase()))) return false;
          }
          
          // Collection filters
          if (settings.collections && settings.collections.length > 0) {
            const trackCollections = (track as any).Collection?.map((c: any) => c.tag.toLowerCase()) || [];
            if (!settings.collections.some(c => trackCollections.includes(c.toLowerCase()))) return false;
          }
          
          // Label filters
          if (settings.labels && settings.labels.length > 0) {
            const trackLabels = (track as any).Label?.map((l: any) => l.tag.toLowerCase()) || [];
            if (!settings.labels.some(l => trackLabels.includes(l.toLowerCase()))) return false;
          }
          
          // Quality filters
          if (track.Media && track.Media.length > 0) {
            const media = track.Media[0];
            
            if (settings.minBitrate && (!media.bitrate || media.bitrate < settings.minBitrate)) return false;
            
            if (settings.audioCodec && settings.audioCodec.length > 0) {
              const codec = media.audioCodec?.toLowerCase() || '';
              if (!settings.audioCodec.some(c => codec.includes(c.toLowerCase()))) return false;
            }
            
            if (settings.minSampleRate && (!media.audioSampleRate || media.audioSampleRate < settings.minSampleRate)) return false;
            
            if (settings.losslessOnly) {
              const codec = media.audioCodec?.toLowerCase() || '';
              if (!['flac', 'alac', 'ape', 'wav'].some(lc => codec.includes(lc))) return false;
            }
          } else if (settings.minBitrate || settings.audioCodec || settings.minSampleRate || settings.losslessOnly) {
            return false;
          }
          
          return true;
        });
        
        logger.info('[Filter Application] Applied filters to popular tracks', {
          before: beforeFilters,
          after: tracks.length
        });
      }
    }
    // If sonic analysis is requested, use that as the primary source
    else if (settings.sonicSeedTrackKey || settings.sonicSeedArtistKey) {
      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'sonic_analysis',
        message: 'Analyzing sonic similarity...',
        progress: 20
      });
      
      const seedKey = settings.sonicSeedTrackKey || settings.sonicSeedArtistKey!;
      
      // If popular tracks are requested and we have an artist seed
      if (settings.sonicUsePopularTracks && settings.sonicSeedArtistKey) {
        const popularTracks = await plex.getArtistPopularTracks(
          libraryId, 
          settings.sonicSeedArtistKey, 
          settings.trackCount
        );
        tracks = popularTracks;
      } else {
        // Get sonically similar tracks
        const sonicTracks = await plex.getSonicallySimilarTracks(
          seedKey,
          libraryId,
          {
            limit: settings.trackCount * 3, // Get more for filtering
            maxDistance: settings.sonicMaxDistance || 0.25,
          }
        );

        tracks = sonicTracks;
      }

      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'expanding_results',
        message: 'Expanding results with similar artists...',
        progress: 50
      });

      // If seed is a track and we want same artist tracks
      if (settings.sonicSeedTrackKey && settings.sonicIncludeSameArtist) {
        const seedTrack = await plex.getTrackDetails(settings.sonicSeedTrackKey);
        if (seedTrack?.grandparentRatingKey) {
          const artistTracks = await plex.getArtistPopularTracks(libraryId, seedTrack.grandparentRatingKey, 20);
          tracks = [...tracks, ...artistTracks];
        }
      }

      // If we want similar artists' tracks
      if (settings.sonicIncludeSimilarArtists) {
        const seedArtistKey = settings.sonicSeedArtistKey || 
          (settings.sonicSeedTrackKey ? (await plex.getTrackDetails(settings.sonicSeedTrackKey))?.grandparentRatingKey : undefined);
        
        if (seedArtistKey) {
          // Get similar tracks which often include tracks from similar artists
          const similarTracks = await plex.getSimilarTracks(seedArtistKey, 50);
          tracks = [...tracks, ...similarTracks];
        }
      }

      // Remove duplicates
      const uniqueTracks = new Map<string, PlexTrack>();
      tracks.forEach(track => uniqueTracks.set(track.ratingKey, track));
      tracks = Array.from(uniqueTracks.values());
    } else {
      progressEmitter?.emit('progress', {
        type: 'progress',
        stage: 'fetching_tracks',
        message: 'Fetching tracks from library...',
        progress: 30
      });
      
      // Use the standard advanced filtering method
      tracks = await plex.getTracksWithAdvancedFilters(libraryId, {
        ...settings,
        limit: settings.trackCount * 2 // Get more tracks than needed for better variety
      });
    }

    // Apply disjoint year range filtering (e.g. multiple selected decades). This runs
    // regardless of which strategy above populated `tracks`, so it correctly excludes
    // years outside the actually-selected ranges even when the initial fetch used a
    // broader single min/max range (e.g. releasedAfterYear/releasedBeforeYear) to narrow
    // the candidate pool for performance.
    if (settings.yearRanges && settings.yearRanges.length > 0) {
      const beforeYearRangeFilter = tracks.length;
      tracks = tracks.filter(track => {
        const trackYear = track.year || track.parentYear;
        if (!trackYear) return false;
        return settings.yearRanges!.some(range => {
          const minOk = range.min === undefined || trackYear >= range.min;
          const maxOk = range.max === undefined || trackYear <= range.max;
          return minOk && maxOk;
        });
      });
      logger.info('[Year Range Filter] Applied disjoint year ranges', {
        before: beforeYearRangeFilter,
        after: tracks.length,
        ranges: settings.yearRanges
      });
    }

    progressEmitter?.emit('progress', {
      type: 'progress',
      stage: 'filtering',
      message: 'Applying filters...',
      progress: 70
    });

    // Apply additional filters if specified (even for sonic results)
    // Note: If popularArtistsOnly was already handled above with popularTracksOnly, skip it here
    const needsPopularArtistFilter = settings.popularArtistsOnly && !settings.popularTracksOnly;
    
    if (settings.genres || settings.excludeGenres || settings.minRating || settings.minPlayCount || needsPopularArtistFilter) {
      
      // If popularArtistsOnly is enabled (and popularTracksOnly was not), filter by popular artists
      if (needsPopularArtistFilter) {
        progressEmitter?.emit('progress', {
          type: 'progress',
          stage: 'filtering_artists',
          message: 'Filtering by popular artists (Last.fm data)...',
          progress: 75
        });

        // Get unique artist keys from tracks
        const artistKeys = new Set<string>();
        tracks.forEach(track => {
          if (track.grandparentRatingKey) {
            artistKeys.add(track.grandparentRatingKey);
          }
        });

        // Check each artist for Last.fm popularity data
        const popularArtistKeys = new Set<string>();
        let checkedCount = 0;
        
        for (const artistKey of Array.from(artistKeys)) {
          try {
            const artistDetails = await plex.getArtistDetails(artistKey);
            
            // Artists with ratingCount > 0 have external popularity data from Last.fm/MusicBrainz
            // This indicates the artist is well-known enough to have ratings on Last.fm
            if (artistDetails && artistDetails.ratingCount && artistDetails.ratingCount > 0) {
              popularArtistKeys.add(artistKey);
              console.log(`[Popular Artist] ${artistDetails.title}: ratingCount=${artistDetails.ratingCount}`);
            }
            
            checkedCount++;
            if (checkedCount % 10 === 0) {
              progressEmitter?.emit('progress', {
                type: 'progress',
                stage: 'filtering_artists',
                message: `Checked ${checkedCount}/${artistKeys.size} artists for Last.fm data...`,
                progress: 75 + (checkedCount / artistKeys.size) * 10
              });
            }
          } catch (error) {
            // Skip artists that fail to fetch
            continue;
          }
        }

        console.log(`[Popular Artists Filter] Found ${popularArtistKeys.size} popular artists out of ${artistKeys.size} total`);

        // Filter tracks to only include those from popular artists
        const beforeCount = tracks.length;
        tracks = tracks.filter(track => 
          track.grandparentRatingKey && popularArtistKeys.has(track.grandparentRatingKey)
        );
        
        console.log(`[Popular Artists Filter] Filtered from ${beforeCount} to ${tracks.length} tracks`);
        
        if (tracks.length === 0) {
          console.warn('[Popular Artists Filter] No tracks found from popular artists. This may mean:');
          console.warn('  1. Plex has not fetched Last.fm data for your artists yet');
          console.warn('  2. Your library contains mostly lesser-known artists');
          console.warn('  3. Last.fm agent is not enabled in Plex settings');
        }
      }

      // Apply other filters
      tracks = tracks.filter(track => {
        // Genre filters
        if (settings.genres && settings.genres.length > 0) {
          const trackGenres = track.Genre?.map((g: any) => g.tag.toLowerCase()) || [];
          const hasGenre = settings.genres.some(g => trackGenres.includes(g.toLowerCase()));
          if (!hasGenre) return false;
        }
        if (settings.excludeGenres && settings.excludeGenres.length > 0) {
          const trackGenres = track.Genre?.map((g: any) => g.tag.toLowerCase()) || [];
          const hasExcludedGenre = settings.excludeGenres.some(g => trackGenres.includes(g.toLowerCase()));
          if (hasExcludedGenre) return false;
        }

        // Rating filter
        if (settings.minRating && (!track.userRating || track.userRating < settings.minRating)) {
          return false;
        }

        // Play count filter
        if (settings.minPlayCount && (!track.viewCount || track.viewCount < settings.minPlayCount)) {
          return false;
        }

        return true;
      });
    }

    progressEmitter?.emit('progress', {
      type: 'progress',
      stage: 'finalizing',
      message: 'Finalizing track selection...',
      progress: 85
    });

    // Shuffle tracks if sortBy is random
    if (settings.sortBy === 'random') {
      tracks = this.shuffle(tracks);
      logger.info('[Shuffle] Randomized track order', { trackCount: tracks.length });
    }

    // Limit to requested track count
    const selectedTracks = tracks.slice(0, settings.trackCount);

    return {
      trackKeys: selectedTracks.map(t => t.ratingKey),
      trackCount: selectedTracks.length
    };
  }

  /**
   * Generate sonic similarity mix based on a seed track
   */
  async generateSonicMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      seedTrackKey: string;
      trackCount: number;
      maxDistance?: number; // 0-1, lower = more similar
      tempoRange?: { min: number; max: number };
      energyRange?: { min: number; max: number };
      danceabilityRange?: { min: number; max: number };
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    const tracks = await plex.getSonicallySimilarTracks(
      settings.seedTrackKey,
      libraryId,
      {
        maxDistance: settings.maxDistance || 0.25,
        limit: settings.trackCount,
        tempoRange: settings.tempoRange,
        energyRange: settings.energyRange,
        danceabilityRange: settings.danceabilityRange
      }
    );

    return {
      trackKeys: tracks.map(t => t.ratingKey),
      trackCount: tracks.length
    };
  }

  /**
   * Generate all mixes at once
   */
  async generateAllMixes(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: MixSettings
  ): Promise<{
    weekly: MixResult;
    daily: MixResult;
    timeCapsule: MixResult;
    newMusic: MixResult;
  }> {
    const [weekly, daily, timeCapsule, newMusic] = await Promise.all([
      this.generateWeeklyMix(serverUrl, plexToken, libraryId, settings.weeklyMix),
      this.generateDailyMix(serverUrl, plexToken, libraryId, settings.dailyMix),
      this.generateTimeCapsule(serverUrl, plexToken, libraryId, settings.timeCapsule),
      this.generateNewMusicMix(serverUrl, plexToken, libraryId, settings.newMusic)
    ]);

    return { weekly, daily, timeCapsule, newMusic };
  }

  /**
   * Generate Deep Cuts Mix: Hidden gems (low play count, exclude popular tracks)
   */
  async generateDeepCutsMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      trackCount: number;
      maxPlayCount?: number; // Exclude tracks played more than this
      minRating?: number; // Only include rated tracks above this
      excludePopularThreshold?: number; // Percentile to exclude (e.g., top 20%)
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get all tracks sorted by play count
    const allTracks = await plex.getTracksWithAdvancedFilters(libraryId, {
      sortBy: 'playCount',
      sortDirection: 'asc',
      limit: settings.trackCount * 3
    });

    // Filter for deep cuts
    let deepCuts = allTracks.filter(track => {
      // Exclude highly played tracks
      if (settings.maxPlayCount && track.viewCount && track.viewCount > settings.maxPlayCount) {
        return false;
      }
      
      // Only include rated tracks if minRating is set
      if (settings.minRating && (!track.userRating || track.userRating < settings.minRating)) {
        return false;
      }

      return true;
    });

    // Shuffle and limit
    deepCuts = this.shuffle(deepCuts).slice(0, settings.trackCount);

    return {
      trackKeys: deepCuts.map(t => t.ratingKey),
      trackCount: deepCuts.length
    };
  }

  /**
   * Generate Artist Discovery Mix: Tracks from similar artists
   */
  async generateArtistDiscoveryMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      seedArtistKeys: string[]; // Rating keys of favorite artists
      tracksPerArtist: number;
      maxSimilarArtists: number;
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Each seed artist's work (hub lookup, then its similar artists' track
    // lookups) is independent of every other seed artist's - batch across
    // seed artists, and batch the similar-artist lookups within each seed
    // too, instead of a fully sequential O(seeds x similarArtists) chain.
    const perSeedTracks = await mapBatched(settings.seedArtistKeys, PLEX_LOOKUP_BATCH_SIZE, async (artistKey) => {
      try {
        const hubs = await plex.getRelatedHubs(artistKey);

        // Find "Similar Artists" or "Fans Also Like" hub
        const similarHub = hubs.find((h: any) =>
          h.title?.toLowerCase().includes('similar') ||
          h.title?.toLowerCase().includes('fans also like')
        );

        if (!similarHub?.Metadata) return [];

        const similarArtists = similarHub.Metadata.slice(0, settings.maxSimilarArtists);
        const perArtistTracks = await mapBatched(similarArtists, PLEX_LOOKUP_BATCH_SIZE, (artist: any) =>
          plex.getArtistPopularTracks(libraryId, artist.ratingKey, settings.tracksPerArtist)
        );
        return perArtistTracks.flat();
      } catch (error: any) {
        logger.error(`[Mixes] Failed to get similar artists for ${artistKey}`, { error: error?.message || error });
        return [];
      }
    });

    const allTracks: PlexTrack[] = perSeedTracks.flat();

    // Remove duplicates and shuffle
    const uniqueTracks = new Map<string, PlexTrack>();
    allTracks.forEach(track => uniqueTracks.set(track.ratingKey, track));
    const finalTracks = this.shuffle(Array.from(uniqueTracks.values()));

    return {
      trackKeys: finalTracks.map(t => t.ratingKey),
      trackCount: finalTracks.length
    };
  }

  /**
   * Generate Mood Mix: Tracks filtered by mood tags
   */
  async generateMoodMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      moods: string[]; // e.g., ["energetic", "chill", "melancholy"]
      trackCount: number;
      useSonicAnalysis?: boolean; // Use sonic similarity for consistency
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get tracks with mood filters
    const tracks = await plex.getTracksWithAdvancedFilters(libraryId, {
      moods: settings.moods,
      limit: settings.trackCount * 2
    });

    // If sonic analysis is requested and we have tracks, use first track as seed
    if (settings.useSonicAnalysis && tracks.length > 0) {
      const seedTrack = tracks[0];
      const sonicTracks = await plex.getSonicallySimilarTracks(
        seedTrack.ratingKey,
        libraryId,
        { limit: settings.trackCount }
      );
      
      // Filter sonic tracks by mood
      const moodFilteredTracks = sonicTracks.filter(track => {
        const trackMoods = (track as any).Mood?.map((m: any) => m.tag.toLowerCase()) || [];
        return settings.moods.some(mood => trackMoods.includes(mood.toLowerCase()));
      });

      if (moodFilteredTracks.length >= settings.trackCount / 2) {
        return {
          trackKeys: moodFilteredTracks.slice(0, settings.trackCount).map(t => t.ratingKey),
          trackCount: moodFilteredTracks.length
        };
      }
    }

    // Shuffle and limit
    const finalTracks = this.shuffle(tracks).slice(0, settings.trackCount);

    return {
      trackKeys: finalTracks.map(t => t.ratingKey),
      trackCount: finalTracks.length
    };
  }

  /**
   * Generate Era Mix: Tracks from a specific decade/era
   */
  async generateEraMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      startYear: number;
      endYear: number;
      trackCount: number;
      usePopularTracks?: boolean; // Use popular tracks from that era
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get tracks from the specified era
    const tracks = await plex.getTracksWithAdvancedFilters(libraryId, {
      releasedAfterYear: settings.startYear,
      releasedBeforeYear: settings.endYear,
      sortBy: settings.usePopularTracks ? 'playCount' : 'random',
      sortDirection: 'desc',
      limit: settings.trackCount * 2
    });

    // Shuffle if not using popular tracks
    const finalTracks = settings.usePopularTracks 
      ? tracks.slice(0, settings.trackCount)
      : this.shuffle(tracks).slice(0, settings.trackCount);

    return {
      trackKeys: finalTracks.map(t => t.ratingKey),
      trackCount: finalTracks.length
    };
  }

  /**
   * Generate Genre Evolution Mix: Show how a genre evolved over time
   */
  async generateGenreEvolutionMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      genre: string;
      trackCount: number;
      tracksPerDecade?: number;
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get all tracks in the genre
    const allTracks = await plex.getTracksWithAdvancedFilters(libraryId, {
      genres: [settings.genre],
      sortBy: 'releaseDate',
      sortDirection: 'asc',
      limit: settings.trackCount * 3
    });

    if (allTracks.length === 0) {
      return { trackKeys: [], trackCount: 0 };
    }

    // Group by decade
    const tracksByDecade = new Map<number, PlexTrack[]>();
    allTracks.forEach(track => {
      if (track.year) {
        const decade = Math.floor(track.year / 10) * 10;
        if (!tracksByDecade.has(decade)) {
          tracksByDecade.set(decade, []);
        }
        tracksByDecade.get(decade)!.push(track);
      }
    });

    // Select tracks from each decade
    const selectedTracks: PlexTrack[] = [];
    const decades = Array.from(tracksByDecade.keys()).sort();
    const tracksPerDecade = settings.tracksPerDecade || Math.ceil(settings.trackCount / decades.length);

    for (const decade of decades) {
      const decadeTracks = tracksByDecade.get(decade)!;
      const selected = this.shuffle(decadeTracks).slice(0, tracksPerDecade);
      selectedTracks.push(...selected);
    }

    // Limit to requested count
    const finalTracks = selectedTracks.slice(0, settings.trackCount);

    return {
      trackKeys: finalTracks.map(t => t.ratingKey),
      trackCount: finalTracks.length
    };
  }

  /**
   * Generate Artist Journey Mix: Chronological journey through an artist's work
   */
  async generateArtistJourneyMix(
    serverUrl: string,
    plexToken: string,
    _libraryId: string, // Unused but kept for API consistency
    settings: {
      artistKey: string;
      tracksPerAlbum: number;
      usePopularTracks?: boolean;
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get all albums from the artist, sorted chronologically
    const albums = await plex.getArtistAlbums(settings.artistKey);
    
    // Sort by year
    albums.sort((a, b) => (a.year || 0) - (b.year || 0));

    const allTracks: PlexTrack[] = [];

    // Get tracks from each album
    for (const album of albums) {
      try {
        if (settings.usePopularTracks) {
          // Get popular tracks from this album
          const albumTracks = await plex.getAlbumTracks(album.ratingKey);
          const sortedTracks = albumTracks.sort((a, b) => (b.viewCount || 0) - (a.viewCount || 0));
          allTracks.push(...sortedTracks.slice(0, settings.tracksPerAlbum));
        } else {
          // Get first N tracks from album
          const albumTracks = await plex.getAlbumTracks(album.ratingKey);
          allTracks.push(...albumTracks.slice(0, settings.tracksPerAlbum));
        }
      } catch (error: any) {
        logger.error(`[Mixes] Failed to get tracks for album ${album.ratingKey}`, { error: error?.message || error });
      }
    }

    return {
      trackKeys: allTracks.map(t => t.ratingKey),
      trackCount: allTracks.length
    };
  }

  /**
   * Generate Workout Intensity Mix: Progressive tempo/energy build
   */
  async generateWorkoutMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      trackCount: number;
      warmupTracks: number; // Low intensity
      peakTracks: number; // High intensity
      cooldownTracks: number; // Low intensity
      minTempo?: number; // BPM
      maxTempo?: number; // BPM
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get tracks with tempo/energy data
    const allTracks = await plex.getTracksWithAdvancedFilters(libraryId, {
      limit: settings.trackCount * 3
    });

    // Filter tracks with tempo data and sort by tempo
    const tracksWithTempo = allTracks
      .filter(t => (t as any).musicAnalysis?.tempo)
      .sort((a, b) => ((a as any).musicAnalysis?.tempo || 0) - ((b as any).musicAnalysis?.tempo || 0));

    if (tracksWithTempo.length === 0) {
      // Fallback: just return random tracks
      return {
        trackKeys: this.shuffle(allTracks).slice(0, settings.trackCount).map(t => t.ratingKey),
        trackCount: Math.min(allTracks.length, settings.trackCount)
      };
    }

    // Split into intensity levels
    const third = Math.floor(tracksWithTempo.length / 3);
    const lowIntensity = tracksWithTempo.slice(0, third);
    const medIntensity = tracksWithTempo.slice(third, third * 2);
    const highIntensity = tracksWithTempo.slice(third * 2);

    // Shuffle each intensity tier exactly once. Segments that draw from the same tier
    // (warmup/cooldown both use lowIntensity, build/cool-down-from-peak both use
    // medIntensity) then take non-overlapping slices of that single shuffled array,
    // instead of re-shuffling from scratch per segment (which could hand out the same
    // track to two different segments of the same playlist).
    const shuffledLow = this.shuffle(lowIntensity);
    const shuffledMed = this.shuffle(medIntensity);
    const shuffledHigh = this.shuffle(highIntensity);

    // Build workout progression
    const workout: PlexTrack[] = [];
    const addedKeys = new Set<string>();

    const addTracks = (source: PlexTrack[]) => {
      for (const track of source) {
        if (!addedKeys.has(track.ratingKey)) {
          workout.push(track);
          addedKeys.add(track.ratingKey);
        }
      }
    };

    // Warmup
    addTracks(shuffledLow.slice(0, settings.warmupTracks));

    // Build to peak
    const buildTracks = Math.floor((settings.trackCount - settings.warmupTracks - settings.peakTracks - settings.cooldownTracks) / 2);
    addTracks(shuffledMed.slice(0, buildTracks));

    // Peak
    addTracks(shuffledHigh.slice(0, settings.peakTracks));

    // Cool down from peak
    addTracks(shuffledMed.slice(buildTracks, buildTracks * 2));

    // Final cooldown
    addTracks(shuffledLow.slice(settings.warmupTracks, settings.warmupTracks + settings.cooldownTracks));

    return {
      trackKeys: workout.map(t => t.ratingKey),
      trackCount: workout.length
    };
  }

  /**
   * Generate Forgotten Favorites Mix: High play count but not played recently
   */
  async generateForgottenFavoritesMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      trackCount: number;
      minPlayCount: number;
      notPlayedInDays: number;
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get tracks with high play count
    const tracks = await plex.getTracksWithAdvancedFilters(libraryId, {
      minPlayCount: settings.minPlayCount,
      notPlayedInLastDays: settings.notPlayedInDays,
      sortBy: 'playCount',
      sortDirection: 'desc',
      limit: settings.trackCount * 2
    });

    // Shuffle and limit
    const finalTracks = this.shuffle(tracks).slice(0, settings.trackCount);

    return {
      trackKeys: finalTracks.map(t => t.ratingKey),
      trackCount: finalTracks.length
    };
  }

  /**
   * Generate Genre Blend Mix: Tracks that span multiple genres
   */
  async generateGenreBlendMix(
    serverUrl: string,
    plexToken: string,
    libraryId: string,
    settings: {
      genres: string[]; // 2-3 genres to blend
      trackCount: number;
      requireAllGenres?: boolean; // Track must have ALL genres vs ANY genre
    }
  ): Promise<MixResult> {
    const plex = this.createPlexClient(serverUrl, plexToken);

    // Get tracks with genre filters
    const tracks = await plex.getTracksWithAdvancedFilters(libraryId, {
      genres: settings.genres,
      limit: settings.trackCount * 3
    });

    // If requireAllGenres, filter for tracks that have all specified genres
    let filteredTracks = tracks;
    if (settings.requireAllGenres) {
      filteredTracks = tracks.filter(track => {
        const trackGenres = track.Genre?.map((g: any) => g.tag.toLowerCase()) || [];
        return settings.genres.every(genre => trackGenres.includes(genre.toLowerCase()));
      });
    }

    // Shuffle and limit
    const finalTracks = this.shuffle(filteredTracks).slice(0, settings.trackCount);

    return {
      trackKeys: finalTracks.map(t => t.ratingKey),
      trackCount: finalTracks.length
    };
  }
}

