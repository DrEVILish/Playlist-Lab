import type { FC } from 'react';
import { useState, useEffect, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import { useApp } from '../contexts/AppContext';
import type { MatchedTrack } from '@playlist-lab/shared';
import { ImportFromPlexHome } from '../components/ImportFromPlexHome';
import { Modal, modalCloseButtonStyle } from '../components/Modal';
import { useEscapeKey } from '../hooks/useEscapeKey';
import { useToast } from '../contexts/ToastContext';

type ImportSource = 'spotify' | 'deezer' | 'apple' | 'tidal' | 'youtube' | 'amazon' | 'qobuz' | 'listenbrainz' | 'file' | 'ai' | 'aria' | 'billboard' | 'lastfm' | 'plexhome';

interface ImportResult {
  matched: MatchedTrack[];
  unmatched: MatchedTrack[];
  playlistName: string;
  coverUrl?: string;
}

interface PopularPlaylist {
  name: string;
  url: string;
  description?: string;
  videoCount?: number;
  count?: number;
  trackCount?: number;
  owner?: string;
  imageUrl?: string;
}

export const ImportPage: FC = () => {
  const { apiClient, refreshPlaylists, refreshMissingTracksCount, settings } = useApp();
  const toast = useToast();
  const navigate = useNavigate();
  const [activeSource, setActiveSource] = useState<ImportSource>(() => {
    const saved = localStorage.getItem('selectedCountry');
    return saved === 'AU' ? 'aria' : 'deezer';
  });
  const [url, setUrl] = useState('');
  const [username, setUsername] = useState('');
  // The identifier actually used to scrape the current import - captured from
  // handleImport()'s urlToImport/username, since many callers (chart/popular
  // buttons) pass the URL directly as a handleImport() argument without ever
  // writing it into `url`/`username` state, which confirm/save-missing used
  // to read from directly and would see stale or empty values.
  const [importedSourceIdentifier, setImportedSourceIdentifier] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [aiPrompt, setAiPrompt] = useState('');
  const [aiTrackCount, setAiTrackCount] = useState(25);
  const [geminiApiKey, setGrokApiKey] = useState('');
  const [isImporting, setIsImporting] = useState(false);
  const [importResult, setImportResult] = useState<ImportResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [playlistName, setPlaylistName] = useState('');
  const [previewPlaylist, setPreviewPlaylist] = useState<PopularPlaylist | null>(null);
  const [previewTracks, setPreviewTracks] = useState<any[] | null>(null);
  const [isLoadingPreview, setIsLoadingPreview] = useState(false);
  const [schedulePlaylist, setSchedulePlaylist] = useState<PopularPlaylist | null>(null);
  const [scheduleFrequency, setScheduleFrequency] = useState<'daily' | 'weekly' | 'fortnightly' | 'monthly' | 'custom'>('daily');
  const [scheduleStartDate, setScheduleStartDate] = useState<string>(() => {
    const today = new Date();
    const year = today.getFullYear();
    const month = String(today.getMonth() + 1).padStart(2, '0');
    const day = String(today.getDate()).padStart(2, '0');
    return `${year}-${month}-${day}`;
  });
  const [schedulePlaylistName, setSchedulePlaylistName] = useState<string>('');
  const [scheduleOverwriteExisting, setScheduleOverwriteExisting] = useState<boolean>(false);
  const [scheduleOverwriteCover, setScheduleOverwriteCover] = useState<boolean>(false);
  const [scheduleRunTime, setScheduleRunTime] = useState<string>('09:00');
  // Ticked in the Import modal to also create a recurring schedule, instead
  // of a one-time import - unticked (the default) just imports once.
  const [scheduleEnabled, setScheduleEnabled] = useState<boolean>(false);
  const [spotifyProfilePlaylists, setSpotifyProfilePlaylists] = useState<PopularPlaylist[]>([]);
  const [isLoadingSpotifyProfile, setIsLoadingSpotifyProfile] = useState(false);
  const [spotifyProfileName, setSpotifyProfileName] = useState('');
  // Saved Spotify users state
  const [savedSpotifyUsers, setSavedSpotifyUsers] = useState<Array<{id: number; spotify_user_id: string; display_name: string}>>([]);
  const [newSpotifyUsername, setNewSpotifyUsername] = useState('');
  const [isAddingSpotifyUser, setIsAddingSpotifyUser] = useState(false);
  const [selectedSavedUser, setSelectedSavedUser] = useState<string | null>(null);
  // Favorites state
  const [favoritePlaylists, setFavoritePlaylists] = useState<Array<{id: number; name: string; source: string; source_url: string; description: string | null; image_url: string | null}>>([]);
  const [favoriteUrls, setFavoriteUrls] = useState<Set<string>>(new Set());
  const [searchResults, setSearchResults] = useState<PopularPlaylist[]>([]);
  const [isSearching, setIsSearching] = useState(false);
  const [selectedCountry, setSelectedCountry] = useState<string>('US');
  const [dynamicPlaylists, setDynamicPlaylists] = useState<PopularPlaylist[]>([]);
  const [isLoadingDynamicPlaylists, setIsLoadingDynamicPlaylists] = useState(false);
  // Review screen state
  const [editableTracks, setEditableTracks] = useState<MatchedTrack[]>([]);
  const [showUnmatchedOnly, setShowUnmatchedOnly] = useState(false);
  const [overwriteExisting, setOverwriteExisting] = useState(false);
  const [keepExistingCover, setKeepExistingCover] = useState(true);
  const [rematchTrack, setRematchTrack] = useState<{ track: MatchedTrack; index: number } | null>(null);
  const [rematchQuery, setRematchQuery] = useState('');
  const [rematchResults, setRematchResults] = useState<any[]>([]);
  const [isSearchingRematch, setIsSearchingRematch] = useState(false);
  const [draggedIndex, setDraggedIndex] = useState<number | null>(null);

  // ARIA chart date states
  const toLocalDateStr = (d: Date) => {
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${y}-${m}-${day}`;
  };
  const formatDateDMY = (iso: string) => {
    const [y, m, d] = iso.split('-');
    return `${d}/${m}/${y}`;
  };
  const getRecentMondays = (count: number) => {
    const mondays: string[] = [];
    const d = new Date();
    const day = d.getDay();
    
    // If it's Friday, Saturday, or Sunday, start with next Monday
    if (day === 5 || day === 6 || day === 0) {
      const daysUntilMonday = day === 0 ? 1 : 8 - day;
      d.setDate(d.getDate() + daysUntilMonday);
    } else {
      // Otherwise start with most recent Monday
      const diff = day === 0 ? 6 : day - 1;
      d.setDate(d.getDate() - diff);
    }
    
    for (let i = 0; i < count; i++) {
      mondays.push(toLocalDateStr(d));
      d.setDate(d.getDate() - 7);
    }
    return mondays;
  };
  const recentMondays = getRecentMondays(52);
  const [ariaTop50Date, setAriaTop50Date] = useState('latest');
  const [_ariaAlbumsDate] = useState('latest');
  const [ariaAustralianDate, setAriaAustralianDate] = useState('latest');
  const [ariaReplayDate, setAriaReplayDate] = useState('latest');
  const [ariaNewMusicDate, setAriaNewMusicDate] = useState('latest');
  const [ariaHipHopDate, setAriaHipHopDate] = useState('latest');
  const [ariaDanceDate, setAriaDanceDate] = useState('latest');
  const [ariaClubDate, setAriaClubDate] = useState('latest');
  const [ariaTop100Year, setAriaTop100Year] = useState(() => new Date().getFullYear().toString());

  // Billboard charts are dated for Saturdays (but released on Tuesdays)
  const getPastSaturdays = (count: number) => {
    const saturdays: string[] = [];
    const d = new Date();
    const day = d.getDay();
    
    // Start with most recent Saturday (including today if today is Saturday)
    if (day === 6) {
      // Today is Saturday, use today
    } else {
      // Go back to last Saturday
      const daysBack = day === 0 ? 1 : day + 1;
      d.setDate(d.getDate() - daysBack);
    }
    
    for (let i = 0; i < count; i++) {
      saturdays.push(toLocalDateStr(d));
      d.setDate(d.getDate() - 7);
    }
    return saturdays;
  };
  const recentSaturdays = getPastSaturdays(52);

  // Billboard date state
  const [billboardDate, setBillboardDate] = useState('latest');

  // Country options for charts/popular playlists
  const countries = [
    { code: 'US', name: 'United States', flag: '🇺🇸' },
    { code: 'GB', name: 'United Kingdom', flag: '🇬🇧' },
    { code: 'CA', name: 'Canada', flag: '🇨🇦' },
    { code: 'AU', name: 'Australia', flag: '🇦🇺' },
    { code: 'DE', name: 'Germany', flag: '🇩🇪' },
    { code: 'FR', name: 'France', flag: '🇫🇷' },
    { code: 'ES', name: 'Spain', flag: '🇪🇸' },
    { code: 'IT', name: 'Italy', flag: '🇮🇹' },
    { code: 'BR', name: 'Brazil', flag: '🇧🇷' },
    { code: 'MX', name: 'Mexico', flag: '🇲🇽' },
    { code: 'JP', name: 'Japan', flag: '🇯🇵' },
    { code: 'KR', name: 'South Korea', flag: '🇰🇷' },
    { code: 'IN', name: 'India', flag: '🇮🇳' },
    { code: 'NL', name: 'Netherlands', flag: '🇳🇱' },
    { code: 'SE', name: 'Sweden', flag: '🇸🇪' },
    { code: 'NO', name: 'Norway', flag: '🇳🇴' },
    { code: 'PL', name: 'Poland', flag: '🇵🇱' },
    { code: 'AR', name: 'Argentina', flag: '🇦🇷' },
    { code: 'CL', name: 'Chile', flag: '🇨🇱' },
    { code: 'NZ', name: 'New Zealand', flag: '🇳🇿' },
  ];

  // Check if Grok API key is set in settings
  const hasGeminiApiKey = !!settings?.grokApiKey;

  // Load saved country preference
  useEffect(() => {
    const savedCountry = localStorage.getItem('selectedCountry');
    if (savedCountry) {
      setSelectedCountry(savedCountry);
    }
  }, []);

  // Load saved Spotify users when Spotify is selected
  useEffect(() => {
    if (activeSource === 'spotify') {
      loadSavedSpotifyUsers();
    }
  }, [activeSource]);

  // Load favorites on mount
  useEffect(() => {
    loadFavorites();
  }, []);

  const loadSavedSpotifyUsers = async () => {
    try {
      const response = await fetch('/api/saved-spotify-users', { credentials: 'include' });
      if (response.ok) {
        const data = await response.json();
        const users = data.users || [];
        setSavedSpotifyUsers(users);
        
        // Auto-refresh display names that look invalid (e.g., "Spotify - Web Player", "Choose a language")
        const invalidPatterns = /^(spotify[\s\-–]|web player|choose|sprache|langue|idioma)/i;
        for (const user of users) {
          if (invalidPatterns.test(user.display_name) || user.display_name === user.spotify_user_id) {
            fetch(`/api/saved-spotify-users/${user.id}/refresh`, {
              method: 'PATCH',
              credentials: 'include',
            }).then(res => {
              if (res.ok) return res.json();
              return null;
            }).then(result => {
              if (result?.displayName) {
                // Update local state immediately with the resolved name
                setSavedSpotifyUsers(prev => prev.map(u => 
                  u.id === user.id ? { ...u, display_name: result.displayName } : u
                ));
                // Also update profile name if this user is currently selected
                if (selectedSavedUser === user.spotify_user_id) {
                  setSpotifyProfileName(result.displayName);
                }
              }
            }).catch(() => {});
          }
        }
      }
    } catch (err) {
      console.error('Failed to load saved Spotify users', err);
    }
  };

  const addSavedSpotifyUser = async () => {
    if (!newSpotifyUsername.trim()) return;
    
    setIsAddingSpotifyUser(true);
    setError(null);
    try {
      // Extract username from URL if needed
      let username = newSpotifyUsername.trim();
      const urlMatch = username.match(/open\.spotify\.com\/user\/([a-zA-Z0-9_-]+)/);
      if (urlMatch) {
        username = urlMatch[1];
      }
      // Remove @ prefix if present
      username = username.replace(/^@/, '');
      
      const response = await fetch('/api/saved-spotify-users', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ spotifyUserId: username, displayName: username }),
      });
      
      if (response.ok) {
        setNewSpotifyUsername('');
        await loadSavedSpotifyUsers();
      } else {
        const errorData = await response.json().catch(() => ({}));
        setError(errorData?.error?.message || 'Failed to add Spotify user');
      }
    } catch (err) {
      setError('Failed to add Spotify user');
    } finally {
      setIsAddingSpotifyUser(false);
    }
  };

  const removeSavedSpotifyUser = async (id: number) => {
    try {
      const response = await fetch(`/api/saved-spotify-users/${id}`, {
        method: 'DELETE',
        credentials: 'include',
      });
      if (response.ok) {
        setSavedSpotifyUsers(prev => prev.filter(u => u.id !== id));
        // Clear selection if removed user was selected
        const removedUser = savedSpotifyUsers.find(u => u.id === id);
        if (removedUser && selectedSavedUser === removedUser.spotify_user_id) {
          setSelectedSavedUser(null);
          setSpotifyProfilePlaylists([]);
          setSpotifyProfileName('');
        }
      }
    } catch (err) {
      setError('Failed to remove saved user');
    }
  };

  const selectSavedUser = async (spotifyUserId: string) => {
    if (selectedSavedUser === spotifyUserId) {
      // Deselect
      setSelectedSavedUser(null);
      setSpotifyProfilePlaylists([]);
      setSpotifyProfileName('');
      return;
    }
    
    setSelectedSavedUser(spotifyUserId);
    setIsLoadingSpotifyProfile(true);
    setSpotifyProfilePlaylists([]);
    
    // Use the saved user's display_name immediately (not the scraper's response)
    const savedUser = savedSpotifyUsers.find(u => u.spotify_user_id === spotifyUserId);
    const validName = savedUser?.display_name && !/^(choose|spotify|web player|sprache|langue)/i.test(savedUser.display_name)
      ? savedUser.display_name : spotifyUserId;
    setSpotifyProfileName(validName);
    setError(null);
    
    try {
      const response = await fetch(
        `/api/import/spotify/user/${encodeURIComponent(spotifyUserId)}/playlists`,
        { credentials: 'include' }
      );
      
      if (response.ok) {
        const data = await response.json();
        const playlists: PopularPlaylist[] = (data.playlists || []).map((p: any) => ({
          name: p.name,
          url: p.id ? `https://open.spotify.com/playlist/${p.id}` : p.url,
          description: p.trackCount ? `${p.trackCount} tracks` : undefined,
          imageUrl: p.coverUrl,
        }));
        setSpotifyProfilePlaylists(playlists);
        // Only update name from scraper if it's valid
        const scraperName = data.displayName;
        if (scraperName && !/^(choose|spotify[\s\-–]|web player|sprache|langue)/i.test(scraperName)) {
          setSpotifyProfileName(scraperName);
        }
      } else {
        const errorData = await response.json().catch(() => ({}));
        setError(errorData?.error?.message || 'Failed to load playlists');
      }
    } catch (err) {
      setError('Failed to load playlists');
    } finally {
      setIsLoadingSpotifyProfile(false);
    }
  };

  // Favorites management
  const loadFavorites = async () => {
    try {
      const response = await fetch('/api/favorite-playlists', { credentials: 'include' });
      if (response.ok) {
        const data = await response.json();
        const favs = data.favorites || [];
        setFavoritePlaylists(favs);
        setFavoriteUrls(new Set(favs.map((f: any) => f.source_url)));
      }
    } catch (err) {
      console.error('Failed to load favorites', err);
    }
  };

  const toggleFavorite = async (playlist: PopularPlaylist) => {
    const isFav = favoriteUrls.has(playlist.url);
    
    // Optimistically update
    if (isFav) {
      setFavoriteUrls(prev => { const next = new Set(prev); next.delete(playlist.url); return next; });
      setFavoritePlaylists(prev => prev.filter(f => f.source_url !== playlist.url));
    } else {
      const tempFav = {
        id: -1,
        name: playlist.name,
        source: activeSource,
        source_url: playlist.url,
        description: playlist.description || null,
        image_url: playlist.imageUrl || null,
      };
      setFavoriteUrls(prev => new Set(prev).add(playlist.url));
      setFavoritePlaylists(prev => [tempFav, ...prev]);
    }
    
    try {
      if (isFav) {
        await fetch('/api/favorite-playlists', {
          method: 'DELETE',
          headers: { 'Content-Type': 'application/json' },
          credentials: 'include',
          body: JSON.stringify({ sourceUrl: playlist.url }),
        });
      } else {
        const response = await fetch('/api/favorite-playlists', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          credentials: 'include',
          body: JSON.stringify({
            name: playlist.name,
            source: activeSource,
            sourceUrl: playlist.url,
            description: playlist.description,
            imageUrl: playlist.imageUrl,
          }),
        });
        if (response.ok) {
          // Update with real ID from server
          const data = await response.json();
          setFavoritePlaylists(prev => prev.map(f => 
            f.source_url === playlist.url && f.id === -1 
              ? { ...f, id: data.favorite.id } 
              : f
          ));
        }
      }
    } catch (err) {
      console.error('Failed to toggle favorite', err);
      // Revert on error
      await loadFavorites();
    }
  };

  const isFavorited = (url: string) => favoriteUrls.has(url);

  useEffect(() => {
    // Only fetch for sources that support dynamic playlists
    if (['deezer', 'youtube', 'apple', 'spotify'].includes(activeSource)) {
      fetchDynamicPlaylists();
    } else {
      setDynamicPlaylists([]);
    }
  }, [activeSource, selectedCountry]);

  const fetchDynamicPlaylists = async (forceRefresh = false) => {
    // Check cache first (unless force refresh)
    const cacheKey = `playlists_${activeSource}_${selectedCountry}`;
    
    if (!forceRefresh) {
      const cached = localStorage.getItem(cacheKey);
      
      if (cached) {
        try {
          const { playlists, timestamp } = JSON.parse(cached);
          const now = Date.now();
          const oneDay = 24 * 60 * 60 * 1000;
          
          // Use cache if less than 1 day old AND has results
          if (now - timestamp < oneDay && playlists.length > 0) {
            setDynamicPlaylists(playlists);
            return;
          }
        } catch (err) {
          console.error('Failed to parse cached playlists:', err);
        }
      }
    }
    
    // Fetch fresh data
    setIsLoadingDynamicPlaylists(true);
    try {
      const response = await fetch(`/api/charts/${activeSource}/${selectedCountry}`, {
        credentials: 'include',
      });
      
      if (response.ok) {
        const data = await response.json();
        const playlists = data.playlists || [];
        setDynamicPlaylists(playlists);
        
        // Cache the results (only if non-empty to avoid caching failures)
        if (playlists.length > 0) {
          localStorage.setItem(cacheKey, JSON.stringify({
            playlists,
            timestamp: Date.now(),
          }));
        } else {
          // Clear any stale empty cache so next load retries
          localStorage.removeItem(cacheKey);
        }
      } else {
        const errorData = await response.json().catch(() => null);
        console.error('Failed to fetch dynamic playlists', errorData);
        setDynamicPlaylists([]);
      }
    } catch (err) {
      console.error('Failed to fetch dynamic playlists:', err);
      setDynamicPlaylists([]);
    } finally {
      setIsLoadingDynamicPlaylists(false);
    }
  };

  // Save country preference when changed
  const handleCountryChange = (countryCode: string) => {
    setSelectedCountry(countryCode);
    localStorage.setItem('selectedCountry', countryCode);
    if (countryCode === 'AU') {
      setActiveSource('aria');
    } else if (activeSource === 'aria') {
      setActiveSource('deezer');
    }
  };

  const sources = [
    ...(selectedCountry === 'AU' ? [{ id: 'aria' as const, name: 'ARIA Charts', placeholder: 'https://www.aria.com.au/charts/...' }] : []),
    { id: 'billboard' as const, name: 'Billboard', placeholder: 'https://www.billboard.com/charts/...' },
    { id: 'lastfm' as const, name: 'Last.fm', placeholder: 'https://www.last.fm/...' },
    { id: 'deezer' as const, name: 'Deezer', placeholder: 'Playlist ID or URL' },
    { id: 'youtube' as const, name: 'YouTube Music', placeholder: 'https://music.youtube.com/playlist?list=...' },
    { id: 'spotify' as const, name: 'Spotify', placeholder: 'https://open.spotify.com/playlist/...' },
    { id: 'apple' as const, name: 'Apple Music', placeholder: 'https://music.apple.com/...' },
    { id: 'amazon' as const, name: 'Amazon Music', placeholder: 'https://music.amazon.com/playlists/...' },
    { id: 'tidal' as const, name: 'Tidal', placeholder: 'https://tidal.com/browse/playlist/...' },
    { id: 'qobuz' as const, name: 'Qobuz', placeholder: 'https://www.qobuz.com/...' },
    { id: 'listenbrainz' as const, name: 'ListenBrainz', placeholder: 'Username' },
    { id: 'file' as const, name: 'File (M3U/PLS/XSPF/CSV)', placeholder: 'Upload an M3U, M3U8, PLS, XSPF, or CSV playlist file' },
    { id: 'ai' as const, name: 'AI Generated', placeholder: 'Describe the playlist you want...' },
    { id: 'plexhome' as const, name: 'Plex Home Users', placeholder: 'Copy a playlist from another Plex Home user' },
  ];


  const currentSource = sources.find(s => s.id === activeSource);
  // Use dynamic playlists for supported sources, empty for Spotify (no login)
  const currentPopular = ['deezer', 'youtube', 'apple'].includes(activeSource) 
      ? dynamicPlaylists 
      : [];

  // Load playlists from a Spotify user profile URL
  const loadSpotifyProfilePlaylists = async (spotifyUserId: string) => {
    setIsLoadingSpotifyProfile(true);
    setSpotifyProfilePlaylists([]);
    setSpotifyProfileName('');
    setError(null);
    
    try {
      const response = await fetch(
        `/api/import/spotify/user/${encodeURIComponent(spotifyUserId)}/playlists`,
        { credentials: 'include' }
      );
      
      if (response.ok) {
        const data = await response.json();
        const playlists: PopularPlaylist[] = (data.playlists || []).map((p: any) => ({
          name: p.name,
          url: p.id ? `https://open.spotify.com/playlist/${p.id}` : p.url,
          description: p.trackCount ? `${p.trackCount} tracks` : undefined,
          imageUrl: p.coverUrl,
        }));
        setSpotifyProfilePlaylists(playlists);
        setSpotifyProfileName(data.displayName || spotifyUserId);
      } else {
        const errorData = await response.json().catch(() => ({}));
        setError(errorData?.error?.message || 'Failed to load Spotify user playlists');
        setSpotifyProfilePlaylists([]);
      }
    } catch (err) {
      console.error('Failed to load Spotify profile playlists', err);
      setError('Failed to load Spotify user playlists');
      setSpotifyProfilePlaylists([]);
    } finally {
      setIsLoadingSpotifyProfile(false);
    }
  };

  const handleSearch = async () => {
    if (!url.trim()) {
      setSearchResults([]);
      return;
    }

    // Spotify uses URL-based import (no search)
    if (activeSource === 'spotify') {
      return;
    }

    setIsSearching(true);
    setError(null);
    setSearchResults([]);

    try {
      // Call backend search endpoint
      const response = await fetch('/api/import/search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ 
          source: activeSource,
          query: url.trim()
        }),
      });

      if (!response.ok) {
        const errorData = await response.json();
        throw new Error(errorData.error?.message || 'Search failed');
      }

      const data = await response.json();
      setSearchResults(data.playlists || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Search failed');
      setSearchResults([]);
    } finally {
      setIsSearching(false);
    }
  };

  // Every import (URL paste, chart/popular click, file upload, AI
  // generation) is now a fire-and-forget background job: the server scrapes,
  // matches, and creates the Plex playlist automatically, and progress shows
  // up in the notification bell (see runNewImportAndFinalize's docs in
  // services/import.ts) - no full-screen progress overlay, no "review
  // matches, then confirm" step.
  const handleImport = async (importUrl?: string, customName?: string) => {
    setError(null);
    setIsImporting(true);

    // Use provided URL or fall back to state
    const urlToImport = importUrl || url;
    setImportedSourceIdentifier(activeSource === 'listenbrainz' ? username : urlToImport);

    // Required by the server's import-queue (job id), even though nothing
    // on the client listens for its progress anymore.
    const sessionId = `import-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;

    try {
      if (activeSource === 'ai') {
        const response = await fetch('/api/import/ai', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          credentials: 'include',
          body: JSON.stringify({ prompt: aiPrompt, trackCount: aiTrackCount, grokApiKey: geminiApiKey }),
        });
        if (!response.ok) {
          const errorData = await response.json().catch(() => ({}));
          const errorMessage = errorData.error?.message || 'AI generation failed';
          throw new Error(errorMessage.includes('No library selected')
            ? 'Please go to Settings and select a music library before using AI generation.'
            : errorMessage);
        }
        return;
      }

      if (activeSource === 'file') {
        if (!file) throw new Error('Please select a file');
        const formData = new FormData();
        formData.append('file', file);
        const response = await fetch('/api/import/file', {
          method: 'POST',
          credentials: 'include',
          body: formData,
        });
        if (!response.ok) {
          const errorData = await response.json().catch(() => ({}));
          throw new Error(errorData.error?.message || errorData.error || 'File import failed');
        }
        return;
      }

      let endpoint = '';
      const body: any = { sessionId };
      if (customName?.trim()) body.customName = customName.trim();
      switch (activeSource) {
        case 'spotify': endpoint = '/api/import/spotify'; body.url = urlToImport; break;
        case 'deezer': endpoint = '/api/import/deezer'; body.url = urlToImport; break;
        case 'apple': endpoint = '/api/import/apple'; body.url = urlToImport; break;
        case 'tidal': endpoint = '/api/import/tidal'; body.url = urlToImport; break;
        case 'youtube': endpoint = '/api/import/youtube'; body.url = urlToImport; break;
        case 'amazon': endpoint = '/api/import/amazon'; body.url = urlToImport; break;
        case 'qobuz': endpoint = '/api/import/qobuz'; body.url = urlToImport; break;
        case 'aria': endpoint = '/api/import/aria'; body.url = urlToImport; break;
        case 'billboard': endpoint = '/api/import/billboard'; body.url = urlToImport; break;
        case 'lastfm': endpoint = '/api/import/lastfm'; body.url = urlToImport; break;
        case 'listenbrainz': endpoint = '/api/import/listenbrainz'; body.username = username; break;
        default: throw new Error('Invalid source');
      }

      const response = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify(body),
      });
      if (!response.ok) {
        const errorData = await response.json().catch(() => ({}));
        throw new Error(errorData.error?.message || errorData.error || 'Import failed');
      }
      const responseData = await response.json();
      // Only worth saying when the import is NOT starting now: a job that
      // starts immediately posts its own live notification, but one sitting
      // in the queue has nothing to show until it gets its turn, so without
      // this the button would look like it did nothing.
      const position = responseData.position || 0;
      if (position > 0) {
        toast.success(`Import queued — ${position} ahead of it`);
      }
    } catch (err) {
      console.error('[ImportPage] Import error:', err);
      setError(err instanceof Error ? err.message : 'Import failed');
    } finally {
      setIsImporting(false);
    }
  };

  const handleCancel = () => {
    setImportResult(null);
    setEditableTracks([]);
    setShowUnmatchedOnly(false);
    setOverwriteExisting(false);
    setKeepExistingCover(true);
    setError(null);
    setPlaylistCreated(false);
    setCreatedPlaylistId(null);
  };

  // Initialize editable tracks when import result changes
  useEffect(() => {
    if (importResult) {
      const allTracks = [...importResult.matched, ...importResult.unmatched];
      setEditableTracks(allTracks);
    }
  }, [importResult]);

  // Manual rematch functions
  const handleOpenRematch = (track: MatchedTrack, index: number) => {
    setRematchTrack({ track, index });
    setRematchQuery(`${track.artist} ${track.title}`);
    setRematchResults([]);
  };

  const handleCloseRematch = () => {
    setRematchTrack(null);
    setRematchQuery('');
    setRematchResults([]);
  };

  // Track whether mousedown started on the backdrop
  const backdropMouseDown = useRef(false);

  useEscapeKey(!!rematchTrack, handleCloseRematch);

  const handleSearchRematch = async () => {
    if (!rematchQuery.trim()) return;

    setIsSearchingRematch(true);
    try {
      const response = await fetch('/api/import/plex/search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({
          query: rematchQuery,
          originalTitle: rematchTrack?.track.title,
          originalArtist: rematchTrack?.track.artist,
        }),
      });

      if (!response.ok) {
        throw new Error('Search failed');
      }

      const data = await response.json();
      setRematchResults(data.tracks || []);
    } catch (err) {
      console.error('Search error:', err);
      setRematchResults([]);
    } finally {
      setIsSearchingRematch(false);
    }
  };

  const handleSelectRematch = (result: any) => {
    if (!rematchTrack) return;

    const updatedTracks = [...editableTracks];
    updatedTracks[rematchTrack.index] = {
      ...rematchTrack.track,
      matched: true,
      plexRatingKey: result.ratingKey,
      plexTitle: result.title,
      plexArtist: result.artist,
      plexAlbum: result.album,
      plexCodec: result.codec,
      plexBitrate: result.bitrate,
      score: 100, // Manual match gets 100% score
      manuallyMatched: true,
    };

    setEditableTracks(updatedTracks);
    handleCloseRematch();
  };

  // Drag and drop functions
  const handleDragStart = (index: number) => {
    setDraggedIndex(index);
  };

  const handleDragOver = (e: React.DragEvent, index: number) => {
    e.preventDefault();
    if (draggedIndex === null || draggedIndex === index) return;

    const updatedTracks = [...editableTracks];
    const draggedTrack = updatedTracks[draggedIndex];
    updatedTracks.splice(draggedIndex, 1);
    updatedTracks.splice(index, 0, draggedTrack);

    setEditableTracks(updatedTracks);
    setDraggedIndex(index);
  };

  const handleDragEnd = () => {
    setDraggedIndex(null);
  };

  // Native HTML5 drag-and-drop (above) never fires on touch devices - this
  // gives mobile a tap-to-move equivalent instead of losing reordering.
  const moveTrack = (index: number, direction: -1 | 1) => {
    const targetIndex = index + direction;
    if (targetIndex < 0 || targetIndex >= editableTracks.length) return;
    const updatedTracks = [...editableTracks];
    const [moved] = updatedTracks.splice(index, 1);
    updatedTracks.splice(targetIndex, 0, moved);
    setEditableTracks(updatedTracks);
  };

  // Export missing tracks
  const handleExportMissing = () => {
    const unmatchedTracks = editableTracks.filter(t => !t.matched);
    if (unmatchedTracks.length === 0) return;

    const csv = [
      'Title,Artist,Album',
      ...unmatchedTracks.map(t => `"${t.title}","${t.artist}","${t.album || ''}"`)
    ].join('\n');

    const blob = new Blob([csv], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${playlistName || 'playlist'}_missing_tracks.csv`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  // State for playlist name edit before saving missing tracks
  const [showMissingNameModal, setShowMissingNameModal] = useState(false);
  const [missingPlaylistName, setMissingPlaylistName] = useState('');

  // Add missing tracks to Missing Tracks list (and create Plex playlist with matched tracks)
  const handleAddToMissingTracks = async () => {
    const unmatchedTracks = editableTracks.filter(t => !t.matched);
    if (unmatchedTracks.length === 0) return;

    // Show modal to let user edit playlist name before saving
    setMissingPlaylistName(playlistName || 'Imported Playlist');
    setShowMissingNameModal(true);
  };

  const handleConfirmMissingSave = async () => {
      const unmatchedTracks = editableTracks.filter(t => !t.matched);
      setShowMissingNameModal(false);

      try {
        if (playlistCreated && createdPlaylistId) {
          // Playlist already exists in Plex — just add missing tracks to it
          await apiClient.addMissingTracks({
            playlistId: createdPlaylistId,
            tracks: unmatchedTracks.map(t => ({
              title: t.title,
              artist: t.artist,
              album: t.album || '',
            })),
            source: activeSource === 'ai' ? 'ai' : activeSource,
          });
        } else {
          // No playlist created yet — create one with matched tracks + save missing
          const matchedTracks = editableTracks.filter(t => t.matched && t.plexRatingKey);
          await apiClient.saveMissingTracks({
            playlistName: missingPlaylistName || 'Imported Playlist',
            source: activeSource === 'ai' ? 'ai' : activeSource,
            sourceUrl: activeSource === 'ai' ? `AI: ${aiPrompt}` : importedSourceIdentifier,
            tracks: unmatchedTracks.map(t => ({
              title: t.title,
              artist: t.artist,
              album: t.album || '',
            })),
            matchedTracks: matchedTracks.map(t => ({
              plexRatingKey: t.plexRatingKey,
              title: t.title,
              artist: t.artist,
            })),
            overwriteExisting,
            keepExistingCover: overwriteExisting ? keepExistingCover : undefined,
            coverUrl: importResult?.coverUrl,
          });
        }

        await refreshPlaylists();
        await refreshMissingTracksCount();

        // Missing tracks now live inline on Home (MissingTracksPanel), not a
        // dedicated /missing route - navigate there instead of hard-reloading
        // into a route that no longer exists.
        toast.success(`Saved — ${unmatchedTracks.length} track${unmatchedTracks.length === 1 ? '' : 's'} added to Missing Tracks`);
        navigate('/');
      } catch (error: any) {
        console.error('Failed to save missing tracks:', error);
        toast.error(`Failed to save missing tracks: ${error.message || 'Unknown error'}`);
      }
    };

  // Update handleConfirm to use editableTracks
  const [playlistCreated, setPlaylistCreated] = useState(false);
  const [createdPlaylistId, setCreatedPlaylistId] = useState<number | null>(null);

  const handleConfirmUpdated = async () => {
    if (!editableTracks.length) return;

    setIsImporting(true);
    setError(null);

    try {
      const matchedTracks = editableTracks.filter(t => t.matched);
      
      const result = await apiClient.confirmImport({
        playlistName,
        source: activeSource === 'ai' ? 'ai' : activeSource,
        sourceUrl: activeSource === 'ai' ? `AI: ${aiPrompt}` : importedSourceIdentifier,
        tracks: matchedTracks,
        overwriteExisting,
        keepExistingCover: overwriteExisting ? keepExistingCover : undefined,
        coverUrl: importResult?.coverUrl,
      });

      await refreshPlaylists();
      await refreshMissingTracksCount();

      // Stay on review screen so user can save missing tracks
      setPlaylistCreated(true);
      setCreatedPlaylistId(result?.id ?? null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create playlist');
    } finally {
      setIsImporting(false);
    }
  };

  // Get match score color
  const getScoreColor = (score?: number): string => {
    if (!score) return 'var(--error)';
    if (score === 100) return 'var(--success)';
    if (score >= 90) return '#90EE90';
    if (score >= 80) return '#FFD700';
    if (score >= 70) return '#FFA500';
    return 'var(--warning)';
  };

  // Filter tracks based on showUnmatchedOnly
  const displayedTracks = showUnmatchedOnly 
    ? editableTracks.filter(t => !t.matched)
    : editableTracks;

  const matchedCount = editableTracks.filter(t => t.matched).length;
  const unmatchedCount = editableTracks.filter(t => !t.matched).length;

  const handlePopularClick = (playlist: PopularPlaylist) => {
    // Trigger import immediately with the URL directly
    handleImport(playlist.url);
  };

  const handlePreview = async (playlist: PopularPlaylist) => {
    setPreviewPlaylist(playlist);
    setIsLoadingPreview(true);
    setPreviewTracks(null);
    setError(null);
    
    try {
      // Fetch playlist preview without importing
      const response = await fetch(`/api/import/preview`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ 
          source: activeSource,
          url: playlist.url 
        }),
      });
      
      const data = await response.json();
      
      if (!response.ok) {
        // Handle preview errors - show empty tracks with error message
        setPreviewTracks([]);
        setError(data.error?.message || data.message || 'Failed to load preview');
      } else {
        // Check if we got an error in the response (empty preview case)
        if (data.error) {
          setPreviewTracks([]);
          setError(data.error);
        } else {
          setPreviewTracks(data.tracks || []);
          setError(null);
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load preview');
      setPreviewTracks([]);
    } finally {
      setIsLoadingPreview(false);
    }
  };

  // Opens the single "Import" modal for a chart/popular/favorite/search
  // entry - lets the user rename the playlist before importing, and
  // optionally tick "Set up a recurring schedule" instead of a one-time
  // import.
  const handleSchedule = (playlist: PopularPlaylist) => {
    setSchedulePlaylist(playlist);
    setSchedulePlaylistName(playlist.name); // Set default playlist name
    setScheduleOverwriteExisting(false); // Reset overwrite option
    setScheduleOverwriteCover(false); // Reset cover art option
    setScheduleRunTime('09:00'); // Reset run time
    setScheduleEnabled(false); // Default to a one-time import
  };

  // The main "Import Playlist" button (URL/username paste, not AI or file
  // upload) also opens the same modal, rather than importing immediately -
  // one "Import" entry point everywhere, name-editable, with the schedule
  // tick available. Opens immediately with whatever name is already known
  // rather than awaiting the Spotify preview fetch first - that scrape can
  // take several seconds, and blocking modal-open on it made the button look
  // unresponsive. The preview name (when needed) fills in afterwards.
  const openImportModal = () => {
    if (activeSource === 'listenbrainz') {
      handleSchedule({ name: username, url: username });
      return;
    }

    const trimmedUrl = url.trim();
    const name = playlistName;
    handleSchedule({ name, url: trimmedUrl });

    if (!name && activeSource === 'spotify') {
      // Name only - asking /preview for this scraped the whole playlist and
      // left the field on "Fetching name…" for 30s or more.
      fetch('/api/import/playlist-name', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ url: trimmedUrl }),
        signal: AbortSignal.timeout(15000),
      })
        .then(response => response.ok ? response.json() : null)
        .then(data => {
          if (!data?.name) return;
          // Only backfill if the user hasn't already typed their own name.
          setSchedulePlaylistName(prev => prev || data.name);
          setSchedulePlaylist(prev => (prev && !prev.name ? { ...prev, name: data.name } : prev));
        })
        .catch(() => { /* Leave blank - server falls back to the scraped name */ });
    }
  };

  const handleScheduleConfirm = async () => {
    if (!schedulePlaylist) return;

    // Unticked: just a normal one-time import under the chosen name, via the
    // same fire-and-forget path every other Import button uses.
    if (!scheduleEnabled) {
      const playlist = schedulePlaylist;
      const name = schedulePlaylistName;
      setSchedulePlaylist(null);
      await handleImport(playlist.url, name);
      return;
    }

    setIsImporting(true);
    setError(null);

    try {
      // Create schedule for chart import
      const response = await fetch('/api/schedules', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({
          schedule_type: 'playlist_refresh',
          frequency: scheduleFrequency,
          start_date: scheduleStartDate,
          config: {
            chartName: schedulePlaylist.name,
            chartSource: activeSource,
            chartUrl: schedulePlaylist.url,
            autoImport: true,
            playlistName: schedulePlaylistName || schedulePlaylist.name,
            // The import modal's "replace existing" tick maps onto the same
            // two modes the schedule editor offers.
            updateMode: scheduleOverwriteExisting ? 'replace' : 'accumulate',
            overwriteCover: scheduleOverwriteCover,
            run_time: scheduleRunTime,
          },
        }),
      });

      if (!response.ok) {
        const errorData = await response.json().catch(() => ({ error: { message: 'Failed to create schedule' } }));
        throw new Error(errorData.error?.message || 'Failed to create schedule');
      }

      toast.success('Schedule created');

      // The schedule's first run waits for its chosen run_time (default
      // 09:00), which could be many hours away - also fire the same
      // one-time import every other Import button uses (it shows its own
      // toast) so the playlist shows up right now instead of the user
      // seeing nothing happen until that time comes around.
      const playlist = schedulePlaylist;
      const name = schedulePlaylistName;
      setSchedulePlaylist(null);
      await handleImport(playlist.url, name);
      navigate('/');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create schedule');
    } finally {
      setIsImporting(false);
    }
  };

  // Plex Home Users is a self-contained "copy their playlist to me" flow,
  // unrelated to the scrape-and-match logic every other source shares below
  // - so it gets its own early return rather than threading a new branch
  // through handleImport() and the rest of this file.
  if (activeSource === 'plexhome') {
    return (
      <div className="page-container">
        <div className="page-header">
          <h1 className="page-title">Import</h1>
        </div>
        <div style={{ marginBottom: '1.5rem' }}>
          <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>Select Source</label>
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
            {sources.map(source => (
              <button
                key={source.id}
                className={`btn ${activeSource === source.id ? 'btn-primary' : 'btn-secondary'}`}
                onClick={() => setActiveSource(source.id)}
              >
                {source.name}
              </button>
            ))}
          </div>
        </div>
        <ImportFromPlexHome />
      </div>
    );
  }

  return (
    <div className="page-container">
      <div className="page-header">
        <h1 className="page-title">Import</h1>
      </div>
      <div style={{ marginBottom: '2rem' }}>
          {/* Country Selector */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
            <label style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
              Country:
            </label>
            <select
              value={selectedCountry}
              onChange={(e) => handleCountryChange(e.target.value)}
              style={{
                padding: '0.5rem 0.75rem',
                border: '1px solid var(--border-color)',
                borderRadius: '4px',
                backgroundColor: 'var(--surface)',
                color: 'var(--text-primary)',
                fontSize: '0.875rem',
                cursor: 'pointer',
              }}
            >
              {countries.map(country => (
                <option key={country.code} value={country.code}>
                  {country.flag} {country.name}
                </option>
              ))}
            </select>
          </div>
      </div>

      {/* Source Selection - at top of page */}
      <div style={{ marginBottom: '2rem' }}>
        <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
          Select Source
        </label>
        <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
          {sources.map(source => (
            <button
              key={source.id}
              className={`btn ${activeSource === source.id ? 'btn-primary' : 'btn-secondary'}`}
              onClick={() => {
                setActiveSource(source.id);
                setUrl('');
                setUsername('');
                setFile(null);
                setError(null);
                setSearchResults([]);
              }}
              disabled={isImporting}
            >
              {source.name}
            </button>
          ))}
        </div>
      </div>

      {/* Spotify Saved Users Section */}
      {activeSource === 'spotify' && (
        <div className="card" style={{ marginBottom: '2rem' }}>
          <h3 style={{ marginTop: 0, marginBottom: '1rem' }}>
            Spotify Users & Playlists
          </h3>
          <p style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '1rem' }}>
            Add Spotify usernames to browse their public playlists. No login required — just paste a username or profile URL.
          </p>
          
          {/* Add User Input */}
          <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
            <input
              type="text"
              value={newSpotifyUsername}
              onChange={(e) => setNewSpotifyUsername(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  addSavedSpotifyUser();
                }
              }}
              placeholder="Spotify username or profile URL"
              disabled={isAddingSpotifyUser}
              style={{
                flex: 1,
                padding: '0.75rem',
                border: '1px solid var(--border-color)',
                borderRadius: '4px',
                backgroundColor: 'var(--surface)',
                color: 'var(--text-primary)',
                fontSize: '0.875rem',
              }}
            />
            <button
              className="btn btn-primary"
              onClick={addSavedSpotifyUser}
              disabled={isAddingSpotifyUser || !newSpotifyUsername.trim()}
              style={{ minWidth: '100px' }}
            >
              {isAddingSpotifyUser ? 'Adding...' : 'Add User'}
            </button>
          </div>
          
          {/* Saved Users Grid */}
          {savedSpotifyUsers.length > 0 && (
            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
              {savedSpotifyUsers.map(user => (
                <div
                  key={user.id}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '0.5rem',
                    padding: '0.5rem 0.75rem',
                    border: `1px solid ${selectedSavedUser === user.spotify_user_id ? 'var(--primary-color)' : 'var(--border-color)'}`,
                    borderRadius: '20px',
                    backgroundColor: selectedSavedUser === user.spotify_user_id ? 'rgba(91, 155, 213, 0.1)' : 'var(--surface)',
                    cursor: 'pointer',
                    transition: 'all 0.2s',
                  }}
                  onClick={() => selectSavedUser(user.spotify_user_id)}
                  title={selectedSavedUser === user.spotify_user_id ? 'Click to deselect' : `Load playlists for ${user.display_name}`}
                >
                  <span style={{
                    fontSize: '0.875rem',
                    fontWeight: selectedSavedUser === user.spotify_user_id ? 600 : 400,
                    color: selectedSavedUser === user.spotify_user_id ? 'var(--primary-color)' : 'var(--text-primary)',
                  }}>
                    {user.display_name}
                  </span>
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      removeSavedSpotifyUser(user.id);
                    }}
                    style={{
                      background: 'none',
                      border: 'none',
                      cursor: 'pointer',
                      padding: '0 0.25rem',
                      fontSize: '1rem',
                      color: 'var(--text-secondary)',
                      lineHeight: 1,
                    }}
                    title="Remove user"
                  >
                    ×
                  </button>
                </div>
              ))}
            </div>
          )}
          
          {/* Loading Playlists Indicator */}
          {isLoadingSpotifyProfile && selectedSavedUser && (
            <div style={{
              marginTop: '1rem',
              padding: '0.75rem',
              backgroundColor: 'rgba(91, 155, 213, 0.1)',
              border: '1px solid rgba(91, 155, 213, 0.3)',
              borderRadius: '4px',
              color: 'var(--text-primary)',
              fontSize: '0.875rem',
              textAlign: 'center',
            }}>
              Loading playlists... This may take a moment for large profiles.
            </div>
          )}
        </div>
      )}

      {!importResult ? (
        <>
          {/* Favorites Section */}
          {favoritePlaylists.filter(f => f.source === activeSource).length > 0 && (
            <div style={{ marginBottom: '2rem' }}>
              <h3 style={{ fontSize: '1rem', marginBottom: '0.75rem', fontWeight: 500, display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                <span style={{ color: '#f5c518' }}>★</span> Favorites
              </h3>
              <div style={{ 
                display: 'grid', 
                gridTemplateColumns: 'repeat(auto-fill, minmax(250px, 1fr))', 
                gap: '0.75rem',
              }}>
                {favoritePlaylists
                  .filter(f => f.source === activeSource)
                  .map((fav) => {
                    const playlist: PopularPlaylist = {
                      name: fav.name,
                      url: fav.source_url,
                      description: fav.description && !/^0\s+tracks?$/.test(fav.description.trim()) ? fav.description : undefined,
                      imageUrl: fav.image_url || undefined,
                    };
                    return (
                      <div
                        key={fav.id}
                        className="card"
                        style={{ 
                          padding: '1rem',
                          display: 'flex',
                          flexDirection: 'column',
                          borderColor: 'rgba(245, 197, 24, 0.3)',
                        }}
                      >
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.5rem', marginBottom: '0.75rem' }}>
                          <div style={{ fontWeight: 500, flex: 1, minWidth: 0 }}>{fav.name}</div>
                          <button
                            onClick={(e) => {
                              e.stopPropagation();
                              toggleFavorite(playlist);
                            }}
                            title="Remove from favorites"
                            style={{
                              background: 'none',
                              border: 'none',
                              cursor: 'pointer',
                              fontSize: '1.25rem',
                              padding: '0 0.25rem',
                              color: '#f5c518',
                              lineHeight: 1,
                              flexShrink: 0,
                            }}
                          >
                            ★
                          </button>
                        </div>
                        {playlist.description && (
                          <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                            {playlist.description}
                          </div>
                        )}
                        <div style={{ display: 'flex', gap: '0.5rem', marginTop: 'auto' }}>
                          <button
                            className="btn btn-primary btn-small"
                            onClick={() => handleSchedule(playlist)}
                            disabled={isImporting}
                            style={{ flex: 1 }}
                          >
                            Import
                          </button>
                          <button
                            className="btn btn-secondary btn-small"
                            onClick={() => handlePreview(playlist)}
                            disabled={isImporting}
                            style={{ flex: 1 }}
                          >
                            Preview
                          </button>
                        </div>
                      </div>
                    );
                  })}
              </div>
            </div>
          )}

          {/* Input Field - Moved to top */}
          {!['aria', 'billboard'].includes(activeSource) && (
            <div className="card" style={{ marginBottom: '2rem' }}>
              {activeSource === 'file' ? (
              <div>
                <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                  Select File
                </label>
                <input
                  type="file"
                  accept=".m3u,.m3u8,.pls,.xspf,.csv,.txt"
                  onChange={(e) => setFile(e.target.files?.[0] || null)}
                  disabled={isImporting}
                  style={{
                    width: '100%',
                    padding: '0.75rem',
                    border: '1px solid var(--border-color)',
                    borderRadius: '4px',
                    backgroundColor: 'var(--surface)',
                    color: 'var(--text-primary)',
                  }}
                />
              </div>
            ) : activeSource === 'listenbrainz' ? (
              <div>
                <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                  ListenBrainz Username
                </label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder={currentSource?.placeholder}
                  disabled={isImporting}
                  style={{
                    width: '100%',
                    padding: '0.75rem',
                    border: '1px solid var(--border-color)',
                    borderRadius: '4px',
                    backgroundColor: 'var(--surface)',
                    color: 'var(--text-primary)',
                  }}
                />
              </div>
            ) : activeSource === 'ai' ? (
              <div>
                {!hasGeminiApiKey && (
                  <>
                    <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                      Gemini API Key
                    </label>
                    <input
                      type="password"
                      value={geminiApiKey}
                      onChange={(e) => setGrokApiKey(e.target.value)}
                      placeholder="Enter your Gemini API key"
                      disabled={isImporting}
                      style={{
                        width: '100%',
                        padding: '0.75rem',
                        border: '1px solid var(--border-color)',
                        borderRadius: '4px',
                        backgroundColor: 'var(--surface)',
                        color: 'var(--text-primary)',
                        marginBottom: '1rem',
                      }}
                    />
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '1rem' }}>
                      Get your free API key at <a href="https://aistudio.google.com/app/apikey" target="_blank" rel="noopener noreferrer" style={{ color: 'var(--primary)' }}>Google AI Studio</a> or save it in <a href="/settings" style={{ color: 'var(--primary)' }}>Settings</a>
                    </div>
                  </>
                )}
                
                {hasGeminiApiKey && (
                  <div style={{ 
                    padding: '0.75rem', 
                    marginBottom: '1rem',
                    backgroundColor: 'rgba(76, 175, 80, 0.1)',
                    border: '1px solid var(--success)',
                    borderRadius: '4px',
                    color: 'var(--success)',
                    fontSize: '0.875rem'
                  }}>
                    ✓ Using Gemini API key from Settings
                  </div>
                )}
                
                <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                  Describe Your Playlist
                </label>
                <textarea
                  value={aiPrompt}
                  onChange={(e) => setAiPrompt(e.target.value)}
                  placeholder="e.g., 'Create a playlist of upbeat 80s rock songs' or 'Relaxing jazz for studying'"
                  disabled={isImporting}
                  rows={4}
                  style={{
                    width: '100%',
                    padding: '0.75rem',
                    border: '1px solid var(--border-color)',
                    borderRadius: '4px',
                    backgroundColor: 'var(--surface)',
                    color: 'var(--text-primary)',
                    resize: 'vertical',
                    fontFamily: 'inherit',
                  }}
                />
                <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.5rem' }}>
                  Tip: Be specific about genre, mood, era, or artists you like
                </div>
                
                <label style={{ display: 'block', marginTop: '1rem', marginBottom: '0.5rem', fontWeight: 500 }}>
                  Number of Tracks
                </label>
                <input
                  type="number"
                  min="10"
                  max="100"
                  value={aiTrackCount}
                  onChange={(e) => setAiTrackCount(Math.min(100, Math.max(10, Number(e.target.value) || 10)))}
                  disabled={isImporting}
                  style={{
                    width: '100px',
                    padding: '0.75rem',
                    border: '1px solid var(--border-color)',
                    borderRadius: '4px',
                    backgroundColor: 'var(--surface)',
                    color: 'var(--text-primary)',
                  }}
                />
                <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.5rem' }}>
                  Choose between 10 and 100 tracks for your playlist
                </div>
              </div>
            ) : !['aria', 'billboard'].includes(activeSource) ? (
              <div>
                <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                  {activeSource === 'spotify'
                    ? 'Spotify Playlist URL'
                    : ['deezer', 'youtube'].includes(activeSource) 
                      ? `${currentSource?.name} URL or Search` 
                      : `${currentSource?.name} URL`}
                </label>
                <div style={{ display: 'flex', gap: '0.5rem' }}>
                  <input
                    type="text"
                    value={url}
                    onChange={(e) => {
                      setUrl(e.target.value);
                      // Clear search results when user types
                      if (searchResults.length > 0) {
                        setSearchResults([]);
                      }
                      // Clear Spotify profile playlists when URL changes
                      if (spotifyProfilePlaylists.length > 0) {
                        setSpotifyProfilePlaylists([]);
                        setSpotifyProfileName('');
                      }
                    }}
                    placeholder={
                      activeSource === 'spotify'
                        ? 'https://open.spotify.com/playlist/...'
                        : ['deezer', 'youtube'].includes(activeSource) 
                          ? `${currentSource?.placeholder} or search for playlists` 
                          : currentSource?.placeholder
                    }
                    disabled={isImporting}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        if (activeSource === 'spotify' && url.trim()) {
                          // Check if it's a Spotify user profile URL
                          const profileMatch = url.match(/open\.spotify\.com\/user\/([a-zA-Z0-9_-]+)/);
                          if (profileMatch) {
                            loadSpotifyProfilePlaylists(profileMatch[1]);
                          } else {
                            handleImport();
                          }
                        } else if (['deezer', 'youtube'].includes(activeSource) && url.trim() && !url.includes('http')) {
                          handleSearch();
                        }
                      }
                    }}
                    style={{
                      flex: 1,
                      padding: '0.75rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                    }}
                  />
                  {['deezer', 'youtube'].includes(activeSource) && (
                    <button
                      className="btn btn-secondary"
                      onClick={handleSearch}
                      disabled={isSearching || !url.trim() || url.includes('http')}
                      style={{ minWidth: '100px' }}
                    >
                      {isSearching ? 'Searching...' : 'Search'}
                    </button>
                  )}
                </div>
                {activeSource === 'spotify' ? (
                  <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.5rem' }}>
                    Paste a playlist URL to import, or a user profile URL to browse their playlists
                  </div>
                ) : ['deezer', 'youtube'].includes(activeSource) ? (
                  <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.5rem' }}>
                    Paste a URL to import directly, or enter keywords to search for playlists
                  </div>
                ) : null}
              </div>
            ) : null}

            {/* Optional Custom Playlist Name */}
            {!['aria', 'billboard'].includes(activeSource) && (
              <div style={{ display: 'flex', gap: '0.5rem', marginTop: '1rem' }}>
                <button
                  className="btn btn-primary"
                  onClick={() => ['ai', 'file'].includes(activeSource) ? handleImport() : openImportModal()}
                  disabled={isImporting || (!url && !username && !file && !aiPrompt) || (activeSource === 'ai' && !hasGeminiApiKey && !geminiApiKey)}
                  style={{ flex: 2 }}
                >
                  {isImporting ? 'Importing...' : activeSource === 'ai' ? 'Generate Playlist' : 'Import Playlist'}
                </button>
                {activeSource === 'spotify' && url.trim() && url.includes('open.spotify.com/playlist/') && (
                  <>
                    <button
                      className="btn btn-secondary"
                      onClick={async () => {
                        // First fetch preview to get the actual playlist name
                        const playlistId = url.match(/playlist\/([a-zA-Z0-9]+)/)?.[1];
                        const playlist: PopularPlaylist = { 
                          name: playlistName || `Spotify Playlist ${playlistId || ''}`.trim(), 
                          url: url.trim() 
                        };
                        await handlePreview(playlist);
                      }}
                      disabled={isImporting || isLoadingPreview}
                      style={{ flex: 1, minWidth: '80px' }}
                    >
                      {isLoadingPreview ? 'Loading...' : 'Preview'}
                    </button>
                    <button
                      className="btn btn-secondary"
                      onClick={async () => {
                        // Use preview name if available, otherwise fetch it first
                        let name = playlistName;
                        const playlistId = url.match(/playlist\/([a-zA-Z0-9]+)/)?.[1];
                        if (!name) {
                          name = `Spotify Playlist ${playlistId || ''}`.trim();
                          // Try to fetch the actual name
                          try {
                            const response = await fetch('/api/import/playlist-name', {
                              method: 'POST',
                              headers: { 'Content-Type': 'application/json' },
                              credentials: 'include',
                              body: JSON.stringify({ url: url.trim() }),
                              signal: AbortSignal.timeout(15000),
                            });
                            if (response.ok) {
                              const data = await response.json();
                              if (data.name) name = data.name;
                            }
                          } catch { /* Use fallback name */ }
                        }
                        toggleFavorite({ 
                          name, 
                          url: url.trim(),
                          description: playlistId ? `Spotify playlist ${playlistId}` : undefined,
                        });
                      }}
                      disabled={isImporting}
                      style={{ flex: 1, minWidth: '80px' }}
                    >
                      {isFavorited(url.trim()) ? '★ Favorited' : '☆ Favorite'}
                    </button>
                  </>
                )}
              </div>
            )}
            </div>
          )}
          {/* Popular/User Playlists */}
          {searchResults.length > 0 ? (
            <>
              <h3 style={{ fontSize: '1rem', marginBottom: '0.75rem', fontWeight: 500 }}>
                Search Results for "{url}"
              </h3>
              
              <div style={{ 
                display: 'grid', 
                gridTemplateColumns: 'repeat(auto-fill, minmax(250px, 1fr))', 
                gap: '0.75rem',
                marginBottom: '1rem',
              }}>
                {searchResults.map((playlist, idx) => (
                  <div
                    key={idx}
                    className="card"
                    style={{ 
                      padding: '1rem',
                      display: 'flex',
                      flexDirection: 'column',
                    }}
                  >
                    <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                      <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>{playlist.name}</div>
                      {playlist.description && (
                        <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                          {playlist.description}
                        </div>
                      )}
                    </div>
                    <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                      <button
                        className="btn btn-secondary btn-small"
                        onClick={() => handlePreview(playlist)}
                        disabled={isImporting}
                        style={{ flex: 1, minWidth: '60px' }}
                      >
                        Preview
                      </button>
                      <button
                        className="btn btn-primary btn-small"
                        onClick={() => handleSchedule(playlist)}
                        disabled={isImporting}
                        style={{ flex: 1, minWidth: '70px' }}
                      >
                        Import
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </>
          ) : activeSource === 'aria' ? (
            <>
              <h3 style={{ fontSize: '1rem', marginBottom: '0.75rem', fontWeight: 500 }}>
                ARIA Charts
              </h3>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))', gap: '0.75rem', marginBottom: '1rem' }}>
                
                {/* Top 50 Singles */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top 50 Singles</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>Weekly ARIA Top 50 Singles Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Chart week (Monday):</label>
                    <select value={ariaTop50Date} onChange={(e) => setAriaTop50Date(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%', cursor: 'pointer' }}>
                      <option value="latest">Latest</option>
                      {recentMondays.map(m => <option key={m} value={m}>{formatDateDMY(m)}</option>)}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: ariaTop50Date === 'latest' ? `ARIA Top 50 Singles - Latest` : `ARIA Top 50 Singles - ${ariaTop50Date}`, url: ariaTop50Date === 'latest' ? `https://www.aria.com.au/charts/singles-chart` : `https://www.aria.com.au/charts/singles-chart/${ariaTop50Date}` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>

                {/* Top Australian Singles */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Australian Singles</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>Australian Artist Singles Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Chart week (Monday):</label>
                    <select value={ariaAustralianDate} onChange={(e) => setAriaAustralianDate(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%', cursor: 'pointer' }}>
                      <option value="latest">Latest</option>
                      {recentMondays.map(m => <option key={m} value={m}>{formatDateDMY(m)}</option>)}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: ariaAustralianDate === 'latest' ? `ARIA Australian Singles - Latest` : `ARIA Australian Singles - ${ariaAustralianDate}`, url: ariaAustralianDate === 'latest' ? `https://www.aria.com.au/charts/australian-artist-singles-chart` : `https://www.aria.com.au/charts/australian-artist-singles-chart/${ariaAustralianDate}` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>

                {/* Top 50 On Replay Singles */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top 50 On Replay Singles</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>Catalogue Singles Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Chart week (Monday):</label>
                    <select value={ariaReplayDate} onChange={(e) => setAriaReplayDate(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%', cursor: 'pointer' }}>
                      <option value="latest">Latest</option>
                      {recentMondays.map(m => <option key={m} value={m}>{formatDateDMY(m)}</option>)}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: ariaReplayDate === 'latest' ? `ARIA On Replay Singles - Latest` : `ARIA On Replay Singles - ${ariaReplayDate}`, url: ariaReplayDate === 'latest' ? `https://www.aria.com.au/charts/catalogue-singles-chart` : `https://www.aria.com.au/charts/catalogue-singles-chart/${ariaReplayDate}` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>

                {/* Top 20 New Music Singles */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top 20 New Music Singles</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>New Music Singles Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Chart week (Monday):</label>
                    <select value={ariaNewMusicDate} onChange={(e) => setAriaNewMusicDate(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%', cursor: 'pointer' }}>
                      {recentMondays.map(m => <option key={m} value={m}>{formatDateDMY(m)}</option>)}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: `ARIA New Music Singles - ${ariaNewMusicDate}`, url: `https://www.aria.com.au/charts/new-music-singles-chart/${ariaNewMusicDate}` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>

                {/* Hip Hop R&B Singles */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Hip Hop R&B Singles</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>Weekly ARIA Hip Hop R&B Singles Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Chart week (Monday):</label>
                    <select value={ariaHipHopDate} onChange={(e) => setAriaHipHopDate(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%', cursor: 'pointer' }}>
                      {recentMondays.map(m => <option key={m} value={m}>{formatDateDMY(m)}</option>)}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: `ARIA Hip Hop R&B Singles - ${ariaHipHopDate}`, url: `https://www.aria.com.au/charts/hip-hop-r-and-b-singles-chart/${ariaHipHopDate}` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>

                {/* Dance Singles */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Dance Singles</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>Weekly ARIA Dance Singles Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Chart week (Monday):</label>
                    <select value={ariaDanceDate} onChange={(e) => setAriaDanceDate(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%', cursor: 'pointer' }}>
                      <option value="latest">Latest</option>
                      {recentMondays.map(m => <option key={m} value={m}>{formatDateDMY(m)}</option>)}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: ariaDanceDate === 'latest' ? `ARIA Dance Singles - Latest` : `ARIA Dance Singles - ${ariaDanceDate}`, url: ariaDanceDate === 'latest' ? `https://www.aria.com.au/charts/dance-singles-chart` : `https://www.aria.com.au/charts/dance-singles-chart/${ariaDanceDate}` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>

                {/* Club Tracks */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Club Tracks</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>Weekly ARIA Club Tracks Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Chart week (Monday):</label>
                    <select value={ariaClubDate} onChange={(e) => setAriaClubDate(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%', cursor: 'pointer' }}>
                      <option value="latest">Latest</option>
                      {recentMondays.map(m => <option key={m} value={m}>{formatDateDMY(m)}</option>)}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: ariaClubDate === 'latest' ? `ARIA Club Tracks - Latest` : `ARIA Club Tracks - ${ariaClubDate}`, url: ariaClubDate === 'latest' ? `https://www.aria.com.au/charts/club-tracks-chart` : `https://www.aria.com.au/charts/club-tracks-chart/${ariaClubDate}` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>

                {/* Top 100 Singles (Annual) */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top 100 Singles (Annual)</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>End of Year Singles Chart</div>
                    <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>Year:</label>
                    <select value={ariaTop100Year} onChange={(e) => setAriaTop100Year(e.target.value)} style={{ padding: '0.4rem', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '0.8rem', width: '100%' }}>
                      {Array.from({ length: 10 }, (_, i) => new Date().getFullYear() - i).map(y => (
                        <option key={y} value={y}>{y}</option>
                      ))}
                    </select>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button className="btn btn-primary btn-small" onClick={() => handleSchedule({ name: `ARIA Top 100 Singles ${ariaTop100Year}`, url: `https://www.aria.com.au/charts/${ariaTop100Year}/singles-chart` })} disabled={isImporting} style={{ flex: 1 }}>Import</button>
                  </div>
                </div>
              </div>
            </>
                    ) : activeSource === 'billboard' ? (
            <>
              <h3 style={{ fontSize: '1rem', marginBottom: '0.75rem', fontWeight: 500 }}>Billboard Charts</h3>
              
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '1rem' }}>
                
                {/* Billboard Hot 100 with Date Picker */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Billboard Hot 100</div>
                    <div style={{ marginBottom: '0.75rem' }}>
                      <label style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', display: 'block', marginBottom: '0.25rem' }}>
                        Chart week (Saturday):
                      </label>
                      <select 
                        value={billboardDate} 
                        onChange={(e) => setBillboardDate(e.target.value)}
                        style={{ 
                          width: '100%', 
                          padding: '0.5rem', 
                          borderRadius: '4px', 
                          border: '1px solid var(--border-color)',
                          backgroundColor: 'var(--bg-primary)',
                          color: 'var(--text-primary)',
                          fontSize: '0.875rem'
                        }}
                      >
                        <option value="latest">Latest</option>
                        {recentSaturdays.map(date => (
                          <option key={date} value={date}>
                            {formatDateDMY(date)}
                          </option>
                        ))}
                      </select>
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: billboardDate === 'latest' ? `Billboard Hot 100 - Latest` : `Billboard Hot 100 - ${formatDateDMY(billboardDate)}`, 
                        url: billboardDate === 'latest' ? `https://www.billboard.com/charts/hot-100/` : `https://www.billboard.com/charts/hot-100/${billboardDate}/`
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

              </div>
            </>
          ) : activeSource === 'lastfm' ? (
            <>
              <h3 style={{ fontSize: '1rem', marginBottom: '0.75rem', fontWeight: 500 }}>Last.fm Charts</h3>
              
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '1rem' }}>
                
                {/* Top Tracks */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Tracks</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular tracks globally
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Tracks', 
                        url: 'https://www.last.fm/charts/top-tracks' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Tracks', 
                        url: 'https://www.last.fm/charts/top-tracks'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Top Artists */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Artists</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Top tracks from popular artists
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Artists', 
                        url: 'https://www.last.fm/charts/top-artists' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Artists', 
                        url: 'https://www.last.fm/charts/top-artists'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Top Tags */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Tags</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Tracks from trending genres/tags
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Tags', 
                        url: 'https://www.last.fm/charts/top-tags' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Tags', 
                        url: 'https://www.last.fm/charts/top-tags'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Rock Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Rock</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular rock tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Rock', 
                        url: 'https://www.last.fm/tag/rock' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Rock', 
                        url: 'https://www.last.fm/tag/rock'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Pop Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Pop</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular pop tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Pop', 
                        url: 'https://www.last.fm/tag/pop' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Pop', 
                        url: 'https://www.last.fm/tag/pop'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Electronic Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Electronic</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular electronic tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Electronic', 
                        url: 'https://www.last.fm/tag/electronic' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Electronic', 
                        url: 'https://www.last.fm/tag/electronic'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Hip Hop Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Hip Hop</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular hip hop tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Hip Hop', 
                        url: 'https://www.last.fm/tag/hip hop' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Hip Hop', 
                        url: 'https://www.last.fm/tag/hip hop'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Indie Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Indie</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular indie tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Indie', 
                        url: 'https://www.last.fm/tag/indie' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Indie', 
                        url: 'https://www.last.fm/tag/indie'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Metal Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Metal</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular metal tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Metal', 
                        url: 'https://www.last.fm/tag/metal' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Metal', 
                        url: 'https://www.last.fm/tag/metal'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Jazz Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Jazz</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular jazz tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Jazz', 
                        url: 'https://www.last.fm/tag/jazz' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Jazz', 
                        url: 'https://www.last.fm/tag/jazz'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

                {/* Classical Genre */}
                <div className="card" style={{ padding: '1rem', display: 'flex', flexDirection: 'column' }}>
                  <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                    <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Top Classical</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      Most popular classical tracks
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button 
                      className="btn btn-secondary btn-small" 
                      onClick={() => handlePreview({ 
                        name: 'Last.fm Top Classical', 
                        url: 'https://www.last.fm/tag/classical' 
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1, minWidth: '60px' }}
                    >
                      Preview
                    </button>
                    <button 
                      className="btn btn-primary btn-small" 
                      onClick={() => handleSchedule({ 
                        name: 'Last.fm Top Classical', 
                        url: 'https://www.last.fm/tag/classical'
                      })} 
                      disabled={isImporting} 
                      style={{ flex: 1 }}
                    >
                      Import
                    </button>
                  </div>
                </div>

              </div>
            </>
          ) : (currentPopular.length > 0 || isLoadingDynamicPlaylists || spotifyProfilePlaylists.length > 0 || isLoadingSpotifyProfile) && (
            <>
              {/* Spotify Profile Playlists Section */}
              {activeSource === 'spotify' && (isLoadingSpotifyProfile || spotifyProfilePlaylists.length > 0) && (
                <>
                  <h3 style={{ fontSize: '1rem', marginBottom: '0.75rem', fontWeight: 500 }}>
                    {isLoadingSpotifyProfile 
                      ? `Loading playlists for ${spotifyProfileName || 'user'}...`
                      : `${spotifyProfileName || 'User'}'s Playlists (${spotifyProfilePlaylists.length})`}
                  </h3>
                  
                  {isLoadingSpotifyProfile ? (
                    <div style={{ textAlign: 'center', padding: '2rem', color: 'var(--text-secondary)', fontSize: '0.875rem', marginBottom: '1rem' }}>
                      Loading playlists from Spotify profile (this may take a moment)...
                    </div>
                  ) : (
                    <div style={{ 
                      display: 'grid', 
                      gridTemplateColumns: 'repeat(auto-fill, minmax(250px, 1fr))', 
                      gap: '0.75rem',
                      marginBottom: '2rem',
                    }}>
                      {spotifyProfilePlaylists.map((playlist, idx) => (
                        <div
                          key={idx}
                          className="card"
                          style={{ 
                            padding: '1rem',
                            display: 'flex',
                            flexDirection: 'column',
                            borderColor: selectedSavedUser ? 'rgba(91, 155, 213, 0.2)' : undefined,
                          }}
                        >
                          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.5rem', marginBottom: '0.5rem' }}>
                            <div style={{ fontWeight: 500, flex: 1, minWidth: 0 }}>{playlist.name}</div>
                            <button
                              onClick={(e) => {
                                e.stopPropagation();
                                toggleFavorite(playlist);
                              }}
                              title={isFavorited(playlist.url) ? 'Remove from favorites' : 'Add to favorites'}
                              style={{
                                background: 'none',
                                border: 'none',
                                cursor: 'pointer',
                                fontSize: '1.25rem',
                                padding: '0 0.25rem',
                                color: isFavorited(playlist.url) ? '#f5c518' : 'var(--text-secondary)',
                                opacity: isFavorited(playlist.url) ? 1 : 0.5,
                                lineHeight: 1,
                                flexShrink: 0,
                              }}
                            >
                              {isFavorited(playlist.url) ? '★' : '☆'}
                            </button>
                          </div>
                          {playlist.imageUrl && (
                            <img 
                              src={playlist.imageUrl} 
                              alt={playlist.name}
                              style={{ 
                                width: '100%', 
                                aspectRatio: '1', 
                                objectFit: 'cover', 
                                borderRadius: '4px',
                                marginBottom: '0.5rem',
                              }}
                            />
                          )}
                          {playlist.description && (
                            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                              {playlist.description}
                            </div>
                          )}
                          <div style={{ display: 'flex', gap: '0.5rem', marginTop: 'auto', flexWrap: 'wrap' }}>
                            <button
                              className="btn btn-secondary btn-small"
                              onClick={() => handlePreview(playlist)}
                              disabled={isImporting}
                              style={{ flex: 1, minWidth: '60px' }}
                            >
                              Preview
                            </button>
                            <button
                              className="btn btn-primary btn-small"
                              onClick={() => handleSchedule(playlist)}
                              disabled={isImporting}
                              style={{ flex: 1, minWidth: '70px' }}
                            >
                              Import
                            </button>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </>
              )}

              {/* Popular Playlists Section - only for non-Spotify sources */}
              {activeSource !== 'spotify' && (
                <>
                  <h3 style={{ fontSize: '1rem', marginBottom: '0.75rem', fontWeight: 500 }}>
                    Popular Playlists
                  </h3>
                                
                  {isLoadingDynamicPlaylists ? (
                    <div style={{ 
                      textAlign: 'center', 
                      padding: '2rem',
                      color: 'var(--text-secondary)',
                      fontSize: '0.875rem',
                      marginBottom: '1rem',
                    }}>
                      Loading playlists...
                    </div>
                  ) : currentPopular.length === 0 && ['amazon', 'apple'].includes(activeSource) ? (
                    <div style={{ 
                      textAlign: 'center', 
                      padding: '2rem',
                      color: 'var(--text-secondary)',
                      fontSize: '0.875rem',
                      marginBottom: '1rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                      backgroundColor: 'rgba(255, 193, 7, 0.05)',
                    }}>
                      <div style={{ marginBottom: '0.5rem', fontWeight: 500 }}>
                        {activeSource === 'amazon' ? 'Amazon Music' : 'Apple Music'} popular playlists are not available
                      </div>
                      <div>
                        These services require browser automation to access. You can still import playlists by pasting a direct URL above.
                      </div>
                    </div>
                  ) : currentPopular.length === 0 ? (
                    <div style={{ 
                      textAlign: 'center', 
                      padding: '2rem',
                      color: 'var(--text-secondary)',
                      fontSize: '0.875rem',
                      marginBottom: '1rem',
                      border: '1px solid var(--border-color)',
                      borderRadius: '4px',
                    }}>
                      No popular playlists found. Try refreshing or selecting a different country.
                    </div>
                  ) : (
                    <div style={{ 
                      display: 'grid', 
                      gridTemplateColumns: 'repeat(auto-fill, minmax(250px, 1fr))', 
                      gap: '0.75rem',
                      marginBottom: '1rem',
                    }}>
                      {currentPopular.map((playlist, idx) => (
                          <div
                            key={`${activeSource}-${playlist.url}-${idx}`}
                            className="card"
                            style={{ 
                              padding: '1rem',
                              display: 'flex',
                              flexDirection: 'column',
                            }}
                          >
                            <div style={{ flex: 1, marginBottom: '0.75rem' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.5rem' }}>
                                <div style={{ fontWeight: 500, marginBottom: '0.5rem', flex: 1, minWidth: 0 }}>{playlist.name}</div>
                                <button
                                  onClick={(e) => {
                                    e.stopPropagation();
                                    toggleFavorite(playlist);
                                  }}
                                  title={isFavorited(playlist.url) ? 'Remove from favorites' : 'Add to favorites'}
                                  style={{
                                    background: 'none',
                                    border: 'none',
                                    cursor: 'pointer',
                                    fontSize: '1.25rem',
                                    padding: '0 0.25rem',
                                    color: isFavorited(playlist.url) ? '#f5c518' : 'var(--text-secondary)',
                                    opacity: isFavorited(playlist.url) ? 1 : 0.4,
                                    transition: 'opacity 0.2s, color 0.2s',
                                    lineHeight: 1,
                                    flexShrink: 0,
                                  }}
                                  onMouseEnter={(e) => { e.currentTarget.style.opacity = '1'; }}
                                  onMouseLeave={(e) => { e.currentTarget.style.opacity = isFavorited(playlist.url) ? '1' : '0.4'; }}
                                >
                                  {isFavorited(playlist.url) ? '★' : '☆'}
                                </button>
                              </div>
                              {playlist.description && (
                                <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>
                                  {playlist.description}
                                </div>
                              )}
                              {(playlist.videoCount || playlist.count || playlist.trackCount) && (
                                <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                                  {playlist.videoCount || playlist.count || playlist.trackCount} tracks
                                </div>
                              )}
                            </div>
                            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                              <button
                                className="btn btn-secondary btn-small"
                                onClick={() => handlePreview(playlist)}
                                disabled={isImporting}
                                style={{ flex: 1, minWidth: '60px' }}
                              >
                                Preview
                              </button>
                              <button
                                className="btn btn-primary btn-small"
                                onClick={() => handleSchedule(playlist)}
                                disabled={isImporting}
                                style={{ flex: 1, minWidth: '70px' }}
                              >
                                Import
                              </button>
                            </div>
                          </div>
                        ))}
                    </div>
                  )}
                </>
              )}
            </>
          )}


          {error && (
            <div style={{
              padding: '1rem',
              backgroundColor: 'rgba(244, 67, 54, 0.1)',
              border: '1px solid var(--error)',
              borderRadius: '4px',
              color: 'var(--error)',
            }}>
              {error}
            </div>
          )}
        </>
      ) : (
        <>
          {/* Import Results - Review Screen */}
          <div style={{ marginBottom: '1rem' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
              <h2 style={{ margin: 0 }}>Review Import</h2>
              <div style={{ display: 'flex', gap: '1rem', alignItems: 'center' }}>
                <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                  <span style={{ color: 'var(--success)', fontWeight: 600 }}>{matchedCount}</span> matched, {' '}
                  <span style={{ color: 'var(--warning)', fontWeight: 600 }}>{unmatchedCount}</span> unmatched
                </div>
                <button
                  onClick={handleCancel}
                  disabled={isImporting}
                  title="Close review"
                  style={{
                    background: 'none',
                    border: 'none',
                    cursor: 'pointer',
                    color: 'var(--text-secondary)',
                    fontSize: '1.25rem',
                    lineHeight: 1,
                    padding: '0.25rem 0.5rem',
                    borderRadius: '4px',
                  }}
                >
                  ✕
                </button>
              </div>
            </div>
            
            <div style={{ marginBottom: '1.5rem' }}>
              <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>
                Playlist Name
              </label>
              <input
                type="text"
                value={playlistName}
                onChange={(e) => setPlaylistName(e.target.value)}
                style={{
                  width: '100%',
                  maxWidth: '500px',
                  padding: '0.75rem',
                  border: '1px solid var(--border-color)',
                  borderRadius: '4px',
                  backgroundColor: 'var(--surface)',
                  color: 'var(--text-primary)',
                  fontSize: '1rem',
                }}
              />
            </div>

            {/* Overwrite Existing */}
            <div style={{ marginBottom: '1rem' }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={overwriteExisting}
                  onChange={(e) => setOverwriteExisting(e.target.checked)}
                  style={{ cursor: 'pointer' }}
                />
                <span style={{ fontSize: '0.875rem' }}>Overwrite existing playlist with same name</span>
              </label>
              {overwriteExisting && (
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer', marginLeft: '1.5rem', marginTop: '0.25rem' }}>
                  <input
                    type="checkbox"
                    checked={keepExistingCover}
                    onChange={(e) => setKeepExistingCover(e.target.checked)}
                    style={{ cursor: 'pointer' }}
                  />
                  <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>Keep existing cover art</span>
                </label>
              )}
            </div>

            {/* Filter Controls */}
            <div style={{ marginBottom: '1rem', display: 'flex', gap: '1rem', alignItems: 'center' }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={showUnmatchedOnly}
                  onChange={(e) => setShowUnmatchedOnly(e.target.checked)}
                  style={{ cursor: 'pointer' }}
                />
                <span style={{ fontSize: '0.875rem' }}>Show unmatched only</span>
              </label>
              
              {unmatchedCount > 0 && (
                <>
                  <button
                    className="btn btn-secondary"
                    onClick={handleExportMissing}
                    style={{ fontSize: '0.875rem', padding: '0.5rem 1rem' }}
                  >
                    Export Missing ({unmatchedCount})
                  </button>
                  <button
                    className="btn btn-primary"
                    onClick={handleAddToMissingTracks}
                    style={{ fontSize: '0.875rem', padding: '0.5rem 1rem' }}
                  >
                    Save the Missing Tracks ({unmatchedCount})
                  </button>
                </>
              )}
            </div>

            {/* Tracks Table */}
            <div style={{ 
              overflowX: 'auto',
              border: '1px solid var(--border-color)',
              borderRadius: '4px',
              marginBottom: '1.5rem',
            }}>
              <table style={{ 
                width: '100%', 
                borderCollapse: 'collapse',
                fontSize: '0.875rem',
              }}>
                <thead>
                  <tr style={{ 
                    backgroundColor: 'var(--surface)',
                    borderBottom: '1px solid var(--border-color)',
                  }}>
                    <th style={{ padding: '0.75rem', textAlign: 'center', fontWeight: 600, width: '30px' }}>⋮⋮</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '40px' }}>#</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Title</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Artist</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Album</th>
                    <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '80px' }}>Format</th>
                    <th style={{ padding: '0.75rem', textAlign: 'center', fontWeight: 600, width: '80px' }}>Score</th>
                  </tr>
                </thead>
                <tbody>
                  {displayedTracks.map((track, idx) => {
                    const originalIndex = editableTracks.indexOf(track);
                    return (
                      <tr
                        key={originalIndex}
                        draggable
                        onDragStart={() => handleDragStart(originalIndex)}
                        onDragOver={(e) => handleDragOver(e, originalIndex)}
                        onDragEnd={handleDragEnd}
                        onClick={() => handleOpenRematch(track, originalIndex)}
                        style={{
                          borderBottom: idx < displayedTracks.length - 1 ? '1px solid var(--border-color)' : 'none',
                          backgroundColor: idx % 2 === 0 ? 'transparent' : 'rgba(255, 255, 255, 0.02)',
                          cursor: 'pointer',
                          opacity: draggedIndex === originalIndex ? 0.5 : 1,
                        }}
                      >
                        <td style={{ padding: '0.75rem', textAlign: 'center', color: 'var(--text-secondary)', cursor: 'grab' }}>
                          <span className="track-drag-handle">⋮⋮</span>
                          <span className="track-move-buttons" onClick={(e) => e.stopPropagation()}>
                            <button
                              className="track-move-btn"
                              onClick={() => moveTrack(originalIndex, -1)}
                              disabled={originalIndex === 0}
                              title="Move up"
                              aria-label="Move track up"
                            >▲</button>
                            <button
                              className="track-move-btn"
                              onClick={() => moveTrack(originalIndex, 1)}
                              disabled={originalIndex === editableTracks.length - 1}
                              title="Move down"
                              aria-label="Move track down"
                            >▼</button>
                          </span>
                        </td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>{originalIndex + 1}</td>
                        <td style={{ padding: '0.75rem' }}>
                          {track.matched ? (
                            <div>
                              <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>
                                {track.title}
                              </div>
                              <div style={{ fontWeight: 500 }}>
                                → {track.plexTitle}
                              </div>
                            </div>
                          ) : (
                            <div style={{ fontWeight: 500, color: 'var(--warning)' }}>{track.title}</div>
                          )}
                        </td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>
                          {track.matched ? track.plexArtist : track.artist}
                        </td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>
                          {track.matched ? (track.plexAlbum || '-') : (track.album || '-')}
                        </td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>
                          {track.matched && track.plexCodec ? (
                            <div>
                              <div>{track.plexCodec}</div>
                              {track.plexBitrate && (
                                <div style={{ fontSize: '0.75rem' }}>
                                  {Math.round(track.plexBitrate / 1000)} kbps
                                </div>
                              )}
                            </div>
                          ) : '-'}
                        </td>
                        <td style={{ 
                          padding: '0.75rem', 
                          textAlign: 'center',
                          fontWeight: 600,
                          color: getScoreColor(track.score),
                        }}>
                          {track.matched ? `${track.score || 0}%` : '-'}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>

            <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap' }}>
              <button
                className="btn btn-primary"
                onClick={handleConfirmUpdated}
                disabled={isImporting || matchedCount === 0 || playlistCreated}
                style={{ minWidth: '150px' }}
              >
                {isImporting ? 'Creating...' : playlistCreated ? `Playlist Created ✓` : `Create Playlist (${matchedCount})`}
              </button>

              {playlistCreated && (
                <button
                  className="btn btn-secondary"
                  onClick={handleCancel}
                  style={{ minWidth: '130px' }}
                >
                  New Import
                </button>
              )}

              <button
                className="btn btn-secondary"
                onClick={handleCancel}
                disabled={isImporting}
                style={{ minWidth: '100px' }}
              >
                {playlistCreated ? 'Close' : 'Cancel'}
              </button>
            </div>

            {playlistCreated && (
              <div style={{
                marginTop: '1rem',
                padding: '1rem',
                backgroundColor: 'rgba(76, 175, 80, 0.1)',
                border: '1px solid var(--success, #4caf50)',
                borderRadius: '4px',
                color: 'var(--success, #4caf50)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                flexWrap: 'wrap',
                gap: '0.75rem',
              }}>
                <span>
                  Playlist "{playlistName}" created successfully.
                  {unmatchedCount > 0 && (
                    <span style={{ color: 'var(--text-secondary)', marginLeft: '0.5rem' }}>
                      {unmatchedCount} unmatched track{unmatchedCount !== 1 ? 's' : ''} remaining.
                    </span>
                  )}
                </span>
                {unmatchedCount > 0 && (
                  <button
                    className="btn btn-secondary"
                    onClick={handleAddToMissingTracks}
                    style={{ fontSize: '0.875rem', padding: '0.4rem 0.9rem', whiteSpace: 'nowrap' }}
                  >
                    Save Missing Tracks ({unmatchedCount})
                  </button>
                )}
              </div>
            )}

            {error && (
              <div style={{
                marginTop: '1rem',
                padding: '1rem',
                backgroundColor: 'rgba(244, 67, 54, 0.1)',
                border: '1px solid var(--error)',
                borderRadius: '4px',
                color: 'var(--error)',
              }}>
                {error}
              </div>
            )}
          </div>
        </>
      )}

      {/* Preview Modal */}
      {previewPlaylist && (
        <div style={{
          position: 'fixed',
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          backgroundColor: 'rgba(0, 0, 0, 0.7)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          zIndex: 1000,
          padding: '1rem',
        }}>
          <div className="card" style={{
            maxWidth: '600px',
            width: '100%',
            maxHeight: '80vh',
            display: 'flex',
            flexDirection: 'column',
          }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
              <h2 style={{ margin: 0 }}>{previewPlaylist.name}</h2>
              <button
                onClick={() => {
                  setPreviewPlaylist(null);
                  setPreviewTracks(null);
                }}
                title="Close"
                style={modalCloseButtonStyle}
              >
                ✕
              </button>
            </div>

            {previewPlaylist.description && (
              <p style={{ color: 'var(--text-secondary)', marginBottom: '1rem' }}>
                {previewPlaylist.description}
              </p>
            )}

            {isLoadingPreview ? (
              <div style={{ textAlign: 'center', padding: '2rem' }}>
                Loading tracks...
              </div>
            ) : previewTracks && previewTracks.length > 0 ? (
              <div style={{ flex: 1, overflowY: 'auto', border: '1px solid var(--border)', borderRadius: '4px' }}>
                {previewTracks.map((track, idx) => (
                  <div
                    key={idx}
                    style={{
                      padding: '0.75rem',
                      borderBottom: idx < previewTracks.length - 1 ? '1px solid var(--border)' : 'none',
                    }}
                  >
                    <div style={{ fontWeight: 500 }}>{track.title}</div>
                    <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                      {track.artist}
                    </div>
                  </div>
                ))}
              </div>
            ) : previewTracks && previewTracks.length === 0 ? (
              <div style={{ 
                textAlign: 'center', 
                padding: '2rem',
                border: '1px solid var(--border)',
                borderRadius: '4px',
                backgroundColor: 'rgba(255, 193, 7, 0.1)',
              }}>
                <div style={{ fontSize: '1.25rem', marginBottom: '0.5rem' }}>ℹ️</div>
                <div style={{ fontWeight: 500, marginBottom: '0.5rem' }}>Preview Unavailable</div>
                <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                  {error || 'Unable to load preview for this playlist.'}
                </div>
                <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginTop: '0.5rem', padding: '0.75rem', backgroundColor: 'rgba(91, 155, 213, 0.1)', borderRadius: '4px' }}>
                  <strong>Don't worry!</strong> You can still import this playlist. The full track list will be fetched and matched during the import process.
                </div>
              </div>
            ) : null}

            <div style={{ marginTop: '1rem', display: 'flex', gap: '0.5rem' }}>
              <button
                className="btn btn-primary"
                onClick={() => {
                  handlePopularClick(previewPlaylist);
                  setPreviewPlaylist(null);
                  setPreviewTracks(null);
                }}
                style={{ flex: 1 }}
              >
                Import This Playlist
              </button>
              <button
                className="btn btn-secondary"
                onClick={() => {
                  setPreviewPlaylist(null);
                  setPreviewTracks(null);
                }}
                style={{ flex: 1 }}
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Manual Rematch Modal */}
      {rematchTrack && (
        <div style={{
          position: 'fixed',
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          backgroundColor: 'rgba(0, 0, 0, 0.7)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          zIndex: 1000,
          padding: '1rem',
        }}
          onMouseDown={(e) => { if (e.target === e.currentTarget) backdropMouseDown.current = true; }}
          onMouseUp={(e) => { if (e.target === e.currentTarget && backdropMouseDown.current) handleCloseRematch(); backdropMouseDown.current = false; }}
        >
          <div className="card" style={{
            maxWidth: '950px',
            width: '100%',
            maxHeight: '80vh',
            display: 'flex',
            flexDirection: 'column',
          }} onMouseDown={(e) => { backdropMouseDown.current = false; e.stopPropagation(); }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
              <h2 style={{ margin: 0 }}>Manual Rematch</h2>
              <button
                onClick={handleCloseRematch}
                title="Close"
                style={modalCloseButtonStyle}
              >
                ✕
              </button>
            </div>

            <div style={{ marginBottom: '1rem', padding: '1rem', backgroundColor: 'rgba(255, 255, 255, 0.05)', borderRadius: '4px' }}>
              <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>
                Original Track:
              </div>
              <div style={{ fontWeight: 500 }}>
                {rematchTrack.track.artist} - {rematchTrack.track.title}
              </div>
              {rematchTrack.track.album && (
                <div style={{ fontSize: '0.875rem', color: 'var(--text-secondary)' }}>
                  {rematchTrack.track.album}
                </div>
              )}
            </div>

            <div style={{ marginBottom: '1rem' }}>
              <div style={{ display: 'flex', gap: '0.5rem' }}>
                <input
                  type="text"
                  value={rematchQuery}
                  onChange={(e) => setRematchQuery(e.target.value)}
                  onKeyPress={(e) => e.key === 'Enter' && handleSearchRematch()}
                  placeholder="Search Plex library..."
                  style={{
                    flex: 1,
                    padding: '0.75rem',
                    border: '1px solid var(--border-color)',
                    borderRadius: '4px',
                    backgroundColor: 'var(--surface)',
                    color: 'var(--text-primary)',
                    fontSize: '1rem',
                  }}
                />
                <button
                  className="btn btn-primary"
                  onClick={handleSearchRematch}
                  disabled={isSearchingRematch || !rematchQuery.trim()}
                  style={{ minWidth: '100px' }}
                >
                  {isSearchingRematch ? 'Searching...' : 'Search'}
                </button>
              </div>
            </div>

            {rematchResults.length > 0 ? (
              <div style={{ flex: 1, overflowY: 'auto', border: '1px solid var(--border-color)', borderRadius: '4px' }}>
                <table style={{ 
                  width: '100%', 
                  borderCollapse: 'collapse',
                  fontSize: '0.875rem',
                }}>
                  <thead>
                    <tr style={{ 
                      backgroundColor: 'var(--surface)',
                      borderBottom: '1px solid var(--border-color)',
                      position: 'sticky',
                      top: 0,
                    }}>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '80px' }} title="The same score a real import would compute for this candidate">Match</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Title</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Artist</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600 }}>Album</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '80px' }}>Format</th>
                      <th style={{ padding: '0.75rem', textAlign: 'left', fontWeight: 600, width: '80px' }}>Bitrate</th>
                      <th style={{ padding: '0.75rem', textAlign: 'center', fontWeight: 600, width: '80px' }}>Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rematchResults.map((result, idx) => (
                      <tr
                        key={idx}
                        style={{
                          borderBottom: idx < rematchResults.length - 1 ? '1px solid var(--border-color)' : 'none',
                          backgroundColor: idx % 2 === 0 ? 'transparent' : 'rgba(255, 255, 255, 0.02)',
                        }}
                      >
                        <td style={{ padding: '0.75rem' }}>
                          {result.matchScore !== undefined ? (
                            <span
                              title={result.matched ? 'Would auto-match at your current settings' : "Below your minimum match score - won't auto-match"}
                              style={{
                                display: 'inline-block',
                                padding: '0.125rem 0.5rem',
                                borderRadius: '4px',
                                fontSize: '0.75rem',
                                fontWeight: 600,
                                color: result.matched ? 'var(--success)' : (result.matchScore >= 50 ? 'var(--warning)' : 'var(--error)'),
                                backgroundColor: result.matched ? 'rgba(102, 187, 106, 0.1)' : (result.matchScore >= 50 ? 'rgba(255, 167, 38, 0.1)' : 'rgba(239, 83, 80, 0.1)'),
                              }}
                            >
                              {Math.round(result.matchScore)}%
                            </span>
                          ) : '-'}
                        </td>
                        <td style={{ padding: '0.75rem', fontWeight: 500 }}>{result.title}</td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>{result.artist}</td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>{result.album || '-'}</td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>
                          {result.codec || '-'}
                        </td>
                        <td style={{ padding: '0.75rem', color: 'var(--text-secondary)' }}>
                          {result.bitrate ? `${result.bitrate} kbps` : '-'}
                        </td>
                        <td style={{ padding: '0.75rem', textAlign: 'center' }}>
                          <button
                            className="btn btn-primary btn-small"
                            onClick={() => handleSelectRematch(result)}
                            style={{ fontSize: '0.75rem', padding: '0.25rem 0.75rem' }}
                          >
                            Select
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : isSearchingRematch ? (
              <div style={{ 
                textAlign: 'center', 
                padding: '2rem',
                border: '1px solid var(--border-color)',
                borderRadius: '4px',
              }}>
                Searching...
              </div>
            ) : (
              <div style={{ 
                textAlign: 'center', 
                padding: '2rem',
                border: '1px solid var(--border-color)',
                borderRadius: '4px',
                color: 'var(--text-secondary)',
              }}>
                {rematchQuery.trim() ? 'No results found. Try a different search.' : 'Enter a search query to find tracks.'}
              </div>
            )}

            <div style={{ marginTop: '1rem', display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
              <button
                className="btn btn-secondary"
                onClick={handleCloseRematch}
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Schedule Modal */}
      {schedulePlaylist && (
        <Modal onClose={() => setSchedulePlaylist(null)} contentStyle={{ maxWidth: '460px', width: '100%', maxHeight: '90vh', overflowY: 'auto' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.75rem' }}>
              <h2 style={{ margin: 0, fontSize: '1.15rem' }}>Import Playlist</h2>
              <button
                onClick={() => setSchedulePlaylist(null)}
                title="Close"
                style={modalCloseButtonStyle}
              >
                ✕
              </button>
            </div>

            {schedulePlaylist.description && (
              <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                {schedulePlaylist.description}
              </div>
            )}

            <div style={{ marginBottom: '0.75rem' }}>
              <label style={{ display: 'block', marginBottom: '0.25rem', fontSize: '0.8rem', fontWeight: 500 }}>
                Playlist Name in Plex
              </label>
              <input
                type="text"
                value={schedulePlaylistName}
                onChange={(e) => setSchedulePlaylistName(e.target.value)}
                placeholder={schedulePlaylist.name || 'Fetching name…'}
                style={{
                  width: '100%',
                  padding: '0.5rem',
                  border: '1px solid var(--border)',
                  borderRadius: '4px',
                  backgroundColor: 'var(--surface)',
                  color: 'var(--text-primary)',
                  fontSize: '0.9rem',
                }}
              />
            </div>

            <div style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem', marginBottom: '0.75rem' }}>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.85rem', cursor: 'pointer' }}
                title="Leave unticked to just import once. Tick to keep this playlist refreshed automatically.">
                <input type="checkbox" checked={scheduleEnabled} onChange={(e) => setScheduleEnabled(e.target.checked)} />
                Set up a recurring schedule
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.85rem', cursor: 'pointer' }}
                title="If a playlist with the same name exists in Plex, it will be replaced">
                <input type="checkbox" checked={scheduleOverwriteExisting} onChange={(e) => setScheduleOverwriteExisting(e.target.checked)} />
                Overwrite existing playlist
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.85rem', cursor: 'pointer' }}
                title="Replace the playlist cover art with the imported playlist's artwork">
                <input type="checkbox" checked={scheduleOverwriteCover} onChange={(e) => setScheduleOverwriteCover(e.target.checked)} />
                Overwrite cover art
              </label>
            </div>

            {scheduleEnabled && (
              <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.75rem' }}>
                <div style={{ flex: 1 }}>
                  <label style={{ display: 'block', marginBottom: '0.25rem', fontSize: '0.8rem', fontWeight: 500 }}>
                    Frequency
                  </label>
                  <select
                    value={scheduleFrequency}
                    onChange={(e) => setScheduleFrequency(e.target.value as any)}
                    style={{
                      width: '100%',
                      padding: '0.5rem',
                      border: '1px solid var(--border)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                      fontSize: '0.9rem',
                    }}
                  >
                    <option value="daily">Daily</option>
                    <option value="weekly">Weekly</option>
                    <option value="fortnightly">Fortnightly</option>
                    <option value="monthly">Monthly</option>
                    <option value="custom">Custom</option>
                  </select>
                </div>

                <div style={{ flex: 1 }}>
                  <label style={{ display: 'block', marginBottom: '0.25rem', fontSize: '0.8rem', fontWeight: 500 }}>
                    Start Date
                  </label>
                  <input
                    type="date"
                    value={scheduleStartDate || new Date().toISOString().split('T')[0]}
                    onChange={(e) => setScheduleStartDate(e.target.value)}
                    min={new Date().toISOString().split('T')[0]}
                    style={{
                      width: '100%',
                      padding: '0.5rem',
                      border: '1px solid var(--border)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                      fontSize: '0.9rem',
                    }}
                  />
                </div>

                <div style={{ flex: 1 }} title="Schedules are checked every 10 minutes">
                  <label style={{ display: 'block', marginBottom: '0.25rem', fontSize: '0.8rem', fontWeight: 500 }}>
                    Run Time
                  </label>
                  <select
                    value={scheduleRunTime}
                    onChange={(e) => setScheduleRunTime(e.target.value)}
                    style={{
                      width: '100%',
                      padding: '0.5rem',
                      border: '1px solid var(--border)',
                      borderRadius: '4px',
                      backgroundColor: 'var(--surface)',
                      color: 'var(--text-primary)',
                      fontSize: '0.9rem',
                    }}
                  >
                    {Array.from({ length: 144 }, (_, i) => {
                      const hour = Math.floor(i / 6);
                      const minute = (i % 6) * 10;
                      const timeStr = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
                      return <option key={timeStr} value={timeStr}>{timeStr}</option>;
                    })}
                  </select>
                </div>
              </div>
            )}

            <div className="modal-actions" style={{ marginTop: '1rem' }}>
              <button
                className="btn btn-secondary"
                onClick={() => setSchedulePlaylist(null)}
                disabled={isImporting}
              >
                Cancel
              </button>
              <button
                className="btn btn-primary"
                onClick={handleScheduleConfirm}
                disabled={isImporting}
              >
                {isImporting ? (scheduleEnabled ? 'Creating...' : 'Importing...') : (scheduleEnabled ? 'Create Schedule' : 'Import')}
              </button>
            </div>
        </Modal>
      )}

      {/* Playlist Name Edit Modal for Missing Tracks Save */}
      {showMissingNameModal && (
        <div style={{
          position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
          backgroundColor: 'rgba(0, 0, 0, 0.7)', display: 'flex',
          alignItems: 'center', justifyContent: 'center', zIndex: 1000,
        }} onClick={() => setShowMissingNameModal(false)}>
          <div style={{
            backgroundColor: 'var(--surface)', borderRadius: '8px', padding: '1.5rem',
            width: '450px', maxWidth: '90vw',
          }} onClick={(e) => e.stopPropagation()}>
            <h3 style={{ margin: '0 0 1rem 0' }}>Save to Plex & Missing Tracks</h3>
            <p style={{ color: 'var(--text-secondary)', fontSize: '0.875rem', marginBottom: '1rem' }}>
              {editableTracks.filter(t => t.matched).length} matched track{editableTracks.filter(t => t.matched).length !== 1 ? 's' : ''} will be added to a Plex playlist.{' '}
              {editableTracks.filter(t => !t.matched).length} unmatched track{editableTracks.filter(t => !t.matched).length !== 1 ? 's' : ''} will be saved to Missing Tracks for later matching.
            </p>
            <label style={{ display: 'block', fontSize: '0.875rem', marginBottom: '0.25rem', color: 'var(--text-secondary)' }}>
              Playlist Name
            </label>
            <input
              type="text"
              value={missingPlaylistName}
              onChange={(e) => setMissingPlaylistName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && missingPlaylistName.trim() && handleConfirmMissingSave()}
              style={{
                width: '100%', padding: '0.5rem 0.75rem', borderRadius: '4px',
                border: '1px solid var(--border-color)', backgroundColor: 'var(--background)',
                color: 'var(--text-primary)', marginBottom: '1rem', boxSizing: 'border-box',
              }}
              autoFocus
            />
            <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
              <button className="btn btn-secondary" onClick={() => setShowMissingNameModal(false)}>
                Cancel
              </button>
              <button
                className="btn btn-primary"
                onClick={handleConfirmMissingSave}
                disabled={!missingPlaylistName.trim()}
              >
                Save
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};


