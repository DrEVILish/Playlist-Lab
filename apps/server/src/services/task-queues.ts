/**
 * Global, resource-type concurrency limiters.
 *
 * Every Plex API call, every external scrape/import, and every playlist
 * matching run funnels through one of three chokepoints - PlexClient's
 * axios instance (plex.ts), scrapePlaylist() inside importPlaylist()
 * (import.ts), and matchPlaylist() (matching.ts) - regardless of which
 * feature triggered it (schedules, manual import, deemix, cross-import,
 * AI mix generation, ...). Capping concurrency at these three chokepoints,
 * instead of in each individual feature/route, bounds each resource's total
 * load app-wide: a burst of unrelated features running at once can no
 * longer stack their local caps on top of each other and overload Plex,
 * the network, or the event loop.
 */

class Limiter {
  private active = 0;
  private readonly queue: Array<() => void> = [];

  constructor(private readonly maxConcurrent: number) {}

  acquire(): Promise<void> {
    if (this.active < this.maxConcurrent) {
      this.active++;
      return Promise.resolve();
    }
    return new Promise<void>(resolve => {
      this.queue.push(() => {
        this.active++;
        resolve();
      });
    });
  }

  release(): void {
    this.active--;
    const next = this.queue.shift();
    if (next) next();
  }

  async run<T>(fn: () => Promise<T>): Promise<T> {
    await this.acquire();
    try {
      return await fn();
    } finally {
      this.release();
    }
  }
}

// One in-flight call per resource type, app-wide - concurrent background
// tasks (schedule refreshes, imports, cross-import, AI generation, ...) are
// what were driving memory/CPU high enough to lock up the WebUI for other
// users, so each chokepoint is capped to strictly one at a time rather than
// a small pool. Note plexLimiter also gates every live-request Plex call
// (browsing, thumbnails via PlexClient), not just background tasks, since
// there is only one PlexClient chokepoint for all Plex traffic - if that
// makes ordinary browsing feel serialized while a big task runs, split
// "task" Plex calls onto their own limiter instead of raising this back up.
export const plexLimiter = new Limiter(1);
export const externalLimiter = new Limiter(1);
export const matchingLimiter = new Limiter(1);
