import { createContext, useContext, useState, useEffect, useRef, type FC, type ReactNode } from 'react';
import { useAuth } from './AuthContext';
import { APIClient, type Schedule } from '@playlist-lab/shared';

interface PlexServer {
  name: string;
  clientId: string;
  url: string;
  libraryId?: string;
  libraryName?: string;
}

interface MatchingSettings {
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

interface MixSettings {
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

interface UserSettings {
  country: string;
  matchingSettings: MatchingSettings;
  mixSettings: MixSettings;
  geminiApiKey?: string;
  grokApiKey?: string;
  aiProvider?: 'gemini' | 'grok';
}

export interface Playlist {
  /** Plex ratingKey - this list comes from GET /api/playlists, a live read of Plex's own playlists, not our DB's playlists table. */
  id: string;
  /** Our internal numeric playlists.id, present only if this Plex playlist was imported through this app (so it has a schedule/missing-tracks/source to look up). */
  dbId?: number;
  plexPlaylistId: string;
  name: string;
  source: string;
  sourceUrl?: string;
  trackCount?: number;
  duration?: number;
  smart?: boolean;
  composite?: string;
  createdAt: number;
  updatedAt: number;
}

interface UpdateInfo {
  updateAvailable: boolean;
  latestVersion?: string;
}

interface AppState {
  server: PlexServer | null;
  settings: UserSettings | null;
  playlists: Playlist[];
  schedules: Schedule[];
  missingTracksCount: number;
  isLoading: boolean;
  version: string;
  updateInfo: UpdateInfo | null;
  isUpdating: boolean;
}

interface AppContextType extends AppState {
  apiClient: APIClient;
  setServer: (server: PlexServer) => void;
  updateSettings: (settings: Partial<UserSettings>) => Promise<void>;
  refreshPlaylists: () => Promise<void>;
  refreshSchedules: () => Promise<void>;
  refreshMissingTracksCount: () => Promise<void>;
  refreshSettings: () => Promise<void>;
  refreshAll: () => Promise<void>;
  installUpdate: () => Promise<void>;
}

const AppContext = createContext<AppContextType | undefined>(undefined);

export const useApp = () => {
  const context = useContext(AppContext);
  if (!context) {
    throw new Error('useApp must be used within an AppProvider');
  }
  return context;
};

interface AppProviderProps {
  children: ReactNode;
}

// Same reasoning as the shared APIClient's own timeout: these endpoints are
// Plex-backed, and while Plex is unresponsive the server may hold a request
// open long enough that the UI is left with neither data nor an error to
// render. Bounding them means a stalled backend degrades to an empty list
// the page can show, rather than a spinner that never resolves.
const API_TIMEOUT_MS = 30000;
const apiFetch = async (input: string, init: RequestInit = {}) => {
  const response = await fetch(input, { credentials: 'include', signal: AbortSignal.timeout(API_TIMEOUT_MS), ...init });
  // A 401 here means either the session or the stored Plex token stopped
  // working, and both are fixed by signing in again. Without this the callers
  // just fall back to empty state, so an expired Plex token looked exactly
  // like an empty library - AuthContext already listens for this event.
  if (response.status === 401) {
    window.dispatchEvent(new Event('auth:session-expired'));
  }
  return response;
};

export const AppProvider: FC<AppProviderProps> = ({ children }) => {
  const { isAuthenticated } = useAuth();
  const [state, setState] = useState<AppState>({
    server: null,
    settings: null,
    playlists: [],
    schedules: [],
    missingTracksCount: 0,
    isLoading: true, // Start as true to prevent premature redirects before initial data loads
    version: '',
    updateInfo: null,
    isUpdating: false,
  });

  // Create API client instance (stable reference — never recreated)
  const apiClient = useRef(new APIClient(window.location.origin)).current;

  // Load initial data when authenticated
  useEffect(() => {
    if (isAuthenticated) {
      refreshAll();
    } else {
      // Reset state when logged out
      setState({
        server: null,
        settings: null,
        playlists: [],
        schedules: [],
        missingTracksCount: 0,
        isLoading: false,
        version: '',
        updateInfo: null,
        isUpdating: false,
      });
    }
  }, [isAuthenticated]);

  // Poll the running version (detects a post-update server restart and
  // reloads the page) and whether a newer release is available. Lives here
  // rather than in the Header/Settings components that display it so there's
  // one poll loop shared by both.
  useEffect(() => {
    if (!isAuthenticated) return;

    let initialVersion = '';
    const checkVersion = async () => {
      try {
        const res = await apiFetch('/api/version', { credentials: 'include' });
        if (!res.ok) return;
        const data = await res.json();
        if (!initialVersion) {
          initialVersion = data.version;
          setState((prev) => ({ ...prev, version: data.version }));
        } else if (data.version !== initialVersion) {
          window.location.reload();
        }
      } catch {
        // Server might be restarting - silently retry next tick
      }
    };

    const checkForUpdates = async () => {
      try {
        const res = await apiFetch('/api/update/check', { credentials: 'include' });
        if (!res.ok) return;
        const data = await res.json();
        setState((prev) => ({ ...prev, updateInfo: data }));
      } catch {
        // Silently fail
      }
    };

    checkVersion();
    checkForUpdates();
    const versionInterval = setInterval(checkVersion, 30 * 1000);
    const updateInterval = setInterval(checkForUpdates, 6 * 60 * 60 * 1000);
    return () => {
      clearInterval(versionInterval);
      clearInterval(updateInterval);
    };
  }, [isAuthenticated]);

  const installUpdate = async () => {
    setState((prev) => ({ ...prev, isUpdating: true }));
    try {
      const res = await apiFetch('/api/update/install', { method: 'POST', credentials: 'include' });
      if (!res.ok) {
        const data = await res.json();
        throw new Error(data.error || 'Unknown error');
      }
      // On success, keep showing "Updating..." until the server restarts and
      // the version poll above notices and reloads the page.
    } catch (err) {
      if (err instanceof Error && err.message.includes('Failed to fetch')) {
        // Server is restarting - expected, keep showing "Updating..."
      } else {
        setState((prev) => ({ ...prev, isUpdating: false }));
        throw err;
      }
    }
  };

  const setServer = (server: PlexServer) => {
    setState((prev) => ({ ...prev, server }));
  };

  const updateSettings = async (settings: Partial<UserSettings>) => {
    try {
      const response = await apiFetch('/api/settings', {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify(settings),
      });

      if (!response.ok) {
        throw new Error('Failed to update settings');
      }

      const data = await response.json();
      // API returns { settings: {...} }
      setState((prev) => ({ ...prev, settings: data.settings }));
    } catch (error) {
      console.error('Failed to update settings:', error);
      throw error;
    }
  };

  const refreshPlaylists = async () => {
    try {
      const response = await apiFetch('/api/playlists', {
        credentials: 'include',
      });

      if (response.ok) {
        const data = await response.json();
        // Handle both array and object with playlists property
        const playlists = Array.isArray(data) ? data : (data.playlists || []);
        setState((prev) => ({ ...prev, playlists }));
      } else {
        console.error('Failed to fetch playlists:', response.status);
        setState((prev) => ({ ...prev, playlists: [] }));
      }
    } catch (error) {
      console.error('Failed to fetch playlists:', error);
      setState((prev) => ({ ...prev, playlists: [] }));
    }
  };

  const refreshSchedules = async () => {
    try {
      const response = await apiFetch('/api/schedules', {
        credentials: 'include',
      });

      if (response.ok) {
        const data = await response.json();
        // Handle both array and object with schedules property
        const schedules = Array.isArray(data) ? data : (data.schedules || []);
        setState((prev) => ({ ...prev, schedules }));
      } else {
        console.error('Failed to fetch schedules:', response.status);
        setState((prev) => ({ ...prev, schedules: [] }));
      }
    } catch (error) {
      console.error('Failed to fetch schedules:', error);
      setState((prev) => ({ ...prev, schedules: [] }));
    }
  };

  const refreshMissingTracksCount = async () => {
    try {
      const response = await apiFetch('/api/missing', {
        credentials: 'include',
      });

      if (response.ok) {
        const data = await response.json();
        // Use totalCount from API (actual track count) instead of array length (playlist group count)
        const count = data.totalCount ?? (Array.isArray(data) ? data.length : (data.missingTracks || []).reduce((sum: number, g: any) => sum + (g.tracks?.length || 0), 0));
        setState((prev) => ({ 
          ...prev, 
          missingTracksCount: count
        }));
      } else {
        console.error('Failed to fetch missing tracks:', response.status);
        setState((prev) => ({ ...prev, missingTracksCount: 0 }));
      }
    } catch (error) {
      console.error('Failed to fetch missing tracks count:', error);
      setState((prev) => ({ ...prev, missingTracksCount: 0 }));
    }
  };

  const refreshSettings = async () => {
    try {
      const response = await apiFetch('/api/settings', {
        credentials: 'include',
      });
      if (response.ok) {
        const data = await response.json();
        // API returns { settings: {...} }
        setState((prev) => ({ ...prev, settings: data.settings }));
      } else {
        console.error('Failed to fetch settings:', response.status);
      }
    } catch (error) {
      console.error('Failed to fetch settings:', error);
    }
  };

  const refreshAll = async () => {
    setState((prev) => ({ ...prev, isLoading: true }));

    try {
      // Fetch server and settings first (these are critical for routing)
      const [serverResponse, settingsResponse] = await Promise.all([
        apiFetch('/api/servers/current', { credentials: 'include' }),
        apiFetch('/api/settings', { credentials: 'include' })
      ]);

      let serverData = null;
      let settingsData = null;

      if (serverResponse.ok) {
        const data = await serverResponse.json();
        serverData = data.server || null;
      }

      if (settingsResponse.ok) {
        const data = await settingsResponse.json();
        settingsData = data.settings;
      }

      // Update server, settings, and isLoading in a single state update
      setState((prev) => ({ 
        ...prev, 
        server: serverData,
        settings: settingsData,
        isLoading: false
      }));

      // Fetch remaining data in parallel (doesn't affect routing)
      await Promise.allSettled([
        refreshPlaylists(),
        refreshSchedules(),
        refreshMissingTracksCount(),
      ]);
    } catch (error) {
      console.error('Failed to refresh app data:', error);
      setState((prev) => ({ ...prev, isLoading: false }));
    }
  };

  const value: AppContextType = {
    ...state,
    apiClient,
    setServer,
    updateSettings,
    refreshPlaylists,
    refreshSchedules,
    refreshMissingTracksCount,
    refreshSettings,
    refreshAll,
    installUpdate,
  };

  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
};
