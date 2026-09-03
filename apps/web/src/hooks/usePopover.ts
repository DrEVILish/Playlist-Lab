import { useState, useRef, useEffect } from 'react';
import { useEscapeKey } from './useEscapeKey';

/**
 * Open/closed state, outside-click, and Escape-to-close behavior shared by
 * every small trigger-button popover in this app (table-header filter
 * menus, etc.). Callers render their own trigger button and popover body
 * against the returned `open`/`toggle`/`close`/`ref`.
 *
 * `menuStyle` anchors a `position: fixed` popover body to the bottom-right
 * of the trigger via the viewport rather than a CSS-positioned ancestor, so
 * it isn't clipped by a scrollable container (e.g. a table wrapper with
 * overflow: auto) sitting between the trigger and the page.
 */
export function usePopover<T extends HTMLElement = HTMLElement>() {
  const [open, setOpen] = useState(false);
  const ref = useRef<T>(null);
  // Popover bodies render via createPortal(document.body) so they can escape
  // clipping ancestors - that also takes them out of ref's DOM subtree, so
  // the outside-click check below needs this second ref to recognize clicks
  // inside the portaled body as "inside" too.
  const panelRef = useRef<HTMLDivElement>(null);
  const [menuStyle, setMenuStyle] = useState<{ top: number; right: number }>({ top: 0, right: 0 });

  useEscapeKey(open, () => setOpen(false));

  useEffect(() => {
    if (!open || !ref.current) return;
    const rect = ref.current.getBoundingClientRect();
    setMenuStyle({ top: rect.bottom + 4, right: window.innerWidth - rect.right });
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onClickOutside = (e: MouseEvent) => {
      const target = e.target as Node;
      if (ref.current?.contains(target)) return;
      if (panelRef.current?.contains(target)) return;
      setOpen(false);
    };
    document.addEventListener('mousedown', onClickOutside);
    return () => document.removeEventListener('mousedown', onClickOutside);
  }, [open]);

  return { open, toggle: () => setOpen(o => !o), close: () => setOpen(false), ref, panelRef, menuStyle };
}
