import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Clock3,
  Copy,
  ExternalLink,
  Loader2,
  RefreshCw,
  ShieldCheck,
  TerminalSquare,
} from 'lucide-react';
import { ApiError } from '@/api/client';
import { checkSystemUpgrade, type UpgradeCommand, type UpgradeInfo } from '@/api/systemUpgrade';
import { Button, Card } from '@/components/ui';
import { cn } from '@/lib/cn';
import type { IconType } from '@/lib/icon';
import { useI18n } from '@/i18n/locale';

const MIN_MANUAL_REFRESH_MS = 650;

export default function SettingsUpgrade() {
  const { tr, locale } = useI18n();
  const [info, setInfo] = useState<UpgradeInfo | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  const run = useCallback(async (manual = false) => {
    setLoading(true);
    setErr(null);
    const started = Date.now();
    try {
      const next = await checkSystemUpgrade();
      setInfo(next);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      if (manual) {
        const elapsed = Date.now() - started;
        if (elapsed < MIN_MANUAL_REFRESH_MS) {
          await wait(MIN_MANUAL_REFRESH_MS - elapsed);
        }
      }
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void run();
  }, [run]);

  const checkedAt = info?.checked_at
    ? new Date(info.checked_at).toLocaleString(locale === 'zh-CN' ? 'zh-CN' : 'en-US')
    : tr('尚未检测', 'Not checked');
  const publishedAt = info?.published_at
    ? new Date(info.published_at).toLocaleDateString(locale === 'zh-CN' ? 'zh-CN' : 'en-US')
    : null;
  const status = getStatus(info);
  const StatusIcon = status.icon;

  const onCopy = async (command: UpgradeCommand) => {
    await copyText(command.command);
    setCopied(command.id);
    window.setTimeout(() => setCopied((current) => (current === command.id ? null : current)), 1600);
  };

  return (
    <div className="space-y-4" aria-busy={loading}>
      <Card className="p-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <StatusIcon size={15} className={status.iconClass} />
              <h2 className="text-sm font-medium text-zinc-100">{tr('版本升级', 'Version upgrade')}</h2>
              {info && (
                <span className={cn('rounded-full px-2 py-0.5 text-[11px] ring-1', status.badgeClass)}>
                  {tr(status.labelZh, status.labelEn)}
                </span>
              )}
            </div>
            <div className="mt-1 flex items-center gap-1.5 text-[11px] text-zinc-500">
              <Clock3 size={12} className="text-zinc-600" />
              <span>{checkedAt}</span>
            </div>
          </div>
          <Button
            variant="primary"
            onClick={() => void run(true)}
            disabled={loading}
            className="w-full justify-center bg-indigo-600 text-white hover:bg-indigo-500 sm:w-auto"
          >
            {loading ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}
            {loading ? tr('检测中', 'Checking') : tr('检测更新', 'Check updates')}
          </Button>
        </div>
        {err && (
          <div className="mt-3 rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-200">
            {err}
          </div>
        )}
      </Card>

      {loading && !info ? (
        <Card className="flex h-32 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={15} className="mr-2 animate-spin" /> {tr('正在检测最新版本…', 'Checking latest version…')}
        </Card>
      ) : info ? (
        <>
          <VersionSummary info={info} publishedAt={publishedAt} />
          <UpgradeState info={info} />
          <CommandList
            commands={info.commands}
            copied={copied}
            onCopy={(command) => void onCopy(command)}
          />
        </>
      ) : (
        <Card className="p-5 text-sm text-zinc-500">{tr('暂无检测结果', 'No check result yet')}</Card>
      )}
    </div>
  );
}

function VersionSummary({ info, publishedAt }: { info: UpgradeInfo; publishedAt: string | null }) {
  const { tr } = useI18n();
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      <Card compact>
        <div className="text-xs text-zinc-500">{tr('当前版本', 'Current version')}</div>
        <div className="mt-1 font-mono text-lg font-semibold text-zinc-100">
          {info.current_version || tr('开发构建', 'Development build')}
        </div>
      </Card>
      <Card compact>
        <div className="flex items-center justify-between gap-2">
          <div className="text-xs text-zinc-500">{tr('最新版本', 'Latest version')}</div>
          {info.release_url && (
            <a
              href={info.release_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:text-accent/80"
            >
              {tr('Release', 'Release')}
              <ExternalLink size={11} />
            </a>
          )}
        </div>
        <div className="mt-1 font-mono text-lg font-semibold text-zinc-100">{info.latest_version}</div>
        {publishedAt && <div className="mt-1 text-[11px] text-zinc-500">{publishedAt}</div>}
      </Card>
    </div>
  );
}

function UpgradeState({ info }: { info: UpgradeInfo }) {
  const { tr } = useI18n();
  if (!info.comparison_supported) {
    return (
      <StatePanel
        tone="warning"
        icon={AlertTriangle}
        title={tr('当前构建版本无法比较', 'Current build cannot be compared')}
        text={tr('开发构建或非标准版本号不会自动判断是否需要升级。', 'Development or non-standard versions cannot be compared automatically.')}
      />
    );
  }
  if (info.update_available) {
    return (
      <StatePanel
        tone="info"
        icon={TerminalSquare}
        title={tr('发现新版本', 'New version available')}
        text={tr('复制下面的升级命令，在目标服务器终端执行。', 'Copy an upgrade command below and run it on the target server terminal.')}
      />
    );
  }
  return (
    <StatePanel
      tone="success"
      icon={CheckCircle2}
      title={tr('当前已是最新版本', 'Already up to date')}
      text={tr('仍可复制命令用于重新安装当前版本。', 'You can still copy a command to reinstall the current version.')}
    />
  );
}

function StatePanel({
  tone,
  icon: Icon,
  title,
  text,
}: {
  tone: 'success' | 'warning' | 'info';
  icon: IconType;
  title: string;
  text: string;
}) {
  const toneClass = {
    success: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-200',
    warning: 'border-amber-500/25 bg-amber-500/10 text-amber-200',
    info: 'border-sky-500/30 bg-sky-500/10 text-sky-300',
  }[tone];
  return (
    <div className={cn('rounded-xl border px-4 py-3', toneClass)}>
      <div className="flex items-start gap-2">
        <Icon size={15} className="mt-0.5 shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-medium">{title}</div>
          <div className="mt-1 text-xs opacity-80">{text}</div>
        </div>
      </div>
    </div>
  );
}

function CommandList({
  commands,
  copied,
  onCopy,
}: {
  commands: UpgradeCommand[];
  copied: string | null;
  onCopy: (command: UpgradeCommand) => void;
}) {
  const { tr } = useI18n();
  const ordered = useMemo(() => {
    const auto = commands.find((item) => item.id === 'auto');
    const rest = commands.filter((item) => item.id !== 'auto');
    return auto ? [...rest, auto] : commands;
  }, [commands]);
  return (
    <section className="space-y-2">
      <div className="flex items-center gap-2 px-1 text-xs font-medium text-zinc-400">
        <TerminalSquare size={13} />
        <span>{tr('升级命令', 'Upgrade commands')}</span>
      </div>
      <Card compact className="divide-y divide-zinc-800/60 p-0">
        {ordered.map((item) => (
          <div key={item.id}>
            <div className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium text-zinc-100">{commandLabel(item, tr)}</span>
                </div>
                <div className="mt-1 font-mono text-[11px] text-zinc-500">{item.arch}</div>
              </div>
              <Button
                variant={copied === item.id ? 'subtle' : 'ghost'}
                onClick={() => onCopy(item)}
                className="w-full justify-center sm:w-auto"
              >
                <Copy size={12} />
                {copied === item.id ? tr('已复制', 'Copied') : tr('复制命令', 'Copy command')}
              </Button>
            </div>
            <pre className="whitespace-pre-wrap break-words border-t border-zinc-200 bg-zinc-50 px-3 py-2 text-[12px] leading-6 text-zinc-800 dark:border-zinc-800/60 dark:bg-zinc-950/60 dark:text-zinc-200">
              <code>{item.command}</code>
            </pre>
          </div>
        ))}
      </Card>
    </section>
  );
}

function getStatus(info: UpgradeInfo | null) {
  if (!info) {
    return {
      icon: ShieldCheck,
      iconClass: 'text-zinc-500',
      labelZh: '未检测',
      labelEn: 'Not checked',
      badgeClass: 'bg-zinc-500/10 text-zinc-300 ring-zinc-500/25',
    };
  }
  if (!info.comparison_supported) {
    return {
      icon: AlertTriangle,
      iconClass: 'text-amber-500',
      labelZh: '需人工确认',
      labelEn: 'Manual check',
      badgeClass: 'bg-amber-500/10 text-amber-300 ring-amber-500/25',
    };
  }
  if (info.update_available) {
    return {
      icon: TerminalSquare,
      iconClass: 'text-sky-500',
      labelZh: '可升级',
      labelEn: 'Update available',
      badgeClass: 'bg-sky-500/10 text-sky-300 ring-sky-500/25',
    };
  }
  return {
    icon: CheckCircle2,
    iconClass: 'text-emerald-500',
    labelZh: '最新',
    labelEn: 'Up to date',
    badgeClass: 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/25',
  };
}

function commandLabel(item: UpgradeCommand, tr: (zh: string, en: string) => string) {
  switch (item.id) {
    case 'auto':
      return tr('自动识别 Linux 架构', 'Auto-detect Linux arch');
    case 'linux-amd64':
      return tr('Linux amd64 服务器', 'Linux amd64 server');
    case 'linux-arm64':
      return tr('Linux arm64 服务器', 'Linux arm64 server');
    default:
      return item.label;
  }
}

async function copyText(text: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const el = document.createElement('textarea');
  el.value = text;
  el.setAttribute('readonly', 'true');
  el.style.position = 'fixed';
  el.style.opacity = '0';
  document.body.appendChild(el);
  el.select();
  document.execCommand('copy');
  document.body.removeChild(el);
}

function wait(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}
