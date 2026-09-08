import type {
  User,
  PlexServer,
  UserSettings,
  MatchingSettings,
  MixSettings,
  Playlist,
  Schedule,
  MissingTrack,
  MatchedTrack,
  JobNotification,
  DeemixSettings,
  PlaylistSortKey,
  QueuedActionResult,
} from '../types';

export class APIError extends Error {
  constructor(
    public code: string,
    message: string,
    public statusCode: number,
    public details?: any
  ) {
    super(message);
    this.name = 'APIError';
  }
}

export class NetworkError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'NetworkError';
  }
}

// Long enough for a genuinely slow library operation, short enough that a
// stalled backend surfaces as an error the UI can render rather than an
// endless spinner.
const REQUEST_TIMEOUT_MS = 30000;

export class APIClient {
  constructor(private baseURL: string, private getToken?: () => string | null) {}

  /**
   * Parses a failed response into an APIError and throws it, dispatching
   * `auth:session-expired` first on a 401 so the app can bounce to /login.
   * Shared between `request()` and any caller (like `importFile`) that has
   * to use a raw `fetch` instead - e.g. because it sends FormData - so the
   * two don't drift on which endpoints are exempt from the expiry event.
   */
  private async throwApiError(endpoint: string, response: Response, fallbackMessage: string): Promise<never> {
    const error: any = await response.json().catch(() => ({
      error: {
        code: 'UNKNOWN_ERROR',
        message: fallbackMessage,
        statusCode: response.status,
      },
    }));

    // Notify the app of session expiry so it can bounce the user to
    // /login. Skip the auth bootstrap endpoints themselves, since a 401
    // from those just means "not logged in yet" (normal on first load)
    // rather than a session that expired mid-use.
    if (
      response.status === 401 &&
      typeof window !== 'undefined' &&
      endpoint !== '/api/auth/me' &&
      endpoint !== '/api/auth/poll'
    ) {
      window.dispatchEvent(new CustomEvent('auth:session-expired'));
    }

    throw new APIError(
      error.error?.code || 'UNKNOWN_ERROR',
      error.error?.message || fallbackMessage,
      response.status,
      error.error?.details
    );
  }

  private async request<T>(
    endpoint: string,
    options?: RequestInit
  ): Promise<T> {
    try {
      const token = this.getToken?.();
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        ...(options?.headers as Record<string, string>),
      };

      if (token) {
        (headers as Record<string, string>)['Authorization'] = `Bearer ${token}`;
      }

      const response = await fetch(`${this.baseURL}${endpoint}`, {
        ...options,
        headers,
        credentials: 'include', // Include cookies for session
        // A request the server never answers - which is what every call
        // becomes while Plex is unresponsive - otherwise leaves the UI on a
        // spinner indefinitely, with no error to render and no way back. A
        // caller that genuinely needs longer passes its own signal.
        signal: options?.signal ?? AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });

      if (!response.ok) {
        await this.throwApiError(endpoint, response, 'Request failed');
      }

      // Handle 204 No Content
      if (response.status === 204) {
        return undefined as T;
      }

      return response.json() as Promise<T>;
    } catch (error) {
      if (error instanceof APIError) {
        throw error;
      }
      if (error instanceof TypeError) {
        throw new NetworkError('Network error - check your connection');
      }
      // AbortSignal.timeout rejects with a TimeoutError DOMException, which
      // is a hung server rather than anything the user did wrong - say so,
      // instead of surfacing a bare "signal is aborted" to the UI.
      if (error instanceof DOMException && error.name === 'TimeoutError') {
        throw new NetworkError('The server did not respond in time - it may be busy or your Plex server may be unreachable');
      }
      throw error;
    }
  }

  // Auth methods
  async startAuth(): Promise<{ id: number; code: string; expiresAt: string }> {
    return this.request('/api/auth/start', { method: 'POST' });
  }

  async pollAuth(
    pinId: number,
    code: string
  ): Promise<{ authToken?: string; user?: User }> {
    return this.request('/api/auth/poll', {
      method: 'POST',
      body: JSON.stringify({ pinId, code }),
    });
  }

  async logout(): Promise<void> {
    return this.request('/api/auth/logout', { method: 'POST' });
  }

  async getMe(): Promise<User> {
    return this.request('/api/auth/me');
  }

  // Server methods
  async getServers(): Promise<PlexServer[]> {
    const response = await this.request<{ servers: PlexServer[] }>('/api/servers');
    return response.servers;
  }

  async selectServer(server: PlexServer): Promise<void> {
    return this.request('/api/servers/select', {
      method: 'POST',
      body: JSON.stringify(server),
    });
  }

  async getLibraries(): Promise<
    Array<{ id: string; name: string; type: string }>
  > {
    return this.request('/api/servers/libraries');
  }

  // Settings methods
  async getSettings(): Promise<UserSettings> {
    return this.request('/api/settings');
  }

  async updateSettings(settings: Partial<UserSettings>): Promise<UserSettings> {
    return this.request('/api/settings', {
      method: 'PUT',
      body: JSON.stringify(settings),
    });
  }

  async updateMatchingSettings(
    settings: Partial<MatchingSettings>
  ): Promise<UserSettings> {
    return this.request('/api/settings/matching', {
      method: 'PUT',
      body: JSON.stringify({ matchingSettings: settings }),
    });
  }

  async updateMixSettings(settings: Partial<MixSettings>): Promise<UserSettings> {
    return this.request('/api/settings/mixes', {
      method: 'PUT',
      body: JSON.stringify({ mixSettings: settings }),
    });
  }

  // Configuration methods
  async getPublicUrlConfig(): Promise<{
    publicUrl: string;
    configuredPublicUrl: string;
    oauthRedirectUrls: Record<string, string>;
    isDefault: boolean;
    trustProxy: boolean;
  }> {
    return this.request('/api/config/public-url');
  }

  async updatePublicUrl(publicUrl: string): Promise<{
    success: boolean;
    message: string;
    publicUrl: string;
    oauthRedirectUrls: Record<string, string>;
  }> {
    return this.request('/api/config/public-url', {
      method: 'PUT',
      body: JSON.stringify({ publicUrl }),
    });
  }

  // Playlist methods
  async getPlaylists(userId?: number): Promise<{ playlists: any[] }> {
    const url = userId ? `/api/playlists?userId=${userId}` : '/api/playlists';
    return this.request(url);
  }

  async getPlaylist(id: number): Promise<{ playlist: any }> {
    return this.request(`/api/playlists/${id}`);
  }

  async createPlaylist(data: {
    name: string;
    tracks: string[];
  }): Promise<{ playlist: any }> {
    return this.request('/api/playlists', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  async updatePlaylist(
    id: number,
    data: Partial<Playlist>
  ): Promise<{ playlist: any }> {
    return this.request(`/api/playlists/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  }

  async deletePlaylist(id: number | string): Promise<{ success: boolean }> {
    return this.request(`/api/playlists/${id}`, { method: 'DELETE' });
  }

  // Deletes by Plex ratingKey instead of our internal numeric id - works for
  // any playlist visible in Plex, including ones never imported through this
  // app (which have no numeric id to pass to deletePlaylist() above).
  async deletePlaylistByPlexId(ratingKey: string): Promise<{ success: boolean }> {
    return this.request(`/api/playlists/by-plex-id/${ratingKey}`, { method: 'DELETE' });
  }

  // Renames by Plex ratingKey instead of our internal numeric id - same
  // reasoning as deletePlaylistByPlexId() above.
  async renamePlaylistByPlexId(ratingKey: string, name: string): Promise<{ success: boolean; name: string }> {
    return this.request(`/api/playlists/by-plex-id/${ratingKey}`, {
      method: 'PUT',
      body: JSON.stringify({ name }),
    });
  }

  // Re-scrapes the playlist from its original online source and replaces its
  // Plex tracks. Fires in the background - use refreshPlaylists() afterward.
  async reimportPlaylist(id: number | string): Promise<{ success: boolean; message: string }> {
    return this.request(`/api/playlists/${id}/reimport`, { method: 'POST' });
  }

  async getPlaylistTracks(id: number | string): Promise<{ tracks: any[] }> {
    return this.request(`/api/playlists/${id}/tracks`);
  }

  async sharePlaylist(id: number | string, targetUserId: number): Promise<{ success: boolean; playlistName: string; trackCount: number }> {
    return this.request(`/api/playlists/${id}/share`, {
      method: 'POST',
      body: JSON.stringify({ targetUserId }),
    });
  }

  async getShareTargets(): Promise<{ users: Array<{ id: number; username: string; thumb?: string }> }> {
    return this.request('/api/playlists/share-targets');
  }

  async addTrackToPlaylist(id: number | string, trackUri: string): Promise<{ success: boolean }> {
    return this.request(`/api/playlists/${id}/tracks`, {
      method: 'POST',
      body: JSON.stringify({ trackUris: [trackUri] }),
    });
  }

  async removeTrackFromPlaylist(id: number | string, trackId: string): Promise<{ success: boolean }> {
    return this.request(`/api/playlists/${id}/tracks/${trackId}`, {
      method: 'DELETE',
    });
  }

  // Merge/clone/split/shuffle all key off the Plex ratingKey (same as
  // deletePlaylistByPlexId/renamePlaylistByPlexId above), not our internal
  // numeric id, so they work on any playlist visible to the user's own Plex
  // account - not just ones tracked in our own playlists table.
  // These run through the server's shared action queue rather than
  // completing within the request - the real result shows up later as a
  // JobNotification with the returned jobId (see QueuedActionResult).
  async mergePlaylists(sourceRatingKeys: string[], targetName?: string, existingTargetRatingKey?: string): Promise<QueuedActionResult> {
    return this.request('/api/playlists/merge', {
      method: 'POST',
      body: JSON.stringify({ sourceRatingKeys, targetName, existingTargetRatingKey }),
    });
  }

  async clonePlaylist(ratingKey: string, name: string): Promise<{ playlist: any }> {
    return this.request(`/api/playlists/${ratingKey}/clone`, {
      method: 'POST',
      body: JSON.stringify({ name }),
    });
  }

  async splitPlaylist(ratingKey: string, groups: Array<{ name: string; trackIds: string[] }>): Promise<QueuedActionResult> {
    return this.request(`/api/playlists/${ratingKey}/split`, {
      method: 'POST',
      body: JSON.stringify({ groups }),
    });
  }

  async shufflePlaylist(ratingKey: string): Promise<QueuedActionResult> {
    return this.request(`/api/playlists/${ratingKey}/shuffle`, { method: 'POST' });
  }

  async sortPlaylist(ratingKey: string, by: PlaylistSortKey, direction: 'asc' | 'desc' = 'asc'): Promise<QueuedActionResult> {
    return this.request(`/api/playlists/${ratingKey}/sort`, {
      method: 'POST',
      body: JSON.stringify({ by, direction }),
    });
  }

  /** Removes repeats of the same Plex track, keeping the first of each. */
  async dedupePlaylist(ratingKey: string): Promise<QueuedActionResult> {
    return this.request(`/api/playlists/${ratingKey}/dedupe`, { method: 'POST' });
  }

  // Import methods
  async importSpotify(url: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/spotify', {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async importDeezer(playlistId: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/deezer', {
      method: 'POST',
      body: JSON.stringify({ playlistId }),
    });
  }

  async importAppleMusic(url: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/apple', {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async importTidal(url: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/tidal', {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async importYouTubeMusic(url: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/youtube', {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async importAmazonMusic(url: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/amazon', {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async importQobuz(url: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/qobuz', {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async importListenBrainz(username: string): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    return this.request('/api/import/listenbrainz', {
      method: 'POST',
      body: JSON.stringify({ username }),
    });
  }

  async importFile(file: File): Promise<{
    matched: MatchedTrack[];
    unmatched: MatchedTrack[];
    playlistName: string;
  }> {
    const formData = new FormData();
    formData.append('file', file);

    const token = this.getToken?.();
    const headers: Record<string, string> = {};
    if (token) {
      headers['Authorization'] = `Bearer ${token}`;
    }

    const response = await fetch(`${this.baseURL}/api/import/file`, {
      method: 'POST',
      body: formData,
      headers,
      credentials: 'include',
    });

    if (!response.ok) {
      await this.throwApiError('/api/import/file', response, 'Import failed');
    }

    return response.json() as any;
  }

  /** Matches a plain {title, artist, album} track list against the user's
   * Plex library without scraping a source URL first - e.g. for restoring
   * a backup file. Feed the result into confirmImport() to create the
   * playlist. */
  async matchTracks(tracks: Array<{ title: string; artist: string; album?: string }>): Promise<{ matched: MatchedTrack[] }> {
    return this.request('/api/import/match', {
      method: 'POST',
      body: JSON.stringify({ tracks }),
    });
  }

  async confirmImport(data: {
    playlistName: string;
    source: string;
    sourceUrl?: string;
    tracks: MatchedTrack[];
    saveMissingTracks?: boolean;
    missingTracks?: Array<{ title: string; artist: string; album?: string }>;
    overwriteExisting?: boolean;
    keepExistingCover?: boolean;
    coverUrl?: string;
  }): Promise<Playlist> {
    return this.request('/api/import/confirm', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  // Mix methods
  async generateWeeklyMix(): Promise<Playlist> {
    return this.request('/api/mixes/weekly', { method: 'POST' });
  }

  async generateDailyMix(): Promise<Playlist> {
    return this.request('/api/mixes/daily', { method: 'POST' });
  }

  async generateTimeCapsule(): Promise<Playlist> {
    return this.request('/api/mixes/timecapsule', { method: 'POST' });
  }

  async generateNewMusicMix(): Promise<Playlist> {
    return this.request('/api/mixes/newmusic', { method: 'POST' });
  }

  async generateCustomMix(settings: {
    name: string;
    trackCount: number;
    
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
    popularTracksOnly?: boolean; // Only include tracks from Plex's "Popular Tracks" section
    popularArtistsOnly?: boolean; // Only include tracks from popular/well-known artists
    
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
    sortBy: 'random' | 'playCount' | 'lastPlayed' | 'dateAdded' | 'releaseDate' | 'rating' | 'duration' | 'title';
    sortDirection: 'asc' | 'desc';
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/custom-advanced', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  async generateSonicMix(settings: {
    name: string;
    seedTrackKey: string;
    trackCount: number;
    maxDistance?: number; // 0-1, lower = more similar
    tempoRange?: { min: number; max: number };
    energyRange?: { min: number; max: number };
    danceabilityRange?: { min: number; max: number };
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/sonic', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  async getLibraryGenres(): Promise<{ genres: string[] }> {
    return this.request('/api/mixes/metadata/genres');
  }

  async getLibraryMoods(): Promise<{ moods: string[] }> {
    return this.request('/api/mixes/metadata/moods');
  }

  async getLibraryStyles(): Promise<{ styles: string[] }> {
    return this.request('/api/mixes/metadata/styles');
  }

  async getLibraryCollections(): Promise<{ collections: string[] }> {
    return this.request('/api/mixes/metadata/collections');
  }

  async generateAllMixes(): Promise<Playlist[]> {
    return this.request('/api/mixes/all', { method: 'POST' });
  }

  // New mix generation methods
  async generateDeepCutsMix(settings?: {
    playlistName?: string;
    trackCount?: number;
    maxPlayCount?: number;
    excludePopular?: boolean;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/deep-cuts', {
      method: 'POST',
      body: JSON.stringify(settings || {}),
    });
  }

  async generateArtistDiscoveryMix(settings: {
    seedArtistKey: string;
    playlistName?: string;
    trackCount?: number;
    tracksPerArtist?: number;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/artist-discovery', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  async generateMoodMix(settings: {
    moods: string[];
    playlistName?: string;
    trackCount?: number;
    useSonicAnalysis?: boolean;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/mood', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  async generateEraMix(settings: {
    startYear: number;
    endYear: number;
    playlistName?: string;
    trackCount?: number;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/era', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  async generateGenreEvolutionMix(settings: {
    genre: string;
    playlistName?: string;
    trackCount?: number;
    tracksPerDecade?: number;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/genre-evolution', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  async generateArtistJourneyMix(settings: {
    artistKey: string;
    playlistName?: string;
    trackCount?: number;
    tracksPerAlbum?: number;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/artist-journey', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  async generateWorkoutMix(settings?: {
    playlistName?: string;
    trackCount?: number;
    warmupTracks?: number;
    peakTracks?: number;
    cooldownTracks?: number;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/workout', {
      method: 'POST',
      body: JSON.stringify(settings || {}),
    });
  }

  async generateForgottenFavoritesMix(settings?: {
    playlistName?: string;
    trackCount?: number;
    minPlayCount?: number;
    notPlayedDays?: number;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/forgotten-favorites', {
      method: 'POST',
      body: JSON.stringify(settings || {}),
    });
  }

  async generateGenreBlendMix(settings: {
    genres: string[];
    playlistName?: string;
    trackCount?: number;
    minGenres?: number;
  }): Promise<{ success: boolean; playlist: { id: string; name: string; trackCount: number } }> {
    return this.request('/api/mixes/genre-blend', {
      method: 'POST',
      body: JSON.stringify(settings),
    });
  }

  // Schedule methods
  async getSchedules(): Promise<Schedule[]> {
    return this.request('/api/schedules');
  }

  async createSchedule(schedule: Omit<Schedule, 'id' | 'userId' | 'lastRun'>): Promise<Schedule> {
    // Transform camelCase to snake_case for backend
    const body = {
      playlist_id: schedule.playlistId,
      schedule_type: schedule.scheduleType,
      frequency: schedule.frequency,
      start_date: schedule.startDate,
      config: schedule.config,
    };

    return this.request('/api/schedules', {
      method: 'POST',
      body: JSON.stringify(body),
    });
  }

  async updateSchedule(
    id: number,
    schedule: Partial<Schedule>
  ): Promise<Schedule> {
    // Transform camelCase to snake_case for backend
    const body: any = {};
    if (schedule.playlistId !== undefined) body.playlist_id = schedule.playlistId;
    if (schedule.scheduleType !== undefined) body.schedule_type = schedule.scheduleType;
    if (schedule.frequency !== undefined) body.frequency = schedule.frequency;
    if (schedule.startDate !== undefined) body.start_date = schedule.startDate;
    if (schedule.config !== undefined) body.config = schedule.config;
    
    return this.request(`/api/schedules/${id}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    });
  }

  async deleteSchedule(id: number): Promise<void> {
    return this.request(`/api/schedules/${id}`, { method: 'DELETE' });
  }

  async getScheduleExecutions(scheduleId: number, limit?: number): Promise<{
    success: boolean;
    executions: Array<{
      id: number;
      scheduleId: number;
      status: 'running' | 'success' | 'failed';
      startedAt: number;
      completedAt?: number;
      tracksMatched: number;
      tracksUnmatched: number;
      errorMessage?: string;
      playlistName?: string;
    }>;
  }> {
    const query = limit ? `?limit=${limit}` : '';
    return this.request(`/api/schedules/${scheduleId}/executions${query}`);
  }

  async getRecentExecutions(limit?: number): Promise<{
    success: boolean;
    executions: Array<{
      id: number;
      scheduleId: number;
      scheduleType: string;
      frequency: string;
      status: 'running' | 'success' | 'failed';
      startedAt: number;
      completedAt?: number;
      tracksMatched: number;
      tracksUnmatched: number;
      errorMessage?: string;
      playlistName?: string;
    }>;
  }> {
    const query = limit ? `?limit=${limit}` : '';
    return this.request(`/api/schedules/executions/recent${query}`);
  }

  async getRunningExecutions(): Promise<{
    success: boolean;
    executions: Array<{
      id: number;
      scheduleId: number;
      scheduleType: string;
      frequency: string;
      status: 'running';
      startedAt: number;
      playlistName?: string;
    }>;
  }> {
    return this.request('/api/schedules/executions/running');
  }

  async deleteExecution(executionId: number): Promise<{
    success: boolean;
    message: string;
  }> {
    return this.request(`/api/schedules/executions/${executionId}`, {
      method: 'DELETE'
    });
  }

  async clearAllExecutions(): Promise<{
    success: boolean;
    message: string;
    deletedCount: number;
  }> {
    return this.request('/api/schedules/executions', {
      method: 'DELETE'
    });
  }

  async runSchedule(scheduleId: number): Promise<{
    success: boolean;
    message: string;
  }> {
    return this.request(`/api/schedules/${scheduleId}/run`, {
      method: 'POST'
    });
  }

  async runAllSchedules(): Promise<{
    success: boolean;
    message: string;
    triggered: number;
  }> {
    return this.request('/api/schedules/run-all', {
      method: 'POST'
    });
  }

  // Missing tracks methods
  async getMissingTracks(): Promise<{
    missingTracks: Array<{
      playlistId: number;
      playlistName: string;
      source: string;
      tracks: MissingTrack[];
    }>;
    totalCount: number;
  }> {
    return this.request('/api/missing');
  }

  // Starts matching in the background (a full retry batch does several real
  // Plex API calls per track, so large batches can take minutes) and returns
  // immediately once the job is queued. Poll getMissingRetryStatus() for
  // live current/total/error progress instead of re-fetching the full
  // missing-tracks list, and check `queued` - a queued batch hasn't started
  // yet, so its retry-status entry won't exist until the running one finishes.
  async retryMissingTracks(playlistId?: number, trackIds?: number[]): Promise<{
    started: boolean;
    /** true when a retry was already running and this one was queued to run right after it, instead of rejected. */
    queued?: boolean;
    totalTracks: number;
    message: string;
  }> {
    return this.request('/api/missing/retry', {
      method: 'POST',
      body: JSON.stringify({ playlistId, trackIds }),
    });
  }

  /** Polled by the header's activity indicator to show live retry progress.
   * `error` is set (and the entry left in place instead of being cleared)
   * when a batch fails outright - e.g. a revoked Plex token - so pollers can
   * distinguish "genuinely still missing" from "the retry itself errored". */
  async getMissingRetryStatus(): Promise<{ active: { current: number; total: number; error?: string } | null }> {
    return this.request('/api/missing/retry-status');
  }

  async removeMissingTrack(id: number): Promise<void> {
    return this.request(`/api/missing/${id}`, { method: 'DELETE' });
  }

  async rematchMissingTrack(id: number, ratingKey: string): Promise<{ success: boolean }> {
    return this.request(`/api/missing/${id}/rematch`, {
      method: 'POST',
      body: JSON.stringify({ ratingKey }),
    });
  }

  async deemixDownload(id: number): Promise<{ success: boolean; matchedTitle: string; matchedArtist: string; score: number }> {
    return this.request(`/api/missing/${id}/deemix-download`, { method: 'POST' });
  }

  /** Retries matching against Plex and then queues every still-missing track
   * for deemix download - one playlist's worth, or the whole library when no
   * id is given. Runs server-side on the shared action queue and returns as
   * soon as it's queued, so progress is watched via the notification bell
   * rather than by keeping this page open. */
  async deemixAll(playlistId?: number): Promise<QueuedActionResult & { message: string }> {
    return this.request('/api/missing/deemix-all', {
      method: 'POST',
      body: JSON.stringify({ playlistId }),
    });
  }

  async replaceSimilarMissingTrack(id: number): Promise<{ success: boolean; replacementTitle: string; replacementArtist: string }> {
    return this.request(`/api/missing/${id}/replace-similar`, { method: 'POST' });
  }

  async lidarrDownload(id: number): Promise<{ success: boolean; matchedArtist: string; matchedAlbum?: string }> {
    return this.request(`/api/missing/${id}/lidarr-download`, { method: 'POST' });
  }

  async getNotifications(): Promise<{ notifications: JobNotification[] }> {
    return this.request('/api/notifications');
  }

  async dismissNotification(id: string): Promise<{ success: boolean }> {
    return this.request(`/api/notifications/${id}/dismiss`, { method: 'POST' });
  }

  /** Clears finished notifications. Running jobs are always kept - their
   * entry is the only place their progress shows. `completedOnly` keeps
   * failures too. */
  async clearNotifications(completedOnly = false): Promise<{ success: boolean }> {
    return this.request('/api/notifications/clear', {
      method: 'POST',
      body: JSON.stringify({ completedOnly }),
    });
  }

  async clearPlaylistMissingTracks(playlistId: number): Promise<void> {
    return this.request(`/api/missing/playlist/${playlistId}`, {
      method: 'DELETE',
    });
  }

  async addMissingTracks(data: {
    playlistId: number;
    tracks: Array<{ title: string; artist: string; album?: string; position?: number }>;
    source: string;
  }): Promise<{ success: boolean; added: number; message: string }> {
    return this.request('/api/missing/add', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  async saveMissingTracks(data: {
    playlistName: string;
    source: string;
    sourceUrl?: string;
    tracks: Array<{ title: string; artist: string; album?: string }>;
    matchedTracks?: Array<{ plexRatingKey?: string; title: string; artist: string }>;
    overwriteExisting?: boolean;
    keepExistingCover?: boolean;
    coverUrl?: string;
  }): Promise<{ success: boolean; playlistId: number; added: number; message: string }> {
    return this.request('/api/missing/save', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  // Admin methods
  async getAdminStats(): Promise<{
    userCount: number;
    activeUsers: number;
    playlistCount: number;
    missingTrackCount: number;
  }> {
    const data = await this.request<{ stats: any }>('/api/admin/stats');
    return {
      userCount: data.stats.totalUsers,
      activeUsers: data.stats.activeUsers,
      playlistCount: data.stats.totalPlaylists,
      missingTrackCount: data.stats.totalMissingTracks,
    };
  }

  async getAdminUsers(): Promise<User[]> {
    const data = await this.request<{ users: User[] }>('/api/admin/users');
    return data.users;
  }

  async getAdminMissingTracks(): Promise<
    Array<{ title: string; artist: string; count: number; addedAt: number }>
  > {
    const data = await this.request<{ missingTracks: Array<{ title: string; artist: string; count: number; addedAt: number }> }>('/api/admin/missing');
    return data.missingTracks;
  }

  /** Queues a whole list of aggregated missing-track rows as one server-side
   * job, so the notification stays in progress until the searches are
   * actually done rather than reporting "queued" as finished. */
  async deemixAllAdmin(tracks: Array<{ title: string; artist: string }>): Promise<QueuedActionResult & { message: string }> {
    return this.request('/api/admin/missing/deemix-all', {
      method: 'POST',
      body: JSON.stringify({ tracks }),
    });
  }

  async deemixDownloadAdmin(title: string, artist: string): Promise<QueuedActionResult> {
    return this.request('/api/admin/missing/deemix-download', {
      method: 'POST',
      body: JSON.stringify({ title, artist }),
    });
  }

  async getAdminJobs(): Promise<
    Array<{ name: string; status: string; lastRun?: number; nextRun?: number | null }>
  > {
    const data = await this.request<{ jobs: Array<{ name: string; status: string; lastRun?: number; nextRun?: number | null }> }>('/api/admin/jobs');
    return data.jobs;
  }

  async getAdminSchedules(): Promise<Array<Schedule & { username: string; playlistName: string | null }>> {
    const data = await this.request<{ schedules: Array<Schedule & { username: string; playlistName: string | null }> }>('/api/admin/schedules');
    return data.schedules;
  }

  async getAdminLogs(limit = 200, level = ''): Promise<{
    entries: Array<{ level: string; message: string; timestamp: string | null; [key: string]: unknown }>;
    truncated: boolean;
  }> {
    const params = new URLSearchParams({ limit: String(limit) });
    if (level) params.set('level', level);
    return this.request(`/api/admin/logs?${params}`);
  }

  async clearAdminLogs(): Promise<{ success: boolean }> {
    return this.request('/api/admin/logs', { method: 'DELETE' });
  }

  async getLogLevel(): Promise<{ level: string; levels: readonly string[] }> {
    return this.request('/api/admin/log-level');
  }

  async updateLogLevel(level: string): Promise<{ success: boolean; level: string }> {
    return this.request('/api/admin/log-level', {
      method: 'PUT',
      body: JSON.stringify({ level }),
    });
  }

  async enableUser(userId: number): Promise<{ success: boolean }> {
    return this.request(`/api/admin/users/${userId}/enable`, { method: 'POST' });
  }

  async disableUser(userId: number): Promise<{ success: boolean }> {
    return this.request(`/api/admin/users/${userId}/disable`, { method: 'POST' });
  }

  async deleteUser(userId: number): Promise<{ success: boolean }> {
    return this.request(`/api/admin/users/${userId}`, { method: 'DELETE' });
  }

  /** `status` is the result of the last ARL check (daily, plus on save and
   * on startup) - null if none has run yet in this server process. */
  async getDeemixArl(): Promise<{ arl: string; status: { ok: boolean; error?: string; at: number } | null }> {
    return this.request('/api/admin/deemix-arl');
  }

  async checkDeemixArl(): Promise<{ ok: boolean; error?: string }> {
    return this.request('/api/admin/deemix-arl/check', { method: 'POST' });
  }

  async updateDeemixArl(arl: string): Promise<{ success: boolean; arl: string }> {
    return this.request('/api/admin/deemix-arl', {
      method: 'PUT',
      body: JSON.stringify({ arl }),
    });
  }

  async getDeemixSettings(): Promise<{ settings: DeemixSettings }> {
    return this.request('/api/admin/deemix-settings');
  }

  async updateDeemixSettings(settings: DeemixSettings): Promise<{ success: boolean }> {
    return this.request('/api/admin/deemix-settings', {
      method: 'PUT',
      body: JSON.stringify({ settings }),
    });
  }

  async getLidarrConfig(): Promise<{ url: string; apiKey: string }> {
    return this.request('/api/admin/lidarr-config');
  }

  async updateLidarrConfig(url: string, apiKey: string): Promise<{ success: boolean; url: string }> {
    return this.request('/api/admin/lidarr-config', {
      method: 'PUT',
      body: JSON.stringify({ url, apiKey }),
    });
  }

  async getHomeUsers(): Promise<
    Array<{ id: number; title: string; username: string; thumb: string; admin: boolean; restricted: boolean; guest: boolean }>
  > {
    const data = await this.request<{ homeUsers: any[] }>('/api/admin/home-users');
    return data.homeUsers;
  }

  // Mix Template methods
  async getMixTemplates(): Promise<{ templates: any[] }> {
    return this.request('/api/mix-templates');
  }

  async getMixTemplate(id: number): Promise<any> {
    return this.request(`/api/mix-templates/${id}`);
  }

  async createMixTemplate(data: {
    name: string;
    description?: string;
    mixType: string;
    configuration: any;
  }): Promise<{ id: number; message: string }> {
    return this.request('/api/mix-templates', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  async updateMixTemplate(id: number, data: {
    name?: string;
    description?: string;
    configuration?: any;
  }): Promise<{ message: string }> {
    return this.request(`/api/mix-templates/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  }

  async deleteMixTemplate(id: number): Promise<{ message: string }> {
    return this.request(`/api/mix-templates/${id}`, {
      method: 'DELETE',
    });
  }

  async generateMixFromTemplate(id: number, playlistName?: string, sessionId?: string): Promise<{
    success: boolean;
    playlistId: string;
    trackCount: number;
    warnings: string[];
    message: string;
  }> {
    return this.request(`/api/mix-templates/${id}/generate`, {
      method: 'POST',
      body: JSON.stringify({ playlistName, sessionId }),
    });
  }
}
