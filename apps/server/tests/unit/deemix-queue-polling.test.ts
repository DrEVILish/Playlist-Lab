/**
 * Unit tests for the deemix queue-polling and folder-layout behaviour that
 * "Deemix All" on a large playlist depends on.
 *
 * deemix-server has no per-item status endpoint - reading one download's
 * progress means fetching the entire queue. Every in-flight download used
 * to do that independently on its own timer, so N concurrent downloads
 * re-fetched and re-parsed the same N-entry payload N times per interval.
 * On a large playlist that pegged the CPU and made the whole instance
 * unresponsive. These tests pin the fix: one shared snapshot per interval,
 * reused by every poller, so the request rate stays flat as N grows.
 */

jest.mock('axios');

jest.mock('../../src/config', () => ({
  configService: { config: { deemixArl: 'test-arl' } },
}));

type Deemix = typeof import('../../src/services/deemix');

/** Each test re-imports the service so it starts with an empty snapshot -
 * which means re-reading the axios mock from the same fresh module registry
 * too, since resetModules() hands the service a different mock instance
 * than the one the outer scope is holding. */
async function loadDeemix(): Promise<{ deemix: Deemix; axios: jest.Mocked<typeof import('axios').default> }> {
  jest.resetModules();
  const axios = (await import('axios')).default as unknown as jest.Mocked<typeof import('axios').default>;
  const deemix = await import('../../src/services/deemix');
  return { deemix, axios };
}

const queueResponse = (uuids: string[]) => ({
  data: {
    queue: Object.fromEntries(uuids.map(u => [u, {
      status: 'downloading', progress: 42, title: u, artist: 'B', size: 1, downloaded: 0, errors: [],
    }])),
  },
});

describe('deemix queue polling', () => {
  let deemix: Deemix;
  let axios: jest.Mocked<typeof import('axios').default>;

  beforeEach(async () => {
    jest.useFakeTimers();
    ({ deemix, axios } = await loadDeemix());

    // deemix's /api/getQueue needs no session, but queueDeemixDownload() is
    // still what these tests use to get a uuid into the queue in the first
    // place, and it does log in.
    axios.post.mockResolvedValue({
      headers: { 'set-cookie': ['connect.sid=abc; Path=/'] },
      data: { result: true, data: { obj: { uuid: 'track_1_3', title: 'A', artist: 'B' } } },
    } as any);
    axios.get.mockResolvedValue({ data: { settings: { maxBitrate: '3' } } } as any);
    await deemix.queueDeemixDownload('https://www.deezer.com/track/1');
    axios.get.mockReset();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  it('serves many concurrent pollers from a single queue fetch', async () => {
    const uuids = Array.from({ length: 50 }, (_, i) => `track_${i}_3`);
    axios.get.mockResolvedValue(queueResponse(uuids) as any);

    // 50 downloads all polling at once - the shape "Deemix All" produces.
    const items = await Promise.all(uuids.map(u => deemix.getDeemixQueueItem(u)));

    expect(items.every(i => i?.progress === 42)).toBe(true);
    // The whole point: one request for all 50, not 50. Without the shared
    // snapshot this is 50, and grows with the playlist.
    expect(axios.get).toHaveBeenCalledTimes(1);
  });

  it('refetches once the snapshot goes stale rather than serving it forever', async () => {
    axios.get.mockResolvedValue(queueResponse(['track_1_3']) as any);

    await deemix.getDeemixQueueItem('track_1_3');
    expect(axios.get).toHaveBeenCalledTimes(1);

    // Within the TTL: still the cached snapshot.
    await deemix.getDeemixQueueItem('track_1_3');
    expect(axios.get).toHaveBeenCalledTimes(1);

    // Past it: progress has to be able to actually move.
    jest.setSystemTime(Date.now() + 5000);
    await deemix.getDeemixQueueItem('track_1_3');
    expect(axios.get).toHaveBeenCalledTimes(2);
  });
});

// The bug this pins: deemix-server answers 200/result:true with an empty
// `obj` when it silently treats an addToQueue call as a duplicate of
// something already queued, rather than actually rejecting it - and that
// carries no information about the item's real state. The fix looks the
// item up directly via deemix's own deterministic uuid scheme instead of
// surfacing a bare "did not return a queued item" error for what is, in
// fact, a perfectly normal "it's already downloading" case.
describe('deemix already-queued handling', () => {
  it('looks up the existing queue entry instead of throwing when addToQueue silently no-ops a duplicate', async () => {
    const { deemix, axios } = await loadDeemix();

    axios.post.mockResolvedValueOnce({
      headers: { 'set-cookie': ['connect.sid=abc; Path=/'] },
      data: {},
    } as any);
    // maxBitrate '3' makes the expected deterministic uuid "track_5_3".
    axios.get.mockResolvedValueOnce({ data: { settings: { maxBitrate: '3' } } } as any);
    // addToQueue "succeeds" but returns no queued item - deemix-server's
    // signal for "this exact track+bitrate is already in the queue".
    axios.post.mockResolvedValueOnce({ data: { result: true, data: { obj: [] } } } as any);
    axios.get.mockResolvedValueOnce({
      data: { queue: { track_5_3: { status: 'downloading', progress: 10, title: 'Already Going', artist: 'Some Artist', size: 1, downloaded: 0, errors: [] } } },
    } as any);

    const result = await deemix.queueDeemixDownload('https://www.deezer.com/track/5');

    expect(result).toEqual({ uuid: 'track_5_3', title: 'Already Going', artist: 'Some Artist', alreadyQueued: true });
  });

  it('still throws a clear error when the item truly cannot be found anywhere in the queue', async () => {
    const { deemix, axios } = await loadDeemix();

    axios.post.mockResolvedValueOnce({
      headers: { 'set-cookie': ['connect.sid=abc; Path=/'] },
      data: {},
    } as any);
    axios.get.mockResolvedValueOnce({ data: { settings: { maxBitrate: '3' } } } as any);
    axios.post.mockResolvedValueOnce({ data: { result: true, data: { obj: [] } } } as any);
    // Not present under its deterministic uuid either - a genuine failure.
    axios.get.mockResolvedValueOnce({ data: { queue: {} } } as any);

    await expect(deemix.queueDeemixDownload('https://www.deezer.com/track/5'))
      .rejects.toThrow(/could not queue/);
  });
});

describe('deemix folder layout', () => {
  it('forces CD subfolders off when saving settings, passing everything else through', async () => {
    jest.resetModules();
    const writes: string[] = [];
    // Partial mock - winston builds its file transports at import time and
    // needs the rest of fs to be real.
    jest.doMock('fs', () => ({
      ...jest.requireActual('fs'),
      writeFileSync: (_path: string, data: string) => { writes.push(data); },
    }));
    jest.doMock('child_process', () => ({
      ...jest.requireActual('child_process'),
      exec: (_cmd: string, cb: Function) => cb(null, { stdout: '', stderr: '' }),
    }));

    const deemix = await import('../../src/services/deemix');
    // deemix's default splits a release into CD1/CD2 folders below the album
    // folder; Plex reads the deepest folder as the album name, so downloads
    // landed in the library as an album literally called "CD1".
    await deemix.updateDeemixSettings({
      createCDFolder: true,
      downloadLocation: '/downloads',
      albumTracknameTemplate: '%artist% - %tracknumber% - %title%',
    });

    const saved = JSON.parse(writes[0]);
    expect(saved.createCDFolder).toBe(false);
    // The genuinely user-configurable settings are left exactly as set.
    expect(saved.downloadLocation).toBe('/downloads');
    expect(saved.albumTracknameTemplate).toBe('%artist% - %tracknumber% - %title%');
  });
});

describe('deemix search matching', () => {
  // Blindly downloading the top search hit was the old behaviour, and a
  // missing track is exactly the case where the right answer often isn't in
  // Deezer's catalogue at all - so the top hit is routinely a karaoke
  // version or an unrelated song sharing a title, which then lands in the
  // library permanently. These pin the gate + score that now stand in the
  // way.
  const settings = {
    minMatchScore: 0.8,
    stripParentheses: true,
    stripBrackets: true,
    ignoreFeaturedArtists: true,
    ignoreRemixInfo: true,
    preferNonCompilation: true,
    variousArtistsNames: ['Various Artists'],
    featuredArtistPatterns: ['feat.', 'ft.'],
    customStripPatterns: [],
    versionSuffixPatterns: [],
    remasterPatterns: ['remaster'],
    penaltyKeywords: [],
    priorityKeywords: [],
  } as any;

  const hit = (id: number, title: string, artist: string, album: string, albumId = id) => ({
    id, title, link: `https://www.deezer.com/track/${id}`,
    artist: { name: artist }, album: { title: album, id: albumId },
  });

  const searchResponse = (results: any[]) => ({ data: { data: results } });

  it('picks the right artist rather than whatever Deezer ranked first', async () => {
    const { deemix, axios } = await loadDeemix();
    axios.get.mockResolvedValue(searchResponse([
      hit(1, 'Blue Monday', 'Karaoke All Stars', 'Karaoke Hits Vol. 4'),
      hit(2, 'Blue Monday', 'New Order', 'Power, Corruption & Lies'),
    ]) as any);

    const [best] = await deemix.findBestDeemixMatches('Blue Monday', 'New Order', settings);

    expect(best.match.artist.name).toBe('New Order');
  });

  it('returns nothing when the only hits are by the wrong artist', async () => {
    const { deemix, axios } = await loadDeemix();
    // A title match against a wrong artist still scores ~91 on its own -
    // the artist gate, not the score, is what has to reject these.
    axios.get.mockResolvedValue(searchResponse([
      hit(1, 'Blue Monday', 'Karaoke All Stars', 'Karaoke Hits Vol. 4'),
      hit(2, 'Blue Monday', 'Some Tribute Band', 'Tribute To New Order'),
    ]) as any);

    const matches = await deemix.findBestDeemixMatches('Blue Monday', 'New Order', settings);

    expect(matches).toEqual([]);
  });

  it('scores on title alone when no artist is given (the Various Artists case)', async () => {
    const { deemix, axios } = await loadDeemix();
    axios.get.mockResolvedValue(searchResponse([
      hit(1, 'Blue Monday', 'New Order', 'Power, Corruption & Lies'),
      hit(2, 'Blue Monday', 'Orgy', 'Candyass'),
      hit(3, 'Completely Different Song', 'Someone Else', 'Some Album'),
    ]) as any);

    const matches = await deemix.findBestDeemixMatches('Blue Monday', '', settings, 5);

    expect(matches).toHaveLength(2);
    expect(matches.map(m => m.match.artist.name).sort()).toEqual(['New Order', 'Orgy']);
  });
});
