import { Input, Label } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/Tabs';
import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Boxes,
  ChevronDown,
  ChevronRight,
  Folder,
  GitBranch,
  Globe,
  Loader2,
  Package,
  PackagePlus,
  PlugZap,
  RefreshCw,
  Trash2,
  Upload,
} from 'lucide-react';
import { useAuth } from '@/store/auth';
import { ApiError } from '@/api/client';
import {
  classifyError,
  installPack,
  uploadPack,
  listInstalledPacks,
  uninstallPack,
  type CapabilityDeclaration,
  type InstallResponse,
  type InstallSource,
  type InstalledPack,
  type LoadWarning,
  type MarketplaceErrorKind,
  type SourceType,
} from '@/api/marketplace';
import { Modal } from '@/components/Modal';
import { CapabilitySummaryView } from '@/components/marketplace/CapabilitySummary';
import { CredentialBindings } from '@/components/marketplace/CredentialBindings';
import { SignatureBadge } from '@/components/marketplace/SignatureBadge';
import { Button, Card, Chip, EmptyState } from '@/components/ui';
import { cn } from '@/lib/cn';
import type { IconType } from '@/lib/icon';
import { tr as trInline, useI18n } from '@/i18n/locale';

// Skill pack install / uninstall surface. Mounted at /skills?tab=install
// (visible nav for this tab is hidden as of 2026-05-19; reachable by URL
// only — see SkillsPage in pages/Skills.tsx for the gating rationale).
// Two blocks:
//   A. 已安装列表 (top, default visible) — list + per-row [详情] expand + [卸载]
//   B. 安装新包 (3-tab card: 本地路径 / Tarball URL / Git URL)
//
// 注册表入口（ongrid-official / openclaw-bridge）下架——后端 v1 stub 不
// 工作。Source.Type "registry" 仍在 API 类型里保留，未来 backend 真正实现
// registry proxy 后再把 tab 加回来。
//
// Install UX is "POST first, confirm after": we send /install, the backend
// runs the loader, and we render the resulting capabilities + warnings in
// a confirm modal. The modal exposes [完成] (close + already-installed) and
// [回滚卸载] (DELETE the pack we just installed) so the user can back out
// after seeing the capabilities — fail-safe UI.
export default function SettingsMarketplace() {
  const { tr } = useI18n();
  const role = useAuth((s) => s.role);
  const isAdmin = role === 'admin';

  const [packs, setPacks] = useState<InstalledPack[]>([]);
  const [packsLoading, setPacksLoading] = useState(true);
  const [packsErr, setPacksErr] = useState<string | null>(null);
  const [installing, setInstalling] = useState(false);
  const [confirm, setConfirm] = useState<InstallResponse | null>(null);
  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const refresh = useCallback(async () => {
    setPacksLoading(true);
    setPacksErr(null);
    try {
      const list = await listInstalledPacks();
      setPacks(list);
    } catch (e) {
      setPacksErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setPacksLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), 5000);
    return () => window.clearTimeout(t);
  }, [toast]);

  const handleInstall = useCallback(
    async (src: InstallSource) => {
      if (!isAdmin) {
        setToast({ kind: 'err', text: tr('需要 admin 权限才能安装', 'Admin permission required to install') });
        return;
      }
      setInstalling(true);
      try {
        const resp = await installPack(src);
        setConfirm(resp);
        // Re-pull the list so the modal opens against fresh ground truth.
        void refresh();
      } catch (e) {
        setToast({ kind: 'err', text: errorToast(e, 'install') });
      } finally {
        setInstalling(false);
      }
    },
    [isAdmin, refresh],
  );

  const handleUpload = useCallback(
    async (file: File) => {
      if (!isAdmin) {
        setToast({ kind: 'err', text: tr('需要 admin 权限才能安装', 'Admin permission required to install') });
        return;
      }
      setInstalling(true);
      try {
        const resp = await uploadPack(file);
        setConfirm(resp);
        void refresh();
      } catch (e) {
        setToast({ kind: 'err', text: errorToast(e, 'install') });
      } finally {
        setInstalling(false);
      }
    },
    [isAdmin, refresh],
  );

  const handleUninstall = useCallback(
    async (packId: string, opts?: { silent?: boolean }) => {
      if (!isAdmin) {
        setToast({ kind: 'err', text: tr('需要 admin 权限才能卸载', 'Admin permission required to uninstall') });
        return;
      }
      try {
        await uninstallPack(packId);
        if (!opts?.silent) {
          setToast({ kind: 'ok', text: tr(`已卸载 ${packId}`, `Uninstalled ${packId}`) });
        }
        await refresh();
      } catch (e) {
        setToast({ kind: 'err', text: errorToast(e, 'uninstall') });
      }
    },
    [isAdmin, refresh],
  );

  const toggleExpand = (packId: string) =>
    setExpanded((cur) => ({ ...cur, [packId]: !cur[packId] }));

  return (
    <div className="space-y-5">
      <InstalledList
        packs={packs}
        loading={packsLoading}
        err={packsErr}
        onRefresh={refresh}
        onUninstall={(p) => handleUninstall(p.pack_id)}
        isAdmin={isAdmin}
        expanded={expanded}
        onToggleExpand={toggleExpand}
      />

      <InstallCard
        installing={installing}
        onInstall={handleInstall}
        onUpload={handleUpload}
        isAdmin={isAdmin}
      />

      <ConfirmModal
        resp={confirm}
        onClose={() => setConfirm(null)}
        onRollback={async () => {
          if (!confirm) return;
          const id = confirm.pack.pack_id;
          setConfirm(null);
          await handleUninstall(id, { silent: true });
          setToast({ kind: 'ok', text: tr(`已回滚卸载 ${id}`, `Rolled back: uninstalled ${id}`) });
        }}
      />

      {toast && (
        <div
          role="status"
          className={cn(
            'fixed bottom-6 right-6 z-50 max-w-sm rounded-lg px-4 py-2.5 text-sm shadow-2xl ring-1 ring-inset',
            toast.kind === 'ok'
              ? 'bg-emerald-500/15 text-emerald-200 ring-emerald-500/40'
              : 'bg-red-500/15 text-red-200 ring-red-500/40',
          )}
        >
          {toast.text}
        </div>
      )}
    </div>
  );
}

// ---------- A. installed list -------------------------------------------------

function InstalledList({
  packs,
  loading,
  err,
  onRefresh,
  onUninstall,
  isAdmin,
  expanded,
  onToggleExpand,
}: {
  packs: InstalledPack[];
  loading: boolean;
  err: string | null;
  onRefresh: () => void;
  onUninstall: (p: InstalledPack) => void;
  isAdmin: boolean;
  expanded: Record<string, boolean>;
  onToggleExpand: (packId: string) => void;
}) {
  const { tr } = useI18n();
  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Boxes size={14} className="text-zinc-400" />
          <h2 className="text-sm font-medium text-zinc-100">{tr(`已安装 (${packs.length})`, `Installed (${packs.length})`)}</h2>
        </div>
        <Button onClick={onRefresh} disabled={loading} variant="ghost">
          {loading ? (
            <Loader2 size={11} className="animate-spin" />
          ) : (
            <RefreshCw size={11} />
          )}
          {tr('刷新', 'Refresh')}
        </Button>
      </div>

      {err && (
        <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}

      {loading && packs.length === 0 ? (
        <div className="flex h-24 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : packs.length === 0 ? (
        <EmptyState
          title={tr('还没有安装任何包', 'No packs installed yet')}
          hint={tr('下方「安装新包」选一个本地路径或 URL 试试', 'Use "Install new pack" below — pick a local path or URL')}
          className="flex h-32 flex-col items-center justify-center gap-2 text-center"
        />
      ) : (
        <ul className="divide-y divide-zinc-800/40 rounded-md border border-zinc-800/60 bg-zinc-950/30">
          {packs.map((p) => (
            <PackRow
              key={p.pack_id}
              pack={p}
              expanded={!!expanded[p.pack_id]}
              onToggle={() => onToggleExpand(p.pack_id)}
              onUninstall={() => onUninstall(p)}
              onReload={onRefresh}
              isAdmin={isAdmin}
            />
          ))}
        </ul>
      )}
    </Card>
  );
}

function PackRow({
  pack,
  expanded,
  onToggle,
  onUninstall,
  onReload,
  isAdmin,
}: {
  pack: InstalledPack;
  expanded: boolean;
  onToggle: () => void;
  onUninstall: () => void;
  onReload: () => void;
  isAdmin: boolean;
}) {
  const { tr } = useI18n();
  const [confirming, setConfirming] = useState(false);
  return (
    <li className="px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="subtle" size="sm"
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          aria-label={expanded ? tr('收起详情', 'Collapse details') : tr('展开详情', 'Expand details')}
          className="p-0.5"
        >
          {expanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
        </Button>
        <Package size={13} className="text-zinc-500" />
        <span className="font-mono text-sm text-zinc-100">{pack.pack_id}</span>
        {pack.version && (
          <Chip className="font-mono">v{pack.version}</Chip>
        )}
        {pack.source && (
          <span className="text-[11px] text-zinc-500">{pack.source}</span>
        )}
        <SignatureBadge state={pack.signature_state} />

        <div className="ml-auto flex items-center gap-1.5">
          <Button onClick={onToggle} variant="ghost">
            {expanded ? tr('收起', 'Collapse') : tr('详情', 'Details')}
          </Button>
          <Hint content={isAdmin ? tr('卸载该包', 'Uninstall this pack') : tr('需要 admin 权限', 'Admin permission required')}><Button
            onClick={() => setConfirming(true)}
            disabled={!isAdmin}

            variant="danger"
          >
            <Trash2 size={11} />
            {tr('卸载', 'Uninstall')}
          </Button></Hint>
        </div>
      </div>

      {expanded && (
        <div className="mt-3 ml-5 rounded-md border border-zinc-800/80 bg-zinc-950/50 px-3 py-3">
          <PackMeta pack={pack} />
          {pack.capabilities ? (
            <div className="mt-3">
              <CapabilitySummaryView decl={pack.capabilities} compact />
            </div>
          ) : (
            <div className="mt-2 text-[11px] text-zinc-500">
              {tr('该包没有保存能力声明（旧版安装或解析失败）', 'This pack has no stored capability declaration (legacy install or parse failure)')}
            </div>
          )}
          <CredentialBindings pack={pack} isAdmin={isAdmin} onSaved={onReload} />
        </div>
      )}

      <Modal
        open={confirming}
        onClose={() => setConfirming(false)}
        title={tr(`卸载 ${pack.pack_id}?`, `Uninstall ${pack.pack_id}?`)}
        size="sm"
        footer={
          <>
            <Button onClick={() => setConfirming(false)} variant="ghost">
              {tr('取消', 'Cancel')}
            </Button>
            <Button
              onClick={() => {
                setConfirming(false);
                onUninstall();
              }}
              variant="danger"
            >
              <Trash2 size={12} />
              {tr('确认卸载', 'Uninstall')}
            </Button>
          </>
        }
      >
        <p className="text-sm text-zinc-300">
          {tr('卸载会从磁盘移除 ', 'Uninstall removes ')}
          <span className="font-mono text-zinc-100">{pack.pack_id}</span>
          {tr(' 并热重载技能注册表。重装请重新走「安装新包」流程。', ' from disk and hot-reloads the skill registry. To reinstall, use "Install new pack" again.')}
        </p>
      </Modal>
    </li>
  );
}

function PackMeta({ pack }: { pack: InstalledPack }) {
  const { tr } = useI18n();
  const installedAt = formatDate(pack.installed_at);
  return (
    <div className="grid grid-cols-1 gap-x-4 gap-y-1 text-[11px] text-zinc-400 sm:grid-cols-2">
      <Meta label={tr('来源', 'Source')}>
        {pack.source}
        {pack.source_url && (
          <>
            {' · '}
            <span className="break-all font-mono text-zinc-500">{pack.source_url}</span>
          </>
        )}
      </Meta>
      <Meta label={tr('版本', 'Version')}>v{pack.version || '—'}</Meta>
      <Meta label={tr('安装时间', 'Installed at')}>{installedAt}</Meta>
      <Meta label="manifest sha">
        <span className="font-mono text-zinc-500">
          {pack.manifest_sha256 ? pack.manifest_sha256.slice(0, 12) + '…' : '—'}
        </span>
      </Meta>
      <Meta label={tr('安装路径', 'Install path')} full>
        <span className="break-all font-mono text-zinc-500">
          {pack.install_path || '—'}
        </span>
      </Meta>
    </div>
  );
}

function Meta({
  label,
  children,
  full,
}: {
  label: string;
  children: React.ReactNode;
  full?: boolean;
}) {
  return (
    <div className={cn(full && 'sm:col-span-2')}>
      <span className="text-zinc-600">{label}: </span>
      <span className="text-zinc-300">{children}</span>
    </div>
  );
}

// ---------- B. install card ---------------------------------------------------

// 注册表入口暂时下架（运营 / 用户都看不懂；也明确"等首批客户
// 上规模再启动 ongrid-official"）。后端 SourceTypeRegistry 保留，未来恢复
// 只需把 'registry' 加回这个 list 即可。
const TABS: Array<{ key: SourceType | 'upload'; labelZh: string; labelEn: string; icon: IconType }> = [
  { key: 'upload', labelZh: '上传文件', labelEn: 'Upload', icon: Upload },
  { key: 'local', labelZh: '本地路径', labelEn: 'Local path', icon: Folder },
  { key: 'tarball', labelZh: 'Tarball URL', labelEn: 'Tarball URL', icon: Globe },
  { key: 'git', labelZh: 'Git URL', labelEn: 'Git URL', icon: GitBranch },
];

function InstallCard({
  installing,
  onInstall,
  onUpload,
  isAdmin,
}: {
  installing: boolean;
  onInstall: (src: InstallSource) => void;
  onUpload: (file: File) => void;
  isAdmin: boolean;
}) {
  const { tr } = useI18n();
  const [tab, setTab] = useState<SourceType | 'upload'>('upload');
  const [path, setPath] = useState('');
  const [tarballURL, setTarballURL] = useState('');
  const [gitURL, setGitURL] = useState('');
  const [gitRef, setGitRef] = useState('');
  const [file, setFile] = useState<File | null>(null);

  const buildSource = useCallback((): InstallSource | null => {
    switch (tab) {
      case 'local':
        if (!path.trim()) return null;
        return { type: 'local', path: path.trim() };
      case 'tarball':
        if (!tarballURL.trim()) return null;
        return { type: 'tarball', url: tarballURL.trim() };
      case 'git':
        if (!gitURL.trim()) return null;
        return {
          type: 'git',
          url: gitURL.trim(),
          ref: gitRef.trim() || undefined,
        };
      case 'registry':
        // Registry tab 已下架（等首批客户上规模再启动）；保留 case
        // 让 SourceType 联合类型完备，但 UI 不再暴露入口，永远走不到这里。
        return null;
      default:
        return null;
    }
  }, [tab, path, tarballURL, gitURL, gitRef]);

  const submit = () => {
    if (installing) return;
    if (tab === 'upload') {
      if (file) onUpload(file);
      return;
    }
    const src = buildSource();
    if (!src) return;
    onInstall(src);
  };

  const canSubmit = useMemo(
    () => (tab === 'upload' ? !!file : buildSource() !== null) && !installing,
    [tab, file, buildSource, installing],
  );

  return (
    <Tabs value={tab} onValueChange={setTab} className="contents"><Card className="p-5">
      <div className="mb-3 flex items-center gap-2">
        <PackagePlus size={14} className="text-zinc-400" />
        <h2 className="text-sm font-medium text-zinc-100">{tr('安装新包', 'Install new pack')}</h2>
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">
        {tr(
          '支持 4 种安装源，全部走后端 loader，安装成功后弹出 Capability 摘要供二次确认。',
          'Four install sources, all go through the backend loader. After a successful install a Capability summary pops up for confirmation.',
        )}
        {!isAdmin && (
          <span className="ml-1 text-amber-400">{tr('仅 admin 可执行安装。', 'Only admins can install.')}</span>
        )}
      </p>

      <TabsList className="mb-3 flex flex-wrap gap-1.5">
        {TABS.map((t) => {
          const Icon = t.icon;
          return (
            <TabsTrigger
              key={t.key}

              value={t.key}


            >
              <Icon size={11} />
              {tr(t.labelZh, t.labelEn)}
            </TabsTrigger>
          );
        })}
      </TabsList>

      <TabsContent value={tab} className="contents"><div className="rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-3">
        {tab === 'upload' && (
          <Field
            label={tr('技能压缩包', 'Skill archive')}
            hint={tr(
              '从浏览器上传一个 .zip / .tar.gz 包（含 SKILL.md 或 .claude-plugin/plugin.json）；服务端解压后按本地目录安装。',
              'Upload a .zip / .tar.gz from your browser (containing SKILL.md or .claude-plugin/plugin.json); the server extracts and installs it.',
            )}
          >
            <Input variant="inset"
              type="file"
              accept=".zip,.tar.gz,.tgz,.tar"
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              disabled={!isAdmin || installing}
              className="block w-full file:mr-3 file:rounded-md file:border-0 file:bg-zinc-800 file:px-3 file:py-1.5 file:text-zinc-200 hover:file:bg-zinc-700"
            />
            {file && (
              <div className="mt-1.5 text-[11px] text-zinc-400">
                {file.name} · {(file.size / 1024).toFixed(0)} KB
              </div>
            )}
          </Field>
        )}

        {tab === 'local' && (
          <Field
            label={tr('绝对路径', 'Absolute path')}
            hint={tr(
              'manager 主机上 admin 已 scp / 解压好的目录，例如 /var/lib/ongrid/uploads/etcd-tools',
              'Directory on the manager host that an admin has already scp\'d / extracted, e.g. /var/lib/ongrid/uploads/etcd-tools',
            )}
          >
            <Input
              type="text"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/var/lib/ongrid/uploads/..."
              disabled={!isAdmin || installing}
              className={inputClass}
            />
          </Field>
        )}

        {tab === 'tarball' && (
          <Field label="Tarball URL" hint={tr('HTTP(S) 直链，扩展名 .tgz / .tar.gz', 'Direct HTTP(S) link, extension .tgz / .tar.gz')}>
            <Input
              type="text"
              value={tarballURL}
              onChange={(e) => setTarballURL(e.target.value)}
              placeholder="https://example.com/foo-0.1.0.tgz"
              disabled={!isAdmin || installing}
              className={inputClass}
            />
          </Field>
        )}

        {tab === 'git' && (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_180px]">
            <Field label="Git URL" hint={tr('git clone --depth=1 拉取，支持 https / git+ssh', 'git clone --depth=1; supports https / git+ssh')}>
              <Input
                type="text"
                value={gitURL}
                onChange={(e) => setGitURL(e.target.value)}
                placeholder="https://github.com/owner/repo.git"
                disabled={!isAdmin || installing}
                className={inputClass}
              />
            </Field>
            <Field label="Ref" hint={tr('branch / tag / commit；留空 = HEAD', 'branch / tag / commit; empty = HEAD')}>
              <Input
                type="text"
                value={gitRef}
                onChange={(e) => setGitRef(e.target.value)}
                placeholder="main"
                disabled={!isAdmin || installing}
                className={inputClass}
              />
            </Field>
          </div>
        )}

        <div className="mt-4 flex items-center gap-3">
          <Hint content={!isAdmin ? tr('需要 admin 权限', 'Admin permission required') : undefined}><Button
            onClick={submit}
            disabled={!canSubmit || !isAdmin}

            variant="primary"
          >
            {installing ? (
              <Loader2 size={13} className="animate-spin" />
            ) : (
              <PlugZap size={13} />
            )}
            <span>{installing ? tr('安装中…', 'Installing…') : tr('安装', 'Install')}</span>
          </Button></Hint>
          <span className="text-[11px] text-zinc-500">
            {tr('安装成功后会弹出 Capability 摘要，确认或回滚由你决定', 'A Capability summary opens after install — confirm or roll back')}
          </span>
        </div>
      </div></TabsContent>
    </Card></Tabs>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <Label className="block">
      <span className="mb-1 block text-[11px] text-zinc-400">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-zinc-500">{hint}</span>}
    </Label>
  );
}

const inputClass =
  "w-full";

// ---------- confirm modal ----------------------------------------------------

function ConfirmModal({
  resp,
  onClose,
  onRollback,
}: {
  resp: InstallResponse | null;
  onClose: () => void;
  onRollback: () => void | Promise<void>;
}) {
  const { tr } = useI18n();
  const open = resp !== null;
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title={
        resp
          ? tr(
              `已安装: ${resp.pack.pack_id}${resp.pack.version ? ' v' + resp.pack.version : ''}`,
              `Installed: ${resp.pack.pack_id}${resp.pack.version ? ' v' + resp.pack.version : ''}`,
            )
          : tr('安装结果', 'Install result')
      }
      footer={
        resp ? (
          <>
            <Button
              onClick={() => {
                void onRollback();
              }}
              variant="danger"
            >
              <Trash2 size={12} />
              {tr('回滚卸载', 'Roll back')}
            </Button>
            <Button onClick={onClose} variant="primary">
              {tr('完成', 'Done')}
            </Button>
          </>
        ) : null
      }
    >
      {resp && <ConfirmBody decl={resp.capabilities} pack={resp.pack} warnings={resp.warnings} />}
    </Modal>
  );
}

function ConfirmBody({
  decl,
  pack,
  warnings,
}: {
  decl: CapabilityDeclaration;
  pack: InstalledPack;
  warnings: LoadWarning[];
}) {
  const { tr } = useI18n();
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-x-4 gap-y-1 text-[11px] text-zinc-400 sm:grid-cols-2">
        <Meta label={tr('来源', 'Source')}>{pack.source || '—'}</Meta>
        <Meta label={tr('签名', 'Signature')}>
          <SignatureBadge state={pack.signature_state} />
        </Meta>
        <Meta label={tr('安装路径', 'Install path')} full>
          <span className="break-all font-mono text-zinc-500">{pack.install_path || '—'}</span>
        </Meta>
      </div>

      <div className="rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-3">
        <div className="mb-2 text-[11px] uppercase tracking-wide text-zinc-500">{tr('能力声明', 'Capability declaration')}</div>
        <CapabilitySummaryView decl={decl} warnings={warnings} />
      </div>

      {/* Bind credentials right here — install requires admin, so the binder
          is always actionable. Renders nothing when the pack declares no
          credential slots. */}
      <CredentialBindings pack={pack} isAdmin />

      <p className="text-[11px] text-zinc-500">
        {tr('包已落盘 + 入库；点「完成」保留，点「回滚卸载」立即移除。', 'Pack is on disk and in the DB. Click Done to keep it, or Roll back to remove it immediately.')}
      </p>
    </div>
  );
}

// ---------- helpers ----------------------------------------------------------

function formatDate(iso: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function errorToast(err: unknown, op: 'install' | 'uninstall'): string {
  const kind: MarketplaceErrorKind = classifyError(err);
  const apiMsg = err instanceof ApiError ? err.message : err instanceof Error ? err.message : '';
  const tr = trInline;
  switch (kind) {
    case 'unauthorized':
      return tr('会话已过期，请重新登录', 'Session expired, please log in again');
    case 'forbidden':
      return tr('需要 admin 权限', 'Admin permission required');
    case 'conflict':
      return tr(
        '该包已经安装过（manifest 一致或 pack_id 冲突），请先卸载旧版再装',
        'This pack is already installed (same manifest or pack_id collision); uninstall the existing one first',
      );
    case 'invalid':
      if (/allow.*list|allow.list|allowed/i.test(apiMsg)) {
        return tr(
          '该 source 未在允许列表，开发模式 ONGRID_MARKETPLACE_DEVMODE=true 可放开',
          'This source is not in the allow-list; set ONGRID_MARKETPLACE_DEVMODE=true for dev mode',
        );
      }
      return apiMsg
        ? tr(`请求参数有问题：${apiMsg}`, `Invalid request: ${apiMsg}`)
        : tr('请求参数有问题，请检查路径 / URL / 注册表项', 'Invalid request; check the path / URL / registry entry');
    case 'not-found':
      return op === 'uninstall'
        ? tr('该包已不存在（可能已被卸载）', 'Pack does not exist (may already be uninstalled)')
        : tr('资源不存在', 'Resource not found');
    case 'network':
      return tr('网络异常，无法连接 manager', 'Network error, cannot reach manager');
    default:
      return apiMsg
        ? tr(`操作失败：${apiMsg}`, `Operation failed: ${apiMsg}`)
        : tr('操作失败，请稍后重试', 'Operation failed, please retry');
  }
}
