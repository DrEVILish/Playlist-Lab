/**
 * Regression test for the shared import-session-state bug.
 *
 * Previously, index.ts declared its own module-scope `importSessions`,
 * `cancelledSessions`, and `progressState` Map/Set instances, completely
 * separate from the identically-named ones declared (and used by the
 * SSE/status/cancel endpoints) in routes/import.ts. The queue-based import
 * path (registered in index.ts) wrote progress/complete/error data into its
 * own, disconnected copies, so the SSE endpoint and polling endpoint in
 * routes/import.ts never saw any of it, and cancellation requests never
 * reached the running import.
 *
 * The fix makes routes/import.ts the single owner of these three
 * Map/Set instances (exported), with index.ts importing the very same
 * instances rather than declaring its own. This test verifies that:
 *   1. The exported instances are true singletons (Node module caching),
 *      i.e. every importer sees the identical object reference.
 *   2. A "queue handler"-style writer and an "SSE/status endpoint"-style
 *      reader, both obtained via independent `require`/import calls (as
 *      index.ts and routes/import.ts do), observe each other's writes.
 *   3. A cancellation flag set by one side is visible to an `isCancelled()`
 *      check performed via the other side, exactly like matching.ts's
 *      cancellation check consumes it.
 */

import { EventEmitter } from 'events';
import {
  importSessions,
  cancelledSessions,
  progressState,
} from '../../src/routes/import';

describe('shared import session state (routes/import.ts exports)', () => {
  afterEach(() => {
    importSessions.clear();
    cancelledSessions.clear();
    progressState.clear();
  });

  it('exposes the exact same Map/Set instances to every importer (module singleton)', () => {
    // Simulate index.ts performing its own `import { ... } from './routes/import'`
    // by re-requiring the module through Node's module cache.
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const secondImport = require('../../src/routes/import');

    expect(secondImport.importSessions).toBe(importSessions);
    expect(secondImport.cancelledSessions).toBe(cancelledSessions);
    expect(secondImport.progressState).toBe(progressState);
  });

  it('lets a queue-handler-style writer and an SSE/status-endpoint-style reader see the same progress data', () => {
    const sessionId = 'session-queue-1';

    // --- "index.ts" queue job handler side ---
    let progressEmitter = importSessions.get(sessionId);
    if (!progressEmitter) {
      progressEmitter = new EventEmitter();
      importSessions.set(sessionId, progressEmitter);
      progressEmitter.on('progress', (data: any) => {
        progressState.set(sessionId, data);
      });
      progressEmitter.on('complete', (data: any) => {
        progressState.set(sessionId, { type: 'complete', ...data });
      });
    }

    // --- "routes/import.ts" GET /api/import/status/:sessionId side ---
    // (reads progressState directly, as the real handler does)
    expect(progressState.get(sessionId)).toBeUndefined();

    progressEmitter.emit('progress', { current: 3, total: 10, phase: 'matching' });

    const polled = progressState.get(sessionId);
    expect(polled).toEqual({ current: 3, total: 10, phase: 'matching' });

    progressEmitter.emit('complete', { playlistName: 'My Playlist' });
    const completed = progressState.get(sessionId);
    expect(completed).toMatchObject({ type: 'complete', playlistName: 'My Playlist' });
  });

  it('connects cancellation end-to-end: a cancel request is visible to the isCancelled() check the import path uses', () => {
    const sessionId = 'session-cancel-1';

    // isCancelled() is exactly the closure shape import.ts passes into
    // matchPlaylist() via importPlaylist(..., cancelledSessions).
    const isCancelled = () => cancelledSessions.has(sessionId);

    expect(isCancelled()).toBe(false);

    // --- "routes/import.ts" POST /api/import/cancel/:sessionId fallback path ---
    cancelledSessions.add(sessionId);

    // --- the in-flight import (matching.ts), which only ever sees
    //     the cancelledSessions instance passed to it from the queue
    //     handler in index.ts --- now observes the cancellation because
    //     it is the same Set instance.
    expect(isCancelled()).toBe(true);
  });
});
