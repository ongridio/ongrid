import { Button } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import {
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/Dialog';
import { X } from 'lucide-react';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

type Props = {
  open: boolean;
  onClose(): void;
  title?: string;
  children: ReactNode;
  footer?: ReactNode;
  size?: 'sm' | 'md' | 'lg' | 'xl';
  /** 允许用户拖动面板左右边缘调整宽度（长文阅读类弹窗用）。 */
  resizable?: boolean;
};

// 拖拽调宽边界：太窄排版崩坏，太宽盖满遮罩失去弹窗语义。
const RESIZE_MIN_PX = 440;
const RESIZE_MAX_VW = 0.95;

export function Modal({ open, onClose, title, children, footer, size = 'md', resizable }: Props) {
  const { tr } = useI18n();
  const panelRef = useRef<HTMLDivElement | null>(null);
  // null = 跟随 size 预设的 max-w；拖过一次之后宽度由用户接管。
  const [userWidth, setUserWidth] = useState<number | null>(null);

  if (!open) return null;

  const startResize = (edge: 'left' | 'right') => (e: ReactPointerEvent) => {
    if (!panelRef.current) return;
    e.preventDefault();
    const startX = e.clientX;
    const startW = panelRef.current.getBoundingClientRect().width;
    const onMove = (ev: globalThis.PointerEvent) => {
      const dx = ev.clientX - startX;
      // 面板水平居中，拖一条边时两侧同时变化 → 总宽随 2×位移走。
      const delta = (edge === 'right' ? dx : -dx) * 2;
      const max = Math.round(window.innerWidth * RESIZE_MAX_VW);
      setUserWidth(Math.min(Math.max(startW + delta, RESIZE_MIN_PX), max));
    };
    const onUp = () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent
        aria-label={title ?? tr('对话框', 'Dialog')}
        ref={panelRef}
        style={userWidth != null ? { width: userWidth, maxWidth: 'none' } : undefined}
        className={cn(
          size === 'sm' && 'max-w-sm',
          size === 'md' && 'max-w-md',
          size === 'lg' && 'max-w-2xl',
          size === 'xl' && 'max-w-4xl'
        )}
      >
        <div className="flex shrink-0 items-center justify-between border-b border-zinc-800 px-5 py-3.5">
          <DialogTitle className="text-sm font-semibold text-zinc-100">{title}</DialogTitle>
          <Button variant="subtle" size="sm"
            type="button"
            onClick={onClose}
            aria-label={tr('关闭', 'Close')}
            className="p-1"
          >
            <X size={16} />
          </Button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer && (
          <div className="flex shrink-0 items-center justify-end gap-2 border-t border-zinc-800 px-5 py-3">
            {footer}
          </div>
        )}
        {resizable && (
          <>
            <Hint content={tr('拖动调整宽度', 'Drag to resize')}><div
              onPointerDown={startResize('left')}

              className="absolute inset-y-0 -left-1 w-2 cursor-ew-resize rounded hover:bg-zinc-600/30"
            /></Hint>
            <Hint content={tr('拖动调整宽度', 'Drag to resize')}><div
              onPointerDown={startResize('right')}

              className="absolute inset-y-0 -right-1 w-2 cursor-ew-resize rounded hover:bg-zinc-600/30"
            /></Hint>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
