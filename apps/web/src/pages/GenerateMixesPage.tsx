import type { FC, ReactNode } from 'react';
import { useState, useCallback } from 'react';
import { Link } from 'react-router-dom';
import { useApp } from '../contexts/AppContext';
import type { Playlist, Schedule } from '@playlist-lab/shared';
import { getNextRunDate } from '../utils/scheduleTime';
import { CustomMixModal, type CustomMixSettings } from './CustomMixModal';
import { AdvancedMixModal } from '../components/AdvancedMixModal';
import { QuickMixSettingsModal } from '../components/QuickMixSettingsModal';
import { SaveTemplateModal } from '../components/SaveTemplateModal';
import { TemplateList, type MixTemplate } from '../components/TemplateList';
import { EditTemplateModal } from '../components/EditTemplateModal';
import { Modal, modalCloseButtonStyle } from '../components/Modal';
import { useConfirm } from '../contexts/ConfirmContext';
import { useEscapeKey } from '../hooks/useEscapeKey';
import './GenerateMixesPage.css';

type MixType = 'weekly' | 'daily' | 'timecapsule' | 'newmusic' | 'deepcuts' | 'artistdiscovery' | 'mood' | 'era' | 'genreevolution' | 'artistjourney' | 'workout' | 'forgottenfavorites' | 'genreblend' | 'custom' | 'all';
type AdvancedMixType = 'artistdiscovery' | 'mood' | 'era' | 'genreevolution' | 'artistjourney' | 'genreblend';
type QuickMixType = 'weekly' | 'daily' | 'timecapsule' | 'newmusic' | 'deepcuts' | 'workout' | 'forgottenfavorites';
type Frequency = 'daily' | 'weekly' | 'fortnightly' | 'monthly';

/** Shared by the "schedule this template" and "schedule this quick mix"
 * modals below - both prompt for the same frequency/start-date/run-time,
 * differing only in the summary shown at the top and what confirming does. */
const ScheduleFrequencyModal: FC<{
  summary: ReactNode;
  frequency: Frequency;
  setFrequency: (f: Frequency) => void;
  startDate: string;
  setStartDate: (d: string) => void;
  runTime: string;
  setRunTime: (t: string) => void;
  isSaving: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}> = ({ summary, frequency, setFrequency, startDate, setStartDate, runTime, setRunTime, isSaving, onConfirm, onCancel }) => (
  <Modal onClose={onCancel} ariaLabel="Schedule Mix" contentStyle={{ maxWidth: '500px' }}>
    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
      <h2 style={{ margin: 0 }}>Schedule Mix</h2>
      <button onClick={onCancel} title="Close" style={modalCloseButtonStyle}>✕</button>
    </div>

    <div style={{ marginBottom: '1.5rem' }}>{summary}</div>

    <div style={{ marginBottom: '1.5rem' }}>
      <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>Update Frequency</label>
      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
        {[
          { value: 'daily', label: 'Daily' },
          { value: 'weekly', label: 'Weekly' },
          { value: 'fortnightly', label: 'Fortnightly (Every 2 weeks)' },
          { value: 'monthly', label: 'Monthly' },
        ].map(option => (
          <label
            key={option.value}
            style={{
              display: 'flex',
              alignItems: 'center',
              padding: '0.75rem',
              border: '1px solid var(--border)',
              borderRadius: '4px',
              cursor: 'pointer',
              backgroundColor: frequency === option.value ? 'var(--surface-hover)' : 'transparent',
            }}
          >
            <input
              type="radio"
              name="frequency"
              value={option.value}
              checked={frequency === option.value}
              onChange={(e) => setFrequency(e.target.value as Frequency)}
              style={{ marginRight: '0.75rem' }}
            />
            {option.label}
          </label>
        ))}
      </div>
    </div>

    <div style={{ marginBottom: '1.5rem' }}>
      <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>Start Date</label>
      <input
        type="date"
        value={startDate}
        onChange={(e) => setStartDate(e.target.value)}
        min={new Date().toISOString().split('T')[0]}
        style={{ width: '100%', padding: '0.75rem', border: '1px solid var(--border)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '1rem' }}
      />
      <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
        First update will occur on this date
      </div>
    </div>

    <div style={{ marginBottom: '1.5rem' }}>
      <label style={{ display: 'block', marginBottom: '0.5rem', fontWeight: 500 }}>Run Time</label>
      <select
        value={runTime}
        onChange={(e) => setRunTime(e.target.value)}
        style={{ width: '100%', padding: '0.75rem', border: '1px solid var(--border)', borderRadius: '4px', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', fontSize: '1rem' }}
      >
        {Array.from({ length: 144 }, (_, i) => {
          const hour = Math.floor(i / 6);
          const minute = (i % 6) * 10;
          const timeStr = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
          return <option key={timeStr} value={timeStr}>{timeStr}</option>;
        })}
      </select>
      <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', marginTop: '0.25rem' }}>
        Schedules are checked every 10 minutes
      </div>
    </div>

    <div style={{ display: 'flex', gap: '0.5rem' }}>
      <button className="btn btn-primary" onClick={onConfirm} disabled={isSaving} style={{ flex: 1 }}>
        {isSaving ? 'Creating...' : 'Create Schedule'}
      </button>
      <button className="btn btn-secondary" onClick={onCancel} disabled={isSaving} style={{ flex: 1 }}>
        Cancel
      </button>
    </div>
  </Modal>
);

/**
 * `onNavigateAway`, when given, is called just before a Link inside this
 * page navigates elsewhere - this page is also rendered inside a header
 * modal (see components/Header.tsx), which controls its own open/closed
 * state independently of the route, so a plain <Link> would change the URL
 * underneath while the modal overlay stayed put. Header passes a callback
 * that closes it; the plain /generate route usage leaves this undefined.
 */
export const GenerateMixesPage: FC<{ onNavigateAway?: () => void }> = ({ onNavigateAway }) => {
  const { apiClient, settings, schedules, refreshPlaylists, refreshSchedules } = useApp();
  const confirmDialog = useConfirm();
  const [isGenerating, setIsGenerating] = useState(false);
  const [generationProgress, setGenerationProgress] = useState<{ message: string; progress: number } | null>(null);
  const [generatedPlaylists, setGeneratedPlaylists] = useState<Playlist[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [selectedMix, setSelectedMix] = useState<MixType | null>(null);
  const [showCustomMixModal, setShowCustomMixModal] = useState(false);
  const [showAdvancedMixModal, setShowAdvancedMixModal] = useState(false);
  const [showQuickMixSettingsModal, setShowQuickMixSettingsModal] = useState(false);
  const [advancedMixType, setAdvancedMixType] = useState<AdvancedMixType | null>(null);
  const [quickMixType, setQuickMixType] = useState<QuickMixType | null>(null);
  const [showSaveTemplateModal, setShowSaveTemplateModal] = useState(false);
  const [showEditTemplateModal, setShowEditTemplateModal] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [successMessage, setSuccessMessage] = useState<string | null>(null);
  const [currentCustomSettings, setCurrentCustomSettings] = useState<CustomMixSettings | null>(null);
  const [templateToEdit, setTemplateToEdit] = useState<any>(null);
  const [initialCustomSettings, setInitialCustomSettings] = useState<Partial<CustomMixSettings> | undefined>(undefined);
  const [templateListKey, setTemplateListKey] = useState(0);
  const [scheduleTemplate, setScheduleTemplate] = useState<MixTemplate | null>(null);
  const [scheduleFrequency, setScheduleFrequency] = useState<'daily' | 'weekly' | 'fortnightly' | 'monthly'>('weekly');
  const [scheduleStartDate, setScheduleStartDate] = useState<string>(() => {
    const today = new Date();
    const year = today.getFullYear();
    const month = String(today.getMonth() + 1).padStart(2, '0');
    const day = String(today.getDate()).padStart(2, '0');
    return `${year}-${month}-${day}`;
  });
  const [scheduleRunTime, setScheduleRunTime] = useState<string>('09:00');
  const [isCreatingSchedule, setIsCreatingSchedule] = useState(false);
  const [scheduleQuickMix, setScheduleQuickMix] = useState<{ mixType: QuickMixType | AdvancedMixType; settings: any } | null>(null);
  const [runningScheduleId, setRunningScheduleId] = useState<number | null>(null);
  const [deletingScheduleId, setDeletingScheduleId] = useState<number | null>(null);

  useEscapeKey(!!scheduleTemplate, () => setScheduleTemplate(null));
  useEscapeKey(!!scheduleQuickMix, () => setScheduleQuickMix(null));

  const mixSchedules = schedules.filter(s => s.scheduleType === 'mix_generation');

  const handleRunMixSchedule = async (scheduleId: number) => {
    setRunningScheduleId(scheduleId);
    setError(null);
    try {
      await apiClient.runSchedule(scheduleId);
      await refreshSchedules();
      setSuccessMessage('Schedule started');
      setTimeout(() => setSuccessMessage(null), 3000);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to run schedule');
    } finally {
      setRunningScheduleId(null);
    }
  };

  const handleDeleteMixSchedule = async (schedule: Schedule) => {
    if (!await confirmDialog('Delete this schedule?')) return;
    setDeletingScheduleId(schedule.id);
    setError(null);
    try {
      await apiClient.deleteSchedule(schedule.id);
      await refreshSchedules();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete schedule');
    } finally {
      setDeletingScheduleId(null);
    }
  };

  // Grouped so mixes that pull from the same underlying signal (your play
  // history vs. your library's structure) sit together, since several of
  // these are easy to confuse at a glance - Time Capsule/Deep Cuts/Forgotten
  // Favorites all sound similar but key off different axes of play history
  // (recency alone, low play count, or high-then-abandoned), which their
  // descriptions now call out explicitly instead of just naming the mix.
  const mixGroups: { group: string; groupDescription: string; mixes: { id: MixType; name: string; description: string }[] }[] = [
    {
      group: 'From Your Listening History',
      groupDescription: 'Built from your play counts and recency - each uses a different angle',
      mixes: [
        { id: 'weekly', name: 'Weekly Mix', description: 'Your most-played artists right now' },
        { id: 'daily', name: 'Daily Mix', description: 'Recent plays, plus related tracks and rediscoveries' },
        { id: 'timecapsule', name: 'Time Capsule', description: "Anything you haven't played in a while, regardless of how often you used to play it" },
        { id: 'forgottenfavorites', name: 'Forgotten Favorites', description: "Tracks you used to play a lot but have stopped playing recently" },
        { id: 'deepcuts', name: 'Deep Cuts Mix', description: "Tracks with low play counts you've likely never given much attention" },
        { id: 'workout', name: 'Workout Mix', description: 'A progressive tempo build for exercise' },
      ],
    },
    {
      group: 'Explore Your Library',
      groupDescription: "Built from your library's structure - genres, eras, artists - not your play history",
      mixes: [
        { id: 'newmusic', name: 'New Music Mix', description: 'Recently added albums' },
        { id: 'artistdiscovery', name: 'Artist Discovery', description: 'Tracks from artists similar to ones you like' },
        { id: 'mood', name: 'Mood Mix', description: 'Tracks filtered by mood tags' },
        { id: 'era', name: 'Era Mix', description: 'Tracks from a specific decade' },
        { id: 'genreevolution', name: 'Genre Evolution', description: 'How a genre evolved over time' },
        { id: 'artistjourney', name: 'Artist Journey', description: "One artist's discography in chronological order" },
        { id: 'genreblend', name: 'Genre Blend', description: 'Tracks spanning multiple genres' },
      ],
    },
  ];

  const handleGenerate = useCallback(async (mixType: MixType) => {
    // Show modal for mix types that require user input
    if (mixType === 'custom') {
      setShowCustomMixModal(true);
      return;
    }
    
    // Show advanced modal for complex mixes
    if (['artistdiscovery', 'mood', 'era', 'genreevolution', 'artistjourney', 'genreblend'].includes(mixType)) {
      setAdvancedMixType(mixType as AdvancedMixType);
      setShowAdvancedMixModal(true);
      return;
    }

    // Show quick settings modal for basic mixes
    if (['weekly', 'daily', 'timecapsule', 'newmusic', 'deepcuts', 'workout', 'forgottenfavorites'].includes(mixType)) {
      setQuickMixType(mixType as QuickMixType);
      setShowQuickMixSettingsModal(true);
      return;
    }

    // Only 'all' should reach here - generate without settings
    if (mixType === 'all') {
      setError(null);
      setGeneratedPlaylists([]);
      setIsGenerating(true);
      setSelectedMix(mixType);

      try {
        const result = await apiClient.generateAllMixes();
        setGeneratedPlaylists(Array.isArray(result) ? result : [result]);
        await refreshPlaylists();
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to generate mixes');
      } finally {
        setIsGenerating(false);
        setSelectedMix(null);
      }
    }
  }, [apiClient, refreshPlaylists]);

  const handleGenerateCustomMix = async (customSettings: CustomMixSettings) => {
    setError(null);
    setGeneratedPlaylists([]);
    setIsGenerating(true);
    setGenerationProgress(null);
    setSelectedMix('custom');
    setShowCustomMixModal(false);
    setCurrentCustomSettings(customSettings); // Store settings for potential template save

    // Generate a unique session ID for progress tracking
    const sessionId = `custom-mix-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
    
    // Set up SSE connection for progress updates
    const eventSource = new EventSource(`/api/mixes/progress/${sessionId}`, {
      withCredentials: true
    });

    eventSource.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data);
        
        if (data.type === 'progress') {
          setGenerationProgress({ message: data.message, progress: data.progress });
        } else if (data.type === 'complete') {
          setGenerationProgress({ message: 'Complete!', progress: 100 });
          eventSource.close();
        } else if (data.type === 'error') {
          setError(data.message);
          eventSource.close();
          setIsGenerating(false);
          setGenerationProgress(null);
          setSelectedMix(null);
        }
      } catch (err) {
        console.error('Failed to parse SSE message:', err);
      }
    };

    eventSource.onerror = () => {
      eventSource.close();
    };

    try {
      const result = await apiClient.generateCustomMix({ ...customSettings, sessionId } as any);
      if (result.success) {
        // Convert the response to match the expected Playlist type
        const now = Date.now();
        setGeneratedPlaylists([{
          id: 0, // Temporary ID, will be replaced after refresh
          plexPlaylistId: result.playlist.id,
          name: result.playlist.name,
          source: 'custom-mix',
          trackCount: result.playlist.trackCount,
          createdAt: now,
          updatedAt: now,
        }]);
        await refreshPlaylists();
      } else {
        setError('Failed to generate custom mix');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to generate custom mix');
    } finally {
      eventSource.close();
      setIsGenerating(false);
      setGenerationProgress(null);
      setSelectedMix(null);
    }
  };

  const handleGenerateAdvancedMix = async (settings: any) => {
    setError(null);
    setGeneratedPlaylists([]);
    setIsGenerating(true);
    setSelectedMix(advancedMixType!);
    setShowAdvancedMixModal(false);

    try {
      let result: any;

      switch (advancedMixType) {
        case 'artistdiscovery':
          result = await apiClient.generateArtistDiscoveryMix(settings);
          break;
        case 'mood':
          result = await apiClient.generateMoodMix(settings);
          break;
        case 'era':
          result = await apiClient.generateEraMix(settings);
          break;
        case 'genreevolution':
          result = await apiClient.generateGenreEvolutionMix(settings);
          break;
        case 'artistjourney':
          result = await apiClient.generateArtistJourneyMix(settings);
          break;
        case 'genreblend':
          result = await apiClient.generateGenreBlendMix(settings);
          break;
      }

      if (result.success) {
        const now = Date.now();
        setGeneratedPlaylists([{
          id: 0,
          plexPlaylistId: result.playlist.id,
          name: result.playlist.name,
          source: advancedMixType!,
          trackCount: result.playlist.trackCount,
          createdAt: now,
          updatedAt: now,
        }]);
        await refreshPlaylists();
      } else {
        setError(result.message || 'Failed to generate mix');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to generate mix');
    } finally {
      setIsGenerating(false);
      setSelectedMix(null);
    }
  };

  const handleGenerateQuickMix = async (settings: any) => {
    setError(null);
    setGeneratedPlaylists([]);
    setIsGenerating(true);
    setSelectedMix(quickMixType!);
    setShowQuickMixSettingsModal(false);

    try {
      let result: any;

      switch (quickMixType) {
        case 'weekly':
          result = await apiClient.generateWeeklyMix();
          break;
        case 'daily':
          result = await apiClient.generateDailyMix();
          break;
        case 'timecapsule':
          result = await apiClient.generateTimeCapsule();
          break;
        case 'newmusic':
          result = await apiClient.generateNewMusicMix();
          break;
        case 'deepcuts':
          result = await apiClient.generateDeepCutsMix(settings);
          break;
        case 'workout':
          result = await apiClient.generateWorkoutMix(settings);
          break;
        case 'forgottenfavorites':
          result = await apiClient.generateForgottenFavoritesMix(settings);
          break;
      }

      // Handle response format
      if (result.success !== undefined) {
        if (result.success) {
          const now = Date.now();
          setGeneratedPlaylists([{
            id: 0,
            plexPlaylistId: result.playlist.id,
            name: result.playlist.name,
            source: quickMixType!,
            trackCount: result.playlist.trackCount,
            createdAt: now,
            updatedAt: now,
          }]);
        } else {
          setError(result.message || 'Failed to generate mix');
        }
      } else {
        setGeneratedPlaylists(Array.isArray(result) ? result : [result]);
      }
      
      await refreshPlaylists();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to generate mix');
    } finally {
      setIsGenerating(false);
      setSelectedMix(null);
    }
  };

  const handleAddQuickMixToSchedule = (settings: any) => {
    // Store the mix type and settings, then open the schedule modal
    if (quickMixType) {
      setScheduleQuickMix({ mixType: quickMixType, settings });
      setShowQuickMixSettingsModal(false);
      setQuickMixType(null);
    }
  };

  const handleScheduleQuickMixConfirm = async () => {
    if (!scheduleQuickMix) return;

    setIsCreatingSchedule(true);
    setError(null);

    try {
      await apiClient.createSchedule({
        scheduleType: 'mix_generation',
        frequency: scheduleFrequency,
        startDate: scheduleStartDate,
        playlistId: undefined,
        config: {
          mixType: scheduleQuickMix.mixType,
          mixName: scheduleQuickMix.settings.playlistName || `${scheduleQuickMix.mixType} Mix`,
          ...scheduleQuickMix.settings,
          run_time: scheduleRunTime,
        },
      } as any);

      // Refresh schedules to show the new one (with small delay to ensure it's saved)
      await new Promise(resolve => setTimeout(resolve, 100));
      await refreshSchedules();

      setSuccessMessage(`Schedule created for "${scheduleQuickMix.settings.playlistName || scheduleQuickMix.mixType}"!`);
      setScheduleQuickMix(null);
      
      setTimeout(() => setSuccessMessage(null), 3000);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create schedule');
    } finally {
      setIsCreatingSchedule(false);
    }
  };

  const handleAddAdvancedMixToSchedule = (settings: any) => {
    if (advancedMixType) {
      setScheduleQuickMix({ mixType: advancedMixType, settings });
      setShowAdvancedMixModal(false);
      setAdvancedMixType(null);
    }
  };

  const handleSaveCustomMixAsTemplate = async (customSettings: CustomMixSettings) => {
    setCurrentCustomSettings(customSettings);
    setShowCustomMixModal(false);
    setShowSaveTemplateModal(true);
  };

  const handleSaveTemplate = async (name: string, description: string) => {
    if (!currentCustomSettings) {
      throw new Error('No custom mix settings to save');
    }

    setIsSaving(true);
    try {
      // Convert CustomMixSettings to template configuration
      const configuration = {
        mixType: 'custom',
        trackCount: currentCustomSettings.trackCount,
        sortBy: currentCustomSettings.sortBy,
        sortDirection: currentCustomSettings.sortDirection,
        customRules: {
          // Time filters
          playedInLastDays: currentCustomSettings.playedInLastDays,
          notPlayedInLastDays: currentCustomSettings.notPlayedInLastDays,
          addedInLastDays: currentCustomSettings.addedInLastDays,
          
          // Release date filters
          yearRange: {
            min: currentCustomSettings.releasedAfterYear,
            max: currentCustomSettings.releasedBeforeYear,
          },
          
          // Rating & popularity
          minRating: currentCustomSettings.minRating,
          maxRating: currentCustomSettings.maxRating,
          minPlayCount: currentCustomSettings.minPlayCount,
          maxPlayCount: currentCustomSettings.maxPlayCount,
          
          // Track characteristics
          minDuration: currentCustomSettings.minDuration,
          maxDuration: currentCustomSettings.maxDuration,
          minTrackNumber: currentCustomSettings.minTrackNumber,
          maxTrackNumber: currentCustomSettings.maxTrackNumber,
          discNumber: currentCustomSettings.discNumber,
          
          // Quality filters
          minBitrate: currentCustomSettings.minBitrate,
          audioCodec: currentCustomSettings.audioCodec,
          minSampleRate: currentCustomSettings.minSampleRate,
          losslessOnly: currentCustomSettings.losslessOnly,
          
          // Metadata filters
          includeGenres: currentCustomSettings.genres,
          excludeGenres: currentCustomSettings.excludeGenres,
          includeMoods: currentCustomSettings.moods,
          excludeMoods: currentCustomSettings.excludeMoods,
          includeStyles: currentCustomSettings.styles,
          excludeStyles: currentCustomSettings.excludeStyles,
          collections: currentCustomSettings.collections,
          labels: currentCustomSettings.labels,
          
          // Artist/Album filters
          artistNames: currentCustomSettings.artistNames,
          albumTitles: currentCustomSettings.albumTitles,
          
          // Sonic Analysis filters
          sonicSeedTrackKey: currentCustomSettings.sonicSeedTrackKey,
          sonicSeedArtistKey: currentCustomSettings.sonicSeedArtistKey,
          sonicMaxDistance: currentCustomSettings.sonicMaxDistance,
          sonicIncludeSameArtist: currentCustomSettings.sonicIncludeSameArtist,
          sonicIncludeSimilarArtists: currentCustomSettings.sonicIncludeSimilarArtists,
          sonicUsePopularTracks: currentCustomSettings.sonicUsePopularTracks,
        },
      };

      await apiClient.createMixTemplate({
        name,
        description: description || undefined,
        mixType: 'custom',
        configuration,
      });

      setSuccessMessage('Template saved successfully!');
      setShowSaveTemplateModal(false);
      setTemplateListKey(prev => prev + 1); // Force template list refresh
      
      // Clear success message after 3 seconds
      setTimeout(() => setSuccessMessage(null), 3000);
    } catch (err) {
      throw err; // Let the modal handle the error display
    } finally {
      setIsSaving(false);
    }
  };

  // Template handlers
  const handleScheduleTemplate = useCallback((template: MixTemplate) => {
    // Open schedule modal instead of navigating
    setScheduleTemplate(template);
  }, []);

  const handleQuickGenerate = useCallback(async (template: MixTemplate) => {
    setError(null);
    setGeneratedPlaylists([]);
    setIsGenerating(true);
    setGenerationProgress(null);
    setSelectedMix('custom');

    // Generate a unique session ID for progress tracking
    const sessionId = `template-mix-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
    
    // Set up SSE connection for progress updates
    const eventSource = new EventSource(`/api/mixes/progress/${sessionId}`, {
      withCredentials: true
    });

    eventSource.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data);
        
        if (data.type === 'progress') {
          setGenerationProgress({ message: data.message, progress: data.progress });
        } else if (data.type === 'complete') {
          setGenerationProgress({ message: 'Complete!', progress: 100 });
          eventSource.close();
        } else if (data.type === 'error') {
          setError(data.message);
          eventSource.close();
          setIsGenerating(false);
          setGenerationProgress(null);
          setSelectedMix(null);
        }
      } catch (err) {
        console.error('Failed to parse SSE message:', err);
      }
    };

    eventSource.onerror = () => {
      eventSource.close();
    };

    try {
      // Use template name as playlist name
      const playlistName = template.name;
      
      // Use the template generation endpoint which handles all mix types
      // and automatically updates usage statistics
      const result = await apiClient.generateMixFromTemplate(template.id, playlistName, sessionId);
      
      // Create a playlist object for display
      const playlist: Playlist = {
        id: parseInt(result.playlistId),
        plexPlaylistId: result.playlistId,
        name: playlistName,
        source: 'template',
        sourceUrl: undefined,
        createdAt: Date.now(),
        updatedAt: Date.now(),
        trackCount: result.trackCount,
        userId: 0, // Will be set by backend
      };
      
      setGeneratedPlaylists([playlist]);
      await refreshPlaylists();
      setTemplateListKey(prev => prev + 1); // Force template list refresh
      
      // Show warnings if any
      if (result.warnings && result.warnings.length > 0) {
        setSuccessMessage(`${result.message}\n\nWarnings:\n${result.warnings.join('\n')}`);
      } else {
        setSuccessMessage(result.message);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to generate mix from template');
    } finally {
      eventSource.close();
      setIsGenerating(false);
      setGenerationProgress(null);
      setSelectedMix(null);
    }
  }, [apiClient, refreshPlaylists]);

  const handleEditTemplate = (template: MixTemplate) => {
    // Convert MixTemplate to the format expected by EditTemplateModal
    const editTemplate = {
      id: template.id,
      name: template.name,
      description: template.description,
      mix_type: template.mixType,
      configuration: template.configuration,
      created_at: template.createdAt,
      updated_at: template.updatedAt,
      last_used_at: template.lastUsedAt,
      use_count: template.useCount,
    };
    setTemplateToEdit(editTemplate as any);
    setShowEditTemplateModal(true);
  };

  const handleSaveEditedTemplate = async (id: number, name: string, description: string, configuration: any) => {
    setIsSaving(true);
    try {
      await apiClient.updateMixTemplate(id, { name, description, configuration });

      setSuccessMessage('Template updated successfully!');
      setShowEditTemplateModal(false);
      setTemplateToEdit(null);
      setTemplateListKey(prev => prev + 1); // Force template list refresh
      
      setTimeout(() => setSuccessMessage(null), 3000);
    } catch (err) {
      throw err;
    } finally {
      setIsSaving(false);
    }
  };

  const handleScheduleConfirm = async () => {
    if (!scheduleTemplate) return;

    setIsCreatingSchedule(true);
    setError(null);

    try {
      await apiClient.createSchedule({
        scheduleType: 'mix_generation',
        frequency: scheduleFrequency,
        startDate: scheduleStartDate,
        playlistId: undefined,
        config: {
          templateId: scheduleTemplate.id,
          templateName: scheduleTemplate.name,
          run_time: scheduleRunTime,
        },
      } as any);

      // Refresh schedules to show the new one (with small delay to ensure it's saved)
      await new Promise(resolve => setTimeout(resolve, 100));
      await refreshSchedules();

      setSuccessMessage(`Schedule created for "${scheduleTemplate.name}"!`);
      setScheduleTemplate(null);
      
      setTimeout(() => setSuccessMessage(null), 3000);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create schedule');
    } finally {
      setIsCreatingSchedule(false);
    }
  };

  return (
    <div className="page-container">
      <div className="page-header">
        <h1 className="page-title">Generate Mixes</h1>
        <p className="page-description">Generate personalized playlists based on your listening history</p>
      </div>

      <div className="generate-mixes-content">
        {/* Quick Mixes Section */}
        <section className="quick-mixes-section">
          <div className="section-header">
            <h2 className="section-title">Quick Mixes</h2>
            <p className="section-description">Generate instant playlists with customizable settings</p>
          </div>
          {mixGroups.map(group => (
            <div key={group.group} style={{ marginBottom: '1.5rem' }}>
              <h3 style={{ fontSize: '0.9375rem', marginBottom: '0.125rem' }}>{group.group}</h3>
              <p className="section-description" style={{ marginBottom: '0.75rem' }}>{group.groupDescription}</p>
              <div className="quick-mixes-grid">
                {group.mixes.map(mix => (
                  <button
                    key={mix.id}
                    className={`quick-mix-card ${isGenerating && selectedMix === mix.id ? 'generating' : ''}`}
                    onClick={() => !isGenerating && handleGenerate(mix.id)}
                    disabled={isGenerating && selectedMix !== mix.id}
                  >
                    <div className="quick-mix-content">
                      <h3 className="quick-mix-name">{mix.name}</h3>
                      <p className="quick-mix-description">{mix.description}</p>
                    </div>
                    {isGenerating && selectedMix === mix.id && (
                      <div className="quick-mix-status">
                        <div className="spinner"></div>
                        <span>Generating...</span>
                      </div>
                    )}
                  </button>
                ))}
              </div>
            </div>
          ))}
          <div className="quick-mixes-grid">
            <button
              className={`quick-mix-card ${isGenerating && selectedMix === 'all' ? 'generating' : ''}`}
              onClick={() => !isGenerating && handleGenerate('all')}
              disabled={isGenerating && selectedMix !== 'all'}
            >
              <div className="quick-mix-content">
                <h3 className="quick-mix-name">Generate All</h3>
                <p className="quick-mix-description">Create every mix above at once</p>
              </div>
              {isGenerating && selectedMix === 'all' && (
                <div className="quick-mix-status">
                  <div className="spinner"></div>
                  <span>Generating...</span>
                </div>
              )}
            </button>
          </div>
        </section>

        {/* Custom Mixes Section */}
        <section className="custom-mixes-section">
          <div className="section-header">
            <h2 className="section-title">Custom Mixes</h2>
            <p className="section-description">Create advanced mixes with custom filters or use saved configurations</p>
          </div>
          
          <div className="custom-mixes-container">
            {/* Saved Mixes (Templates) */}
            <div className="saved-mixes-wrapper">
              <TemplateList
                key={templateListKey}
                onGenerate={handleQuickGenerate}
                onSchedule={handleScheduleTemplate}
                onEdit={handleEditTemplate}
              />
            </div>

            {/* Create New Custom Mix Button */}
            <button
              className="create-custom-mix-btn-compact"
              onClick={() => setShowCustomMixModal(true)}
              disabled={isGenerating}
            >
              <span className="create-custom-icon-compact">+</span>
              <span className="create-custom-text-compact">Create New Custom Mix</span>
            </button>
          </div>
        </section>

        {/* Scheduled Mixes - mix-generation schedules aren't tied to any
            playlist, so they can't show in the playlist table's Schedule
            column like playlist-refresh schedules do; this is their only
            manage UI. */}
        {mixSchedules.length > 0 && (
          <section className="generated-playlists-section">
            <div className="section-header">
              <h2 className="section-title">Scheduled Mixes</h2>
            </div>
            <div className="generated-playlists-list">
              {mixSchedules.map(schedule => (
                <div key={schedule.id} className="generated-playlist-item">
                  <div className="generated-playlist-info">
                    <div className="generated-playlist-name">
                      {schedule.config?.mixName || schedule.config?.templateName || 'Mix'}
                    </div>
                    <div className="playlist-status">
                      {schedule.frequency} • next {getNextRunDate(schedule)}
                      {schedule.lastRun ? ` • last run ${new Date(schedule.lastRun * 1000).toLocaleDateString()}` : ''}
                    </div>
                  </div>
                  <div className="playlist-actions">
                    <button
                      className="btn btn-secondary btn-small"
                      onClick={() => handleRunMixSchedule(schedule.id)}
                      disabled={runningScheduleId === schedule.id}
                    >
                      {runningScheduleId === schedule.id ? 'Running…' : 'Run Now'}
                    </button>
                    <button
                      className="btn btn-secondary btn-small"
                      onClick={() => handleDeleteMixSchedule(schedule)}
                      disabled={deletingScheduleId === schedule.id}
                    >
                      Delete
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </section>
        )}

        {/* Generated Playlists */}
        {generatedPlaylists.length > 0 && (
          <section className="generated-playlists-section">
            <div className="section-header">
              <h2 className="section-title">Generated Playlists</h2>
            </div>
            <div className="generated-playlists-list">
              {generatedPlaylists.map(playlist => (
                <div key={playlist.id} className="generated-playlist-item">
                  <div className="generated-playlist-info">
                    <div className="generated-playlist-name">{playlist.name}</div>
                    <div className="playlist-status">Created successfully • {playlist.trackCount} tracks</div>
                  </div>
                  <div className="playlist-actions">
                    <Link to="/" onClick={onNavigateAway} className="btn btn-secondary btn-small">
                      View
                    </Link>
                    <Link to={`/?scheduleFor=${playlist.id}`} onClick={onNavigateAway} className="btn btn-primary btn-small">
                      Schedule
                    </Link>
                  </div>
                </div>
              ))}
            </div>
          </section>
        )}

        {successMessage && (
          <div className="generate-mixes-success">
            {successMessage}
          </div>
        )}

        {error && (
          <div className="generate-mixes-error">
            {error}
          </div>
        )}

        {/* Progress Display */}
        {isGenerating && generationProgress && (
          <div className="generation-progress-container">
            <div className="progress-header">
              <div className="spinner"></div>
              <span className="progress-message">{generationProgress.message}</span>
            </div>
            <div className="progress-bar-wrapper">
              <div 
                className="progress-bar-fill" 
                style={{ width: `${generationProgress.progress}%` }}
              />
            </div>
            <div className="progress-percentage">{Math.round(generationProgress.progress)}%</div>
          </div>
        )}
      </div>

      {/* Custom Mix Modal */}
      {showCustomMixModal && (
        <CustomMixModal
          onClose={() => {
            setShowCustomMixModal(false);
            setInitialCustomSettings(undefined);
          }}
          onGenerate={handleGenerateCustomMix}
          onSaveAsTemplate={handleSaveCustomMixAsTemplate}
          isGenerating={isGenerating}
          initialSettings={initialCustomSettings}
        />
      )}

      {/* Advanced Mix Modal */}
      {showAdvancedMixModal && advancedMixType && (
        <AdvancedMixModal
          mixType={advancedMixType}
          onClose={() => {
            setShowAdvancedMixModal(false);
            setAdvancedMixType(null);
          }}
          onGenerate={handleGenerateAdvancedMix}
          onAddToSchedule={handleAddAdvancedMixToSchedule}
          isGenerating={isGenerating}
        />
      )}

      {/* Quick Mix Settings Modal */}
      {showQuickMixSettingsModal && quickMixType && (
        <QuickMixSettingsModal
          mixType={quickMixType}
          onClose={() => {
            setShowQuickMixSettingsModal(false);
            setQuickMixType(null);
          }}
          onGenerate={handleGenerateQuickMix}
          onAddToSchedule={handleAddQuickMixToSchedule}
          isGenerating={isGenerating}
          defaultSettings={settings?.mixSettings}
        />
      )}

      {/* Save Template Modal */}
      {showSaveTemplateModal && (
        <SaveTemplateModal
          onClose={() => setShowSaveTemplateModal(false)}
          onSave={handleSaveTemplate}
          isSaving={isSaving}
          initialName={currentCustomSettings?.name || ''}
        />
      )}

      {/* Edit Template Modal */}
      {showEditTemplateModal && templateToEdit && (
        <EditTemplateModal
          template={templateToEdit}
          onClose={() => {
            setShowEditTemplateModal(false);
            setTemplateToEdit(null);
          }}
          onSave={handleSaveEditedTemplate}
          isSaving={isSaving}
        />
      )}

      {scheduleTemplate && (
        <ScheduleFrequencyModal
          summary={
            <>
              <div style={{ fontWeight: 500, marginBottom: "0.5rem" }}>{scheduleTemplate.name}</div>
              {scheduleTemplate.description && (
                <div style={{ fontSize: "0.875rem", color: "var(--text-secondary)" }}>
                  {scheduleTemplate.description}
                </div>
              )}
              <div style={{ fontSize: "0.75rem", color: "var(--text-muted)", marginTop: "0.5rem" }}>
                This schedule will generate a new playlist from this template
              </div>
            </>
          }
          frequency={scheduleFrequency}
          setFrequency={setScheduleFrequency}
          startDate={scheduleStartDate}
          setStartDate={setScheduleStartDate}
          runTime={scheduleRunTime}
          setRunTime={setScheduleRunTime}
          isSaving={isCreatingSchedule}
          onConfirm={handleScheduleConfirm}
          onCancel={() => setScheduleTemplate(null)}
        />
      )}

      {scheduleQuickMix && (
        <ScheduleFrequencyModal
          summary={
            <>
              <div style={{ fontWeight: 500, marginBottom: "0.5rem" }}>
                {scheduleQuickMix.settings.playlistName || `${scheduleQuickMix.mixType} Mix`}
              </div>
              <div style={{ fontSize: "0.75rem", color: "var(--text-muted)", marginTop: "0.5rem" }}>
                This schedule will generate a new {scheduleQuickMix.mixType} mix
              </div>
            </>
          }
          frequency={scheduleFrequency}
          setFrequency={setScheduleFrequency}
          startDate={scheduleStartDate}
          setStartDate={setScheduleStartDate}
          runTime={scheduleRunTime}
          setRunTime={setScheduleRunTime}
          isSaving={isCreatingSchedule}
          onConfirm={handleScheduleQuickMixConfirm}
          onCancel={() => setScheduleQuickMix(null)}
        />
      )}

    </div>
  );
};
