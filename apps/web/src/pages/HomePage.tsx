import type { FC } from 'react';
import { PlaylistsPage } from './PlaylistsPage';
import { SchedulesPage } from './SchedulesPage';

/** Home = playlists + schedules combined onto one page (they used to be
 * separate nav destinations). Playlist-tied schedules show inline in the
 * playlist table itself now, so SchedulesPage only renders anything below
 * it for schedules that table can't represent - mix-generation schedules,
 * or a chart-import schedule before its first run creates a playlist -
 * and renders nothing at all when there's none of those (see its own
 * early-return). */
export const HomePage: FC = () => (
  // Both PlaylistsPage and SchedulesPage are `.page-container`s designed to
  // be the sole flex:1/min-height:0 child of .layout-main, sized to fill it
  // and let .layout-main's own overflow-y:auto handle scrolling. Rendered
  // as siblings that's a problem: two flex:1/min-height:0 items compete for
  // the same finite box, and the one with less content gets starved to
  // ~0 height while the other hogs all the space. This wrapper takes over
  // as .layout-main's single flex item instead - deliberately *not*
  // flex:1/min-height:0 itself, so it sizes to its natural (stacked)
  // content height and .layout-main scrolls the combined result, the same
  // way it used to scroll one page-container's content.
  <div>
    <PlaylistsPage />
    <SchedulesPage />
  </div>
);
