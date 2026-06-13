import {
  useEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
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

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = '';
    };
  }, [open, onClose]);

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
    <div
      role="dialog"
      aria-modal="true"
      aria-label={title ?? 'Dialog'}
      className="fixed inset-0 z-50 flex items-center justify-center px-4"
    >
      <div
        className="absolute inset-0 bg-black/70 backdrop-blur-sm"
        onClick={onClose}
        aria-hidden
      />
      {/* Bound the panel to viewport: max-h-[90vh] keeps header / footer
          on screen, flex column lets the middle body absorb the
          remainder and scroll on overflow. Without this the panel grew
          taller than the viewport and (because body scroll is locked
          while the modal is open) the user had no way to reach lower
          content — affecting any modal whose form / content is long
          (rule editor, doc editor, plugin spec, etc.). */}
      <div
        ref={panelRef}
        style={userWidth != null ? { width: userWidth, maxWidth: 'none' } : undefined}
        className={cn(
          'anim-scale relative flex max-h-[90vh] w-full flex-col rounded-2xl border border-zinc-800 bg-zinc-900 shadow-2xl',
          size === 'sm' && 'max-w-sm',
          size === 'md' && 'max-w-md',
          size === 'lg' && 'max-w-2xl',
          size === 'xl' && 'max-w-4xl'
        )}
      >
        <div className="flex shrink-0 items-center justify-between border-b border-zinc-800 px-5 py-3.5">
          <h2 className="text-sm font-semibold text-zinc-100">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            aria-label={tr('关闭', 'Close')}
            className="rounded-lg p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
          >
            <X size={16} />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer && (
          <div className="flex shrink-0 items-center justify-end gap-2 border-t border-zinc-800 px-5 py-3">
            {footer}
          </div>
        )}
        {resizable && (
          <>
            <div
              onPointerDown={startResize('left')}
              title={tr('拖动调整宽度', 'Drag to resize')}
              className="absolute inset-y-0 -left-1 w-2 cursor-ew-resize rounded hover:bg-zinc-600/30"
            />
            <div
              onPointerDown={startResize('right')}
              title={tr('拖动调整宽度', 'Drag to resize')}
              className="absolute inset-y-0 -right-1 w-2 cursor-ew-resize rounded hover:bg-zinc-600/30"
            />
          </>
        )}
      </div>
    </div>
  );
}
