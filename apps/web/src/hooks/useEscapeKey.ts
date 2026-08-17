import { useEffect } from 'react';

/** Calls onClose when Escape is pressed, while active is true. */
export function useEscapeKey(active: boolean, onClose: () => void) {
  useEffect(() => {
    if (!active) return;
    const onKeyDown = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [active, onClose]);
}
