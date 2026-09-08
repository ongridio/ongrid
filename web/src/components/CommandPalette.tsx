import { Input } from '@/components/ui';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/Dialog';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Search, MessageSquare, Compass } from 'lucide-react';
import { listSessions, type ChatSession } from '@/api/chat';
import { APP_ROUTES, scoreRoute, fuzzyMatchScore, type AppRoute } from '@/lib/routes';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

type Props = {
  open: boolean;
  onClose(): void;
};

// CommandPalette is the ⌘P / Ctrl+P quick switcher. It fuzzy-matches
// against in-app routes (the same set the sidebar exposes) plus the
// user's 5 most recent chat sessions. Selection navigates and closes.
//
// Design notes:
//  - We refetch the recent sessions on every open so a session created
//    elsewhere shows up without a full page reload. Cheap call, capped
//    at 5 rows.
//  - Keyboard model: ↑/↓ moves the active row across the flat result
//    list (routes first, sessions second), Enter activates, Esc closes.
//  - Dialog owns focus containment, Escape, and restoring the trigger focus.
export function CommandPalette({ open, onClose }: Props) {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [activeIndex, setActiveIndex] = useState(0);
  const [recent, setRecent] = useState<ChatSession[]>([]);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Reset state on open and pre-load recent sessions. We don't memoize
  // recent across opens because the list is small and sessions change.
  useEffect(() => {
    if (!open) return;
    setQuery('');
    setActiveIndex(0);
    let cancelled = false;
    void listSessions()
      .then((r) => {
        if (cancelled) return;
        setRecent((r.items ?? []).slice(0, 5));
      })
      .catch(() => {
        if (!cancelled) setRecent([]);
      });
    return () => {
      cancelled = true;
    };
  }, [open]);

  // Score routes against the query. When query is empty we just show
  // the full list in declared order so the palette is browseable.
  const routeMatches = useMemo<AppRoute[]>(() => {
    const q = query.trim();
    if (!q) return APP_ROUTES;
    const scored = APP_ROUTES.map((r) => ({ r, s: scoreRoute(q, r) })).filter((x) => x.s >= 0);
    scored.sort((a, b) => b.s - a.s);
    return scored.map((x) => x.r);
  }, [query]);

  // Score sessions by title. Empty title falls back to "会话 N" so the
  // user can still find untitled sessions by typing 会话.
  const sessionMatches = useMemo<ChatSession[]>(() => {
    const q = query.trim();
    if (!q) return recent;
    const scored = recent
      .map((s, i) => ({
        s,
        score: fuzzyMatchScore(q, s.title || tr(`会话 ${i + 1}`, `Session ${i + 1}`)),
      }))
      .filter((x) => x.score >= 0);
    scored.sort((a, b) => b.score - a.score);
    return scored.map((x) => x.s);
  }, [query, recent]);

  // Flat list — routes first, then sessions — for keyboard nav.
  type FlatItem =
    | { kind: 'route'; route: AppRoute }
    | { kind: 'session'; session: ChatSession; index: number };
  const flat = useMemo<FlatItem[]>(() => {
    const items: FlatItem[] = [];
    for (const r of routeMatches) items.push({ kind: 'route', route: r });
    sessionMatches.forEach((s, i) => items.push({ kind: 'session', session: s, index: i }));
    return items;
  }, [routeMatches, sessionMatches]);

  // Keep activeIndex in range as the result set changes.
  useEffect(() => {
    if (activeIndex >= flat.length) setActiveIndex(0);
  }, [flat.length, activeIndex]);

  if (!open) return null;

  function activate(item: FlatItem) {
    if (item.kind === 'route') {
      navigate(item.route.path);
    } else {
      navigate(`/chat/${item.session.id}`);
    }
    onClose();
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActiveIndex((i) => (flat.length === 0 ? 0 : (i + 1) % flat.length));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActiveIndex((i) => (flat.length === 0 ? 0 : (i - 1 + flat.length) % flat.length));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const item = flat[activeIndex];
      if (item) activate(item);

    }
  }

  // Build an index map so each result row knows whether it's the
  // currently active one.
  const routeStart = 0;
  const sessionStart = routeMatches.length;

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent initialFocus={inputRef} className="max-w-xl overflow-hidden" aria-label={tr('命令面板', 'Command palette')}>
        <DialogTitle className="sr-only">{tr('命令面板', 'Command palette')}</DialogTitle>
        <div className="flex items-center gap-2 border-b border-zinc-800 px-4 py-3">
          <Search size={14} className="text-zinc-500" />
          <Input variant="inset"
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActiveIndex(0);
            }}
            onKeyDown={onKeyDown}
            placeholder={tr('跳转到… 搜索路由、会话', 'Go to… search routes, sessions')}
            aria-label={tr('搜索', 'Search')}
            className="flex-1"
          />
          <kbd className="hidden rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-zinc-500 sm:inline">
            Esc
          </kbd>
        </div>

        <div className="max-h-[60vh] overflow-y-auto py-1">
          {flat.length === 0 && (
            <div className="px-4 py-8 text-center text-[12px] text-zinc-500">{tr('无结果', 'No results')}</div>
          )}

          {routeMatches.length > 0 && (
            <Section title={tr('导航', 'Navigation')} icon={Compass}>
              {routeMatches.map((r, i) => {
                const idx = routeStart + i;
                return (
                  <Row
                    key={r.path}
                    active={idx === activeIndex}
                    onMouseEnter={() => setActiveIndex(idx)}
                    onClick={() => activate({ kind: 'route', route: r })}
                  >
                    <span className="truncate text-zinc-100">{r.label}</span>
                    <span className="ml-2 truncate text-[11px] text-zinc-500">{r.group}</span>
                    <span className="ml-auto truncate text-[10px] text-zinc-600">{r.path}</span>
                  </Row>
                );
              })}
            </Section>
          )}

          {sessionMatches.length > 0 && (
            <Section title={tr('最近会话', 'Recent sessions')} icon={MessageSquare}>
              {sessionMatches.map((s, i) => {
                const idx = sessionStart + i;
                const title = s.title || tr(`会话 ${i + 1}`, `Session ${i + 1}`);
                return (
                  <Row
                    key={s.id}
                    active={idx === activeIndex}
                    onMouseEnter={() => setActiveIndex(idx)}
                    onClick={() => activate({ kind: 'session', session: s, index: i })}
                  >
                    <span className="truncate text-zinc-100">{title}</span>
                    <span className="ml-auto truncate text-[10px] text-zinc-600">{s.id.slice(0, 8)}</span>
                  </Row>
                );
              })}
            </Section>
          )}
        </div>

        <div className="flex items-center justify-between border-t border-zinc-800 px-4 py-2 text-[10px] text-zinc-600">
          <span>{tr('↑↓ 切换 · Enter 跳转 · Esc 关闭', '↑↓ navigate · Enter open · Esc close')}</span>
          <span>⌘P / Ctrl+P</span>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Section({
  title,
  icon: Icon,
  children,
}: {
  title: string;
  icon: typeof Search;
  children: React.ReactNode;
}) {
  return (
    <div className="py-1">
      <div className="flex items-center gap-1.5 px-3 pb-0.5 pt-1 text-[10px] uppercase tracking-wide text-zinc-500">
        <Icon size={10} />
        <span>{title}</span>
      </div>
      {children}
    </div>
  );
}

function Row({
  active,
  onMouseEnter,
  onClick,
  children,
}: {
  active: boolean;
  onMouseEnter(): void;
  onClick(): void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={active}
      onMouseEnter={onMouseEnter}
      onClick={onClick}
      className={cn(
        'flex w-full items-center gap-2 px-3 py-1.5 text-left text-[12px]',
        active ? 'bg-zinc-800 text-zinc-50' : 'text-zinc-200 hover:bg-zinc-900',
      )}
    >
      {children}
    </button>
  );
}
