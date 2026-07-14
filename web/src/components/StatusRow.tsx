import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Activity, AlertTriangle, MessageSquare, Coins, PowerOff } from 'lucide-react';
import { cn } from '@/lib/cn';
import type { IconType } from '@/lib/icon';
import { listDevices, type Device } from '@/api/devices';
import { listSessions, type ChatSession } from '@/api/chat';
import { request } from '@/api/client';
import { useIncidentBadge } from '@/store/incidentBadge';
import { useI18n } from '@/i18n/locale';

type Tone = 'ok' | 'warn' | 'muted';

type UsageToday = {
  total_tokens?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
};

type PillState = {
  icon: IconType;
  label: string;
  tone: Tone;
  onClick: () => void;
  key: string;
};

const REFRESH_MS = 30_000;

function isWithinThisWeek(iso: string | undefined | null): boolean {
  if (!iso) return false;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return false;
  const now = new Date();
  // start of week = monday 00:00 local
  const day = now.getDay(); // 0 sun .. 6 sat
  const diffToMonday = (day + 6) % 7;
  const monday = new Date(now);
  monday.setHours(0, 0, 0, 0);
  monday.setDate(now.getDate() - diffToMonday);
  return d.getTime() >= monday.getTime();
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

export function StatusRow() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [devicesError, setDevicesError] = useState(false);
  const [sessions, setSessions] = useState<ChatSession[] | null>(null);
  const [sessionsError, setSessionsError] = useState(false);
  const [usage, setUsage] = useState<UsageToday | null>(null);
  const [usageHidden, setUsageHidden] = useState(false);
  // Real unacked incident count — shared store, same source as the
  // Sidebar badge + Alerts page header. Previously this row was
  // mislabeling "offline edge 数量" as "告警", which is why Home and
  // Alerts disagreed.
  const incidentOpen = useIncidentBadge((s) => s.openCount);

  useEffect(() => {
    let cancelled = false;

    async function tick() {
      // Only registered devices count here. A newly created Edge is an
      // enrollment credential until its agent completes first registration.
      try {
        const r = await listDevices();
        if (!cancelled) {
          setDevices(r.items ?? []);
          setDevicesError(false);
        }
      } catch {
        if (!cancelled) {
          setDevicesError(true);
          setDevices(null);
        }
      }

      // sessions
      try {
        const r = await listSessions();
        if (!cancelled) {
          setSessions(r.items ?? []);
          setSessionsError(false);
        }
      } catch {
        if (!cancelled) {
          setSessionsError(true);
          setSessions(null);
        }
      }

      // usage today (optional)
      if (!usageHidden) {
        try {
          const r = await request<UsageToday>('GET', '/usage/today');
          if (!cancelled) setUsage(r ?? null);
        } catch {
          if (!cancelled) {
            setUsage(null);
            setUsageHidden(true);
          }
        }
      }
    }

    void tick();
    const id = window.setInterval(() => {
      void tick();
    }, REFRESH_MS);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
    // usageHidden referenced inside tick; stable enough — we don't want to re-arm interval on it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const pills: PillState[] = [];

  if (devices && !devicesError) {
    const total = devices.length;
    const online = devices.filter((device) => device.online === true).length;
    const offline = total - online;
    if (total > 0) {
      pills.push({
        key: 'online',
        icon: Activity,
        label: tr(`${online}/${total} 在线设备`, `${online}/${total} online`),
        tone: online === total ? 'ok' : online === 0 ? 'warn' : 'ok',
        onClick: () => navigate('/edges'),
      });
      if (offline > 0) {
        pills.push({
          key: 'offline',
          icon: PowerOff,
          label: tr(`${offline} 离线`, `${offline} offline`),
          tone: 'warn',
          onClick: () => navigate('/edges'),
        });
      }
    }
  }

  if (incidentOpen > 0) {
    pills.push({
      key: 'incidents',
      icon: AlertTriangle,
      label: tr(`${incidentOpen} 未确认告警`, `${incidentOpen} open alert${incidentOpen === 1 ? '' : 's'}`),
      tone: 'warn',
      onClick: () => navigate('/alerts'),
    });
  }

  if (sessions && !sessionsError) {
    const weekly = sessions.filter((s) => isWithinThisWeek(s.created_at)).length;
    if (weekly > 0) {
      pills.push({
        key: 'sessions',
        icon: MessageSquare,
        label: tr(`本周 ${weekly} 会话`, `${weekly} session${weekly === 1 ? '' : 's'} this week`),
        tone: 'muted',
        onClick: () => {
          /* current page already lists recents */
        },
      });
    }
  }

  if (usage && !usageHidden) {
    const t = usage.total_tokens ?? 0;
    if (t > 0) {
      pills.push({
        key: 'tokens',
        icon: Coins,
        label: tr(`今日 ${formatTokens(t)} token`, `${formatTokens(t)} tokens today`),
        tone: 'muted',
        onClick: () => {
          /* no destination yet */
        },
      });
    }
  }

  // First-render shimmer: nothing fetched yet at all.
  const initialLoading = devices === null && sessions === null;

  if (initialLoading) {
    return (
      <div className="flex h-9 w-full items-center gap-2" aria-hidden>
        <div className="h-6 w-20 animate-pulse rounded-full bg-zinc-900/60" />
        <div className="h-6 w-24 animate-pulse rounded-full bg-zinc-900/60" />
      </div>
    );
  }

  if (pills.length === 0) {
    // Nothing meaningful to show — render an empty 36px row to preserve rhythm.
    return <div className="h-9" aria-hidden />;
  }

  return (
    <div className="flex h-9 w-full flex-wrap items-center gap-2">
      {pills.map((p) => (
        <Pill key={p.key} icon={p.icon} label={p.label} tone={p.tone} onClick={p.onClick} />
      ))}
    </div>
  );
}

function Pill({
  icon: Icon,
  label,
  tone,
  onClick,
}: {
  icon: IconType;
  label: string;
  tone: Tone;
  onClick: () => void;
}) {
  const isOk = tone === 'ok';
  const isWarn = tone === 'warn';
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs transition-colors',
        isOk && 'border-zinc-800 bg-zinc-900/80 text-zinc-300 hover:border-zinc-700',
        isWarn &&
          'border-amber-500/30 bg-amber-500/10 text-amber-300 hover:border-amber-500/50',
        !isOk && !isWarn &&
          'border-zinc-800/80 bg-zinc-900/40 text-zinc-500 hover:border-zinc-800 hover:text-zinc-400'
      )}
    >
      {isOk && <span className="h-1.5 w-1.5 rounded-full bg-emerald-400" />}
      {isWarn && <span className="h-1.5 w-1.5 rounded-full bg-amber-400" />}
      <Icon size={12} className="opacity-80" />
      <span>{label}</span>
    </button>
  );
}
