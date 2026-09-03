/**
 * Unit tests for isAtLeastAsSevere, the severity filter shared by the admin
 * log viewer and (indirectly) winston's own write-time filtering.
 *
 * The bug this pins: with the server's log level turned down to error-only,
 * the admin log viewer kept showing info-level entries anyway. The cause was
 * that the viewer read raw lines out of combined.log/the deemix journal and
 * displayed them as-is, with no floor applied at read time - so history
 * written before the level was lowered (and deemix-server's own journal,
 * which this app's level setting has no control over) still showed
 * everything regardless of the configured level.
 */

import { isAtLeastAsSevere } from '../../src/utils/logger';

describe('isAtLeastAsSevere', () => {
  it('lets a same-severity entry through', () => {
    expect(isAtLeastAsSevere('error', 'error')).toBe(true);
    expect(isAtLeastAsSevere('info', 'info')).toBe(true);
  });

  it('lets a more severe entry through a looser threshold', () => {
    expect(isAtLeastAsSevere('error', 'info')).toBe(true);
    expect(isAtLeastAsSevere('warn', 'debug')).toBe(true);
  });

  it('blocks a less severe entry when the threshold is error-only', () => {
    // This is the exact scenario from the bug report: level set to
    // error-only, an info-level line should not pass the filter.
    expect(isAtLeastAsSevere('info', 'error')).toBe(false);
    expect(isAtLeastAsSevere('warn', 'error')).toBe(false);
    expect(isAtLeastAsSevere('debug', 'error')).toBe(false);
  });

  it('treats an unrecognized level as the least severe, not the most', () => {
    // A malformed/unknown level string should never leak past a strict
    // threshold just because it didn't match a known rank.
    expect(isAtLeastAsSevere('bogus', 'error')).toBe(false);
    expect(isAtLeastAsSevere('error', 'bogus')).toBe(true);
  });
});
