import type { ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { useI18n } from '@/i18n/locale';
import { useUi } from '@/store/ui';

// 复用主菜单顶部的品牌区域，不新增可见按钮；键盘可聚焦后按右方向键展开。
export function SessionRevealRegion({ children, className }: { children: ReactNode; className?: string }) {
  const { tr } = useI18n();
  const { pathname } = useLocation();
  const collapsed = useUi((s) => s.sessionsCollapsed);
  const setOpen = useUi((s) => s.setSessionsPreviewOpen);
  const setHovered = useUi((s) => s.setSessionsTriggerHovered);
  const enabled = collapsed && (pathname === '/' || pathname.startsWith('/chat/'));
  return <div className={className} data-session-reveal
    tabIndex={enabled ? 0 : undefined}
    aria-label={enabled ? tr('会话栏预览区域', 'Session preview area') : undefined}
    onPointerEnter={(event) => { if (enabled && event.pointerType !== 'touch') setHovered(true); }}
    onPointerLeave={() => setHovered(false)}
    onKeyDown={(event) => {
      if (!enabled || event.target !== event.currentTarget || !['Enter', 'ArrowRight'].includes(event.key)) return;
      event.preventDefault();
      setOpen(true);
      requestAnimationFrame(() => document.querySelector<HTMLButtonElement>('[data-session-pin]')?.focus());
    }}
  >{children}</div>;
}
