import type { FC, ReactNode } from 'react';
import { Fragment, useState, useEffect, useMemo, useRef } from 'react';
import { useApp } from '../contexts/AppContext';
import type { Schedule, MissingTrack } from '@playlist-lab/shared';
import { PlaylistEditor } from '../components/playlist-panel/PlaylistEditor';
import { ShareModal } from '../components/playlist-panel/ShareModal';
import { ExportModal } from '../components/playlist-panel/ExportModal';
import { ScheduleModal } from '../components/playlist-panel/ScheduleModal';
import { MissingTracksPanel } from '../components/playlist-panel/MissingTracksPanel';
import { SharedWithMeModal } from '../components/playlist-panel/SharedWithMeModal';
import { BackupRestorePage } from './BackupRestorePage';
import { getNextRunTimestamp, getNextRunRelative, getNextRunDate } from '../utils/scheduleTime';
import { useEscapeKey } from '../hooks/useEscapeKey';
import { EditIcon, ShareIcon, ExportIcon, ReimportIcon, BackupIcon, DeleteIcon, FilterIcon, SmartIcon } from '../components/icons';
import './PlaylistsPage.css';

interface FilterOption { value: string; label: string }

/** Small popover filter menu attached to a table header. Shared by the
 * Source, Missing Tracks and Schedule columns below - click-outside and
 * Escape both close it, and the button that opens it is highlighted
 * whenever a non-default option is selected. */
const FilterMenu: FC<{ options: FilterOption[]; value: string; onChange: (v: string) => void }> = ({ options, value, onChange }) => {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement>(null);
  const active = value !== options[0]?.value;

  useEscapeKey(open, () => setOpen(false));

  useEffect(() => {
    if (!open) return;
    const onClickOutside = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onClickOutside);
    return () => document.removeEventListener('mousedown', onClickOutside);
  }, [open]);

  return (
    <span className="th-filter" ref={ref}>
      <button
        type="button"
        className={`th-filter-btn ${active ? 'active' : ''}`}
        onClick={(e) => { e.stopPropagation(); setOpen(o => !o); }}
        title="Filter"
      >
        <FilterIcon />
      </button>
      {open && (
        <div className="th-filter-menu" onClick={(e) => e.stopPropagation()}>
          {options.map(opt => (
            <button
              key={opt.value}
              type="button"
              className={`th-filter-option ${value === opt.value ? 'active' : ''}`}
              onClick={() => { onChange(opt.value); setOpen(false); }}
            >
              {opt.label}
            </button>
          ))}
        </div>
      )}
    </span>
  );
};

/** Popover filter with min/max number inputs, for the Tracks and Duration
 * columns. Shares the open/close-on-outside-click/Escape behavior with
 * FilterMenu above but needs free-form inputs instead of a fixed option list. */
const RangeFilterMenu: FC<{ unit: string; min: string; max: string; onChange: (min: string, max: string) => void }> = ({ unit, min, max, onChange }) => {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement>(null);
  const active = min !== '' || max !== '';

  useEscapeKey(open, () => setOpen(false));

  useEffect(() => {
    if (!open) return;
    const onClickOutside = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onClickOutside);
    return () => document.removeEventListener('mousedown', onClickOutside);
  }, [open]);

  return (
    <span className="th-filter" ref={ref}>
      <button
        type="button"
        className={`th-filter-btn ${active ? 'active' : ''}`}
        onClick={(e) => { e.stopPropagation(); setOpen(o => !o); }}
        title="Filter"
      >
        <FilterIcon />
      </button>
      {open && (
        <div className="th-filter-menu th-filter-menu-range" onClick={(e) => e.stopPropagation()}>
          <label>
            Min {unit}
            <input type="number" min={0} value={min} onChange={(e) => onChange(e.target.value, max)} placeholder="0" />
          </label>
          <label>
            Max {unit}
            <input type="number" min={0} value={max} onChange={(e) => onChange(min, e.target.value)} placeholder="Any" />
          </label>
          {active && (
            <button type="button" className="th-filter-option" onClick={() => onChange('', '')}>Clear</button>
          )}
        </div>
      )}
    </span>
  );
};

/** Sortable column header, optionally with a filter popover. Declared at
 * module scope (not inside PlaylistsPage) and takes sort state as explicit
 * props rather than closing over it - a component declared inside another
 * component's body is a new function identity every render, which makes
 * React remount its whole subtree (losing FilterMenu/RangeFilterMenu's open
 * state) every time any state this file owns changes, including on every
 * keystroke in a range filter's inputs. */
const SortableHeader: FC<{
  label: string;
  sortKeyName: SortKey;
  currentSortKey: SortKey;
  currentSortDir: SortDir;
  onSort: (key: SortKey) => void;
  filter?: ReactNode;
}> = ({ label, sortKeyName, currentSortKey, currentSortDir, onSort, filter }) => (
  <th className="sortable" onClick={() => onSort(sortKeyName)}>
    <div className={filter ? 'th-with-filter' : undefined}>
      <span className="th-label">
        {label}
        {currentSortKey === sortKeyName && <span className="sort-arrow">{currentSortDir === 'asc' ? '▲' : '▼'}</span>}
      </span>
      {filter}
    </div>
  </th>
);

interface Playlist {
  id: string;
  dbId?: number;
  name: string;
  source: string;
  sourceUrl?: string;
  trackCount: number;
  duration: number;
  smart?: boolean;
  composite?: string;
}

type SortKey = 'name' | 'tracks' | 'duration' | 'missing' | 'schedule' | 'nextRun' | 'lastRun';
type SortDir = 'asc' | 'desc';
type Modal = { type: 'edit' | 'share' | 'export' | 'schedule'; playlist: Playlist } | null;

export const PlaylistsPage: FC = () => {
  const { apiClient, schedules, server, playlists: contextPlaylists, isLoading, refreshPlaylists } = useApp();
  const playlists = contextPlaylists as unknown as Playlist[];
  const [error, setError] = useState<string | null>(null);
  const [missingByDbId, setMissingByDbId] = useState<Record<number, MissingTrack[]>>({});
  const [expandedMissingFor, setExpandedMissingFor] = useState<number | null>(null);
  const [modal, setModal] = useState<Modal>(null);
  const [search, setSearch] = useState('');
  const [sortKey, setSortKey] = useState<SortKey>('name');
  const [sortDir, setSortDir] = useState<SortDir>('asc');
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [reimportingId, setReimportingId] = useState<string | null>(null);
  const [backingUpId, setBackingUpId] = useState<string | null>(null);
  const [showBackupAll, setShowBackupAll] = useState(false);
  const [showSharedWithMe, setShowSharedWithMe] = useState(false);
  const [showAttentionOnly, setShowAttentionOnly] = useState(false);
  const [runningExecutions, setRunningExecutions] = useState<any[]>([]);
  const [recentExecutions, setRecentExecutions] = useState<any[]>([]);
  const [sourceFilter, setSourceFilter] = useState('all');
  const [missingFilter, setMissingFilter] = useState('all');
  const [scheduleFilter, setScheduleFilter] = useState('all');
  const [smartFilter, setSmartFilter] = useState('all');
  const [tracksMin, setTracksMin] = useState('');
  const [tracksMax, setTracksMax] = useState('');
  const [durationMin, setDurationMin] = useState(''); // minutes
  const [durationMax, setDurationMax] = useState(''); // minutes
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);

  useEscapeKey(modal?.type === 'edit', () => setModal(null));
  useEscapeKey(showBackupAll, () => setShowBackupAll(false));

  const loadMissingTracks = async () => {
    try {
      const response = await apiClient.getMissingTracks();
      const byDbId: Record<number, MissingTrack[]> = {};
      for (const group of response.missingTracks || []) {
        byDbId[group.playlistId] = group.tracks;
      }
      setMissingByDbId(byDbId);
    } catch (err) {
      console.error('Failed to load missing tracks:', err);
    }
  };

  // Schedule run status (for the colored dot in the Schedule column and the
  // "Needs Attention" filter) - independently polled here rather than
  // shared with the Schedules section below, matching how the rest of this
  // app polls (e.g. the header activity indicator and Queue modal each poll
  // the import queue on their own too).
  const loadScheduleStatus = async () => {
    try {
      const [running, recent] = await Promise.all([
        apiClient.getRunningExecutions(),
        apiClient.getRecentExecutions(100),
      ]);
      setRunningExecutions(running.executions || []);
      setRecentExecutions(recent.executions || []);
    } catch (err) {
      console.error('Failed to load schedule status:', err);
    }
  };

  useEffect(() => {
    loadMissingTracks();
    loadScheduleStatus();
    const interval = setInterval(loadScheduleStatus, 15000);
    return () => clearInterval(interval);
  }, []);

  const getScheduleStatus = (scheduleId: number): 'running' | 'success' | 'failed' | 'never' => {
    if (runningExecutions.some((e: any) => e.scheduleId === scheduleId)) return 'running';
    const executions = recentExecutions.filter((e: any) => e.scheduleId === scheduleId);
    if (executions.length === 0) return 'never';
    const latest = executions.reduce((a: any, b: any) => (b.startedAt > a.startedAt ? b : a));
    return latest.status ?? 'never';
  };

  // Deep link from elsewhere in the app (e.g. a schedule's "View missing
  // tracks" link): /playlists?missingFor=<dbId> expands that row's panel.
  useEffect(() => {
    if (playlists.length === 0) return;
    const params = new URLSearchParams(window.location.search);
    const missingFor = params.get('missingFor');
    if (missingFor) {
      setExpandedMissingFor(parseInt(missingFor, 10));
      window.history.replaceState({}, '', '/');
    }
  }, [playlists]);

  const scheduleByDbId = useMemo(() => {
    const map = new Map<number, Schedule>();
    for (const s of schedules) {
      if (s.playlistId) map.set(s.playlistId, s);
    }
    return map;
  }, [schedules]);

  const getCoverUrl = (composite?: string) => {
    if (!composite || !server) return null;
    return `/api/proxy/image?url=${encodeURIComponent(`${server.url}${composite}`)}`;
  };

  const formatDuration = (ms: number) => {
    const totalMinutes = Math.floor(ms / 60000);
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    return hours > 0 ? `${hours}h ${minutes}m` : `${minutes}m`;
  };

  const getSourceLabel = (source?: string) => (source && source !== 'plex' ? source.charAt(0).toUpperCase() + source.slice(1) : null);
  const NON_REIMPORTABLE_SOURCES = ['plex', 'manual', 'template'];
  const isReimportable = (playlist: Playlist) => !!playlist.dbId && !!playlist.source && !NON_REIMPORTABLE_SOURCES.includes(playlist.source);

  const handleDelete = async (playlist: Playlist) => {
    if (!confirm(`Delete "${playlist.name}" from Plex? This cannot be undone.`)) return;
    setDeletingId(playlist.id);
    setError(null);
    try {
      await apiClient.deletePlaylistByPlexId(playlist.id);
      await refreshPlaylists();
    } catch (err: any) {
      setError(err.message || 'Failed to delete playlist');
    } finally {
      setDeletingId(null);
    }
  };

  // Reimport is fire-and-forget on the backend (same pattern as a schedule's
  // "Run Now"), so we just wait a beat before refreshing to give the refresh
  // a chance to land, rather than polling for completion.
  const handleReimport = async (playlist: Playlist) => {
    if (!playlist.dbId) return;
    setReimportingId(playlist.id);
    setError(null);
    try {
      await apiClient.reimportPlaylist(playlist.dbId);
      setTimeout(() => {
        refreshPlaylists();
        loadMissingTracks();
      }, 4000);
    } catch (err: any) {
      setError(err.message || 'Failed to start reimport');
    } finally {
      setReimportingId(null);
    }
  };

  const handleQuickBackup = async (playlist: Playlist) => {
    setBackingUpId(playlist.id);
    setError(null);
    try {
      const response = await apiClient.getPlaylistTracks(playlist.id);
      const tracks = response.tracks || [];
      const backupData = {
        version: 1,
        exportDate: new Date().toISOString(),
        serverName: 'Plex Server',
        playlists: [{
          title: playlist.name,
          tracks: tracks.map((t: any) => ({
            title: t.title,
            artist: t.artist || t.grandparentTitle || 'Unknown',
            album: t.album || t.parentTitle,
          })),
          backupDate: new Date().toISOString(),
        }],
      };
      const blob = new Blob([JSON.stringify(backupData, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${playlist.name.replace(/[^a-z0-9]/gi, '_')}-backup-${new Date().toISOString().split('T')[0]}.json`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (err: any) {
      setError(err.message || 'Failed to back up playlist');
    } finally {
      setBackingUpId(null);
    }
  };

  const toggleSort = (key: SortKey) => {
    if (sortKey === key) {
      setSortDir(prev => (prev === 'asc' ? 'desc' : 'asc'));
    } else {
      setSortKey(key);
      setSortDir('asc');
    }
  };

  const needsAttention = (playlist: Playlist) => {
    const missingCount = playlist.dbId ? (missingByDbId[playlist.dbId]?.length ?? 0) : 0;
    if (missingCount > 0) return true;
    const schedule = playlist.dbId ? scheduleByDbId.get(playlist.dbId) : undefined;
    return !!schedule && getScheduleStatus(schedule.id) === 'failed';
  };

  const sourceFilterOptions = useMemo((): FilterOption[] => {
    const labels = new Set<string>();
    playlists.forEach(p => labels.add(getSourceLabel(p.source) || 'Plex'));
    return [{ value: 'all', label: 'All Sources' }, ...Array.from(labels).sort().map(l => ({ value: l, label: l }))];
  }, [playlists]);

  const missingFilterOptions: FilterOption[] = [
    { value: 'all', label: 'All' },
    { value: 'has', label: 'Has Missing Tracks' },
    { value: 'none', label: 'No Missing Tracks' },
  ];

  const scheduleFilterOptions: FilterOption[] = [
    { value: 'all', label: 'All' },
    { value: 'none', label: 'Not Scheduled' },
    { value: 'daily', label: 'Daily' },
    { value: 'weekly', label: 'Weekly' },
    { value: 'fortnightly', label: 'Fortnightly' },
    { value: 'monthly', label: 'Monthly' },
    { value: 'failed', label: 'Failed Last Run' },
    { value: 'running', label: 'Running Now' },
  ];

  const smartFilterOptions: FilterOption[] = [
    { value: 'all', label: 'All' },
    { value: 'smart', label: 'Smart Playlists Only' },
    { value: 'regular', label: 'Regular Playlists Only' },
  ];

  const stats = useMemo(() => {
    const totalMissing = Object.values(missingByDbId).reduce((sum, tracks) => sum + tracks.length, 0);
    const activeSchedules = schedules.filter(s => s.scheduleType === 'playlist_refresh' && s.playlistId).length;
    const attentionCount = playlists.filter(needsAttention).length;
    return { total: playlists.length, totalMissing, activeSchedules, attentionCount };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playlists, missingByDbId, schedules, scheduleByDbId, runningExecutions, recentExecutions]);

  const filteredSorted = useMemo(() => {
    let filtered = search.trim()
      ? playlists.filter(p => p.name.toLowerCase().includes(search.trim().toLowerCase()))
      : playlists;
    if (showAttentionOnly) {
      filtered = filtered.filter(needsAttention);
    }
    if (sourceFilter !== 'all') {
      filtered = filtered.filter(p => (getSourceLabel(p.source) || 'Plex') === sourceFilter);
    }
    if (missingFilter !== 'all') {
      filtered = filtered.filter(p => {
        const count = p.dbId ? (missingByDbId[p.dbId]?.length ?? 0) : 0;
        return missingFilter === 'has' ? count > 0 : count === 0;
      });
    }
    if (scheduleFilter !== 'all') {
      filtered = filtered.filter(p => {
        const schedule = p.dbId ? scheduleByDbId.get(p.dbId) : undefined;
        if (scheduleFilter === 'none') return !schedule;
        if (!schedule) return false;
        if (scheduleFilter === 'failed') return getScheduleStatus(schedule.id) === 'failed';
        if (scheduleFilter === 'running') return getScheduleStatus(schedule.id) === 'running';
        return schedule.frequency === scheduleFilter;
      });
    }
    if (smartFilter !== 'all') {
      filtered = filtered.filter(p => smartFilter === 'smart' ? !!p.smart : !p.smart);
    }
    if (tracksMin !== '') filtered = filtered.filter(p => p.trackCount >= Number(tracksMin));
    if (tracksMax !== '') filtered = filtered.filter(p => p.trackCount <= Number(tracksMax));
    if (durationMin !== '') filtered = filtered.filter(p => p.duration >= Number(durationMin) * 60000);
    if (durationMax !== '') filtered = filtered.filter(p => p.duration <= Number(durationMax) * 60000);

    const sorted = [...filtered].sort((a, b) => {
      let cmp = 0;
      switch (sortKey) {
        case 'name':
          cmp = a.name.localeCompare(b.name);
          break;
        case 'tracks':
          cmp = (a.trackCount || 0) - (b.trackCount || 0);
          break;
        case 'duration':
          cmp = (a.duration || 0) - (b.duration || 0);
          break;
        case 'missing': {
          const av = a.dbId ? (missingByDbId[a.dbId]?.length ?? 0) : 0;
          const bv = b.dbId ? (missingByDbId[b.dbId]?.length ?? 0) : 0;
          cmp = av - bv;
          break;
        }
        case 'schedule':
        case 'nextRun': {
          const as = a.dbId ? scheduleByDbId.get(a.dbId) : undefined;
          const bs = b.dbId ? scheduleByDbId.get(b.dbId) : undefined;
          const av = as ? (getNextRunTimestamp(as) ?? Infinity) : Infinity;
          const bv = bs ? (getNextRunTimestamp(bs) ?? Infinity) : Infinity;
          cmp = av - bv;
          break;
        }
        case 'lastRun': {
          const as = a.dbId ? scheduleByDbId.get(a.dbId) : undefined;
          const bs = b.dbId ? scheduleByDbId.get(b.dbId) : undefined;
          cmp = (as?.lastRun ?? -Infinity) - (bs?.lastRun ?? -Infinity);
          break;
        }
      }
      return sortDir === 'asc' ? cmp : -cmp;
    });

    return sorted;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playlists, search, sortKey, sortDir, missingByDbId, scheduleByDbId, showAttentionOnly, runningExecutions, recentExecutions, sourceFilter, missingFilter, scheduleFilter, smartFilter, tracksMin, tracksMax, durationMin, durationMax]);

  // Any filter/search/sort change should reset back to page 1 so users don't
  // land on a now-empty page.
  useEffect(() => {
    setPage(1);
  }, [search, showAttentionOnly, sourceFilter, missingFilter, scheduleFilter, smartFilter, tracksMin, tracksMax, durationMin, durationMax, sortKey, sortDir]);

  const totalPages = Math.max(1, Math.ceil(filteredSorted.length / pageSize));
  const pagedPlaylists = useMemo(
    () => filteredSorted.slice((page - 1) * pageSize, page * pageSize),
    [filteredSorted, page, pageSize]
  );

  return (
    <div className="page-container">
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '0.75rem' }}>
        <h1 className="page-title">Playlists</h1>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
          <input
            type="text"
            placeholder="Search playlists..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            style={{ padding: '0.5rem 0.75rem', borderRadius: '4px', border: '1px solid var(--border)', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', width: '220px' }}
          />
          <button className="btn btn-secondary" onClick={() => setShowSharedWithMe(true)}>Shared With Me</button>
          <button className="btn btn-secondary" onClick={() => setShowBackupAll(true)}>Backup / Restore</button>
        </div>
      </div>

      <div className="playlists-stats">
        <div className="playlists-stat">
          <span className="playlists-stat-value">{stats.total}</span>
          <span className="playlists-stat-label">Playlists</span>
        </div>
        <div className="playlists-stat">
          <span className="playlists-stat-value">{stats.activeSchedules}</span>
          <span className="playlists-stat-label">Scheduled</span>
        </div>
        <div className={`playlists-stat ${stats.totalMissing > 0 ? 'warn' : ''}`}>
          <span className="playlists-stat-value">{stats.totalMissing}</span>
          <span className="playlists-stat-label">Missing Tracks</span>
        </div>
        <button
          className={`playlists-stat playlists-stat-button ${stats.attentionCount > 0 ? 'warn' : ''} ${showAttentionOnly ? 'active' : ''}`}
          onClick={() => setShowAttentionOnly(v => !v)}
          disabled={stats.attentionCount === 0}
          title="Playlists with missing tracks or a failed scheduled refresh"
        >
          <span className="playlists-stat-value">{stats.attentionCount}</span>
          <span className="playlists-stat-label">Needs Attention{showAttentionOnly ? ' (shown)' : ''}</span>
        </button>
      </div>

      {error && (
        <div className="error-message" style={{ marginBottom: '1rem' }}>
          {error}
          <button onClick={() => setError(null)} style={{ marginLeft: '0.5rem', background: 'none', border: 'none', color: 'inherit', cursor: 'pointer' }}>×</button>
        </div>
      )}

      {isLoading ? (
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-secondary)' }}>Loading playlists...</div>
      ) : filteredSorted.length === 0 ? (
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-secondary)' }}>No playlists found</div>
      ) : (
        <div className="playlists-table-container">
          <table className="playlists-table">
            <thead>
              <tr>
                <SortableHeader
                  label="Playlist"
                  sortKeyName="name"
                  currentSortKey={sortKey}
                  currentSortDir={sortDir}
                  onSort={toggleSort}
                  filter={<FilterMenu options={smartFilterOptions} value={smartFilter} onChange={setSmartFilter} />}
                />
                <th>
                  <div className="th-with-filter">
                    <span className="th-label">Source</span>
                    <FilterMenu options={sourceFilterOptions} value={sourceFilter} onChange={setSourceFilter} />
                  </div>
                </th>
                <SortableHeader
                  label="Tracks"
                  sortKeyName="tracks"
                  currentSortKey={sortKey}
                  currentSortDir={sortDir}
                  onSort={toggleSort}
                  filter={<RangeFilterMenu unit="tracks" min={tracksMin} max={tracksMax} onChange={(mn, mx) => { setTracksMin(mn); setTracksMax(mx); }} />}
                />
                <SortableHeader
                  label="Duration"
                  sortKeyName="duration"
                  currentSortKey={sortKey}
                  currentSortDir={sortDir}
                  onSort={toggleSort}
                  filter={<RangeFilterMenu unit="min" min={durationMin} max={durationMax} onChange={(mn, mx) => { setDurationMin(mn); setDurationMax(mx); }} />}
                />
                <SortableHeader
                  label="Missing Tracks"
                  sortKeyName="missing"
                  currentSortKey={sortKey}
                  currentSortDir={sortDir}
                  onSort={toggleSort}
                  filter={<FilterMenu options={missingFilterOptions} value={missingFilter} onChange={setMissingFilter} />}
                />
                <SortableHeader
                  label="Schedule"
                  sortKeyName="schedule"
                  currentSortKey={sortKey}
                  currentSortDir={sortDir}
                  onSort={toggleSort}
                  filter={<FilterMenu options={scheduleFilterOptions} value={scheduleFilter} onChange={setScheduleFilter} />}
                />
                <SortableHeader label="Next Run" sortKeyName="nextRun" currentSortKey={sortKey} currentSortDir={sortDir} onSort={toggleSort} />
                <SortableHeader label="Last Run" sortKeyName="lastRun" currentSortKey={sortKey} currentSortDir={sortDir} onSort={toggleSort} />
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {pagedPlaylists.map(playlist => {
                const missingTracks = playlist.dbId ? (missingByDbId[playlist.dbId] || []) : [];
                const schedule = playlist.dbId ? scheduleByDbId.get(playlist.dbId) : undefined;
                const scheduleStatus = schedule ? getScheduleStatus(schedule.id) : null;
                const isExpanded = expandedMissingFor === playlist.dbId;
                const sourceLabel = getSourceLabel(playlist.source);

                return (
                  <Fragment key={playlist.id}>
                    <tr className={needsAttention(playlist) ? 'row-attention' : ''}>
                      <td>
                        <div className="playlist-name-cell">
                          <div className="playlist-cover">
                            {playlist.composite && (
                              <img
                                src={getCoverUrl(playlist.composite) || ''}
                                alt=""
                                onError={(e) => { e.currentTarget.style.visibility = 'hidden'; }}
                              />
                            )}
                          </div>
                          <span className="playlist-name" title={playlist.name}>{playlist.name}</span>
                          {playlist.smart && (
                            <span className="smart-badge" title="Smart playlist - built from Plex's own rules, not a fixed track list">
                              <SmartIcon /> Smart
                            </span>
                          )}
                        </div>
                      </td>
                      <td>
                        {sourceLabel && playlist.sourceUrl ? (
                          <a href={playlist.sourceUrl} target="_blank" rel="noopener noreferrer" className="playlist-source-link">
                            {sourceLabel} ↗
                          </a>
                        ) : sourceLabel ? (
                          <span className="playlist-source-label">{sourceLabel}</span>
                        ) : '—'}
                      </td>
                      <td>{playlist.trackCount}</td>
                      <td>{formatDuration(playlist.duration)}</td>
                      <td>
                        {missingTracks.length > 0 ? (
                          <button
                            className="badge-button warn"
                            onClick={() => setExpandedMissingFor(isExpanded ? null : (playlist.dbId ?? null))}
                            title="View missing tracks"
                          >
                            {missingTracks.length} {isExpanded ? '▲' : '▼'}
                          </button>
                        ) : '0'}
                      </td>
                      <td>
                        {schedule ? (
                          <button className="badge-button schedule-badge" onClick={() => setModal({ type: 'schedule', playlist })} title={`Last run: ${scheduleStatus}`}>
                            <span className={`status-dot status-dot-${scheduleStatus}`} />
                            {schedule.frequency}
                            {getNextRunRelative(schedule) && <span className="schedule-next-run">{getNextRunRelative(schedule)}</span>}
                          </button>
                        ) : playlist.dbId ? (
                          <button className="btn btn-secondary btn-small" onClick={() => setModal({ type: 'schedule', playlist })}>
                            + Schedule
                          </button>
                        ) : (
                          <span className="playlist-source-label" title="This playlist wasn't imported through Playlist Lab, so it can't be scheduled yet">
                            —
                          </span>
                        )}
                      </td>
                      <td>
                        {schedule ? getNextRunDate(schedule) : '—'}
                      </td>
                      <td>
                        {schedule?.lastRun ? new Date(schedule.lastRun * 1000).toLocaleDateString() : '—'}
                      </td>
                      <td className="col-actions">
                        <div className="row-actions">
                          <button className="icon-btn" onClick={() => setModal({ type: 'edit', playlist })} title="Edit tracks"><EditIcon /></button>
                          <button className="icon-btn" onClick={() => setModal({ type: 'share', playlist })} title="Share with Plex friends"><ShareIcon /></button>
                          <button className="icon-btn" onClick={() => setModal({ type: 'export', playlist })} title="Export to file or YouTube"><ExportIcon /></button>
                          {isReimportable(playlist) && (
                            <button className="icon-btn" onClick={() => handleReimport(playlist)} disabled={reimportingId === playlist.id} title="Re-fetch this playlist from its original source now">
                              <ReimportIcon />
                            </button>
                          )}
                          <button className="icon-btn" onClick={() => handleQuickBackup(playlist)} disabled={backingUpId === playlist.id} title="Download a JSON backup of this playlist"><BackupIcon /></button>
                          <button className="icon-btn icon-btn-danger" onClick={() => handleDelete(playlist)} disabled={deletingId === playlist.id} title="Delete from Plex"><DeleteIcon /></button>
                        </div>
                      </td>
                    </tr>
                    {isExpanded && playlist.dbId && (
                      <tr>
                        <td colSpan={9} style={{ padding: 0 }}>
                          <MissingTracksPanel
                            playlistId={playlist.dbId}
                            tracks={missingTracks}
                            onChanged={loadMissingTracks}
                          />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {!isLoading && filteredSorted.length > 0 && (
        <div className="playlists-pagination">
          <span className="playlists-pagination-info">
            Showing {(page - 1) * pageSize + 1}–{Math.min(page * pageSize, filteredSorted.length)} of {filteredSorted.length}
          </span>
          <select value={pageSize} onChange={(e) => setPageSize(Number(e.target.value))} className="playlists-pagination-size">
            {[25, 50, 100, 200].map(n => <option key={n} value={n}>{n} / page</option>)}
          </select>
          <button className="btn btn-secondary btn-small" onClick={() => setPage(p => Math.max(1, p - 1))} disabled={page <= 1}>Previous</button>
          <span className="playlists-pagination-page">Page {page} of {totalPages}</span>
          <button className="btn btn-secondary btn-small" onClick={() => setPage(p => Math.min(totalPages, p + 1))} disabled={page >= totalPages}>Next</button>
        </div>
      )}

      {modal?.type === 'edit' && (
        <div className="modal-overlay" onClick={() => setModal(null)}>
          <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '95vw', width: '1100px', maxHeight: '90vh', overflow: 'auto' }}>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
              <button className="btn btn-secondary btn-small" onClick={() => setModal(null)}>Close</button>
            </div>
            <PlaylistEditor playlist={modal.playlist} onPlaylistUpdated={refreshPlaylists} />
          </div>
        </div>
      )}

      {modal?.type === 'share' && (
        <ShareModal playlistId={modal.playlist.id} playlistName={modal.playlist.name} onClose={() => setModal(null)} />
      )}

      {modal?.type === 'export' && (
        <ExportModal playlistId={modal.playlist.id} playlistName={modal.playlist.name} trackCount={modal.playlist.trackCount} onClose={() => setModal(null)} />
      )}

      {modal?.type === 'schedule' && modal.playlist.dbId && (
        <ScheduleModal
          playlistId={String(modal.playlist.dbId)}
          playlistName={modal.playlist.name}
          schedule={scheduleByDbId.get(modal.playlist.dbId)}
          onClose={() => setModal(null)}
        />
      )}

      {showSharedWithMe && <SharedWithMeModal onClose={() => setShowSharedWithMe(false)} />}

      {showBackupAll && (
        <div className="modal-overlay" onClick={() => setShowBackupAll(false)}>
          <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '900px', width: '95vw', maxHeight: '90vh', overflow: 'auto' }}>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '0.5rem' }}>
              <button className="btn btn-secondary btn-small" onClick={() => setShowBackupAll(false)}>Close</button>
            </div>
            <BackupRestorePage />
          </div>
        </div>
      )}
    </div>
  );
};
