import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { Plus, RotateCw, Trash2, MoreVertical, Copy, Check, ExternalLink, TerminalSquare } from 'lucide-react';
import { StatusPill } from '@/components/StatusPill';
import { Modal } from '@/components/Modal';
import { cn } from '@/lib/cn';
import { openMetricDrilldown } from '@/lib/drilldown';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import {
  listEdges,
  createEdge,
  deleteEdge,
  rotateSecret,
  setEdgeRoles,
  EDGE_ROLES,
  EDGE_ROLE_LABELS,
  EDGE_ROLE_LABELS_EN,
  type Edge,
  type EdgeRole,
  type CreateEdgeResponse,
  type RotateSecretResponse,
  upgradeEdgeAgent,
  upgradeEdgePackage,
} from '@/api/edges';
import { getManagerVersion } from '@/api/version';
import { usePermissions } from '@/store/me';
import { notifyDevicesChanged } from '@/lib/events';
import { useI18n } from '@/i18n/locale';

// Sidebar headers that map to ?roles= filters. Empty string = "全部"; the
// sentinel "unknown" lights up the 未分类 sub-item. Pulled out so the page
// title and the role editor share a single source of truth.
// Each entry is a [zh, en] pair consumed via tr() below.
const ROLE_FILTER_TITLES: Record<string, [string, string]> = {
  '': ['全部设备', 'All devices'],
  server: ['服务器', 'Servers'],
  storage: ['存储', 'Storage'],
  network: ['网络设备', 'Network devices'],
  unknown: ['未分类设备', 'Uncategorized devices'],
};

export default function EdgesPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  // Sidebar sub-items navigate by appending ?roles=server|storage|network|unknown.
  // No param = "全部". We forward the param to the backend so filtering uses the
  // sargable IN-list path (see internal/manager/biz/edge.ListFilter).
  const rolesFilter = useMemo(() => {
    const v = new URLSearchParams(location.search).get('roles')?.trim() ?? '';
    return v;
  }, [location.search]);
  const headerTitle = (() => {
    const pair = ROLE_FILTER_TITLES[rolesFilter];
    return pair ? tr(pair[0], pair[1]) : tr('设备', 'Devices');
  })();

  const [edges, setEdges] = useState<Edge[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // managerVersion drives the Agent column's drift chip — fetched once
  // on mount; failures degrade silently to "no chip" rather than red
  // because version mismatch isn't operationally critical.
  const [managerVersion, setManagerVersion] = useState<string>('');
  useEffect(() => {
    void getManagerVersion()
      .then((r) => setManagerVersion(r.manager_version || ''))
      .catch(() => setManagerVersion(''));
  }, []);
  const [createOpen, setCreateOpen] = useState(false);
  const [secretReveal, setSecretReveal] = useState<{
    title: string;
    accessKey: string;
    secretKey: string;
  } | null>(null);
  const [rolesEditTarget, setRolesEditTarget] = useState<Edge | null>(null);
  const [upgradeTarget, setUpgradeTarget] = useState<Edge | null>(null);
  // per-row "整包升级" busy state + last-result toast. We don't
  // open a modal — the action is single-click and the result lands in
  // the existing toast pipeline.
  const [pkgUpgradingId, setPkgUpgradingId] = useState<number | null>(null);
  const [pkgUpgradeToast, setPkgUpgradeToast] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const refresh = useCallback(async () => {
    try {
      const r = await listEdges(rolesFilter ? { roles: rolesFilter } : undefined);
      // Backend currently doesn't filter by roles (the param is sent
      // for forward-compat); the post-split device.roles lives on
      // each row in `Roles []string`. Filter client-side so the
      // 服务器 / 存储 / 网络 sub-views show only matching devices,
      // and 未分类 lands in its own bucket — never mixed in.
      let items = r.items ?? [];
      if (rolesFilter === 'unknown') {
        items = items.filter((e) => !e.roles || e.roles.length === 0);
      } else if (rolesFilter) {
        items = items.filter(
          (e) => Array.isArray(e.roles) && (e.roles as string[]).includes(rolesFilter),
        );
      }
      setEdges(items);
      setError(null);
    } catch (err) {
      setError((err as Error).message || tr('加载失败', 'Load failed'));
    } finally {
      setLoading(false);
    }
  }, [rolesFilter]);

  useEffect(() => {
    void refresh();
  }, [refresh]);
  usePoll(refresh, 10_000);

  async function onCreate(name: string) {
    const created: CreateEdgeResponse = await createEdge({ name });
    setSecretReveal({
      title: tr('已创建设备', 'Device created'),
      accessKey: created.access_key_id,
      secretKey: created.secret_key,
    });
    void refresh();
  }

  async function onRotate(id: number, name: string, accessKey: string) {
    if (!confirm(tr(`确定要轮换 ${name} 的密钥？旧密钥将立即失效。`, `Rotate ${name}'s secret? The old key takes effect immediately becomes invalid.`))) return;
    try {
      const r: RotateSecretResponse = await rotateSecret(id);
      setSecretReveal({
        title: tr(`已轮换 ${name} 的密钥`, `Rotated ${name}'s secret`),
        accessKey,
        secretKey: r.secret_key,
      });
    } catch (err) {
      alert((err as Error).message || tr('轮换失败', 'Rotate failed'));
    }
  }

  async function onDelete(id: number, name: string) {
    if (!confirm(tr(`确定要删除 ${name}？此操作不可恢复。`, `Delete ${name}? This cannot be undone.`))) return;
    try {
      await deleteEdge(id);
      void refresh();
    } catch (err) {
      alert((err as Error).message || tr('删除失败', 'Delete failed'));
    }
  }

  // one-button upgrade. Confirms with the operator (the edge
  // briefly restarts), POSTs to the resolver-backed endpoint, surfaces
  // a toast. The actual swap happens on systemctl restart inside the
  // edge; we trust the auto-rollback gate on the far side.
  async function onPackageUpgrade(e: Edge) {
    if (!confirm(tr(
      `升级 ${e.name} 整包？agent 会短暂重启；失败会自动回滚到当前版本。`,
      `Upgrade ${e.name} package? Agent will briefly restart; failed upgrades auto-rollback to current version.`,
    ))) return;
    setPkgUpgradingId(e.id);
    setPkgUpgradeToast(null);
    try {
      const resp = await upgradeEdgePackage(e.id);
      const ok = resp.applied;
      setPkgUpgradeToast({
        kind: ok ? 'ok' : 'err',
        text: ok
          ? tr(
              `${e.name} → ${resp.version} 已 stage ${resp.manifest_files} 个文件，重启 swap 中`,
              `${e.name} → ${resp.version} staged ${resp.manifest_files} files; restarting to apply`,
            )
          : tr(
              `${e.name} stage 成功但 apply 失败：${resp.apply_error ?? '未知'}`,
              `${e.name} staged OK but apply failed: ${resp.apply_error ?? 'unknown'}`,
            ),
      });
      void refresh();
    } catch (err) {
      setPkgUpgradeToast({
        kind: 'err',
        text: (err as Error).message || tr('升级失败', 'Upgrade failed'),
      });
    } finally {
      setPkgUpgradingId(null);
    }
  }

  return (
    <>
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <header className="app-header flex items-center justify-between border-b border-zinc-800/60 px-6 py-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{headerTitle}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr(`${edges.length} 台设备 · 每 10 秒自动刷新`, `${edges.length} device(s) · auto-refresh every 10s`)}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Link
              to="/edges/shell-sessions"
              className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
              title={tr('WebSSH 会话审计 / 活跃会话', 'WebSSH session audit / active sessions')}
            >
              <TerminalSquare size={12} /> {tr('WebSSH 会话', 'WebSSH sessions')}
            </Link>
            <button
              type="button"
              onClick={() => setCreateOpen(true)}
              aria-label={tr('新建设备', 'New device')}
              className="inline-flex items-center gap-1.5 rounded-md bg-accent px-2.5 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90"
            >
              <Plus size={12} /> {tr('新建', 'New')}
            </button>
          </div>
        </header>

        <div className="flex-1 overflow-y-auto px-6 py-6">
          {error && (
            <div
              role="alert"
              className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
            >
              {error}
            </div>
          )}

          <div className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
            <table className="w-full text-sm">
              <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="px-4 py-2.5 text-left">ID</th>
                  <th className="px-4 py-2.5 text-left">{tr('名称', 'Name')}</th>
                  <th className="px-4 py-2.5 text-left">{tr('主机名', 'Hostname')}</th>
                  <th className="px-4 py-2.5 text-left">IP</th>
                  <th className="px-4 py-2.5 text-left">{tr('角色', 'Roles')}</th>
                  <th className="px-4 py-2.5 text-left">{tr('状态', 'Status')}</th>
                  <th className="px-4 py-2.5 text-left">{tr('最后心跳', 'Last heartbeat')}</th>
                  <th className="px-4 py-2.5 text-left">Access Key</th>
                  <th className="px-4 py-2.5 text-left">Agent</th>
                  <th className="px-4 py-2.5 text-right">{tr('操作', 'Actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/40">
                {loading && edges.length === 0 ? (
                  <tr>
                    <td colSpan={10} className="px-4 py-10 text-center text-zinc-500">
                      {tr('加载中…', 'Loading…')}
                    </td>
                  </tr>
                ) : edges.length === 0 ? (
                  <tr>
                    <td colSpan={10} className="px-4 py-10 text-center text-zinc-500">
                      {rolesFilter
                        ? tr(
                            `没有 ${ROLE_FILTER_TITLES[rolesFilter]?.[0] ?? rolesFilter} 设备。点设备名打开详情后可在右上角分配角色。`,
                            `No ${ROLE_FILTER_TITLES[rolesFilter]?.[1] ?? rolesFilter} devices. Open a device detail page to assign roles.`,
                          )
                        : tr(
                            '暂无设备。点击右上角"新建"创建一个。',
                            'No devices yet. Click "New" in the top right to create one.',
                          )}
                    </td>
                  </tr>
                ) : (
                  edges.map((e) => (
                    <tr
                      key={e.id}
                      className="cursor-pointer transition-colors hover:bg-zinc-900/40"
                      onClick={() => navigate(`/edges/${encodeURIComponent(e.id)}`)}
                    >
                      {/* Identity columns are pinned `whitespace-nowrap`
                          — when the table is squeezed (sidebar + many
                          columns) we'd rather let the action column
                          wrap than have a name break across lines.
                          Heartbeat / access-key / agent are short and
                          formatted to a known width. */}
                      <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">
                        {e.id}
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-100">
                        {e.name || (
                          <span className="italic text-zinc-500">{tr('（待主机上线）', '(waiting for host)')}</span>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
                        {extractHostname(e.host_info) ?? '—'}
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">
                        {extractIP(e.host_info) ?? '—'}
                      </td>
                      <td
                        className="cursor-pointer whitespace-nowrap px-4 py-2.5"
                        title={tr('点击分配角色', 'Click to assign roles')}
                        onClick={(ev) => {
                          ev.stopPropagation();
                          setRolesEditTarget(e);
                        }}
                      >
                        <RoleChips roles={e.roles ?? []} />
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5">
                        <StatusPill status={e.status} />
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
                        {e.last_seen_at ? relativeTime(e.last_seen_at) : '—'}
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">
                        <span className="rounded bg-zinc-800/60 px-1.5 py-0.5">
                          {e.access_key_id.slice(0, 8)}…
                        </span>
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">
                        <AgentVersionCell agentVersion={e.agent_version} managerVersion={managerVersion} />
                      </td>
                      <td
                        className="whitespace-nowrap px-4 py-2.5 text-right"
                        onClick={(ev) => ev.stopPropagation()}
                      >
                        <button
                          type="button"
                          onClick={() => void openServerChart(e)}
                          title={tr(`在 Grafana 查看 ${e.name} 图表`, `View ${e.name} chart in Grafana`)}
                          aria-label={tr(`在 Grafana 查看 ${e.name} 图表`, `View ${e.name} chart in Grafana`)}
                          className="mr-1 inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
                        >
                          <ExternalLink size={14} />
                          <span>{tr('查看图表', 'View chart')}</span>
                        </button>
                        <ShellButton edge={e} canMutate={canMutate} />
                        <RowMenu
                          onRotate={() => onRotate(e.id, e.name, e.access_key_id)}
                          onDelete={() => onDelete(e.id, e.name)}
                          onUpgrade={() => setUpgradeTarget(e)}
                          onUpgradePackage={() => void onPackageUpgrade(e)}
                          upgradePackageBusy={pkgUpgradingId === e.id}
                        />
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      </main>

      <CreateEdgeModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onSubmit={async (name) => {
          await onCreate(name);
          setCreateOpen(false);
        }}
      />

      <SecretRevealModal
        data={secretReveal}
        onClose={() => setSecretReveal(null)}
      />

      {rolesEditTarget && (
        <RolesEditorModal
          edge={rolesEditTarget}
          onClose={() => setRolesEditTarget(null)}
          onSaved={() => {
            setRolesEditTarget(null);
            void refresh();
          }}
        />
      )}
      {upgradeTarget && (
        <UpgradeModal
          edge={upgradeTarget}
          managerVersion={managerVersion}
          onClose={() => setUpgradeTarget(null)}
          onTriggered={() => {
            setUpgradeTarget(null);
            // Don't immediately refresh — the edge needs ~30s to come
            // back online with the new version. Operator can refresh
            // manually; auto-refresh polling will pick it up too.
          }}
        />
      )}
      {pkgUpgradeToast && (
        <div
          role="status"
          onClick={() => setPkgUpgradeToast(null)}
          className={cn(
            'fixed bottom-6 right-6 z-50 max-w-md cursor-pointer rounded-lg px-4 py-2.5 text-sm shadow-2xl ring-1 ring-inset',
            pkgUpgradeToast.kind === 'ok'
              ? 'bg-emerald-500/15 text-emerald-200 ring-emerald-500/40'
              : 'bg-red-500/15 text-red-200 ring-red-500/40',
          )}
        >
          {pkgUpgradeToast.text}
        </div>
      )}
    </>
  );

  async function openServerChart(edge: Edge) {
    await openMetricDrilldown({
      expr: `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{device_id="${edge.id}",mode="idle"}[5m])))`,
      rangeInput: '1h',
      stepInput: '30s',
      title: `${edge.name} CPU`,
      edgeId: edge.id,
    });
  }
}

// AgentVersionCell shows the edge's reported agent_version + a drift
// pill comparing it to the manager. Three states:
//   - no agent_version reported → 灰 "—" (pre-fix binary)
//   - matches manager (or unknown manager) → 灰 "vX.Y.Z" no pill
//   - differs from manager → amber "vX.Y.Z · 落后"
// We don't try semver-compare ("0.7.40" vs "0.7.43"); a string mismatch
// is enough signal that an operator should look. Strict comparison also
// avoids false greens during pre-release tagging weirdness.
function AgentVersionCell({
  agentVersion,
  managerVersion,
}: {
  agentVersion?: string;
  managerVersion: string;
}) {
  const { tr } = useI18n();
  if (!agentVersion) {
    return <span className="text-zinc-600">—</span>;
  }
  const drifted = managerVersion && agentVersion !== managerVersion;
  return (
    <span className="inline-flex items-center gap-1">
      <span className="rounded bg-zinc-800/60 px-1.5 py-0.5">{agentVersion}</span>
      {drifted && (
        <span
          className="rounded border border-amber-700/50 bg-amber-900/20 px-1.5 py-0.5 text-[10px] text-amber-300"
          title={tr(`manager 版本 ${managerVersion} — 该 edge 与 manager 不同步`, `manager version ${managerVersion} — this edge is out of sync with the manager`)}
        >
          {tr('落后', 'outdated')}
        </span>
      )}
    </span>
  );
}

// UpgradeModal — operator confirms the upgrade target URL + sha256 and
// the manager dispatches an agent_upgrade RPC to the edge. The actual
// swap happens on the edge's next process restart (systemd
// ExecStartPre swap script). Form is intentionally explicit (URL +
// sha256 typed in by hand) for v1 — a future revision should let
// the operator pick from a manager-side artifact registry instead.
function UpgradeModal({
  edge,
  managerVersion,
  onClose,
  onTriggered,
}: {
  edge: Edge;
  managerVersion: string;
  onClose(): void;
  onTriggered(): void;
}) {
  const { tr } = useI18n();
  const [url, setUrl] = useState(() => {
    // Pre-fill with the same-origin manager's edge artifact path. Operators
    // typically host edge binaries on `/edge/ongrid-edge-linux-amd64`
    // alongside the install script (deploy/install/edge/ layout).
    const origin = window.location.origin.replace(/\/+$/, '');
    return `${origin}/edge/ongrid-edge-linux-amd64`;
  });
  const [sha256, setSha256] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async () => {
    if (!url.trim() || sha256.trim().length !== 64) {
      setErr(tr('需要 URL + 64 位小写 sha256', 'URL + 64-char lowercase sha256 required'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await upgradeEdgeAgent(edge.id, url.trim(), sha256.trim().toLowerCase());
      onTriggered();
    } catch (e) {
      setErr((e as Error)?.message ?? tr('触发失败', 'Trigger failed'));
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <Modal open title={tr(`升级 ${edge.name} (#${edge.id})`, `Upgrade ${edge.name} (#${edge.id})`)} onClose={onClose}>
      <div className="space-y-3 text-xs text-zinc-300">
        <div>
          <div className="text-zinc-500">{tr('当前版本', 'Current version')}</div>
          <div className="font-mono">
            {edge.agent_version ? edge.agent_version : tr('— 未上报', '— not reported')}
            {managerVersion && (
              <span className="ml-2 text-zinc-500">/ manager {managerVersion}</span>
            )}
          </div>
        </div>
        <label className="block">
          <span className="mb-1 block text-zinc-500">{tr('下载 URL', 'Download URL')}</span>
          <input
            type="text"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-zinc-500">{tr('SHA256（64 位小写 hex）', 'SHA256 (64-char lowercase hex)')}</span>
          <input
            type="text"
            value={sha256}
            onChange={(e) => setSha256(e.target.value)}
            placeholder="e.g. 3a7f...  by `sha256sum ongrid-edge-linux-amd64`"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <p className="text-[11px] text-zinc-500">
          {tr(
            'edge 会下载、校验 sha256，原子 stage 后干净退出；systemd ExecStartPre 在重启时把新二进制 mv 到 ',
            'edge downloads, verifies sha256, stages atomically and exits cleanly; on restart systemd ExecStartPre mv\'s the new binary to ',
          )}<code className="font-mono">/usr/local/bin/ongrid-edge</code>{tr('。失败时旧版本保持不变。', '. On failure the old version is left in place.')}
        </p>
        {err && (
          <div className="rounded-md border border-red-500/30 bg-red-500/10 px-2 py-1.5 text-red-300">
            {err}
          </div>
        )}
        <div className="flex justify-end gap-2 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-zinc-700 px-3 py-1.5 text-zinc-300 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            disabled={submitting}
            onClick={submit}
            className="rounded-md bg-accent px-3 py-1.5 text-accent-fg hover:bg-accent/90 disabled:opacity-50"
          >
            {submitting ? tr('触发中…', 'Triggering…') : tr('触发升级', 'Trigger upgrade')}
          </button>
        </div>
      </div>
    </Modal>
  );
}

// RoleChips renders the roles bit set as small color-coded chips. Empty
// list shows a "未分类" placeholder. The wrapping <td> is what's clickable;
// these chips are non-interactive on their own.
function RoleChips({ roles }: { roles: EdgeRole[] }) {
  const { tr } = useI18n();
  // The wrapping <td> is what's clickable — these chips are visual
  // indicators only. The dashed "+" chip exists to ADVERTISE the
  // affordance: without it operators saw a row of solid chips and
  // didn't realise they could click to manage roles (user feedback
  // 2026-05-20).
  if (roles.length === 0) {
    return (
      <span className="inline-flex items-center gap-1 rounded border border-dashed border-zinc-600 px-1.5 py-0.5 text-[11px] text-zinc-400 hover:border-accent hover:text-accent">
        <Plus size={11} />
        {tr('分配角色', 'Assign roles')}
      </span>
    );
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {roles.map((r) => (
        <span
          key={r}
          className={cn(
            'inline-flex items-center rounded border px-1.5 py-0.5 text-[11px]',
            ROLE_CHIP_CLASS[r],
          )}
        >
          {tr(EDGE_ROLE_LABELS[r], EDGE_ROLE_LABELS_EN[r])}
        </span>
      ))}
      <span
        className="inline-flex items-center rounded border border-dashed border-zinc-700 px-1 py-0.5 text-[11px] text-zinc-500 hover:border-accent hover:text-accent"
        aria-label={tr('编辑角色', 'Edit roles')}
      >
        <Plus size={10} />
      </span>
    </span>
  );
}

// Per-role chip styling. Kept terse (border + faint bg) to avoid stealing
// attention from the row's primary signal (status + last heartbeat).
const ROLE_CHIP_CLASS: Record<EdgeRole, string> = {
  server:   'border-sky-500/30    bg-sky-500/10    text-sky-300',
  storage:  'border-violet-500/30 bg-violet-500/10 text-violet-300',
  network:  'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
  database: 'border-amber-500/30  bg-amber-500/10  text-amber-300',
};

// RolesEditorModal lets an admin toggle the three role bits for one edge.
// Keep "全选" / "全清" out of MVP — three checkboxes is already trivial UX.
// Saving sends the full roles array (PATCH .../roles {roles:[...]}); empty
// array means "未分类". Backend rejects unknown names so the UI doesn't
// have to client-side validate.
function RolesEditorModal({
  edge,
  onClose,
  onSaved,
}: {
  edge: Edge;
  onClose(): void;
  onSaved(): void;
}) {
  const { tr } = useI18n();
  const [selected, setSelected] = useState<Set<EdgeRole>>(new Set(edge.roles ?? []));
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const toggle = (r: EdgeRole) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(r)) next.delete(r);
      else next.add(r);
      return next;
    });
  };

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      // Roles live on the host device (post device/edge split). If the
      // edge hasn't reported host_info yet we have no device to write to.
      if (edge.device_id == null) {
        throw new Error(
          tr(
            '此设备尚未上报 host 信息，请等 agent 上线后再分配角色。',
            'This edge has not reported host info yet — wait for it to come online before assigning roles.',
          ),
        );
      }
      // Iterate EDGE_ROLES so the wire array stays in canonical order;
      // backend doesn't care about order but tests are easier this way.
      const out = EDGE_ROLES.filter((r) => selected.has(r));
      await setEdgeRoles(edge.device_id, out);
      // Notify ambient surfaces (Sidebar's role sub-items, etc.) that the
      // fleet's role set may have changed. Sidebar refetches and the new
      // chip appears without a page reload.
      notifyDevicesChanged();
      onSaved();
    } catch (e) {
      setErr((e as Error).message || tr('保存失败', 'Save failed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr(`分配角色 · ${edge.name}`, `Assign roles · ${edge.name}`)}
      size="sm"
      footer={
        <>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={submitting}
            className="rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
          >
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </button>
        </>
      }
    >
      <div className="space-y-2">
        <p className="text-xs text-zinc-500">
          {tr(
            '一台设备可同时承担多个角色（例：超融合一体机 = 服务器 + 存储）。不勾选 = 未分类。',
            'A device can hold multiple roles (e.g. a hyper-converged box = server + storage). Leave empty for uncategorized.',
          )}
        </p>
        <div className="space-y-1">
          {EDGE_ROLES.map((r) => (
            <label
              key={r}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm text-zinc-200 hover:bg-zinc-800/60"
            >
              <input
                type="checkbox"
                checked={selected.has(r)}
                onChange={() => toggle(r)}
                className="h-3.5 w-3.5 accent-zinc-300"
              />
              <span
                className={cn(
                  'inline-flex items-center rounded border px-1.5 py-0.5 text-[11px]',
                  ROLE_CHIP_CLASS[r],
                )}
              >
                {tr(EDGE_ROLE_LABELS[r], EDGE_ROLE_LABELS_EN[r])}
              </span>
            </label>
          ))}
        </div>
        {err && <div className="text-xs text-red-400">{err}</div>}
      </div>
    </Modal>
  );
}

function extractHostname(hostInfo: Edge['host_info']): string | null {
  if (!hostInfo) return null;
  if (typeof hostInfo === 'string') {
    const parsed = safeParseHostInfo(hostInfo);
    if (!parsed) {
      const raw = hostInfo.trim();
      return raw && !raw.startsWith('{') ? raw : null;
    }
    return pickHostname(parsed);
  }
  if (typeof hostInfo === 'object') {
    return pickHostname(hostInfo);
  }
  return null;
}

function safeParseHostInfo(value: string): Record<string, unknown> | null {
  try {
    const parsed = JSON.parse(value) as unknown;
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

function pickHostname(value: Record<string, unknown>): string | null {
  const candidates = [
    value.hostname,
    value.hostName,
    value.nodename,
    value.nodeName,
    value.host,
    value.instance,
  ];
  for (const candidate of candidates) {
    if (typeof candidate !== 'string') continue;
    const normalized = candidate.trim();
    if (!normalized) continue;
    return normalized.includes(':') ? normalized.split(':')[0] || normalized : normalized;
  }
  return null;
}

function extractIP(hostInfo: Edge['host_info']): string | null {
  if (!hostInfo) return null;
  if (typeof hostInfo === 'string') {
    const parsed = safeParseHostInfo(hostInfo);
    if (!parsed) return null;
    return extractIPFromObj(parsed);
  }
  if (typeof hostInfo === 'object') {
    return extractIPFromObj(hostInfo);
  }
  return null;
}

function extractIPFromObj(obj: Record<string, unknown>): string | null {
  const v = obj.ip_address;
  if (typeof v === 'string' && v.trim()) return v.trim();
  return null;
}

// ShellButton opens the WebSSH page for one device in a NEW tab. The
// route key is device_id, not edge.id — Prom labels and the backend
// WS handler both use device_id. Disabled when the edge is offline
// or hasn't been linked to a Device row yet (device_id null).
//
// Why new tab: a shell session is its own thing — closing the host
// page (Edges) would normally tear it down via beforeunload. Letting
// it live in its own tab matches user mental model ("multiple shells
// open at once") and lets them keep using the rest of the SPA without
// disconnecting.
function ShellButton({ edge, canMutate }: { edge: Edge; canMutate: boolean }) {
  const { tr } = useI18n();
  const disabled = !canMutate || !edge.device_id || edge.status !== 'online';
  const reason = !canMutate
    ? tr('只读账号不能进入终端', 'Viewer accounts cannot open the terminal')
    : !edge.device_id
      ? tr('设备未上线（尚未注册 device 记录）', 'Device offline (no device record registered yet)')
      : edge.status !== 'online'
        ? tr('设备未上线', 'Device offline')
        : '';
  const href = edge.device_id
    ? `/devices/${encodeURIComponent(String(edge.device_id))}/shell`
    : '#';
  if (disabled) {
    return (
      <span
        title={reason}
        aria-label={`${edge.name} ${reason}`}
        className="mr-1 inline-flex cursor-not-allowed items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-600"
      >
        <TerminalSquare size={14} />
        <span>{tr('终端', 'Terminal')}</span>
      </span>
    );
  }
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      title={tr(`打开 ${edge.name} 终端 (WebSSH) — 在新标签页`, `Open ${edge.name} terminal (WebSSH) — new tab`)}
      aria-label={tr(`打开 ${edge.name} 终端，新标签页`, `Open ${edge.name} terminal in a new tab`)}
      className="mr-1 inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
    >
      <TerminalSquare size={14} />
      <span>{tr('终端', 'Terminal')}</span>
    </a>
  );
}

function RowMenu({
  onRotate,
  onDelete,
  onUpgrade,
  onUpgradePackage,
  upgradePackageBusy,
}: {
  onRotate(): void;
  onDelete(): void;
  onUpgrade(): void;
  onUpgradePackage(): void;
  upgradePackageBusy: boolean;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const [position, setPosition] = useState<{ top: number; right: number } | null>(null);

  const syncPosition = useCallback(() => {
    const trigger = triggerRef.current;
    if (!trigger) return;
    const rect = trigger.getBoundingClientRect();
    setPosition({
      top: rect.bottom + 6,
      right: window.innerWidth - rect.right,
    });
  }, []);

  useEffect(() => {
    if (!open) return;
    syncPosition();
    const onViewportChange = () => syncPosition();
    window.addEventListener('resize', onViewportChange);
    window.addEventListener('scroll', onViewportChange, true);
    return () => {
      window.removeEventListener('resize', onViewportChange);
      window.removeEventListener('scroll', onViewportChange, true);
    };
  }, [open, syncPosition]);

  const menu = useMemo(() => {
    if (!open || !position) return null;
    return createPortal(
      <>
        <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} aria-hidden />
        <div
          role="menu"
          className="fixed z-50 w-40 overflow-hidden rounded-lg border border-zinc-800 bg-zinc-900 shadow-xl"
          style={{ top: position.top, right: position.right }}
        >
          {/* primary action: one-button bundle upgrade. URL +
              sha auto-resolved by manager. Disabled while a previous
              click's request is still in flight. */}
          <button
            type="button"
            disabled={upgradePackageBusy}
            onClick={() => {
              setOpen(false);
              onUpgradePackage();
            }}
            className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800 disabled:opacity-50"
          >
            <ExternalLink size={13} /> {upgradePackageBusy ? tr('升级中…', 'Upgrading…') : tr('升级整包（agent + 插件）', 'Upgrade package (agent + plugins)')}
          </button>
          {/* Legacy custom-URL upgrade. Kept as fallback for
              cross-version downgrades / pinned URLs the resolver
              wouldn't pick. */}
          <button
            type="button"
            onClick={() => {
              setOpen(false);
              onUpgrade();
            }}
            className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800"
          >
            <ExternalLink size={13} /> {tr('自定义升级 (URL + sha)', 'Custom upgrade (URL + sha)')}
          </button>
          <button
            type="button"
            onClick={() => {
              setOpen(false);
              onRotate();
            }}
            className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800"
          >
            <RotateCw size={13} /> {tr('轮换密钥', 'Rotate secret')}
          </button>
          <button
            type="button"
            onClick={() => {
              setOpen(false);
              onDelete();
            }}
            className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-red-300 hover:bg-red-500/10"
          >
            <Trash2 size={13} /> {tr('删除', 'Delete')}
          </button>
        </div>
      </>,
      document.body,
    );
  }, [onDelete, onRotate, open, position]);

  return (
    <div className="relative inline-block">
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-label={tr("更多操作", "More actions")}
        className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
      >
        <MoreVertical size={15} />
      </button>
      {menu}
    </div>
  );
}

function CreateEdgeModal({
  open,
  onClose,
  onSubmit,
}: {
  open: boolean;
  onClose(): void;
  onSubmit(name: string): Promise<void>;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState('');
  const [pending, setPending] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      setName('');
      setErr(null);
      setPending(false);
    }
  }, [open]);

  async function go() {
    if (pending) return;
    setPending(true);
    setErr(null);
    try {
      // Empty name is allowed; backend will mint a 10-char id as the
      // default label.
      await onSubmit(name.trim());
    } catch (e) {
      setErr((e as Error).message || tr('创建失败', 'Create failed'));
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={tr('新建设备', 'New device')}
      footer={
        <>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            onClick={() => void go()}
            disabled={pending}
            className="rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:cursor-not-allowed disabled:opacity-60"
          >
            {pending ? tr('创建中…', 'Creating…') : tr('创建', 'Create')}
          </button>
        </>
      }
    >
      <label htmlFor="edge-name" className="mb-1 block text-[11px] text-zinc-500">
        {tr('名称', 'Name')}
      </label>
      <input
        id="edge-name"
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={tr("留空，主机上线后自动填主机名", "Leave blank; auto-fill on first heartbeat")}
        className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
        onKeyDown={(e) => {
          if (e.key === 'Enter') void go();
        }}
      />
      <p className="mt-2 text-[11px] text-zinc-500">
        {tr(
          '名称可留空。设备上线后会自动以上报的主机名填入。创建后将一次性显示 secret_key，关闭弹窗后无法再次查看。',
          'Name may be left blank — it auto-fills with the reported hostname on first heartbeat. secret_key is shown once after creation and cannot be retrieved again.',
        )}
      </p>
      {err && (
        <div
          role="alert"
          className="mt-2 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
        >
          {err}
        </div>
      )}
    </Modal>
  );
}

function SecretRevealModal({
  data,
  onClose,
}: {
  data: { title: string; accessKey: string; secretKey: string } | null;
  onClose(): void;
}) {
  const { tr } = useI18n();
  if (!data) return null;
  return (
    <Modal
      open={true}
      onClose={onClose}
      title={data.title}
      size="md"
      footer={
        <button
          type="button"
          onClick={onClose}
          className="rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white"
        >
          {tr('我已保存', "I've saved it")}
        </button>
      }
    >
      <p className="mb-3 text-xs text-amber-300/90">
        {tr('以下安装命令包含 secret_key，仅显示一次。请立即复制保存到目标主机。', 'The install command below carries the secret_key and is shown only once. Copy it to the target host now.')}
      </p>
      <InstallCommandRow accessKey={data.accessKey} secretKey={data.secretKey} />
    </Modal>
  );
}

function InstallCommandRow({ accessKey, secretKey }: { accessKey: string; secretKey: string }) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  const host = typeof window !== 'undefined' ? window.location.host : 'ongrid.example.com';
  const hostnameOnly = host.split(':')[0] || host;
  const tunnelAddr = `${hostnameOnly}:40012`;
  const cmd =
    `curl -k -sSL https://${host}/install.sh | bash -s -- ` +
    `--access-key=${accessKey} ` +
    `--secret-key=${secretKey} ` +
    `--server-edge-addr=${tunnelAddr} ` +
    `--server-http-addr=${host}`;
  const display =
    `curl -k -sSL https://${host}/install.sh | bash -s -- \\\n` +
    `  --access-key=${accessKey} \\\n` +
    `  --secret-key=${secretKey} \\\n` +
    `  --server-edge-addr=${tunnelAddr} \\\n` +
    `  --server-http-addr=${host}`;
  return (
    <div className="mt-4">
      <div className="mb-1 flex items-center justify-between">
        <div className="text-[11px] uppercase tracking-wider text-zinc-500">
          {tr('在目标主机上一键安装', 'One-line install on the target host')}
        </div>
        <button
          type="button"
          onClick={() => {
            navigator.clipboard
              .writeText(cmd)
              .then(() => {
                setCopied(true);
                setTimeout(() => setCopied(false), 2000);
              })
              .catch(() => {
                /* noop */
              });
          }}
          aria-label={tr("复制安装命令", "Copy install command")}
          className={cn(
            'inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs',
            copied
              ? 'bg-emerald-500/15 text-emerald-300'
              : 'bg-zinc-800 text-zinc-300 hover:bg-zinc-700',
          )}
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? tr('已复制', 'Copied') : tr('复制单行', 'Copy one-liner')}
        </button>
      </div>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-lg border border-zinc-800 bg-zinc-950/60 px-3 py-2 font-mono text-[11px] leading-relaxed text-zinc-200">
        {display}
      </pre>
      <p className="mt-1.5 text-[11px] text-zinc-500">
        {tr('自签证书：浏览器警告 + curl ', 'Self-signed cert: browser warning + curl ')}<code className="rounded bg-zinc-800 px-1">-k</code>{tr(' 已忽略校验。目标主机需 root（脚本会自动 sudo 重试）；支持 linux amd64 / arm64。', ' skips verification. The target host needs root (the script auto-retries with sudo); linux amd64 / arm64 are supported.')}
      </p>
    </div>
  );
}

