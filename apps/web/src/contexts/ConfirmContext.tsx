import { createContext, useContext, useState, useCallback, type FC, type ReactNode } from 'react';
import { ConfirmDialog, type ConfirmOptions } from '../components/ConfirmDialog';

type ConfirmFn = (message: ReactNode, options?: Omit<ConfirmOptions, 'message'>) => Promise<boolean>;
type PromptFn = (message: ReactNode, options?: Omit<ConfirmOptions, 'message'>) => Promise<string | null>;

const ConfirmContext = createContext<ConfirmFn | undefined>(undefined);
const PromptContext = createContext<PromptFn | undefined>(undefined);

/** Replacement for window.confirm(): `if (!await confirm('Delete this?')) return;`
 * Renders a proper styled, keyboard-accessible dialog instead of the
 * browser's native (unstyled, un-skinnable) confirm popup. */
export const useConfirm = (): ConfirmFn => {
  const ctx = useContext(ConfirmContext);
  if (!ctx) throw new Error('useConfirm must be used within a ConfirmProvider');
  return ctx;
};

/** Replacement for window.prompt(): `const name = await prompt('New name?')`,
 * resolving to null when cancelled. Renders the same styled dialog
 * useConfirm() does with a text field in it, instead of the browser's
 * native prompt box - which ignored the app's theme entirely and was the
 * one piece of unstyled browser chrome left in the UI. */
export const usePrompt = (): PromptFn => {
  const ctx = useContext(PromptContext);
  if (!ctx) throw new Error('usePrompt must be used within a ConfirmProvider');
  return ctx;
};

interface PendingConfirm {
  options: ConfirmOptions;
  /** Resolves the useConfirm() promise, or the usePrompt() one with the
   * entered text - whichever opened this dialog. */
  resolve: (value: any) => void;
  isPrompt: boolean;
}

export const ConfirmProvider: FC<{ children: ReactNode }> = ({ children }) => {
  const [pending, setPending] = useState<PendingConfirm | null>(null);

  const confirm = useCallback<ConfirmFn>((message, options) => {
    return new Promise<boolean>((resolve) => {
      setPending({ options: { ...options, message }, resolve, isPrompt: false });
    });
  }, []);

  const prompt = useCallback<PromptFn>((message, options) => {
    return new Promise<string | null>((resolve) => {
      setPending({
        // A prompt asks for a value rather than warning about a destructive
        // action, so it gets the neutral icon and a plain Confirm button
        // unless the caller says otherwise.
        options: { danger: false, confirmLabel: 'Create', ...options, message, input: options?.input ?? {} },
        resolve,
        isPrompt: true,
      });
    });
  }, []);

  const handleConfirm = (value: string) => {
    pending?.resolve(pending.isPrompt ? value.trim() : true);
    setPending(null);
  };

  const handleCancel = () => {
    pending?.resolve(pending.isPrompt ? null : false);
    setPending(null);
  };

  return (
    <ConfirmContext.Provider value={confirm}>
      <PromptContext.Provider value={prompt}>
        {children}
        {pending && <ConfirmDialog {...pending.options} onConfirm={handleConfirm} onCancel={handleCancel} />}
      </PromptContext.Provider>
    </ConfirmContext.Provider>
  );
};
