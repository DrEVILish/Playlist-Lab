/**
 * Default batch size for independent per-item Plex lookups (one artist/
 * album/track at a time becoming a network round trip). 5 balances speed
 * against not bursting an unbounded number of simultaneous requests at the
 * user's own Plex server.
 */
export const PLEX_LOOKUP_BATCH_SIZE = 5;

/**
 * Runs `fn` over `items` in fixed-size concurrent batches instead of one at
 * a time - the independent per-item network calls in mix generation (one
 * Plex lookup per artist/album/track) used to run sequentially, which turns
 * a request that could take ~1 batch round-trip into dozens of round trips.
 * Batching (rather than firing all of them at once) keeps a single mix
 * request from bursting an unbounded number of simultaneous requests at the
 * user's own Plex server. Result order matches `items`' order.
 */
export async function mapBatched<T, R>(
  items: T[],
  batchSize: number,
  fn: (item: T, index: number) => Promise<R>
): Promise<R[]> {
  const results: R[] = new Array(items.length);
  for (let i = 0; i < items.length; i += batchSize) {
    const batch = items.slice(i, i + batchSize);
    const batchResults = await Promise.all(batch.map((item, j) => fn(item, i + j)));
    for (let j = 0; j < batchResults.length; j++) results[i + j] = batchResults[j];
  }
  return results;
}
