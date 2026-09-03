/**
 * Regression test for the admin log viewer going blank.
 *
 * winston's File transport stops writing to `combined.log` once it hits
 * maxsize - it rolls over to `combined1.log`, `combined2.log`, ... and keeps
 * writing to the newest. Anything that reads the un-numbered name by hand
 * therefore reads the *oldest* history once a rollover has happened, and
 * does so silently: the file still exists and still parses, it's just hours
 * stale. In the admin viewer that showed up as the app's own entries
 * vanishing entirely, because the deemix journal merged in alongside them is
 * always current and filled every slot in the newest-first list.
 */

import fs from 'fs';
import os from 'os';
import path from 'path';

describe('currentCombinedLogPaths', () => {
  let dir: string;
  let currentCombinedLogPaths: typeof import('../../src/utils/logger').currentCombinedLogPaths;

  beforeEach(async () => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), 'plab-logs-'));
    process.env.LOG_DIR = dir;
    jest.resetModules();
    ({ currentCombinedLogPaths } = await import('../../src/utils/logger'));
  });

  afterEach(() => {
    delete process.env.LOG_DIR;
    fs.rmSync(dir, { recursive: true, force: true });
  });

  const write = (name: string, ageMs: number) => {
    const full = path.join(dir, name);
    fs.writeFileSync(full, '{}\n');
    const when = new Date(Date.now() - ageMs);
    fs.utimesSync(full, when, when);
    return full;
  };

  it('picks the rotated file winston is actually writing to, not the un-numbered name', () => {
    write('combined.log', 60 * 60 * 1000); // rolled over an hour ago
    const live = write('combined3.log', 0);

    expect(currentCombinedLogPaths()[0]).toBe(live);
  });

  it('returns the previous file too, so a just-rolled-over log still has history behind it', () => {
    const older = write('combined2.log', 10 * 60 * 1000);
    const live = write('combined3.log', 0);
    write('combined1.log', 60 * 60 * 1000);

    expect(currentCombinedLogPaths()).toEqual([live, older]);
  });

  it('ignores error.log and anything else that is not a combined log', () => {
    write('error.log', 0);
    const live = write('combined.log', 1000);

    expect(currentCombinedLogPaths()).toEqual([live]);
  });
});
