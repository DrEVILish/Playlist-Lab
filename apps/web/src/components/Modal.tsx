import type { FC, ReactNode, CSSProperties } from 'react';
import { useEffect, useRef } from 'react';
import { useEscapeKey } from '../hooks/useEscapeKey';

const FOCUSABLE_SELECTOR = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

// The one ✕ close button style every modal in the app should use, for a
// caller that writes its own `<div style={{ display: 'flex',
// justifyContent: 'space-between' }}><h2>Title</h2><button>...</button>
// </div>` header row.
export const modalCloseButtonStyle: CSSProperties = {
  background: 'none',
  border: 'none',
  cursor: 'pointer',
  color: 'var(--text-secondary)',
  fontSize: '1.25rem',
  lineHeight: 1,
  padding: '0.25rem 0.5rem',
  borderRadius: '4px',
};

// For callers that wrap an existing page component (which renders its own
// <h1 className="page-title">/<h1 className="playlist-title"> flush against
// .modal-content's own 2rem padding, with no extra top spacing) rather than
// writing a header row themselves - float the ✕ over that title's line
// instead of pushing it down in a dead row above it. Pass `position:
// 'relative'` in the Modal's contentStyle so this anchors to the modal, not
// the viewport.
export const embeddedPageCloseButtonStyle: CSSProperties = {
  ...modalCloseButtonStyle,
  position: 'absolute',
  top: '2rem',
  right: '2rem',
  fontSize: '1.5rem',
  zIndex: 1,
};

/**
 * Shared overlay + content shell for the app's many small dialogs: owns the
 * click-outside-to-close backdrop, the click-inside-doesn't-close guard, the
 * Escape-to-close binding, and basic modal accessibility (role="dialog",
 * initial focus, Tab trapped inside the dialog, focus restored to whatever
 * triggered it on close) - so each modal only has to supply its own content
 * and sizing. `contentStyle` merges over `.modal-content`'s CSS defaults for
 * callers that need a non-default width/height/layout.
 */
export const Modal: FC<{ onClose: () => void; children: ReactNode; contentStyle?: CSSProperties; ariaLabel?: string }> = ({ onClose, children, contentStyle, ariaLabel }) => {
  const contentRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLElement | null>(null);
  // A plain onClick={onClose} on the overlay closes on any click event that
  // targets it - but a native click event's target is resolved from mouseup
  // (via the nearest common ancestor of mousedown/mouseup when they differ),
  // so selecting text inside the modal and releasing the drag outside it
  // still fires click on the overlay and closes the modal unintentionally.
  // Requiring mousedown *and* mouseup to both land directly on the overlay
  // limits closing to genuine outside clicks.
  const mouseDownOnOverlay = useRef(false);

  useEscapeKey(true, onClose);

  useEffect(() => {
    triggerRef.current = document.activeElement as HTMLElement | null;
    const focusable = contentRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR);
    (focusable?.[0] ?? contentRef.current)?.focus();

    return () => {
      // Only restore focus if it's still where the modal left it - if
      // something else (e.g. another modal opening on top) already moved
      // focus elsewhere, don't fight it.
      if (triggerRef.current && document.body.contains(triggerRef.current)) {
        triggerRef.current.focus();
      }
    };
  }, []);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== 'Tab' || !contentRef.current) return;
    const focusable = Array.from(contentRef.current.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];

    if (e.shiftKey) {
      if (document.activeElement === first || document.activeElement === contentRef.current) {
        e.preventDefault();
        last.focus();
      }
    } else if (document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return (
    <div
      className="modal-overlay"
      onMouseDown={(e) => { mouseDownOnOverlay.current = e.target === e.currentTarget; }}
      onMouseUp={(e) => {
        if (mouseDownOnOverlay.current && e.target === e.currentTarget) onClose();
        mouseDownOnOverlay.current = false;
      }}
    >
      <div
        ref={contentRef}
        className="modal-content"
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        tabIndex={-1}
        onKeyDown={handleKeyDown}
        style={contentStyle}
      >
        {children}
      </div>
    </div>
  );
};
