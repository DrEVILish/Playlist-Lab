import { SourceAdapter, TargetAdapter } from './types';
import { externalLimiter } from '../services/task-queues';

// The bulk network-I/O methods per adapter kind - fetching/searching/writing
// a whole playlist against the external service's API - as opposed to
// one-shot OAuth/config calls, which are left unlimited. Plex is excluded:
// PlexClient already gates every Plex HTTP call through plexLimiter and
// matchPlaylist() already gates matching through matchingLimiter, so
// wrapping the plex adapter here too would have it acquire externalLimiter
// and then, from inside that same call, block forever waiting on a limiter
// slot it's already holding.
const SOURCE_METHODS = ['listPlaylists', 'searchPlaylists', 'fetchTracks', 'searchPlaylistsUnauthenticated', 'fetchUserPlaylistsUnauthenticated', 'fetchTracksUnauthenticated'] as const;
const TARGET_METHODS = ['searchCatalog', 'matchTracks', 'createPlaylist'] as const;

function rateLimited<T extends object>(adapter: T, methods: readonly string[]): T {
  const wrapped = Object.create(adapter);
  for (const method of methods) {
    const fn = (adapter as any)[method];
    if (typeof fn === 'function') {
      wrapped[method] = (...args: any[]) => externalLimiter.run(() => fn.apply(adapter, args));
    }
  }
  return wrapped;
}

export class AdapterRegistry {
  private sources = new Map<string, SourceAdapter>();
  private targets = new Map<string, TargetAdapter>();

  registerSource(adapter: SourceAdapter): void {
    this.sources.set(adapter.meta.id, adapter.meta.id === 'plex' ? adapter : rateLimited(adapter, SOURCE_METHODS));
  }

  registerTarget(adapter: TargetAdapter): void {
    this.targets.set(adapter.meta.id, adapter.meta.id === 'plex' ? adapter : rateLimited(adapter, TARGET_METHODS));
  }

  getSource(id: string): SourceAdapter | undefined {
    return this.sources.get(id);
  }

  getTarget(id: string): TargetAdapter | undefined {
    return this.targets.get(id);
  }

  listSources(): SourceAdapter[] {
    return Array.from(this.sources.values());
  }

  listTargets(): TargetAdapter[] {
    return Array.from(this.targets.values()).filter(a => !a.meta.isSourceOnly);
  }
}

export const adapterRegistry = new AdapterRegistry();
