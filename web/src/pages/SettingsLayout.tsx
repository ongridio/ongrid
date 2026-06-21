import { Suspense } from 'react';
import { NavLink, Outlet } from 'react-router-dom';
import {
  KeyRound,
  Bell,
  Loader2,
  MessagesSquare,
  Gauge,
  HeartPulse,
  Lock,
  Plug,
  Shield,
} from 'lucide-react';
import { cn } from '@/lib/cn';
import type { IconType } from '@/lib/icon';
import { Card, EmptyState, PageHeader } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

type RailItem = {
  to: string;
  icon: IconType;
  labelZh: string;
  labelEn: string;
  hintZh: string;
  hintEn: string;
  disabled?: boolean;
  badgeIcon?: IconType;
  badgeTitle?: string;
};

const RAIL_ITEMS: RailItem[] = [
  { to: 'health', icon: HeartPulse, labelZh: '健康', labelEn: 'Health', hintZh: '平台自检 / 依赖状态', hintEn: 'Platform self-check / dependency status' },
  { to: 'integrations', icon: Plug, labelZh: '集成', labelEn: 'Integrations', hintZh: 'Prometheus / Grafana 等外部系统', hintEn: 'External systems — Prometheus / Grafana / etc.' },
  // marketplace entry retired 2026-05-19 — install/uninstall moved to
  // /skills?tab=install so the whole skill story (loaded + installed)
  // lives on one page. The Settings route still redirects bookmarks.
  { to: 'llm', icon: KeyRound, labelZh: '模型', labelEn: 'LLM', hintZh: 'OpenAI / 兼容服务', hintEn: 'OpenAI / compatible providers' },
  { to: 'secrets', icon: Lock, labelZh: '凭证', labelEn: 'Credentials', hintZh: '技能 / MCP 注入的凭据（env / 文件）', hintEn: 'Credentials injected into skills / MCP (env / files)' },
  // 通知 = 单向告警推送(channel in the alert sense). channels = 双向 chat
  // bot(IM in the messaging sense). We pair URL ↔ label so the route name
  // matches what the user sees in the sidebar; back-compat redirects from
  // the previous /communications and /bots paths live in App.tsx.
  { to: 'notifications', icon: Bell, labelZh: '通知', labelEn: 'Notifications', hintZh: '飞书 / 钉钉 / 企业微信 / Slack / Telegram / Webhook（告警推送）', hintEn: 'Slack / Telegram / Larksuite / DingTalk / WeCom / Webhook (alert delivery)' },
  { to: 'channels', icon: MessagesSquare, labelZh: '渠道', labelEn: 'Channels', hintZh: '飞书 / 钉钉 / Telegram / Slack 双向多轮', hintEn: 'Slack / Telegram / Larksuite / DingTalk — two-way multi-turn' },
  { to: 'preferences', icon: Gauge, labelZh: '偏好', labelEn: 'Preferences', hintZh: '默认时间窗 / 自动刷新', hintEn: 'Default time window / auto-refresh' },
];

export default function SettingsLayout() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  // route-level gate. Sidebar already hides /settings/* for
  // non-admins, but a stale deep-link still lands here — short-circuit
  // to an EmptyState so we don't render the rail + outlet (which is
  // mostly admin-only mutation UI behind individual per-page checks).
  if (!isAdmin) {
    return (
      <main className="anim-fade flex flex-1 flex-col overflow-hidden p-6">
        <Card className="p-6">
          <EmptyState
            icon={Shield}
            title={tr('需要管理员权限', 'Admin permission required')}
            hint={tr('设置页只对管理员开放。请联系管理员授予权限。', 'Settings are admin-only. Ask an admin to grant permission.')}
          />
        </Card>
      </main>
    );
  }
  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader title={tr('设置', 'Settings')} subtitle={tr('产品级配置；admin 可改', 'Product-wide configuration; admin can edit')} />

      <div className="flex-1 overflow-hidden">
        <div className="grid h-full grid-cols-1 lg:grid-cols-[240px_1fr]">
          {/* Left rail (desktop) / horizontal chip row (mobile) */}
          <nav
            aria-label={tr('设置分类', 'Settings categories')}
            className={cn(
              'border-zinc-800 bg-zinc-950/40',
              'lg:h-full lg:overflow-y-auto lg:border-r',
              'flex shrink-0 gap-1 overflow-x-auto border-b px-3 py-3 lg:flex-col lg:gap-0.5 lg:px-3 lg:py-4'
            )}
          >
            {RAIL_ITEMS.map((item) => (
              <RailLink key={item.to} item={item} />
            ))}
          </nav>

          <div className="overflow-y-auto px-6 py-6">
            <div className="mx-auto max-w-3xl">
              {/* Local Suspense so lazy-loaded leaf routes don't bubble
                  back to the app-level boundary in Layout.tsx — that
                  would unmount the whole settings shell (rail + page
                  header) and replay `anim-fade`, which reads as a full
                  page reload when the user just clicks a rail link. */}
              <Suspense fallback={<SettingsLoading />}>
                <Outlet />
              </Suspense>
            </div>
          </div>
        </div>
      </div>
    </main>
  );
}

function RailLink({ item }: { item: RailItem }) {
  const { tr } = useI18n();
  const Icon = item.icon;
  const Badge = item.badgeIcon;
  const label = tr(item.labelZh, item.labelEn);
  const hint = tr(item.hintZh, item.hintEn);
  if (item.disabled) {
    return (
      <div
        aria-disabled
        title={hint}
        className="group relative flex shrink-0 cursor-not-allowed items-center gap-2 rounded-md px-3 py-2 text-sm text-zinc-600 lg:gap-3"
      >
        <Icon size={14} className="shrink-0 text-zinc-700" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium">{label}</div>
          <div className="hidden truncate text-[11px] text-zinc-600 lg:block">{hint}</div>
        </div>
      </div>
    );
  }
  return (
    <NavLink
      to={item.to}
      className={({ isActive }) =>
        cn(
          'group relative flex shrink-0 items-center gap-2 rounded-md px-3 py-2 text-sm transition-colors',
          'lg:gap-3',
          isActive
            ? 'bg-zinc-800 text-zinc-100'
            : 'text-zinc-400 hover:bg-zinc-800/60 hover:text-zinc-100'
        )
      }
    >
      {({ isActive }) => (
        <>
          {isActive && (
            <span
              aria-hidden
              className="absolute inset-y-1.5 left-0 hidden w-0.5 rounded-r bg-accent lg:block"
            />
          )}
          <Icon
            size={14}
            className={cn(
              'shrink-0',
              isActive ? 'text-accent' : 'text-zinc-500 group-hover:text-zinc-300'
            )}
          />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1">
              <span className="truncate text-[13px] font-medium">{label}</span>
              {Badge && (
                <Badge
                  size={10}
                  className="shrink-0 text-amber-400/80"
                  aria-label={item.badgeTitle ?? 'restricted'}
                />
              )}
            </div>
            <div className="hidden truncate text-[11px] text-zinc-500 lg:block">{hint}</div>
          </div>
        </>
      )}
    </NavLink>
  );
}

function SettingsLoading() {
  const { tr } = useI18n();
  return (
    <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
      <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
    </div>
  );
}
