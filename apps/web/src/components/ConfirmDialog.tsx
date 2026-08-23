import type { FC, ReactNode } from 'react';
import { useEffect } from 'react';
import { useEscapeKey } from '../hooks/useEscapeKey';
import './ConfirmDialog.css';

export interface ConfirmOptions {
  title?: string;
  message: ReactNode;
  warning?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Styles the confirm button and icon as destructive. Defaults to true -
   * every confirm() call this replaced across the app guarded a destructive
   * action. */
  danger?: boolean;
}

/**
 * Generic replacement for window.confirm(): a styled, keyboard-accessible
 * (Enter confirms, Escape/click-outside cancels, Tab is trapped inside)
 * modal. Rendered by contexts/ConfirmContext.tsx's ConfirmProvider - use the
 * useConfirm() hook rather than this component directly.
 */
export const ConfirmDialog: FC<ConfirmOptions & { onConfirm: () => void; onCancel: () => void }> = ({
  title,
  message,
  warning,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  danger = true,
  onConfirm,
  onCancel,
}) => {
  useEscapeKey(true, onCancel);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        onConfirm();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onConfirm]);

  useEffect(() => {
    const modal = document.querySelector('.confirm-dialog-modal');
    const focusable = modal?.querySelectorAll<HTMLElement>('button, [tabindex]:not([tabindex="-1"])');
    const first = focusable?.[0];
    const last = focusable?.[focusable.length - 1];
    first?.focus();

    const handleTabKey = (e: KeyboardEvent) => {
      if (e.key !== 'Tab' || !first || !last) return;
      if (e.shiftKey) {
        if (document.activeElement === first) { e.preventDefault(); last.focus(); }
      } else if (document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', handleTabKey);
    return () => window.removeEventListener('keydown', handleTabKey);
  }, []);

  return (
    <div
      className="confirm-dialog-overlay"
      onClick={onCancel}
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-dialog-title"
      aria-describedby="confirm-dialog-message"
    >
      <div className="confirm-dialog-modal" onClick={(e) => e.stopPropagation()}>
        <div className="confirm-dialog-header">
          <div className="confirm-dialog-icon" aria-hidden="true">{danger ? '⚠️' : '❔'}</div>
          <h2 id="confirm-dialog-title" className="confirm-dialog-title">{title ?? (danger ? 'Are you sure?' : 'Confirm')}</h2>
        </div>

        <div className="confirm-dialog-content">
          <p id="confirm-dialog-message" className="confirm-dialog-message">{message}</p>
          {warning && <p className="confirm-dialog-warning" role="alert">{warning}</p>}
        </div>

        <div className="confirm-dialog-actions">
          <button onClick={onConfirm} className={`btn-confirm ${danger ? 'btn-confirm-danger' : ''}`} aria-label={confirmLabel}>
            {confirmLabel}
          </button>
          <button onClick={onCancel} className="btn-cancel" aria-label={cancelLabel}>
            {cancelLabel}
          </button>
        </div>

        <div className="confirm-dialog-hint" role="note">
          Press <kbd>Enter</kbd> to confirm • <kbd>Esc</kbd> to cancel
        </div>
      </div>
    </div>
  );
};
