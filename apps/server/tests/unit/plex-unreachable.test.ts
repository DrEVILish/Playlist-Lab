/**
 * Unit tests for the fail-fast behaviour when a Plex server stops answering.
 *
 * PlexClient allows 60s per request, and the direct-IP fallback retries once,
 * so a single call to an unresponsive server can occupy two minutes. A page
 * that makes several then appears to hang rather than fail, which is what
 * made the whole UI feel dead whenever the Plex box was busy. After a
 * connection-level failure the server is marked unreachable for a cooldown
 * and further calls reject immediately, so one request pays the timeout and
 * the rest return at once with something the UI can render.
 */

jest.mock('axios');

describe('PlexClient unreachable cooldown', () => {
  let PlexClient: typeof import('../../src/services/plex').PlexClient;
  let handlers: { onOk?: Function; onErr?: Function; onReq?: Function };

  beforeEach(async () => {
    jest.resetModules();
    jest.clearAllMocks();
    handlers = {};

    // resetModules() hands the module under test a different axios mock than
    // the one the outer scope holds, so the mock has to be read back from the
    // same fresh registry.
    const mockedAxios = (await import('axios')).default as unknown as jest.Mocked<typeof import('axios').default>;

    // Capture the interceptors the constructor installs so they can be driven
    // directly - the cooldown lives in them, not in the transport.
    mockedAxios.create.mockReturnValue({
      interceptors: {
        request: { use: (fn: Function) => { handlers.onReq = fn; } },
        response: { use: (ok: Function, err: Function) => { if (!handlers.onErr) { handlers.onOk = ok; handlers.onErr = err; } } },
      },
      get: jest.fn(),
      post: jest.fn(),
    } as any);

    ({ PlexClient } = await import('../../src/services/plex'));
  });

  const connectionFailure = () => ({ message: 'timeout of 60000ms exceeded', code: 'ECONNABORTED' });

  it('lets requests through while the server is healthy', () => {
    new PlexClient('http://plex.local:32400', 'token');
    expect(() => handlers.onReq!({ url: '/library/sections' })).not.toThrow();
  });

  it('fails a subsequent request immediately once a connection failure is seen', async () => {
    new PlexClient('http://plex.local:32400', 'token');

    await expect(handlers.onErr!(connectionFailure())).rejects.toBeDefined();

    // The whole point: no waiting on the timeout a second time.
    expect(() => handlers.onReq!({ url: '/library/sections' })).toThrow('Plex server is not responding');
  });

  it('does not open the cooldown for an error the server actually answered', async () => {
    new PlexClient('http://plex.local:32400', 'token');

    // A 404 means Plex is alive and talking - unrelated to reachability.
    await expect(handlers.onErr!({ message: 'Not Found', response: { status: 404 } })).rejects.toBeDefined();

    expect(() => handlers.onReq!({ url: '/library/sections' })).not.toThrow();
  });

  it('recovers as soon as any request succeeds', async () => {
    new PlexClient('http://plex.local:32400', 'token');
    await expect(handlers.onErr!(connectionFailure())).rejects.toBeDefined();
    expect(() => handlers.onReq!({ url: '/x' })).toThrow();

    handlers.onOk!({ status: 200 });

    expect(() => handlers.onReq!({ url: '/x' })).not.toThrow();
  });

  it('tracks servers independently, so one dead server does not block another', async () => {
    new PlexClient('http://dead.local:32400', 'token');
    const deadReq = handlers.onReq!;
    await expect(handlers.onErr!(connectionFailure())).rejects.toBeDefined();

    handlers = {};
    new PlexClient('http://alive.local:32400', 'token');

    expect(() => deadReq({ url: '/x' })).toThrow('Plex server is not responding');
    expect(() => handlers.onReq!({ url: '/x' })).not.toThrow();
  });

  it('identifies its own fail-fast error so callers can tell it from a Plex error', () => {
    new PlexClient('http://plex.local:32400', 'token');
    handlers.onErr!(connectionFailure()).catch(() => {});
    try {
      handlers.onReq!({ url: '/x' });
      throw new Error('expected it to throw');
    } catch (e) {
      expect(PlexClient.isUnreachableError(e)).toBe(true);
    }
  });
});
