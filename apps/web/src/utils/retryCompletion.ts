import type { APIClient } from '@playlist-lab/shared';

/**
 * Polls the retry-status endpoint (real server-reported progress, not a
 * derived guess) until the current retry chain - including anything queued
 * behind it, since they share one per-user chain server-side - finishes,
 * and reports whether it failed outright. Waits for `active` to appear at
 * least once before treating a null response as "done", since a
 * queued-but-not-yet-started batch has no retry-status entry until the one
 * ahead of it finishes; without that, a track queued behind a large "Retry
 * All" would otherwise look instantly finished.
 *
 * Checks immediately rather than sleeping first: the server sets its
 * activeRetries entry synchronously before the caller can possibly make its
 * first status request (it can't fire until the POST /retry response, which
 * is sent after that entry is already set), so an immediate check reliably
 * observes it. A retry that resolves within a pre-sleep (a single fast
 * track, or a small "Retry All") would otherwise never be seen as active,
 * so `hasStarted` would never flip true and this would spin until the
 * timeout with the triggering button stuck disabled the whole time.
 *
 * Shared by the missing-tracks panel and the playlist editor's "replace
 * what's still missing with similar tracks" action, both of which retry
 * matching first and only act on whatever is genuinely still missing after.
 */
export async function waitForRetryCompletion(
  apiClient: APIClient,
  maxWaitMs = 5 * 60 * 1000,
  onStarted?: () => void
): Promise<{ error?: string; timedOut?: boolean }> {
  const start = Date.now();
  let hasStarted = false;
  while (Date.now() - start < maxWaitMs) {
    const { active } = await apiClient.getMissingRetryStatus();
    if (active?.error) return { error: active.error };
    if (active) {
      if (!hasStarted) onStarted?.();
      hasStarted = true;
    } else if (hasStarted) {
      return {};
    }
    await new Promise(resolve => setTimeout(resolve, 1500));
  }
  return { timedOut: true };
}
