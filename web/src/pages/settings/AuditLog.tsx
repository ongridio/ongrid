import { FilterField } from '@/components/ui/FilterField';
import { Input } from '@/components/ui';
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/Dialog';
import { Select } from '@/components/ui/Select';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Filter, Loader2, RefreshCw, Shield, X } from 'lucide-react';
import { ApiError } from '@/api/client';
import { listAuditLogs, type AuditLog, type AuditListFilters } from '@/api/audit';
import { Button, Card, Chip, EmptyState, PageHeader, PaginationFooter } from '@/components/ui';
import { useMe } from '@/store/me';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

const PAGE_SIZE = 50;

// /admin/audit — HLD-010 audit trail viewer. Admin-only. Read-only
// (audit_logs is append-only by design — even the retention sweep
// only deletes; there is no edit path).
export default function SettingsAuditLog() {
  const { tr } = useI18n();
  const { me, loading: meLoading } = useMe();
  const isAdmin = me?.role === 'admin';

  const [items, setItems] = useState<AuditLog[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string>('');
  const [filter, setFilter] = useState<AuditListFilters>({ limit: PAGE_SIZE, offset: 0 });
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<AuditLog | null>(null);

  const load = useCallback(async () => {
    if (!isAdmin) return;
    setLoading(true);
    setErr('');
    try {
      const r = await listAuditLogs({ ...filter, limit: PAGE_SIZE, offset: page * PAGE_SIZE });
      setItems(r.items);
      setTotal(r.total);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [filter, isAdmin, page]);

  useEffect(() => {
    void load();
  }, [load]);

  const actionOptions = useMemo(() => {
    const s = new Set<string>(items.map((i) => i.action));
    return Array.from(s).sort();
  }, [items]);

  const resourceOptions = useMemo(() => {
    const s = new Set<string>(items.map((i) => i.resource_type));
    return Array.from(s).sort();
  }, [items]);

  const actionLabel = useCallback(
    (a: string) => formatAuditAction(a, tr),
    [tr],
  );
  const resourceLabel = useCallback(
    (r: string) => formatAuditResource(r, tr),
    [tr],
  );

  if (meLoading) {
    return (
      <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
        <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
      </div>
    );
  }
  if (!isAdmin) {
    return (
      <main className="anim-fade flex flex-1 flex-col overflow-hidden p-6">
        <Card className="p-6">
          <EmptyState
            icon={Shield}
            title={tr('需要管理员权限', 'Admin permission required')}
            hint={tr('只有管理员可以查看审计日志。', 'Only admins can view the audit log.')}
          />
        </Card>
      </main>
    );
  }

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden p-6">
      <PageHeader
        title={tr('审计日志', 'Audit log')}
        subtitle={tr(
          `共 ${total} 条记录；保留 180 天`,
          `${total} entries; 180-day retention`,
        )}
        actions={
          <Button onClick={load} variant="ghost">
            <RefreshCw size={14} className={cn('mr-1', loading && 'animate-spin')} />
            {tr('刷新', 'Refresh')}
          </Button>
        }
      />

      <Card className="mt-4 p-3">
        <div className="flex flex-wrap items-center gap-2">
          <Filter size={14} className="shrink-0 text-zinc-500" />
          <FilterField label={tr('用户邮箱', 'User email')} className="w-64">
            <Input
              type="text"
              value={filter.user_email ?? ''}
              onChange={(e) => {
                setPage(0);
                setFilter((f) => ({ ...f, user_email: e.target.value || undefined }));
              }}
              placeholder={tr('用户邮箱', 'User email')}
              aria-label={tr('用户邮箱', 'User email')}
            />
          </FilterField>
          <FilterField label={tr('动作', 'Action')} className="w-56">
            <Select label={tr('动作', 'Action')}
              value={filter.action ?? ''}
              onValueChange={(selectedValue) => {
                setPage(0);
                setFilter((f) => ({ ...f, action: selectedValue || undefined }));
              }}

            >
              <option value="">{tr('全部 action', 'All actions')}</option>
              {actionOptions.map((a) => (
                <option key={a} value={a}>{actionLabel(a)}</option>
              ))}
            </Select>
          </FilterField>
          <FilterField label={tr('资源类型', 'Resource type')} className="w-60">
            <Select label={tr('资源类型', 'Resource type')}
              value={filter.resource_type ?? ''}
              onValueChange={(selectedValue) => {
                setPage(0);
                setFilter((f) => ({ ...f, resource_type: selectedValue || undefined }));
              }}

            >
              <option value="">{tr('全部资源', 'All resources')}</option>
              {resourceOptions.map((a) => (
                <option key={a} value={a}>{resourceLabel(a)}</option>
              ))}
            </Select>
          </FilterField>
          <FilterField label={tr('结果', 'Status')} className="w-52">
            <Select label={tr('结果', 'Status')}
              value={filter.status ?? ''}
              onValueChange={(selectedValue) => {
                setPage(0);
                setFilter((f) => ({ ...f, status: (selectedValue as AuditListFilters['status']) || undefined }));
              }}

            >
              <option value="">{tr('全部结果', 'All status')}</option>
              <option value="success">success</option>
              <option value="failure">failure</option>
              <option value="denied">denied</option>
            </Select>
          </FilterField>
          {(filter.user_email || filter.action || filter.resource_type || filter.status) && (
            <Button variant="ghost" onClick={() => {
              setPage(0);
              setFilter({ limit: PAGE_SIZE, offset: 0 });
            }}>
              <X size={14} className="mr-1" />
              {tr('清空筛选', 'Clear filters')}
            </Button>
          )}
        </div>
      </Card>

      {err && (
        <Card className="mt-3 border-red-700/40 bg-red-900/20 p-3 text-sm text-red-300">{err}</Card>
      )}

      <Card className="mt-3 flex-1 overflow-auto">
        <table className="min-w-full text-left text-[13px]">
          <thead className="sticky top-0 z-10 bg-zinc-900 text-zinc-400">
            <tr>
              <th className="px-3 py-2">{tr('时间', 'Time')}</th>
              <th className="px-3 py-2">{tr('用户', 'Actor')}</th>
              <th className="px-3 py-2">action</th>
              <th className="px-3 py-2">{tr('资源', 'Resource')}</th>
              <th className="px-3 py-2">{tr('结果', 'Status')}</th>
              <th className="px-3 py-2">IP</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {items.map((row) => (
              <tr key={row.id} className="border-t border-zinc-800 hover:bg-zinc-900/40">
                <td className="px-3 py-2 font-mono text-xs text-zinc-300">{fmtTime(row.occurred_at)}</td>
                <td className="px-3 py-2">
                  <div className="text-zinc-200">{row.user_email || '—'}</div>
                  {row.role && <div className="text-[11px] text-zinc-500">{row.role}</div>}
                </td>
                <td className="px-3 py-2">
                  <div className="text-zinc-200">{actionLabel(row.action)}</div>
                  <div className="font-mono text-[11px] text-zinc-500">{row.action}</div>
                </td>
                <td className="px-3 py-2">
                  <div className="text-zinc-300">{resourceLabel(row.resource_type)}</div>
                  <div className="text-[11px] text-zinc-500">
                    {row.resource_name || row.resource_id || '—'}
                  </div>
                </td>
                <td className="px-3 py-2">
                  <StatusChip status={row.status} />
                </td>
                <td className="px-3 py-2 font-mono text-xs text-zinc-400">{row.ip || '—'}</td>
                <td className="px-3 py-2 text-right">
                  <Button variant="ghost" onClick={() => setSelected(row)}>
                    {tr('详情', 'Detail')}
                  </Button>
                </td>
              </tr>
            ))}
            {!loading && items.length === 0 && (
              <tr>
                <td colSpan={7} className="px-3 py-8 text-center text-zinc-500">
                  {tr('没有匹配的审计记录', 'No matching audit entries')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>
      <PaginationFooter
        page={page}
        pageSize={PAGE_SIZE}
        shown={items.length}
        total={total}
        loading={loading}
        matchLabel={Boolean(filter.user_email || filter.action || filter.resource_type || filter.status)}
        onPageChange={setPage}
      />

      {selected && <DetailDrawer row={selected} onClose={() => setSelected(null)} />}
    </main>
  );
}

function StatusChip({ status }: { status: AuditLog['status'] }) {
  const cls =
    status === 'success'
      ? 'bg-green-900/30 text-green-300'
      : status === 'denied'
        ? 'bg-yellow-900/30 text-yellow-300'
        : 'bg-red-900/30 text-red-300';
  return <Chip className={cls}>{status}</Chip>;
}

function fmtTime(s: string): string {
  try {
    return new Date(s).toLocaleString();
  } catch {
    return s;
  }
}

function DetailDrawer({ row, onClose }: { row: AuditLog; onClose: () => void }) {
  const { tr } = useI18n();
  let payload: string = row.payload_json ?? '';
  try {
    if (payload) payload = JSON.stringify(JSON.parse(payload), null, 2);
  } catch {
    /* keep raw */
  }
  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent placement="right" className="max-w-lg overflow-y-auto p-4">
        <div className="mb-3 flex items-center justify-between">
          <DialogTitle className="text-base font-semibold">{tr('审计详情', 'Audit detail')}</DialogTitle>
          <Button variant="ghost" onClick={onClose} aria-label={tr('关闭', 'Close')}>
            <X size={14} />
          </Button>
        </div>
        <dl className="space-y-2 text-sm">
          <Row label={tr('时间', 'Time')} value={fmtTime(row.occurred_at)} />
          <Row label={tr('动作', 'Action')} value={`${formatAuditAction(row.action, tr)} (${row.action})`} />
          <Row label={tr('资源', 'Resource')} value={`${formatAuditResource(row.resource_type, tr)} / ${row.resource_name || row.resource_id || '—'}`} />
          <Row label={tr('用户', 'Actor')} value={`${row.user_email || '—'} (${row.role || 'n/a'})`} />
          <Row label="IP" value={row.ip || '—'} mono />
          <Row label="user-agent" value={row.user_agent || '—'} mono />
          <Row label={tr('结果', 'Status')} value={row.status} />
          {row.error_message && <Row label="error" value={row.error_message} mono />}
          {row.request_id && <Row label="request_id" value={row.request_id} mono />}
        </dl>
        {payload && (
          <div className="mt-4">
            <div className="mb-1 text-xs uppercase text-zinc-500">payload</div>
            <pre className="overflow-x-auto rounded border border-zinc-800 bg-zinc-900 p-2 text-xs text-zinc-200">
              {payload}
            </pre>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[120px_1fr] gap-2">
      <dt className="text-zinc-500">{label}</dt>
      <dd className={cn('text-zinc-200', mono && 'font-mono break-all text-xs')}>{value}</dd>
    </div>
  );
}

// Human-readable label for a canonical audit action string. Falls back
// to the raw key when an unknown action appears (e.g. data from a
// not-yet-deployed manager version, or legacy http_* rows the operator
// hasn't purged yet). Keep this in sync with internal/manager/model/
// audit/log.go.
type Tr = (zh: string, en: string) => string;

const ACTION_LABELS: Record<string, [string, string]> = {
  auth_login_failed: ['登录失败', 'Sign-in failed'],
  user_create: ['新建用户', 'Create user'],
  user_update: ['修改用户', 'Update user'],
  user_delete: ['删除用户', 'Delete user'],
  user_export: ['导出用户', 'Export users'],
  device_update: ['修改设备', 'Update device'],
  device_delete: ['删除设备', 'Delete device'],
  rule_create: ['新建告警规则', 'Create alert rule'],
  rule_update: ['修改告警规则', 'Update alert rule'],
  rule_delete: ['删除告警规则', 'Delete alert rule'],
  incident_ack: ['确认事件', 'Ack incident'],
  incident_resolve: ['关闭事件', 'Resolve incident'],
  incident_silence: ['静默事件', 'Silence incident'],
  setting_update: ['修改设置', 'Update setting'],
  setting_delete: ['删除设置', 'Delete setting'],
  channel_create: ['新建通道', 'Create channel'],
  channel_update: ['修改通道', 'Update channel'],
  channel_delete: ['删除通道', 'Delete channel'],
  repo_create: ['新建代码仓库', 'Add repo'],
  repo_delete: ['删除代码仓库', 'Delete repo'],
  repo_sync: ['同步代码仓库', 'Sync repo'],
  skill_install: ['安装技能', 'Install skill'],
  skill_uninstall: ['卸载技能', 'Uninstall skill'],
};

const RESOURCE_LABELS: Record<string, [string, string]> = {
  user: ['用户', 'User'],
  device: ['设备', 'Device'],
  incident: ['告警事件', 'Incident'],
  rule: ['告警规则', 'Alert rule'],
  channel: ['通知通道', 'Channel'],
  setting: ['设置', 'Setting'],
  repo: ['代码仓库', 'Repo'],
  skill: ['技能', 'Skill'],
  llm: ['LLM', 'LLM'],
  git_ssh_key: ['Git SSH 密钥', 'Git SSH key'],
  grafana: ['Grafana', 'Grafana'],
  rag: ['知识库', 'RAG'],
  audit: ['审计', 'Audit'],
  auth: ['认证', 'Auth'],
};

export function formatAuditAction(action: string, tr: Tr): string {
  const pair = ACTION_LABELS[action];
  if (pair) return tr(pair[0], pair[1]);
  return action;
}

export function formatAuditResource(resource: string, tr: Tr): string {
  const pair = RESOURCE_LABELS[resource];
  if (pair) return tr(pair[0], pair[1]);
  return resource;
}
