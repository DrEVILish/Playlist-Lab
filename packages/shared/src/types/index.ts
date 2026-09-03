// User types
export interface User {
  id: number;
  plexUserId: string;
  plexUsername: string;
  plexToken: string;
  plexThumb?: string;
  createdAt: number;
  lastLogin: number;
  isAdmin?: boolean;
  isEnabled?: boolean;
  hasServer?: boolean;
}

// Plex Server types
export interface PlexServer {
  name: string;
  clientId: string;
  url: string;
  libraryId?: string;
  libraryName?: string;
  // Server-specific token, present only for servers shared with (not owned
  // by) this account - required to authenticate against that PMS directly.
  accessToken?: string;
}

// Settings types
export interface UserSettings {
  country: string;
  matchingSettings: MatchingSettings;
  mixSettings: MixSettings;
  geminiApiKey?: string;
  grokApiKey?: string;
  aiProvider?: 'gemini' | 'grok';
}

export interface MatchingSettings {
  minMatchScore: number;
  stripParentheses: boolean;
  stripBrackets: boolean;
  useFirstArtistOnly: boolean;
  ignoreFeaturedArtists: boolean;
  ignoreRemixInfo: boolean;
  ignoreVersionInfo: boolean;
  preferNonCompilation: boolean;
  penalizeMonoVersions: boolean;
  penalizeLiveVersions: boolean;
  preferHigherRated: boolean;
  minRatingForMatch: number;
  autoCompleteOnPerfectMatch: boolean;
  playlistPrefixes: {
    enabled: boolean;
    spotify: string;
    deezer: string;
    apple: string;
    tidal: string;
    youtube: string;
    amazon: string;
    qobuz: string;
    listenbrainz: string;
    file: string;
    ai: string;
  };
  customStripPatterns: string[];
  featuredArtistPatterns: string[];
  versionSuffixPatterns: string[];
  remasterPatterns: string[];
  variousArtistsNames: string[];
  penaltyKeywords: string[];
  priorityKeywords: string[];
}

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

// Playlist types
export interface Playlist {
  id: number;
  userId?: number;
  plexPlaylistId: string;
  name: string;
  source: string;
  sourceUrl?: string;
  trackCount?: number;
  duration?: number;
  createdAt: number;
  updatedAt: number;
}

// Schedule types
export interface Schedule {
  id: number;
  userId: number;
  playlistId?: number;
  scheduleType: 'playlist_refresh' | 'mix_generation';
  frequency: 'daily' | 'weekly' | 'fortnightly' | 'monthly';
  startDate: string;
  lastRun?: number;
  config?: any;
  /** Source service the playlist/chart originates from, e.g. 'spotify', 'deezer', 'billboard' */
  source?: string;
  /** Link to the original playlist/chart on its source service, if known */
  sourceUrl?: string;
  /** Unix timestamp (seconds) the schedule was created */
  createdAt?: number;
}

// Missing track types
export interface MissingTrack {
  id: number;
  userId: number;
  playlistId: number;
  title: string;
  artist: string;
  album?: string;
  position: number;
  afterTrackKey?: string;
  addedAt: number;
  source: string;
}

// Live status feed for the header's notification center (deemix downloads,
// missing-tracks retry batches, manually-triggered schedule runs, ...)
/** Fields a playlist can be reordered by - mirrors SORT_KEYS in
 * routes/playlists.ts. */
export type PlaylistSortKey = 'title' | 'artist' | 'album' | 'year' | 'duration';

/** Returned by any action routed through the server's shared action queue
 * instead of running immediately - `position` is 0 when it's about to run
 * next, otherwise how many other queued items are ahead of it (never any
 * detail about whose items those are). The real result arrives later via a
 * JobNotification with this same id. */
export interface QueuedActionResult {
  queued: true;
  jobId: string;
  position: number;
}

export interface JobNotification {
  id: string;
  type: 'deemix' | 'lidarr' | 'retry-match' | 'schedule' | 'import' | 'track-vanished' | 'action';
  title: string;
  detail?: string;
  status: 'in-progress' | 'success' | 'error';
  /** 0-100, omitted when progress isn't known yet */
  progress?: number;
  createdAt: number;
  updatedAt: number;
}

// deemix-server's own download/tagging settings (the fields the admin
// Deemix settings panel edits) - an index signature covers the rest of its
// ~50 fields we pass through untouched.
export interface DeemixSettings {
  downloadLocation: string;
  maxBitrate: string;
  fallbackBitrate: boolean;
  tracknameTemplate: string;
  albumTracknameTemplate: string;
  createArtistFolder: boolean;
  artistNameTemplate: string;
  createAlbumFolder: boolean;
  albumNameTemplate: string;
  createSingleFolder: boolean;
  saveArtwork: boolean;
  tags: Record<string, boolean | string>;
  [key: string]: any;
}

// Cached playlist types
export interface CachedPlaylist {
  id: number;
  source: string;
  sourceId: string;
  name: string;
  description?: string;
  tracks: ExternalTrack[];
  scrapedAt: number;
}

// External service types
export interface ExternalTrack {
  title: string;
  artist: string;
  album?: string;
}

export interface ExternalPlaylist {
  id: string;
  name: string;
  description?: string;
  source: string;
  tracks: ExternalTrack[];
}

// Matched track types
export interface MatchedTrack {
  title: string;
  artist: string;
  album?: string;
  matched: boolean;
  plexRatingKey?: string;
  plexTitle?: string;
  plexArtist?: string;
  plexAlbum?: string;
  plexCodec?: string;
  plexBitrate?: number;
  score?: number;
  /** Set when the user picked this Plex track by hand via a Rematch search,
   * rather than the automatic matcher finding it - lets the server remember
   * the choice for next time. */
  manuallyMatched?: boolean;
}

// Plex track types
export interface PlexTrack {
  ratingKey: string;
  title: string;
  grandparentTitle: string; // Artist
  parentTitle: string; // Album
  Media?: Array<{
    audioCodec?: string;
    bitrate?: number;
  }>;
}

// API Error types
export interface APIErrorData {
  code: string;
  message: string;
  details?: any;
  statusCode: number;
}

// API Response types
export interface APIResponse<T> {
  data?: T;
  error?: APIErrorData;
}
