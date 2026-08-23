import { useState, useRef, useEffect } from 'react';
import { useEscapeKey } from './useEscapeKey';

/**
 * Open/closed state, outside-click, and Escape-to-close behavior shared by
 * every small trigger-button popover in this app (table-header filter
 * menus, etc.). Callers render their own trigger button and popover body
 * against the returned `open`/`toggle`/`close`/`ref`.
 */
export function usePopover<T extends HTMLElement = HTMLElement>() {
  const [open, setOpen] = useState(false);
  const ref = useRef<T>(null);

  useEscapeKey(open, () => setOpen(false));

  useEffect(() => {
    if (!open) return;
    const onClickOutside = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onClickOutside);
    return () => document.removeEventListener('mousedown', onClickOutside);
  }, [open]);

  return { open, toggle: () => setOpen(o => !o), close: () => setOpen(false), ref };
}
