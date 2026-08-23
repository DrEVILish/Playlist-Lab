import type { FC, ReactNode, CSSProperties } from 'react';
import { useEffect, useRef } from 'react';
import { useEscapeKey } from '../hooks/useEscapeKey';

const FOCUSABLE_SELECTOR = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

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
    <div className="modal-overlay" onClick={onClose}>
      <div
        ref={contentRef}
        className="modal-content"
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={handleKeyDown}
        style={contentStyle}
      >
        {children}
      </div>
    </div>
  );
};
