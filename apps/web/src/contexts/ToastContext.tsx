import { createContext, useContext, useState, useCallback, useRef, type FC, type ReactNode } from 'react';
import './ToastContext.css';

interface Toast {
  id: number;
  message: string;
  kind: 'success' | 'error';
}

interface ToastApi {
  success: (message: string) => void;
  error: (message: string) => void;
}

const ToastContext = createContext<ToastApi | undefined>(undefined);

/** Replacement for window.alert(): `toast.success('Saved!')` /
 * `toast.error('Failed to save')`. Renders a small auto-dismissing banner
 * instead of a blocking native popup, stacked bottom-right. */
export const useToast = (): ToastApi => {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within a ToastProvider');
  return ctx;
};

const AUTO_DISMISS_MS = 5000;

export const ToastProvider: FC<{ children: ReactNode }> = ({ children }) => {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(0);

  const dismiss = useCallback((id: number) => {
    setToasts(prev => prev.filter(t => t.id !== id));
  }, []);

  const push = useCallback((kind: Toast['kind'], message: string) => {
    const id = nextId.current++;
    setToasts(prev => [...prev, { id, message, kind }]);
    setTimeout(() => dismiss(id), AUTO_DISMISS_MS);
  }, [dismiss]);

  const api: ToastApi = {
    success: (message) => push('success', message),
    error: (message) => push('error', message),
  };

  return (
    <ToastContext.Provider value={api}>
      {children}
      {toasts.length > 0 && (
        <div className="toast-container" role="status" aria-live="polite">
          {toasts.map(t => (
            <div key={t.id} className={`toast toast-${t.kind}`}>
              <span className="toast-message">{t.message}</span>
              <button className="toast-close" onClick={() => dismiss(t.id)} aria-label="Dismiss notification">×</button>
            </div>
          ))}
        </div>
      )}
    </ToastContext.Provider>
  );
};
