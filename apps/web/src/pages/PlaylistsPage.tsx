import type { FC, ReactNode, ThHTMLAttributes, MouseEvent as ReactMouseEvent, DragEvent as ReactDragEvent } from 'react';
import { Fragment, useState, useEffect, useMemo, useRef, Suspense, lazy } from 'react';
import { createPortal } from 'react-dom';
import { useApp } from '../contexts/AppContext';
import type { Playlist } from '../contexts/AppContext';
import type { Schedule, MissingTrack } from '@playlist-lab/shared';
import { Modal, embeddedPageCloseButtonStyle } from '../components/Modal';
import { ShareModal } from '../components/playlist-panel/ShareModal';
import { ScheduleModal } from '../components/playlist-panel/ScheduleModal';
import { MissingTracksPanel } from '../components/playlist-panel/MissingTracksPanel';
import { getNextRunTimestamp, getNextRunRelative, getNextRunDate } from '../utils/scheduleTime';
import { usePopover } from '../hooks/usePopover';
import { useConfirm, usePrompt } from '../contexts/ConfirmContext';
import { useToast } from '../contexts/ToastContext';
import { MISSING_CHANGED_EVENT } from '../components/HeaderActions';
import { EditIcon, ShareIcon, ExportIcon, ReimportIcon, BackupIcon, DeleteIcon, FilterIcon, SmartIcon, CloneIcon } from '../components/icons';
import './PlaylistsPage.css';

// Lazy-loaded: PlaylistsPage is the always-shown landing page, so anything
// it statically imports ships in the initial bundle regardless of whether
// it's ever opened. PlaylistEditor (776 lines) and ExportModal (which pulls
// in the whole cross-import YouTube-export wizard, ~2,300 lines across its
// step components) are only needed after a per-row Edit/Export click.
const PlaylistEditor = lazy(() => import('../components/playlist-panel/PlaylistEditor').then(m => ({ default: m.PlaylistEditor })));
const ExportModal = lazy(() => import('../components/playlist-panel/ExportModal').then(m => ({ default: m.ExportModal })));

const ModalFallback = () => <div style={{ padding: '3rem', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading...</div>;

interface FilterOption { value: string; label: string }

/** Small popover filter menu attached to a table header. Shared by the
 * Source, Missing Tracks and Schedule columns below - click-outside and
 * Escape both close it, and the button that opens it is highlighted
 * whenever a non-default option is selected. */
const FilterMenu: FC<{ options: FilterOption[]; value: string; onChange: (v: string) => void }> = ({ options, value, onChange }) => {
  const { open, toggle, close, ref, panelRef, menuStyle } = usePopover<HTMLSpanElement>();
  const active = value !== options[0]?.value;

  return (
    <span className="th-filter" ref={ref}>
      <button
        type="button"
        className={`th-filter-btn ${active ? 'active' : ''}`}
        onClick={(e) => { e.stopPropagation(); toggle(); }}
        title="Filter"
      >
        <FilterIcon />
      </button>
      {open && createPortal(
        <div className="th-filter-menu" style={menuStyle} onClick={(e) => e.stopPropagation()} ref={panelRef}>
          {options.map(opt => (
            <button
              key={opt.value}
              type="button"
              className={`th-filter-option ${value === opt.value ? 'active' : ''}`}
              onClick={() => { onChange(opt.value); close(); }}
            >
              {opt.label}
            </button>
          ))}
        </div>,
        document.body
      )}
    </span>
  );
};

/** Popover filter with a single text input, for the Playlist column's
 * name search. Shares the open/close-on-outside-click/Escape behavior with
 * FilterMenu above but needs a free-form input instead of a fixed option list. */
const SearchFilterMenu: FC<{ value: string; onChange: (v: string) => void }> = ({ value, onChange }) => {
  const { open, toggle, ref, panelRef, menuStyle } = usePopover<HTMLSpanElement>();
  const active = value !== '';

  return (
    <span className="th-filter" ref={ref}>
      <button
        type="button"
        className={`th-filter-btn ${active ? 'active' : ''}`}
        onClick={(e) => { e.stopPropagation(); toggle(); }}
        title="Search"
      >
        <FilterIcon />
      </button>
      {open && createPortal(
        <div className="th-filter-menu" style={menuStyle} onClick={(e) => e.stopPropagation()} ref={panelRef}>
          <input
            type="text"
            autoFocus
            placeholder="Search playlists..."
            value={value}
            onChange={(e) => onChange(e.target.value)}
            style={{ padding: '0.375rem 0.5rem', borderRadius: '4px', border: '1px solid var(--border)', backgroundColor: 'var(--surface)', color: 'var(--text-primary)', width: '180px' }}
          />
          {active && (
            <button type="button" className="th-filter-option" onClick={() => onChange('')}>Clear</button>
          )}
        </div>,
        document.body
      )}
    </span>
  );
};

/** Popover filter with min/max number inputs, for the Tracks and Duration
 * columns. Shares the open/close-on-outside-click/Escape behavior with
 * FilterMenu above but needs free-form inputs instead of a fixed option list. */
const RangeFilterMenu: FC<{ unit: string; min: string; max: string; onChange: (min: string, max: string) => void }> = ({ unit, min, max, onChange }) => {
  const { open, toggle, ref, panelRef, menuStyle } = usePopover<HTMLSpanElement>();
  const active = min !== '' || max !== '';

  return (
    <span className="th-filter" ref={ref}>
      <button
        type="button"
        className={`th-filter-btn ${active ? 'active' : ''}`}
        onClick={(e) => { e.stopPropagation(); toggle(); }}
        title="Filter"
      >
        <FilterIcon />
      </button>
      {open && createPortal(
        <div className="th-filter-menu th-filter-menu-range" style={menuStyle} onClick={(e) => e.stopPropagation()} ref={panelRef}>
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
        </div>,
        document.body
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
  dragProps?: ThHTMLAttributes<HTMLTableCellElement>;
}> = ({ label, sortKeyName, currentSortKey, currentSortDir, onSort, filter, dragProps }) => (
  <th {...dragProps} className={`sortable${dragProps?.className ? ` ${dragProps.className}` : ''}`} onClick={() => onSort(sortKeyName)}>
    <div className={filter ? 'th-with-filter' : undefined}>
      <span className="th-label">
        {label}
        {currentSortKey === sortKeyName && <span className="sort-arrow">{currentSortDir === 'asc' ? '▲' : '▼'}</span>}
      </span>
      {filter}
    </div>
  </th>
);

type SortKey = 'name' | 'tracks' | 'duration' | 'missing' | 'schedule' | 'nextRun' | 'lastRun' | 'dateAdded' | 'dateUpdated';
type SortDir = 'asc' | 'desc';

const MOBILE_SORT_OPTIONS: Array<{ value: SortKey; label: string }> = [
  { value: 'name', label: 'Name' },
  { value: 'tracks', label: 'Tracks' },
  { value: 'duration', label: 'Duration' },
  { value: 'missing', label: 'Missing Tracks' },
  { value: 'nextRun', label: 'Next Run' },
  { value: 'lastRun', label: 'Last Run' },
  { value: 'dateAdded', label: 'Date Added' },
  { value: 'dateUpdated', label: 'Date Updated' },
];
type Modal = { type: 'edit' | 'share' | 'export' | 'schedule'; playlist: Playlist } | null;

// Columns beyond the always-shown Playlist/Actions ones - user-toggleable by
// right-clicking any column header, persisted so the choice sticks across
// visits. New columns default off so an update doesn't suddenly widen
// everyone's table; existing ones default on to match today's layout.
type ColumnId = 'source' | 'tracks' | 'duration' | 'missing' | 'schedule' | 'nextRun' | 'lastRun' | 'dateAdded' | 'dateUpdated' | 'sourceUrl';
const COLUMN_DEFS: { id: ColumnId; label: string; defaultOn: boolean }[] = [
  { id: 'source', label: 'Source', defaultOn: true },
  { id: 'tracks', label: 'Tracks', defaultOn: true },
  { id: 'duration', label: 'Duration', defaultOn: true },
  { id: 'missing', label: 'Missing Tracks', defaultOn: true },
  { id: 'schedule', label: 'Schedule', defaultOn: true },
  { id: 'nextRun', label: 'Next Run', defaultOn: true },
  { id: 'lastRun', label: 'Last Run', defaultOn: true },
  { id: 'dateAdded', label: 'Date Added', defaultOn: true },
  { id: 'dateUpdated', label: 'Date Updated', defaultOn: false },
  { id: 'sourceUrl', label: 'Source URL', defaultOn: false },
];
const COLUMN_PREFS_KEY = 'playlistsTableColumns';
const COLUMN_ORDER_KEY = 'playlistsTableColumnOrder';

function loadVisibleColumns(): Set<ColumnId> {
  try {
    const saved = localStorage.getItem(COLUMN_PREFS_KEY);
    if (saved) return new Set(JSON.parse(saved) as ColumnId[]);
  } catch { /* fall through to defaults */ }
  // Mobile: fewer default columns means less horizontal scrolling on first load;
  // the existing column-visibility menu still lets anyone add more back.
  if (window.innerWidth < 768) return new Set(['tracks', 'duration']);
  return new Set(COLUMN_DEFS.filter(c => c.defaultOn).map(c => c.id));
}

// Reconciled against COLUMN_DEFS rather than trusted as-is, so a saved order
// from before a column was added/removed in code doesn't lose the new one or
// keep a dangling id around.
function loadColumnOrder(): ColumnId[] {
  const allIds = COLUMN_DEFS.map(c => c.id);
  let saved: ColumnId[] = [];
  try {
    const raw = localStorage.getItem(COLUMN_ORDER_KEY);
    if (raw) saved = (JSON.parse(raw) as ColumnId[]).filter(id => allIds.includes(id));
  } catch { /* fall through to defaults */ }
  const missing = allIds.filter(id => !saved.includes(id));
  return [...saved, ...missing];
}

/** Checkbox per toggleable column, listed in the table's current column
 * order, shown in the menu you get by right-clicking any column header. */
const ColumnCheckboxes: FC<{ order: ColumnId[]; visible: Set<ColumnId>; onToggle: (id: ColumnId) => void }> = ({ order, visible, onToggle }) => (
  <>
    {order.map(id => {
      const col = COLUMN_DEFS.find(c => c.id === id);
      if (!col) return null;
      return (
        <label key={id} className="th-filter-option" style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={visible.has(id)} onChange={() => onToggle(id)} />
          {col.label}
        </label>
      );
    })}
  </>
);

export const PlaylistsPage: FC = () => {
  const { apiClient, schedules, server, playlists, isLoading, refreshPlaylists } = useApp();
  const confirmDialog = useConfirm();
  const promptDialog = usePrompt();
  const toast = useToast();
  const [error, setError] = useState<string | null>(null);
  const [missingByDbId, setMissingByDbId] = useState<Record<number, MissingTrack[]>>({});
  const [expandedMissingFor, setExpandedMissingFor] = useState<number | null>(null);
  // Below this width the table (which needs room for ~10 columns) is
  // replaced with a tap-to-expand card list instead of trying to squeeze
  // or horizontally scroll a data table on a phone screen.
  const [isMobile, setIsMobile] = useState(() => window.matchMedia('(max-width: 768px)').matches);
  const [expandedCardFor, setExpandedCardFor] = useState<string | null>(null);
  const [modal, setModal] = useState<Modal>(null);
  const [search, setSearch] = useState('');
  const [sortKey, setSortKey] = useState<SortKey>('dateAdded');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [reimportingId, setReimportingId] = useState<string | null>(null);
  const [runningMixScheduleId, setRunningMixScheduleId] = useState<number | null>(null);
  const [backingUpId, setBackingUpId] = useState<string | null>(null);
  const [cloningId, setCloningId] = useState<string | null>(null);
  const [isMerging, setIsMerging] = useState(false);
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [isSavingRename, setIsSavingRename] = useState(false);
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
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [isBulkBackingUp, setIsBulkBackingUp] = useState(false);
  const [isBulkDeleting, setIsBulkDeleting] = useState(false);
  const [visibleColumns, setVisibleColumns] = useState<Set<ColumnId>>(loadVisibleColumns);
  const [columnOrder, setColumnOrder] = useState<ColumnId[]>(loadColumnOrder);
  const [draggedColumn, setDraggedColumn] = useState<ColumnId | null>(null);
  const [dragOverColumn, setDragOverColumn] = useState<ColumnId | null>(null);
  const [columnMenuPos, setColumnMenuPos] = useState<{ x: number; y: number } | null>(null);
  const columnMenuRef = useRef<HTMLDivElement>(null);
  const tableContainerRef = useRef<HTMLDivElement>(null);
  // Sticky headers via "stick to the page as it scrolls" turned out fragile
  // in practice (any mismatch between the guessed nav height and the real
  // one leaves a gap a row can scroll into, showing through above the
  // "stuck" header). Bounding the table's own height and scrolling inside
  // it instead - the standard sticky-table-header pattern - sidesteps that
  // entirely: the header just sticks to top: 0 of its own scroll box, no
  // offset math needed. The one number this still needs is how much
  // viewport is left below the table's starting position, which is
  // measured rather than guessed.
  const [tableMaxHeight, setTableMaxHeight] = useState<number | undefined>(undefined);

  useEffect(() => {
    const update = () => {
      if (!tableContainerRef.current) return;
      const top = tableContainerRef.current.getBoundingClientRect().top;
      setTableMaxHeight(Math.max(240, window.innerHeight - top - 16));
    };
    update();
    window.addEventListener('resize', update);
    return () => window.removeEventListener('resize', update);
  }, [isLoading]);

  useEffect(() => {
    const handler = () => loadMissingTracks();
    window.addEventListener(MISSING_CHANGED_EVENT, handler);
    return () => window.removeEventListener(MISSING_CHANGED_EVENT, handler);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const mq = window.matchMedia('(max-width: 768px)');
    const handler = () => setIsMobile(mq.matches);
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, []);

  // `modal.playlist` is a snapshot taken when the modal was opened, so a
  // background refresh of the context's `playlists` list (e.g. once a queued
  // shuffle/sort/dedupe/split/merge action finishes and NotificationCenter
  // calls refreshPlaylists()) wouldn't otherwise reach the open editor -
  // this keeps it in sync so PlaylistEditor's own reload effect (keyed off
  // playlist.updatedAt) actually fires once Plex's data changes.
  useEffect(() => {
    if (!modal) return;
    const fresh = playlists.find(p => p.id === modal.playlist.id);
    if (fresh && fresh.updatedAt !== modal.playlist.updatedAt) {
      setModal({ ...modal, playlist: fresh });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playlists]);

  useEffect(() => {
    localStorage.setItem(COLUMN_PREFS_KEY, JSON.stringify([...visibleColumns]));
  }, [visibleColumns]);

  useEffect(() => {
    localStorage.setItem(COLUMN_ORDER_KEY, JSON.stringify(columnOrder));
  }, [columnOrder]);

  // Right-click-anywhere-on-a-header menu: close on outside click/Escape,
  // same as the popover menus (usePopover doesn't fit here since this one's
  // positioned at the cursor rather than anchored to a trigger button).
  useEffect(() => {
    if (!columnMenuPos) return;
    const handleClick = (e: MouseEvent) => {
      if (columnMenuRef.current && !columnMenuRef.current.contains(e.target as Node)) setColumnMenuPos(null);
    };
    const handleKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setColumnMenuPos(null); };
    document.addEventListener('mousedown', handleClick);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('mousedown', handleClick);
      document.removeEventListener('keydown', handleKey);
    };
  }, [columnMenuPos]);

  const orderedVisibleColumns = useMemo(
    () => columnOrder.filter(id => visibleColumns.has(id)),
    [columnOrder, visibleColumns]
  );

  const toggleColumnVisible = (id: ColumnId) => {
    setVisibleColumns(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleColumnHeaderContextMenu = (e: ReactMouseEvent) => {
    e.preventDefault();
    setColumnMenuPos({ x: e.clientX, y: e.clientY });
  };

  const handleColumnDragStart = (id: ColumnId) => (e: ReactDragEvent) => {
    setDraggedColumn(id);
    e.dataTransfer.effectAllowed = 'move';
  };

  const handleColumnDragOver = (id: ColumnId) => (e: ReactDragEvent) => {
    e.preventDefault();
    if (id !== draggedColumn) setDragOverColumn(id);
  };

  const handleColumnDrop = (targetId: ColumnId) => (e: ReactDragEvent) => {
    e.preventDefault();
    setDragOverColumn(null);
    const sourceId = draggedColumn;
    setDraggedColumn(null);
    if (!sourceId || sourceId === targetId) return;
    setColumnOrder(prev => {
      const next = prev.filter(id => id !== sourceId);
      const targetIndex = next.indexOf(targetId);
      next.splice(targetIndex, 0, sourceId);
      return next;
    });
  };

  // Shared drag-reorder + right-click-to-manage-columns behavior for every
  // toggleable header cell (not the pinned Playlist/Actions ones).
  const columnDragProps = (id: ColumnId): ThHTMLAttributes<HTMLTableCellElement> => ({
    draggable: true,
    onDragStart: handleColumnDragStart(id),
    onDragOver: handleColumnDragOver(id),
    onDrop: handleColumnDrop(id),
    onDragEnd: () => { setDraggedColumn(null); setDragOverColumn(null); },
    onContextMenu: handleColumnHeaderContextMenu,
    className: dragOverColumn === id ? 'column-drag-over' : undefined,
    title: 'Drag to reorder, right-click to show/hide columns',
  });

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
  // shared with the Schedules section below.
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

  // Re-fetch missing tracks whenever the playlists list itself refreshes
  // (initial load, or NotificationCenter refreshing it after a completed
  // import/schedule run) - a freshly imported playlist's missing-track count
  // otherwise wouldn't show up until the next full page load.
  useEffect(() => {
    loadMissingTracks();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playlists]);

  useEffect(() => {
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
  // /playlists?scheduleFor=<dbId> opens the schedule modal for that row
  // (used by Generate Mixes' "Schedule" link on a freshly created playlist).
  useEffect(() => {
    if (playlists.length === 0) return;
    const params = new URLSearchParams(window.location.search);
    const missingFor = params.get('missingFor');
    const scheduleFor = params.get('scheduleFor');
    if (missingFor) {
      setExpandedMissingFor(parseInt(missingFor, 10));
      window.history.replaceState({}, '', '/');
    }
    if (scheduleFor) {
      const playlist = playlists.find(p => p.dbId === parseInt(scheduleFor, 10));
      if (playlist) setModal({ type: 'schedule', playlist });
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
    if (!await confirmDialog(`Delete "${playlist.name}" from Plex? This cannot be undone.`)) return;
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

  const handleStartRename = (playlist: Playlist) => {
    setRenamingId(playlist.id);
    setRenameValue(playlist.name);
  };

  const handleCancelRename = () => {
    setRenamingId(null);
    setRenameValue('');
  };

  const handleSubmitRename = async (playlist: Playlist) => {
    const trimmed = renameValue.trim();
    if (!trimmed || trimmed === playlist.name) {
      handleCancelRename();
      return;
    }
    setIsSavingRename(true);
    try {
      if (playlist.dbId) {
        await apiClient.updatePlaylist(playlist.dbId, { name: trimmed });
      } else {
        // Never imported through this app (a native Plex playlist) - no
        // numeric id to rename via, so go straight through Plex by ratingKey.
        await apiClient.renamePlaylistByPlexId(playlist.id, trimmed);
      }
      await refreshPlaylists();
      toast.success('Playlist renamed');
      handleCancelRename();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to rename playlist');
    } finally {
      setIsSavingRename(false);
    }
  };

  const toggleSelected = (id: string) => {
    setSelectedIds(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleBulkBackup = async () => {
    if (selectedIds.size === 0) return;
    setIsBulkBackingUp(true);
    setError(null);
    try {
      const selected = playlists.filter(p => selectedIds.has(p.id));
      const backupPlaylists = await Promise.all(selected.map(async (p) => {
        const response = await apiClient.getPlaylistTracks(p.id);
        return {
          title: p.name,
          tracks: (response.tracks || []).map((t: any) => ({
            title: t.title,
            artist: t.artist || t.grandparentTitle || 'Unknown',
            album: t.album || t.parentTitle,
          })),
          backupDate: new Date().toISOString(),
        };
      }));

      const blob = new Blob([JSON.stringify({ version: 1, exportDate: new Date().toISOString(), serverName: 'Plex Server', playlists: backupPlaylists }, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `plex-playlists-backup-${new Date().toISOString().split('T')[0]}.json`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success(`Backed up ${backupPlaylists.length} playlist(s)`);
    } catch (err: any) {
      toast.error(err.message || 'Failed to back up selected playlists');
    } finally {
      setIsBulkBackingUp(false);
    }
  };

  const handleBulkDelete = async () => {
    if (selectedIds.size === 0) return;
    if (!await confirmDialog(`Delete ${selectedIds.size} playlist(s) from Plex? This cannot be undone.`)) return;
    setIsBulkDeleting(true);
    setError(null);
    try {
      const ids = Array.from(selectedIds);
      const results = await Promise.allSettled(ids.map(id => apiClient.deletePlaylistByPlexId(id)));
      const failed = results.filter(r => r.status === 'rejected').length;
      setSelectedIds(new Set());
      await refreshPlaylists();
      if (failed > 0) toast.error(`Failed to delete ${failed} of ${ids.length} playlist(s)`);
      else toast.success(`Deleted ${ids.length} playlist(s)`);
    } finally {
      setIsBulkDeleting(false);
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
      // Keep the row's disabled/loading state up for the same 4s wait below,
      // not just the initial request - clearing it right after the request
      // is accepted left the button looking idle for the whole reimport with
      // no in-place indication anything was still happening on that row.
      setTimeout(() => {
        refreshPlaylists();
        loadMissingTracks();
        setReimportingId(null);
      }, 4000);
    } catch (err: any) {
      setError(err.message || 'Failed to start reimport');
      setReimportingId(null);
    }
  };

  // A generated mix (Daily Mix, Forgotten Favorites, etc.) has no online
  // source to reimport from - regenerating it means re-running its mix
  // schedule, same "Run Now" action the Schedule modal and Generate Mixes
  // page already offer, just reachable in one click from this table too.
  const handleRunMixSchedule = async (schedule: Schedule) => {
    setRunningMixScheduleId(schedule.id);
    setError(null);
    try {
      await apiClient.runSchedule(schedule.id);
      toast.success('Regenerating mix...');
      setTimeout(() => {
        refreshPlaylists();
        setRunningMixScheduleId(null);
      }, 4000);
    } catch (err: any) {
      toast.error(err.message || 'Failed to regenerate mix');
      setRunningMixScheduleId(null);
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

  const handleClone = async (playlist: Playlist) => {
    const name = await promptDialog('Name for the cloned playlist', {
      title: 'Duplicate playlist',
      input: { defaultValue: `${playlist.name} (Copy)`, label: 'Cloned playlist name' },
      confirmLabel: 'Duplicate',
    });
    if (!name) return;
    setCloningId(playlist.id);
    setError(null);
    try {
      await apiClient.clonePlaylist(playlist.id, name);
      await refreshPlaylists();
      toast.success(`Cloned "${playlist.name}" as "${name}"`);
    } catch (err: any) {
      toast.error(err.message || 'Failed to clone playlist');
    } finally {
      setCloningId(null);
    }
  };

  const handleMergeSelected = async () => {
    if (selectedIds.size < 2) return;
    const name = await promptDialog(`Combining ${selectedIds.size} playlists into one new playlist.`, {
      title: 'Merge playlists',
      input: { placeholder: 'Name for the merged playlist', label: 'Merged playlist name' },
      confirmLabel: 'Merge',
    });
    if (!name) return;
    setIsMerging(true);
    setError(null);
    try {
      // Runs through the server's shared action queue now - the merged
      // playlist shows up once the notification bell reports success and
      // refreshPlaylists() runs (see NotificationCenter's justFinished check).
      const { position } = await apiClient.mergePlaylists(Array.from(selectedIds), name);
      setSelectedIds(new Set());
      toast.success(position > 0 ? `Queued - position ${position} in queue` : `Merging into "${name}"...`);
    } catch (err: any) {
      toast.error(err.message || 'Failed to merge playlists');
    } finally {
      setIsMerging(false);
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
    { value: 'any', label: 'Scheduled' },
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
        if (scheduleFilter === 'any') return !!schedule;
        if (!schedule) return false;
        if (scheduleFilter === 'failed') return getScheduleStatus(schedule.id) === 'failed';
        if (scheduleFilter === 'running') return getScheduleStatus(schedule.id) === 'running';
        return schedule.frequency === scheduleFilter;
      });
    }
    if (smartFilter !== 'all') {
      filtered = filtered.filter(p => smartFilter === 'smart' ? !!p.smart : !p.smart);
    }
    if (tracksMin !== '') filtered = filtered.filter(p => (p.trackCount ?? 0) >= Number(tracksMin));
    if (tracksMax !== '') filtered = filtered.filter(p => (p.trackCount ?? 0) <= Number(tracksMax));
    if (durationMin !== '') filtered = filtered.filter(p => (p.duration ?? 0) >= Number(durationMin) * 60000);
    if (durationMax !== '') filtered = filtered.filter(p => (p.duration ?? 0) <= Number(durationMax) * 60000);

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
        case 'dateAdded':
          cmp = a.createdAt - b.createdAt;
          break;
        case 'dateUpdated':
          cmp = a.updatedAt - b.updatedAt;
          break;
      }
      return sortDir === 'asc' ? cmp : -cmp;
    });

    return sorted;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playlists, search, sortKey, sortDir, missingByDbId, scheduleByDbId, runningExecutions, recentExecutions, sourceFilter, missingFilter, scheduleFilter, smartFilter, tracksMin, tracksMax, durationMin, durationMax]);

  return (
    <div className="page-container">
      <div className="playlists-stats">
        <div className="playlists-stat">
          <span className="playlists-stat-value">{stats.total}</span>
          <span className="playlists-stat-label">Playlists</span>
        </div>
        <button
          className={`playlists-stat playlists-stat-button ${scheduleFilter === 'any' ? 'active' : ''}`}
          onClick={() => setScheduleFilter(f => f === 'any' ? 'all' : 'any')}
          disabled={stats.activeSchedules === 0}
          title="Show only playlists that have a schedule"
        >
          <span className="playlists-stat-value">{stats.activeSchedules}</span>
          <span className="playlists-stat-label">Scheduled</span>
        </button>
        <div className={`playlists-stat ${stats.totalMissing > 0 ? 'warn' : ''}`}>
          <span className="playlists-stat-value">{stats.totalMissing}</span>
          <span className="playlists-stat-label">Missing Tracks</span>
        </div>
        <button
          className={`playlists-stat playlists-stat-button ${stats.attentionCount > 0 ? 'warn' : ''} ${missingFilter === 'has' ? 'active' : ''}`}
          onClick={() => setMissingFilter(f => f === 'has' ? 'all' : 'has')}
          disabled={stats.attentionCount === 0}
          title="Show only playlists with missing tracks"
        >
          <span className="playlists-stat-value">{stats.attentionCount}</span>
          <span className="playlists-stat-label">Needs Attention</span>
        </button>
      </div>

      {error && (
        <div className="error-message" style={{ marginBottom: '1rem' }}>
          {error}
          <button onClick={() => setError(null)} style={{ marginLeft: '0.5rem', background: 'none', border: 'none', color: 'inherit', cursor: 'pointer' }}>×</button>
        </div>
      )}

      {selectedIds.size > 0 && (
        <div className="bulk-actions-bar">
          <span className="bulk-actions-count">{selectedIds.size} selected</span>
          <button className="btn btn-secondary btn-small" onClick={handleBulkBackup} disabled={isBulkBackingUp || isBulkDeleting || isMerging}>
            {isBulkBackingUp ? 'Backing up...' : 'Backup Selected'}
          </button>
          {selectedIds.size >= 2 && (
            <button className="btn btn-secondary btn-small" onClick={handleMergeSelected} disabled={isBulkBackingUp || isBulkDeleting || isMerging} title="Combine these playlists' tracks into a new playlist">
              {isMerging ? 'Merging...' : 'Merge Selected'}
            </button>
          )}
          <button className="btn btn-secondary btn-small" onClick={handleBulkDelete} disabled={isBulkBackingUp || isBulkDeleting || isMerging} style={{ color: 'var(--error)' }}>
            {isBulkDeleting ? 'Deleting...' : 'Delete Selected'}
          </button>
          <button className="btn btn-secondary btn-small" onClick={() => setSelectedIds(new Set())} disabled={isBulkBackingUp || isBulkDeleting || isMerging}>
            Clear Selection
          </button>
        </div>
      )}

      {isLoading ? (
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-secondary)' }}>Loading playlists...</div>
      ) : filteredSorted.length === 0 ? (
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-secondary)' }}>No playlists found</div>
      ) : isMobile ? (
        <>
        <div className="playlists-sortbar">
          <label htmlFor="mobile-sort">Sort</label>
          <select
            id="mobile-sort"
            value={sortKey}
            onChange={(e) => setSortKey(e.target.value as SortKey)}
          >
            {MOBILE_SORT_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
          <button
            type="button"
            onClick={() => setSortDir(d => d === 'asc' ? 'desc' : 'asc')}
            title={sortDir === 'asc' ? 'Ascending - tap for descending' : 'Descending - tap for ascending'}
            aria-label={`Sort direction: ${sortDir === 'asc' ? 'ascending' : 'descending'}`}
          >
            {sortDir === 'asc' ? '▲' : '▼'}
          </button>
        </div>
        <div className="playlists-cards">
          {filteredSorted.map(playlist => {
            const missingTracks = playlist.dbId ? (missingByDbId[playlist.dbId] || []) : [];
            const schedule = playlist.dbId ? scheduleByDbId.get(playlist.dbId) : undefined;
            const scheduleStatus = schedule ? getScheduleStatus(schedule.id) : null;
            const sourceLabel = getSourceLabel(playlist.source);
            const isCardExpanded = expandedCardFor === playlist.id;

            return (
              <div key={playlist.id} className={`playlist-card${needsAttention(playlist) ? ' row-attention' : ''}`}>
                <div
                  className="playlist-card-header"
                  onClick={() => setExpandedCardFor(isCardExpanded ? null : playlist.id)}
                  role="button"
                  tabIndex={0}
                  aria-expanded={isCardExpanded}
                >
                  <input
                    type="checkbox"
                    aria-label={`Select ${playlist.name}`}
                    checked={selectedIds.has(playlist.id)}
                    onClick={(e) => e.stopPropagation()}
                    onChange={() => toggleSelected(playlist.id)}
                  />
                  <div className="playlist-cover">
                    {playlist.composite && (
                      <img
                        src={getCoverUrl(playlist.composite) || ''}
                        alt=""
                        onError={(e) => { e.currentTarget.style.visibility = 'hidden'; }}
                      />
                    )}
                  </div>
                  <div className="playlist-card-title">
                    <span className="playlist-name">{playlist.name}</span>
                    <span className="playlist-card-summary">
                      {playlist.trackCount ?? 0} tracks • {formatDuration(playlist.duration ?? 0)}
                      {playlist.smart && ' • Smart'}
                      {missingTracks.length > 0 && ` • ${missingTracks.length} missing`}
                    </span>
                  </div>
                  <span className={`playlist-card-chevron${isCardExpanded ? ' expanded' : ''}`}>▾</span>
                </div>

                {isCardExpanded && (
                  <div className="playlist-card-details">
                    <div className="playlist-card-detail-row">
                      <span className="playlist-card-detail-label">Source</span>
                      {sourceLabel && playlist.sourceUrl ? (
                        <a href={playlist.sourceUrl} target="_blank" rel="noopener noreferrer" className="playlist-source-link">{sourceLabel} ↗</a>
                      ) : sourceLabel ? (
                        <span className="playlist-source-label">{sourceLabel}</span>
                      ) : '—'}
                    </div>
                    <div className="playlist-card-detail-row">
                      <span className="playlist-card-detail-label">Schedule</span>
                      {schedule ? (
                        <button className="badge-button schedule-badge" onClick={() => setModal({ type: 'schedule', playlist })} title={`Last run: ${scheduleStatus}`}>
                          <span className={`status-dot status-dot-${scheduleStatus}`} />
                          {schedule.frequency}
                          {getNextRunRelative(schedule) && <span className="schedule-next-run">{getNextRunRelative(schedule)}</span>}
                        </button>
                      ) : playlist.dbId ? (
                        <button className="badge-button" onClick={() => setModal({ type: 'schedule', playlist })}>+ Schedule</button>
                      ) : (
                        <span className="playlist-source-label" title="This playlist wasn't imported through Playlist Lab, so it can't be scheduled yet">—</span>
                      )}
                    </div>
                    {schedule && (
                      <div className="playlist-card-detail-row">
                        <span className="playlist-card-detail-label">Next / Last Run</span>
                        <span>{getNextRunDate(schedule)} / {schedule.lastRun ? new Date(schedule.lastRun * 1000).toLocaleDateString() : '—'}</span>
                      </div>
                    )}
                    <div className="playlist-card-detail-row">
                      <span className="playlist-card-detail-label">Date Added</span>
                      <span>{new Date(playlist.createdAt).toLocaleDateString()}</span>
                    </div>
                    {missingTracks.length > 0 && playlist.dbId && (
                      <MissingTracksPanel
                        playlistId={playlist.dbId}
                        tracks={missingTracks}
                        onChanged={loadMissingTracks}
                        compact
                      />
                    )}
                    <div className="row-actions playlist-card-actions">
                      <button className="icon-btn" onClick={() => setModal({ type: 'edit', playlist })} title="Edit tracks" aria-label={`Edit tracks in ${playlist.name}`}><EditIcon /></button>
                      <button className="icon-btn" onClick={() => setModal({ type: 'share', playlist })} title="Share with another Playlist Lab user" aria-label={`Share ${playlist.name} with another Playlist Lab user`}><ShareIcon /></button>
                      <button className="icon-btn" onClick={() => setModal({ type: 'export', playlist })} title="Export to file or YouTube" aria-label={`Export ${playlist.name} to file or YouTube`}><ExportIcon /></button>
                      {isReimportable(playlist) && (
                        <button className="icon-btn" onClick={() => handleReimport(playlist)} disabled={reimportingId === playlist.id} title="Re-fetch this playlist from its original source now" aria-label={`Re-import ${playlist.name} from its source`}>
                          <ReimportIcon />
                        </button>
                      )}
                      {!isReimportable(playlist) && schedule?.scheduleType === 'mix_generation' && (
                        <button className="icon-btn" onClick={() => handleRunMixSchedule(schedule)} disabled={runningMixScheduleId === schedule.id} title="Regenerate this mix now" aria-label={`Regenerate ${playlist.name} now`}>
                          <ReimportIcon />
                        </button>
                      )}
                      <button className="icon-btn" onClick={() => handleQuickBackup(playlist)} disabled={backingUpId === playlist.id} title="Download a JSON backup of this playlist" aria-label={`Download a backup of ${playlist.name}`}><BackupIcon /></button>
                      <button className="icon-btn" onClick={() => handleClone(playlist)} disabled={cloningId === playlist.id} title="Duplicate this playlist" aria-label={`Duplicate ${playlist.name}`}><CloneIcon /></button>
                      <button className="icon-btn icon-btn-danger" onClick={() => handleDelete(playlist)} disabled={deletingId === playlist.id} title="Delete from Plex" aria-label={`Delete ${playlist.name} from Plex`}><DeleteIcon /></button>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>
        </>
      ) : (
        <div ref={tableContainerRef} className="playlists-table-container" style={{ maxHeight: tableMaxHeight, overflowY: 'auto' }}>
          <table className="playlists-table">
            <thead>
              <tr>
                <th className="col-select">
                  <input
                    type="checkbox"
                    aria-label="Select all playlists"
                    checked={filteredSorted.length > 0 && filteredSorted.every(p => selectedIds.has(p.id))}
                    ref={(el) => {
                      if (el) el.indeterminate = filteredSorted.some(p => selectedIds.has(p.id)) && !filteredSorted.every(p => selectedIds.has(p.id));
                    }}
                    onChange={(e) => {
                      setSelectedIds(prev => {
                        const next = new Set(prev);
                        for (const p of filteredSorted) {
                          if (e.target.checked) next.add(p.id);
                          else next.delete(p.id);
                        }
                        return next;
                      });
                    }}
                  />
                </th>
                <SortableHeader
                  label="Playlist"
                  sortKeyName="name"
                  currentSortKey={sortKey}
                  currentSortDir={sortDir}
                  onSort={toggleSort}
                  filter={
                    <span style={{ display: 'flex', gap: '0.25rem' }}>
                      <SearchFilterMenu value={search} onChange={setSearch} />
                      <FilterMenu options={smartFilterOptions} value={smartFilter} onChange={setSmartFilter} />
                    </span>
                  }
                />
                {orderedVisibleColumns.map(id => {
                  switch (id) {
                    case 'source':
                      return (
                        <th key={id} {...columnDragProps(id)}>
                          <div className="th-with-filter">
                            <span className="th-label">Source</span>
                            <FilterMenu options={sourceFilterOptions} value={sourceFilter} onChange={setSourceFilter} />
                          </div>
                        </th>
                      );
                    case 'tracks':
                      return (
                        <SortableHeader
                          key={id}
                          label="Tracks"
                          sortKeyName="tracks"
                          currentSortKey={sortKey}
                          currentSortDir={sortDir}
                          onSort={toggleSort}
                          filter={<RangeFilterMenu unit="tracks" min={tracksMin} max={tracksMax} onChange={(mn, mx) => { setTracksMin(mn); setTracksMax(mx); }} />}
                          dragProps={columnDragProps(id)}
                        />
                      );
                    case 'duration':
                      return (
                        <SortableHeader
                          key={id}
                          label="Duration"
                          sortKeyName="duration"
                          currentSortKey={sortKey}
                          currentSortDir={sortDir}
                          onSort={toggleSort}
                          filter={<RangeFilterMenu unit="min" min={durationMin} max={durationMax} onChange={(mn, mx) => { setDurationMin(mn); setDurationMax(mx); }} />}
                          dragProps={columnDragProps(id)}
                        />
                      );
                    case 'missing':
                      return (
                        <SortableHeader
                          key={id}
                          label="Missing Tracks"
                          sortKeyName="missing"
                          currentSortKey={sortKey}
                          currentSortDir={sortDir}
                          onSort={toggleSort}
                          filter={<FilterMenu options={missingFilterOptions} value={missingFilter} onChange={setMissingFilter} />}
                          dragProps={columnDragProps(id)}
                        />
                      );
                    case 'schedule':
                      return (
                        <SortableHeader
                          key={id}
                          label="Schedule"
                          sortKeyName="schedule"
                          currentSortKey={sortKey}
                          currentSortDir={sortDir}
                          onSort={toggleSort}
                          filter={<FilterMenu options={scheduleFilterOptions} value={scheduleFilter} onChange={setScheduleFilter} />}
                          dragProps={columnDragProps(id)}
                        />
                      );
                    case 'nextRun':
                      return <SortableHeader key={id} label="Next Run" sortKeyName="nextRun" currentSortKey={sortKey} currentSortDir={sortDir} onSort={toggleSort} dragProps={columnDragProps(id)} />;
                    case 'lastRun':
                      return <SortableHeader key={id} label="Last Run" sortKeyName="lastRun" currentSortKey={sortKey} currentSortDir={sortDir} onSort={toggleSort} dragProps={columnDragProps(id)} />;
                    case 'dateAdded':
                      return <SortableHeader key={id} label="Date Added" sortKeyName="dateAdded" currentSortKey={sortKey} currentSortDir={sortDir} onSort={toggleSort} dragProps={columnDragProps(id)} />;
                    case 'dateUpdated':
                      return <SortableHeader key={id} label="Date Updated" sortKeyName="dateUpdated" currentSortKey={sortKey} currentSortDir={sortDir} onSort={toggleSort} dragProps={columnDragProps(id)} />;
                    case 'sourceUrl':
                      return <th key={id} {...columnDragProps(id)}>Source URL</th>;
                    default:
                      return null;
                  }
                })}
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {filteredSorted.map(playlist => {
                const missingTracks = playlist.dbId ? (missingByDbId[playlist.dbId] || []) : [];
                const schedule = playlist.dbId ? scheduleByDbId.get(playlist.dbId) : undefined;
                const scheduleStatus = schedule ? getScheduleStatus(schedule.id) : null;
                const isExpanded = expandedMissingFor === playlist.dbId;
                const sourceLabel = getSourceLabel(playlist.source);

                return (
                  <Fragment key={playlist.id}>
                    <tr className={needsAttention(playlist) ? 'row-attention' : ''}>
                      <td className="col-select">
                        <input
                          type="checkbox"
                          aria-label={`Select ${playlist.name}`}
                          checked={selectedIds.has(playlist.id)}
                          onChange={() => toggleSelected(playlist.id)}
                        />
                      </td>
                      <td className="col-name">
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
                          {renamingId === playlist.id ? (
                            <input
                              type="text"
                              className="playlist-name-input"
                              value={renameValue}
                              autoFocus
                              disabled={isSavingRename}
                              onChange={(e) => setRenameValue(e.target.value)}
                              onBlur={() => handleSubmitRename(playlist)}
                              onKeyDown={(e) => {
                                if (e.key === 'Enter') { e.preventDefault(); handleSubmitRename(playlist); }
                                else if (e.key === 'Escape') { e.preventDefault(); handleCancelRename(); }
                              }}
                            />
                          ) : (
                            <span
                              className="playlist-name playlist-name-editable"
                              title="Click to rename"
                              onClick={() => handleStartRename(playlist)}
                            >
                              {playlist.name}
                            </span>
                          )}
                          {playlist.smart && (
                            <span className="smart-badge" title="Smart playlist - built from Plex's own rules, not a fixed track list">
                              <SmartIcon /> Smart
                            </span>
                          )}
                        </div>
                      </td>
                      {orderedVisibleColumns.map(id => {
                        switch (id) {
                          case 'source':
                            return (
                              <td key={id}>
                                {sourceLabel && playlist.sourceUrl ? (
                                  <a href={playlist.sourceUrl} target="_blank" rel="noopener noreferrer" className="playlist-source-link">
                                    {sourceLabel} ↗
                                  </a>
                                ) : sourceLabel ? (
                                  <span className="playlist-source-label">{sourceLabel}</span>
                                ) : '—'}
                              </td>
                            );
                          case 'tracks':
                            return <td key={id}>{playlist.trackCount ?? 0}</td>;
                          case 'duration':
                            return <td key={id}>{formatDuration(playlist.duration ?? 0)}</td>;
                          case 'missing':
                            return (
                              <td key={id}>
                                {missingTracks.length > 0 || isExpanded ? (
                                  <button
                                    className={`badge-button${missingTracks.length > 0 ? ' warn' : ''}`}
                                    onClick={() => setExpandedMissingFor(isExpanded ? null : (playlist.dbId ?? null))}
                                    title={missingTracks.length > 0 ? 'View missing tracks' : 'Close missing tracks'}
                                  >
                                    {missingTracks.length} {isExpanded ? '▲' : '▼'}
                                  </button>
                                ) : '0'}
                              </td>
                            );
                          case 'schedule':
                            return (
                              <td key={id}>
                                {schedule ? (
                                  <button className="badge-button schedule-badge" onClick={() => setModal({ type: 'schedule', playlist })} title={`Last run: ${scheduleStatus}`}>
                                    <span className={`status-dot status-dot-${scheduleStatus}`} />
                                    {schedule.frequency}
                                    {getNextRunRelative(schedule) && <span className="schedule-next-run">{getNextRunRelative(schedule)}</span>}
                                  </button>
                                ) : playlist.dbId ? (
                                  <button className="badge-button" onClick={() => setModal({ type: 'schedule', playlist })}>
                                    + Schedule
                                  </button>
                                ) : (
                                  <span className="playlist-source-label" title="This playlist wasn't imported through Playlist Lab, so it can't be scheduled yet">
                                    —
                                  </span>
                                )}
                              </td>
                            );
                          case 'nextRun':
                            return <td key={id}>{schedule ? getNextRunDate(schedule) : '—'}</td>;
                          case 'lastRun':
                            return <td key={id}>{schedule?.lastRun ? new Date(schedule.lastRun * 1000).toLocaleDateString() : '—'}</td>;
                          case 'dateAdded':
                            return <td key={id}>{new Date(playlist.createdAt).toLocaleDateString()}</td>;
                          case 'dateUpdated':
                            return <td key={id}>{new Date(playlist.updatedAt).toLocaleDateString()}</td>;
                          case 'sourceUrl':
                            return (
                              <td key={id}>
                                {playlist.sourceUrl ? (
                                  <a href={playlist.sourceUrl} target="_blank" rel="noopener noreferrer" className="playlist-source-link" title={playlist.sourceUrl}>
                                    Link ↗
                                  </a>
                                ) : '—'}
                              </td>
                            );
                          default:
                            return null;
                        }
                      })}
                      <td className="col-actions">
                        <div className="row-actions">
                          <button className="icon-btn" onClick={() => setModal({ type: 'edit', playlist })} title="Edit tracks" aria-label={`Edit tracks in ${playlist.name}`}><EditIcon /></button>
                          <button className="icon-btn" onClick={() => setModal({ type: 'share', playlist })} title="Share with another Playlist Lab user" aria-label={`Share ${playlist.name} with another Playlist Lab user`}><ShareIcon /></button>
                          <button className="icon-btn" onClick={() => setModal({ type: 'export', playlist })} title="Export to file or YouTube" aria-label={`Export ${playlist.name} to file or YouTube`}><ExportIcon /></button>
                          {isReimportable(playlist) && (
                            <button className="icon-btn" onClick={() => handleReimport(playlist)} disabled={reimportingId === playlist.id} title="Re-fetch this playlist from its original source now" aria-label={`Re-import ${playlist.name} from its source`}>
                              <ReimportIcon />
                            </button>
                          )}
                          {!isReimportable(playlist) && schedule?.scheduleType === 'mix_generation' && (
                            <button className="icon-btn" onClick={() => handleRunMixSchedule(schedule)} disabled={runningMixScheduleId === schedule.id} title="Regenerate this mix now" aria-label={`Regenerate ${playlist.name} now`}>
                              <ReimportIcon />
                            </button>
                          )}
                          <button className="icon-btn" onClick={() => handleQuickBackup(playlist)} disabled={backingUpId === playlist.id} title="Download a JSON backup of this playlist" aria-label={`Download a backup of ${playlist.name}`}><BackupIcon /></button>
                          <button className="icon-btn" onClick={() => handleClone(playlist)} disabled={cloningId === playlist.id} title="Duplicate this playlist" aria-label={`Duplicate ${playlist.name}`}><CloneIcon /></button>
                          <button className="icon-btn icon-btn-danger" onClick={() => handleDelete(playlist)} disabled={deletingId === playlist.id} title="Delete from Plex" aria-label={`Delete ${playlist.name} from Plex`}><DeleteIcon /></button>
                        </div>
                      </td>
                    </tr>
                    {isExpanded && playlist.dbId && (
                      <tr>
                        <td colSpan={3 + visibleColumns.size} style={{ padding: 0 }}>
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

      {columnMenuPos && (
        <div
          ref={columnMenuRef}
          className="th-filter-menu"
          style={{ position: 'fixed', top: columnMenuPos.y, left: columnMenuPos.x, right: 'auto', zIndex: 1000 }}
        >
          <ColumnCheckboxes order={columnOrder} visible={visibleColumns} onToggle={toggleColumnVisible} />
        </div>
      )}

      {modal?.type === 'edit' && (
        <Modal onClose={() => setModal(null)} contentStyle={{ maxWidth: '95vw', width: '1100px', maxHeight: '90vh', overflow: 'auto', position: 'relative' }}>
          <button onClick={() => setModal(null)} title="Close" style={embeddedPageCloseButtonStyle}>✕</button>
          <Suspense fallback={<ModalFallback />}>
            <PlaylistEditor playlist={{ ...modal.playlist, trackCount: modal.playlist.trackCount ?? 0, duration: modal.playlist.duration ?? 0, updatedAt: modal.playlist.updatedAt }} onPlaylistUpdated={refreshPlaylists} />
          </Suspense>
        </Modal>
      )}

      {modal?.type === 'share' && (
        <ShareModal playlistId={modal.playlist.id} playlistName={modal.playlist.name} onClose={() => setModal(null)} />
      )}

      {modal?.type === 'export' && (
        <Suspense fallback={<ModalFallback />}>
          <ExportModal playlistId={modal.playlist.id} playlistName={modal.playlist.name} trackCount={modal.playlist.trackCount} onClose={() => setModal(null)} />
        </Suspense>
      )}

      {modal?.type === 'schedule' && modal.playlist.dbId && (
        <ScheduleModal
          playlistId={String(modal.playlist.dbId)}
          playlistName={modal.playlist.name}
          schedule={scheduleByDbId.get(modal.playlist.dbId)}
          onClose={() => setModal(null)}
        />
      )}

    </div>
  );
};
