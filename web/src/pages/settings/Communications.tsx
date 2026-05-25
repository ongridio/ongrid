import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Bell,
  Loader2,
  MessageCircle,
  MessageSquareShare,
  Send,
  Webhook,
  Slack,
  Plus,
  Trash2,
} from 'lucide-react';
import { Modal } from '@/components/Modal';
import { Button, Card, Chip } from '@/components/ui';
import { cn } from '@/lib/cn';
import {
  createChannel,
  deleteChannel,
  listChannels,
  testChannel,
  updateChannel,
  type Channel,
  type ChannelInput,
  type ChannelTestResult,
} from '@/api/alerts';
import { ApiError } from '@/api/client';
import type { IconType } from '@/lib/icon';
import { useI18n } from '@/i18n/locale';

// One IM type ↔ one card. Order = display order on the page.
//
// The legacy "log" channel type was removed in 2026-05: manager
// stdout is ephemeral and the alert_events table already records
// every notification attempt with status — that's the audit trail.
// Operators looking for delivery history should hit
// 设置 → 告警事件 instead.
type ChannelType = 'webhook' | 'slack' | 'feishu' | 'dingtalk' | 'wecom' | 'telegram';

type TypeMeta = {
  type: ChannelType;
  group: 'cn' | 'intl' | 'generic';
  labelZh: string;
  labelEn: string;
  icon: IconType;
  hintZh: string;
  hintEn: string;
  endpointPlaceholder: string;
};

const TYPE_CARDS: TypeMeta[] = [
  {
    type: 'feishu',
    group: 'cn',
    labelZh: '飞书',
    labelEn: 'Feishu',
    icon: MessageSquareShare,
    hintZh: '飞书自定义机器人 webhook，支持签名校验。',
    hintEn: 'Feishu custom-bot webhook; supports signature verification.',
    endpointPlaceholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxx',
  },
  {
    type: 'dingtalk',
    group: 'cn',
    labelZh: '钉钉',
    labelEn: 'DingTalk',
    icon: Send,
    hintZh: '钉钉自定义机器人 webhook，支持加签 secret。',
    hintEn: 'DingTalk custom-bot webhook; supports signature secret.',
    endpointPlaceholder: 'https://oapi.dingtalk.com/robot/send?access_token=xxxx',
  },
  {
    type: 'wecom',
    group: 'cn',
    labelZh: '企业微信',
    labelEn: 'WeCom',
    icon: MessageCircle,
    hintZh: '企业微信群机器人 webhook（key 在 URL 里）。',
    hintEn: 'WeCom group-bot webhook (key embedded in the URL).',
    endpointPlaceholder: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxx',
  },
  {
    type: 'slack',
    group: 'intl',
    labelZh: 'Slack',
    labelEn: 'Slack',
    icon: Slack,
    hintZh: 'Slack incoming webhook（用于工作区频道）。',
    hintEn: 'Slack incoming webhook (for workspace channels).',
    endpointPlaceholder: 'https://hooks.slack.com/services/Txxx/Bxxx/xxx',
  },
  {
    type: 'telegram',
    group: 'intl',
    labelZh: 'Telegram',
    labelEn: 'Telegram',
    icon: Send,
    hintZh: 'Telegram bot sendMessage 接口；endpoint 填 https://api.telegram.org/bot<token>/sendMessage，secret 字段填 Chat ID。',
    hintEn: 'Telegram bot sendMessage API; endpoint = https://api.telegram.org/bot<token>/sendMessage, put the Chat ID in the secret field.',
    endpointPlaceholder: 'https://api.telegram.org/bot<token>/sendMessage',
  },
  {
    type: 'webhook',
    group: 'generic',
    labelZh: 'Webhook',
    labelEn: 'Webhook',
    icon: Webhook,
    hintZh: '通用 JSON webhook；secret 设置后会附 X-Ongrid-Signature。',
    hintEn: 'Generic JSON webhook; if a secret is set the request carries X-Ongrid-Signature.',
    endpointPlaceholder: 'https://example.com/ingest',
  },
];

// Locale-aware ordering: surface the channels most relevant to the UI
// language first. English → Slack / Telegram first; Chinese → 飞书 / 钉钉 /
// 企业微信 first. Webhook (generic) stays last either way. Within a group the
// declaration order above is preserved (Array.prototype.sort is stable).
const GROUP_RANK: Record<string, Record<TypeMeta['group'], number>> = {
  'en-US': { intl: 0, cn: 1, generic: 2 },
  'zh-CN': { cn: 0, intl: 1, generic: 2 },
};
function orderCardsByLocale(locale: string): TypeMeta[] {
  const rank = GROUP_RANK[locale] ?? GROUP_RANK['zh-CN'];
  return [...TYPE_CARDS].sort((a, b) => rank[a.group] - rank[b.group]);
}

type Toast = { kind: 'ok' | 'err'; text: string } | null;

export default function SettingsCommunications() {
  const { tr, locale } = useI18n();
  const orderedCards = useMemo(() => orderCardsByLocale(locale), [locale]);
  const [items, setItems] = useState<Channel[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<{
    mode: 'create' | 'edit';
    type?: ChannelType;
    channel?: Channel;
  } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Channel | null>(null);
  const [testingId, setTestingId] = useState<number | null>(null);
  const [testResult, setTestResult] = useState<{ id: number; result: ChannelTestResult } | null>(null);
  const [toast, setToast] = useState<Toast>(null);

  const fetchChannels = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const r = await listChannels();
      setItems(r.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchChannels();
  }, [fetchChannels]);

  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(null), 4000);
    return () => clearTimeout(t);
  }, [toast]);

  const handleTest = useCallback(async (channel: Channel) => {
    setTestingId(channel.id);
    setTestResult(null);
    try {
      const r = await testChannel(channel.id);
      setTestResult({ id: channel.id, result: r });
      if (r.accepted) {
        setToast({ kind: 'ok', text: tr(`已向 ${channel.name} 投递测试消息`, `Test message sent to ${channel.name}`) });
      } else {
        setToast({ kind: 'err', text: tr(`投递失败：${r.message ?? '未知错误'}`, `Delivery failed: ${r.message ?? 'unknown error'}`) });
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      setToast({ kind: 'err', text: tr(`投递失败：${msg}`, `Delivery failed: ${msg}`) });
    } finally {
      setTestingId(null);
    }
  }, []);

  const grouped = useMemo(() => {
    const m = new Map<string, Channel[]>();
    for (const c of items) {
      const list = m.get(c.type) ?? [];
      list.push(c);
      m.set(c.type, list);
    }
    return m;
  }, [items]);

  return (
    <>
      {/* Layout mirrors Settings → 模型 (LLM) and Settings → 集成:
          a thin intro panel at the top explaining what this page is,
          then one Card per channel-type with p-5 / 14-px icon / 11-px
          description / bottom action row. Each channel-type is
          treated like its own integration; the channel list slots in
          between description and action row. */}
      <div className="space-y-4">
        <div className="rounded-lg border border-zinc-800/60 bg-zinc-900/30 px-4 py-3 text-[12px] text-zinc-400">
          <div className="mb-1 flex items-center gap-2 text-zinc-200">
            <Bell size={14} className="text-zinc-400" />
            <span className="font-medium">{tr('通知渠道', 'Notification channels')}</span>
          </div>
          {tr('告警 / 巡检 / 主动 AIOps 的单向外部落点。env 变量 ', 'One-way external sinks for alerts / inspections / proactive AIOps. The env var ')}
          <code className="font-mono text-zinc-300">ONGRID_NOTIFY_*</code>
          {tr(' 启动时同步入库；UI 改动持久化到 DB，下次启动 env 不会覆盖。投递记录（成功 / 失败 / 重试）请查 ', ' is synced into the DB at startup; UI edits are persisted and not overwritten by env on next boot. Delivery records (success / failure / retry) live under ')}
          <b>{tr('设置 → 告警事件', 'Settings → Alert events')}</b>
          {tr('。需要双向 bot 会话请看 ', '. For two-way bot sessions, see ')}
          <b>{tr('设置 → IM 机器人', 'Settings → IM bots')}</b>{tr('。', '.')}
        </div>

        {err && (
          <Card className="p-4">
            <div className="text-sm text-red-300">
              {tr('加载失败：', 'Load failed: ')}{err}
            </div>
          </Card>
        )}

        {loading ? (
          <Card className="p-5">
            <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
              <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
            </div>
          </Card>
        ) : (
          orderedCards.map((meta) => (
            <TypeCard
              key={meta.type}
              meta={meta}
              channels={grouped.get(meta.type) ?? []}
              testingId={testingId}
              testResult={testResult}
              onAdd={() => setEditing({ mode: 'create', type: meta.type })}
              onTest={(ch) => handleTest(ch)}
              onEdit={(ch) => setEditing({ mode: 'edit', type: ch.type as ChannelType, channel: ch })}
              onDelete={(ch) => setConfirmDelete(ch)}
            />
          ))
        )}
      </div>

      {editing && (
        <ChannelEditorModal
          mode={editing.mode}
          presetType={editing.type}
          channel={editing.channel}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            fetchChannels(true);
            setToast({ kind: 'ok', text: editing.mode === 'create' ? tr('渠道已创建', 'Channel created') : tr('渠道已更新', 'Channel updated') });
          }}
        />
      )}

      {confirmDelete && (
        <Modal
          open
          onClose={() => setConfirmDelete(null)}
          title={tr('删除通知渠道', 'Delete notification channel')}
          footer={
            <>
              <Button onClick={() => setConfirmDelete(null)} variant="ghost">
                {tr('取消', 'Cancel')}
              </Button>
              <Button
                variant="danger"
                onClick={async () => {
                  try {
                    await deleteChannel(confirmDelete.id);
                    setConfirmDelete(null);
                    fetchChannels(true);
                    setToast({ kind: 'ok', text: tr(`已删除 ${confirmDelete.name}`, `Deleted ${confirmDelete.name}`) });
                  } catch (e) {
                    const msg = e instanceof ApiError ? e.message : (e as Error).message;
                    setToast({ kind: 'err', text: tr(`删除失败：${msg}`, `Delete failed: ${msg}`) });
                  }
                }}
              >
                {tr('删除', 'Delete')}
              </Button>
            </>
          }
        >
          <div className="text-sm text-zinc-300">
            {tr('确定要删除渠道 ', 'Delete channel ')}
            <span className="font-mono text-zinc-100">{confirmDelete.name}</span>
            {tr(' 吗？删除后将不再投递新告警，已投递历史记录保留。', '? It will stop receiving new alerts; existing delivery history is retained.')}
          </div>
        </Modal>
      )}

      {toast && (
        <div
          className={cn(
            'fixed bottom-6 right-6 z-50 max-w-sm rounded-lg px-4 py-2.5 text-sm shadow-2xl ring-1 ring-inset',
            toast.kind === 'ok'
              ? 'bg-emerald-500/15 text-emerald-200 ring-emerald-500/40'
              : 'bg-red-500/15 text-red-200 ring-red-500/40'
          )}
        >
          {toast.text}
        </div>
      )}
    </>
  );
}

function TypeCard({
  meta,
  channels,
  testingId,
  testResult,
  onAdd,
  onTest,
  onEdit,
  onDelete,
}: {
  meta: TypeMeta;
  channels: Channel[];
  testingId: number | null;
  testResult: { id: number; result: ChannelTestResult } | null;
  onAdd(): void;
  onTest(ch: Channel): void;
  onEdit(ch: Channel): void;
  onDelete(ch: Channel): void;
}) {
  const { tr } = useI18n();
  const Icon = meta.icon;
  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center gap-2">
        <Icon size={14} className="text-zinc-400" />
        <h2 className="text-sm font-medium text-zinc-100">{tr(meta.labelZh, meta.labelEn)}</h2>
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">{tr(meta.hintZh, meta.hintEn)}</p>

      {channels.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-800 bg-zinc-950/30 px-4 py-6 text-center text-[11px] text-zinc-500">
          {tr(`还没有 ${meta.labelZh} 渠道。`, `No ${meta.labelEn} channels yet.`)}
        </div>
      ) : (
        <ul className="divide-y divide-zinc-800/60 overflow-hidden rounded-lg border border-zinc-800 bg-zinc-950/40">
          {channels.map((ch) => (
            <li key={ch.id} className={cn('px-4 py-2.5', !ch.enabled && 'opacity-60')}>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium text-zinc-100">{ch.name}</span>
                    <Chip tone={ch.enabled ? 'success' : 'default'}>
                      {ch.enabled ? 'enabled' : 'disabled'}
                    </Chip>
                  </div>
                  {ch.endpoint && (
                    <div className="mt-1 truncate font-mono text-[11px] text-zinc-500">
                      {ch.endpoint}
                    </div>
                  )}
                  {testResult?.id === ch.id && !testResult.result.accepted && testResult.result.message && (
                    <div className="mt-1.5 rounded border border-red-500/40 bg-red-500/5 px-2 py-1 text-[11px] text-red-300">
                      {tr('投递失败：', 'Delivery failed: ')}{testResult.result.message}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 gap-1.5">
                  <Button
                    onClick={() => onTest(ch)}
                    disabled={testingId === ch.id}
                    variant="ghost"
                  >
                    {testingId === ch.id ? tr('投递中…', 'Sending…') : tr('测试', 'Test')}
                  </Button>
                  <Button onClick={() => onEdit(ch)} variant="ghost">
                    {tr('编辑', 'Edit')}
                  </Button>
                  <Button
                    onClick={() => onDelete(ch)}
                    aria-label={tr('删除', 'Delete')}
                    variant="danger"
                  >
                    <Trash2 size={11} />
                  </Button>
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}

      <div className="mt-5 flex flex-wrap items-center gap-3">
        <Button onClick={onAdd} variant="subtle">
          <Plus size={14} />
          <span>{tr('新建', 'New')}</span>
        </Button>
        <span className="text-xs text-zinc-500">
          {channels.length === 0
            ? tr('未配置', 'Not configured')
            : tr(`已配置 ${channels.length} 个渠道`, `${channels.length} channel(s) configured`)}
        </span>
      </div>
    </Card>
  );
}

function ChannelEditorModal({
  mode,
  presetType,
  channel,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  presetType?: ChannelType;
  channel?: Channel;
  onClose(): void;
  onSaved(): void;
}) {
  const { tr, locale } = useI18n();
  const [form, setForm] = useState<ChannelInput>(() => ({
    name: channel?.name ?? '',
    type: channel?.type ?? presetType ?? 'webhook',
    endpoint: channel?.endpoint ?? '',
    secret: '',
    enabled: channel?.enabled ?? true,
  }));
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const endpointTruncated = useMemo(
    () => mode === 'edit' && (channel?.endpoint?.endsWith('...') ?? false),
    [mode, channel?.endpoint]
  );

  const typeLabel = (() => {
    const m = TYPE_CARDS.find((t) => t.type === form.type);
    return m ? tr(m.labelZh, m.labelEn) : form.type;
  })();

  const submit = async () => {
    if (!form.name.trim()) {
      setErr(tr('请填写渠道名称', 'Please enter a channel name'));
      return;
    }
    if (!form.type.trim()) {
      setErr(tr('请选择渠道类型', 'Please pick a channel type'));
      return;
    }
    if (!form.endpoint.trim()) {
      setErr(tr('请填写 endpoint URL', 'Please enter an endpoint URL'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      if (mode === 'create') await createChannel(form);
      else if (channel) await updateChannel(channel.id, form);
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      title={mode === 'create' ? tr(`新建 ${typeLabel} 渠道`, `New ${typeLabel} channel`) : tr(`编辑：${channel?.name}`, `Edit: ${channel?.name}`)}
      footer={
        <>
          <Button onClick={onClose} variant="ghost">
            {tr('取消', 'Cancel')}
          </Button>
          <Button onClick={submit} disabled={submitting} variant="subtle">
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-sm">
        <Field label={tr('名称', 'Name')}>
          <input
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            placeholder={`primary-${form.type}`}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </Field>
        <Field label={tr('类型', 'Type')}>
          <select
            value={form.type}
            onChange={(e) => setForm({ ...form, type: e.target.value })}
            disabled={mode === 'edit'}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none disabled:opacity-60"
          >
            {orderCardsByLocale(locale).map((t) => (
              <option key={t.type} value={t.type}>
                {tr(t.labelZh, t.labelEn)} ({t.type})
              </option>
            ))}
          </select>
        </Field>
        <Field label="Endpoint URL">
          <input
            value={form.endpoint}
            onChange={(e) => setForm({ ...form, endpoint: e.target.value })}
            placeholder={
              TYPE_CARDS.find((t) => t.type === form.type)?.endpointPlaceholder ?? ''
            }
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
          {endpointTruncated && (
            <div className="mt-1 text-[11px] text-amber-400">
              {tr(
                '当前显示的 endpoint 是脱敏值（结尾有省略号）。如果不修改，请清空后重新粘贴完整地址再保存。',
                'The endpoint shown is masked (ends with ellipsis). To keep it as-is, leave the field; to change it, clear and paste the full URL before saving.',
              )}
            </div>
          )}
        </Field>
        <Field label={tr('Secret（可选，签名/验签用）', 'Secret (optional, for signing / verification)')}>
          <input
            type="password"
            value={form.secret ?? ''}
            onChange={(e) => setForm({ ...form, secret: e.target.value })}
            placeholder={
              mode === 'edit'
                ? tr('留空保留旧值；输入 - 表示清除', 'Leave empty to keep the existing value; enter - to clear')
                : tr('可选：签名密钥 / 验签 token', 'Optional: signing key / verification token')
            }
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </Field>
        <label className="inline-flex items-center gap-2 text-xs text-zinc-300">
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
            className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
          />
          {tr('启用此渠道', 'Enable this channel')}
        </label>
        {err && <div className="text-xs text-red-400">{err}</div>}
      </div>
    </Modal>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-zinc-500">{label}</span>
      {children}
    </label>
  );
}
