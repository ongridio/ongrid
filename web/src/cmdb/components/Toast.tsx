import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { CheckCircle2, X, XCircle } from 'lucide-react';

import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

// Transient feedback for actions that have already happened.
//
// WHY A TOAST AND NOT A BANNER: saving a row, deleting a model or importing a
// sheet all finish somewhere other than where the operator's eyes are. A banner
// inside the page would be missed after a drawer closes, and the design language
// forbids inventing a second notification surface per page.
//
// IT IS NOT FOR STATE. Loading, an empty list and a failed load are page states
// and stay in the page — a toast that reports "loading" and disappears is a
// worse answer than a table that says so. Only outcomes go here.
//
// IT IS NOT AN ALERT SYSTEM. Nothing here is queued, retried or persisted: a
// message that matters after a reload belongs in the page, not in a toast.

export type ToastTone = 'success' | 'error' | 'info';

type ToastItem = {
  id: number;
  tone: ToastTone;
  message: string;
};

type ToastApi = {
  success(message: string): void;
  error(message: string): void;
  info(message: string): void;
};

const ToastContext = createContext<ToastApi | null>(null);

/**
 * The toast API. Throws outside a provider rather than returning a no-op: a page
 * whose success message silently vanished is a bug nobody reports.
 */
export function useToast(): ToastApi {
  const api = useContext(ToastContext);
  if (!api) throw new Error('useToast must be used inside <ToastProvider>');
  return api;
}

const DISMISS_AFTER_MS = 4000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const { tr } = useI18n();
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setItems((current) => current.filter((item) => item.id !== id));
  }, []);

  const push = useCallback((tone: ToastTone, message: string) => {
    const trimmed = message.trim();
    if (!trimmed) return;
    const id = nextId.current;
    nextId.current += 1;
    setItems((current) => [...current, { id, tone, message: trimmed }]);
  }, []);

  const api = useMemo<ToastApi>(
    () => ({
      success: (message: string) => push('success', message),
      error: (message: string) => push('error', message),
      info: (message: string) => push('info', message),
    }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2">
        {items.map((item) => (
          <ToastCard key={item.id} item={item} onDismiss={() => dismiss(item.id)} closeLabel={tr('关闭', 'Dismiss')} />
        ))}
      </div>
    </ToastContext.Provider>
  );
}

function ToastCard({ item, onDismiss, closeLabel }: { item: ToastItem; onDismiss: () => void; closeLabel: string }) {
  useEffect(() => {
    const timer = window.setTimeout(onDismiss, DISMISS_AFTER_MS);
    return () => window.clearTimeout(timer);
  }, [onDismiss]);

  const Icon = item.tone === 'error' ? XCircle : item.tone === 'success' ? CheckCircle2 : null;

  return (
    <div
      // AN ERROR IS AN ALERT AND A SUCCESS IS A STATUS: a screen reader should
      // interrupt for the first and not for the second.
      role={item.tone === 'error' ? 'alert' : 'status'}
      className={cn(
        'pointer-events-auto flex items-start gap-2 rounded-lg border px-3 py-2 text-xs shadow-lg',
        item.tone === 'error'
          ? 'border-red-500/40 bg-card text-red-300'
          : item.tone === 'success'
            ? 'border-emerald-500/40 bg-card text-emerald-300'
            : 'border-border bg-card text-text-muted',
      )}
    >
      {Icon && <Icon size={14} className="mt-0.5 shrink-0" aria-hidden="true" />}
      <span className="min-w-0 flex-1 break-words">{item.message}</span>
      <button
        type="button"
        onClick={onDismiss}
        aria-label={closeLabel}
        className="shrink-0 rounded p-0.5 text-text-faint hover:text-text focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
      >
        <X size={12} aria-hidden="true" />
      </button>
    </div>
  );
}
