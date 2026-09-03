import winston from 'winston';
import fs from 'fs';
import path from 'path';
import os from 'os';

const LOG_LEVEL = process.env.LOG_LEVEL || 'info';

// Determine log directory:
// 1. Use LOG_DIR env var if set (set by tray app to AppData location)
// 2. In production, use AppData\Playlist Lab\logs
// 3. Otherwise use ./logs in current directory
function getLogDir(): string {
  if (process.env.LOG_DIR) {
    return process.env.LOG_DIR;
  }
  
  const isProduction = process.cwd().includes('Program Files') || process.cwd().includes('Program Files (x86)');
  if (isProduction) {
    return path.join(os.homedir(), 'AppData', 'Roaming', 'Playlist Lab', 'logs');
  }
  
  return './logs';
}

export const LOG_DIR = getLogDir();

// Exposed so other code (the admin error-log viewer) can read the exact
// files these transports write to, instead of re-deriving/guessing the path.
export const ERROR_LOG_PATH = path.join(LOG_DIR, 'error.log');
export const COMBINED_LOG_PATH = path.join(LOG_DIR, 'combined.log');

/**
 * The combined-log files actually worth reading right now, newest first.
 *
 * winston's File transport does not keep writing to `combined.log` once
 * `maxsize` is reached - it rolls over to `combined1.log`, `combined2.log`
 * and so on, and keeps writing to the highest-numbered file. So
 * COMBINED_LOG_PATH is only the live file until the first rollover, and
 * after that it's the *oldest* history rather than the newest. Anything
 * reading it by name goes quietly stale instead of failing, which is what
 * made the admin log viewer stop showing recent entries: it was reading
 * hours-old history, and the merged-in deemix journal (which is always
 * current) filled every slot in the newest-first list.
 *
 * Returns the two newest by mtime, so a file that has only just rolled over
 * and holds a handful of lines still comes with the history behind it.
 */
export function currentCombinedLogPaths(limit = 2): string[] {
  try {
    return fs.readdirSync(LOG_DIR)
      .filter(name => /^combined\d*\.log$/.test(name))
      .map(name => {
        const full = path.join(LOG_DIR, name);
        return { full, mtime: fs.statSync(full).mtimeMs };
      })
      .sort((a, b) => b.mtime - a.mtime)
      .slice(0, limit)
      .map(f => f.full);
  } catch {
    // Log directory missing/unreadable - fall back to the un-rotated name so
    // callers still have something to try.
    return [COMBINED_LOG_PATH];
  }
}

// winston.format.errors() only unwraps an Error passed as the log message
// itself (or as info.message) - it doesn't look inside metadata. This
// codebase's routes almost universally log `logger.error('msg', { error, ... })`
// with the raw caught Error under an `error` key, and a bare `new Error()` has
// no own enumerable properties, so winston.format.json() serializes it to
// `{}` - silently discarding the message and stack that would explain the
// failure. Replacing any Error found in metadata with a plain object fixes
// every one of those call sites at once instead of rewriting each of them.
// Must run after format.splat() - splat's info[SPLAT] re-merge restores the
// original raw metadata object (Error and all) onto info, undoing this if it
// runs first.
const serializeErrorFields = winston.format((info) => {
  for (const key of Object.keys(info)) {
    const value = (info as Record<string, unknown>)[key];
    if (value instanceof Error) {
      const plain: Record<string, unknown> = { ...value, message: value.message, stack: value.stack };
      // Axios attaches the request config - including auth headers like
      // X-Plex-Token or Spotify bearer tokens - as an own enumerable
      // property on its errors, and `request` is a raw (circular) socket.
      // Spreading the error above would otherwise write live credentials
      // into combined.log/error.log on every failed Plex/Spotify API call,
      // including failed logins with a bad/expired token.
      delete plain.config;
      delete plain.request;
      if (plain.response && typeof plain.response === 'object') {
        const { status, statusText } = plain.response as { status?: number; statusText?: string };
        plain.response = { status, statusText };
      }
      (info as Record<string, unknown>)[key] = plain;
    }
  }
  return info;
});

// Create logger instance
export const logger = winston.createLogger({
  level: LOG_LEVEL,
  format: winston.format.combine(
    winston.format.timestamp({ format: 'YYYY-MM-DD HH:mm:ss' }),
    winston.format.errors({ stack: true }),
    winston.format.splat(),
    serializeErrorFields(),
    winston.format.json()
  ),
  defaultMeta: { service: 'playlist-lab-server' },
  transports: [
    // Write all logs to console
    new winston.transports.Console({
      format: winston.format.combine(
        winston.format.colorize(),
        winston.format.printf(({ level, message, timestamp, ...meta }) => {
          const metaStr = Object.keys(meta).length ? JSON.stringify(meta) : '';
          return `${timestamp} [${level}]: ${message} ${metaStr}`;
        })
      ),
    }),
    // Write all logs with level 'error' and below to error.log
    new winston.transports.File({
      filename: path.join(LOG_DIR, 'error.log'),
      level: 'error',
      maxsize: 5242880, // 5MB
      maxFiles: 5,
    }),
    // Write all logs to combined.log
    new winston.transports.File({
      filename: path.join(LOG_DIR, 'combined.log'),
      maxsize: 5242880, // 5MB
      maxFiles: 5,
    }),
  ],
});

// If we're not in production, log to console with simpler format
if (process.env.NODE_ENV !== 'production') {
  logger.add(new winston.transports.Console({
    format: winston.format.combine(
      winston.format.colorize(),
      winston.format.simple()
    ),
  }));
}

/**
 * Confirmed matches get their own file rather than going through `logger`.
 * The main log level is routinely turned down to error-only, which would
 * discard exactly the record needed to review matching decisions and improve
 * them later - so this transport is deliberately independent of it.
 */
export const MATCH_LOG_PATH = path.join(LOG_DIR, 'matches.log');

export const matchLogger = winston.createLogger({
  level: 'info',
  format: winston.format.combine(
    winston.format.timestamp({ format: 'YYYY-MM-DD HH:mm:ss' }),
    winston.format.json()
  ),
  transports: [
    new winston.transports.File({ filename: MATCH_LOG_PATH, maxsize: 5242880, maxFiles: 5 }),
  ],
});

export const LOG_LEVELS = ['error', 'warn', 'info', 'debug'] as const;

/** Severity order for filtering an already-written entry (a log line read
 * back from a file, or a merged-in entry from another service's journal)
 * against the configured level - winston only filters at write time. */
export function isAtLeastAsSevere(level: string, threshold: string): boolean {
  const rank = (l: string) => {
    const i = (LOG_LEVELS as readonly string[]).indexOf(l);
    return i === -1 ? LOG_LEVELS.length : i;
  };
  return rank(level) <= rank(threshold);
}

// Changes verbosity at runtime. The Console and combined.log transports have
// no `level` of their own, so they fall back to logger.level on every write -
// error.log keeps its own fixed 'error' level and is unaffected.
export function setLogLevel(level: string): void {
  logger.level = level;
}
