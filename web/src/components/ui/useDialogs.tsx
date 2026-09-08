import { Input } from './Input';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Dialog, DialogContent, DialogDescription, DialogTitle } from './Dialog';
import { Button } from './Button';
import { useI18n } from '@/i18n/locale';

type Request = { kind: 'confirm' | 'prompt' | 'alert'; message: string };

/** Local confirmation state: cancelling or unmounting never approves an action. */
export function useDialogs() {
  const { tr } = useI18n();
  const [request, setRequest] = useState<Request | null>(null);
  const [value, setValue] = useState('');
  const pending = useRef<((value: string | null) => void) | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const settle = useCallback((result: string | null) => {
    pending.current?.(result);
    pending.current = null;
    setRequest(null);
  }, []);
  useEffect(() => () => { pending.current?.(null); pending.current = null; }, []);
  const ask = useCallback((kind: Request['kind'], message: string, initial = '') => {
    pending.current?.(null);
    setValue(initial);
    setRequest({ kind, message });
    return new Promise<string | null>((resolve) => { pending.current = resolve; });
  }, []);
  const confirmAction = useCallback(async (message: string) => (await ask('confirm', message)) !== null, [ask]);
  const promptAction = useCallback((message: string, initial = '') => ask('prompt', message, initial), [ask]);
  const alertAction = useCallback(async (message: string) => { await ask('alert', message); }, [ask]);
  const dialog = request && <Dialog open onOpenChange={(open) => { if (!open) settle(null); }}>
    <DialogContent initialFocus={request.kind === 'prompt' ? input : cancel} className="p-5">
      <DialogTitle className="text-sm font-semibold">{request.kind === 'alert' ? tr('提示', 'Notice') : tr('确认操作', 'Confirm action')}</DialogTitle>
      <DialogDescription className="mt-2 whitespace-pre-wrap break-words text-sm text-text-muted">{request.message}</DialogDescription>
      <form onSubmit={(event) => { event.preventDefault(); settle(request.kind === 'prompt' ? value : 'confirmed'); }}>
        {request.kind === 'prompt' && <Input ref={input} aria-label={request.message} value={value} onChange={(event) => setValue(event.target.value)} className="mt-3 w-full" />}
        <div className="mt-5 flex justify-end gap-2">
          {request.kind !== 'alert' && <Button ref={cancel} onClick={() => settle(null)}>{tr('取消', 'Cancel')}</Button>}
          <Button ref={request.kind === 'alert' ? cancel : undefined} variant="primary" type="submit">{tr('确认', 'Confirm')}</Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>;
  return { confirmAction, promptAction, alertAction, dialog };
}
