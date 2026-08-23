import { createContext, useContext, useState, useCallback, type FC, type ReactNode } from 'react';
import { ConfirmDialog, type ConfirmOptions } from '../components/ConfirmDialog';

type ConfirmFn = (message: ReactNode, options?: Omit<ConfirmOptions, 'message'>) => Promise<boolean>;

const ConfirmContext = createContext<ConfirmFn | undefined>(undefined);

/** Replacement for window.confirm(): `if (!await confirm('Delete this?')) return;`
 * Renders a proper styled, keyboard-accessible dialog instead of the
 * browser's native (unstyled, un-skinnable) confirm popup. */
export const useConfirm = (): ConfirmFn => {
  const ctx = useContext(ConfirmContext);
  if (!ctx) throw new Error('useConfirm must be used within a ConfirmProvider');
  return ctx;
};

interface PendingConfirm {
  options: ConfirmOptions;
  resolve: (value: boolean) => void;
}

export const ConfirmProvider: FC<{ children: ReactNode }> = ({ children }) => {
  const [pending, setPending] = useState<PendingConfirm | null>(null);

  const confirm = useCallback<ConfirmFn>((message, options) => {
    return new Promise<boolean>((resolve) => {
      setPending({ options: { ...options, message }, resolve });
    });
  }, []);

  const handleConfirm = () => {
    pending?.resolve(true);
    setPending(null);
  };

  const handleCancel = () => {
    pending?.resolve(false);
    setPending(null);
  };

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {pending && <ConfirmDialog {...pending.options} onConfirm={handleConfirm} onCancel={handleCancel} />}
    </ConfirmContext.Provider>
  );
};
