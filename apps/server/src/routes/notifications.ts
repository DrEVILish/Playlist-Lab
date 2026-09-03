import { Router, Request, Response } from 'express';
import { requireAuth } from '../middleware/auth';
import { listNotifications, dismissNotification, clearNotifications, onNotificationsChanged } from '../services/job-notifications';

const router = Router();

/**
 * GET /api/notifications
 * Live feed for the header's notification center (deemix downloads, retry-matching batches, ...)
 */
router.get('/', requireAuth, (req: Request, res: Response) => {
  res.json({ notifications: listNotifications(req.session.userId!) });
});

/**
 * GET /api/notifications/stream
 * Server-sent events carrying the caller's whole notification feed whenever
 * it changes. Replaces the client polling this route on a timer: background
 * jobs update progress at their own irregular pace, so a fixed interval was
 * either behind the work or asking for nothing repeatedly, and it kept a
 * request-per-few-seconds running for every open tab whether or not anything
 * was happening.
 */
router.get('/stream', requireAuth, (req: Request, res: Response) => {
  const userId = req.session.userId!;

  res.setHeader('Content-Type', 'text/event-stream');
  res.setHeader('Cache-Control', 'no-cache');
  res.setHeader('Connection', 'keep-alive');
  // Tells nginx and friends not to buffer, which would otherwise hold events
  // until the response ended - i.e. forever, for a stream.
  res.setHeader('X-Accel-Buffering', 'no');
  res.flushHeaders?.();

  let open = true;
  const send = () => {
    if (!open) return;
    try {
      res.write(`data: ${JSON.stringify({ notifications: listNotifications(userId) })}\n\n`);
      (res as any).flush?.();
    } catch {
      open = false;
    }
  };

  // Send the current state immediately so a tab that connects mid-job renders
  // straight away instead of waiting for the next change.
  send();

  const unsubscribe = onNotificationsChanged(changedUserId => {
    if (changedUserId === userId) send();
  });

  // Without traffic, an idle connection can be dropped by an intermediary
  // with neither end noticing. A comment line is not an event, so this costs
  // the client nothing to ignore.
  const keepAlive = setInterval(() => {
    if (!open) return;
    try {
      res.write(': keep-alive\n\n');
      (res as any).flush?.();
    } catch {
      open = false;
    }
  }, 25000);

  req.on('close', () => {
    open = false;
    clearInterval(keepAlive);
    unsubscribe();
  });
});

/**
 * POST /api/notifications/:id/dismiss
 */
router.post('/:id/dismiss', requireAuth, (req: Request, res: Response) => {
  dismissNotification(req.session.userId!, (req.params as Record<string, string>).id);
  res.json({ success: true });
});

/**
 * POST /api/notifications/clear
 * Body `{ completedOnly: true }` keeps failures as well as running jobs.
 */
router.post('/clear', requireAuth, (req: Request, res: Response) => {
  clearNotifications(req.session.userId!, req.body?.completedOnly === true);
  res.json({ success: true });
});

export default router;
