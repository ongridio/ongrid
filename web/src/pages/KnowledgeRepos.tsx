import { Button, Label, Input, Textarea } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { Hint } from '@/components/ui/Tooltip';
// KnowledgeRepos page — add / sync / remove git repos. Each
// successfully-synced repo populates knowledge_docs (source_type=repo);
// the LLM's query_knowledge tool then searches them alongside manual
// docs.
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  ChevronLeft,
  GitBranch,
  KeyRound,
  Plus,
  Pencil,
  RefreshCw,
  Trash2,
} from 'lucide-react';
import { cn } from '@/lib/cn';
import { fullDateTime } from '@/lib/format';
import { Modal } from '@/components/Modal';
import {
  createRepo,
  createSSHIdentity,
  deleteRepo,
  deleteSSHIdentity,
  generateSSHIdentity,
  isBuiltinVault,
  listRepos,
  listSSHIdentities,
  repositoryName,
  syncRepo,
  updateRepo,
  type KnowledgeRepo,
  type SSHIdentity,
} from '@/api/knowledge';
import { ApiError } from '@/api/client';
import { useI18n } from '@/i18n/locale';

// gitErrorHint localizes the RAW git output the backend stores in
// last_sync_error (locale-neutral English from git itself). Ported from the
// old backend annotateGitError so the message follows the UI locale instead
// of being frozen in Chinese at sync time. Returns '' for unrecognized
// output (the caller still shows the raw text in a details block).
function gitErrorHint(raw: string, url: string, tr: (zh: string, en: string) => string): string {
  const low = raw.toLowerCase();
  const ssh = url.startsWith('git@') || url.startsWith('ssh://');
  if (
    ssh &&
    (low.includes('permission denied (publickey)') ||
      low.includes('could not read from remote repository') ||
      low.includes('permission denied, please try again'))
  )
    return tr(
      'SSH 认证失败：服务器拒绝了 key。确认公钥已加到该仓库的 Deploy keys 或你账号的 SSH keys，且未被删除。',
      'SSH auth failed: the key was rejected. Confirm the public key is added to the repo Deploy keys or your account SSH keys, and not removed.',
    );
  if (ssh && low.includes('host key verification failed'))
    return tr(
      'SSH host key 不匹配：服务器指纹与已存 known_hosts 不一致（中间人 / DNS 劫持 / 服务器换密钥）。请人工核对。',
      'SSH host key mismatch: the server fingerprint differs from the stored known_hosts (MITM / DNS hijack / server rekey). Verify manually.',
    );
  if (low.includes('could not read username') || low.includes('authentication failed'))
    return tr(
      '凭证缺失或被拒：私有仓库需要 token / 凭证，或已配置的凭证无访问权。请在凭证里配置。',
      'Credentials missing or rejected: a private repo needs a token, or the configured credential lacks access. Configure it under credentials.',
    );
  if (low.includes('repository not found'))
    return tr(
      '找不到仓库：检查 URL 拼写（大小写敏感）；若是私有仓库，确认凭证有访问权。',
      'Repository not found: check the URL spelling (case-sensitive); if private, confirm the credential has access.',
    );
  if (low.includes('rate limit'))
    return tr('API 限流，稍后重试或更换 token。', 'API rate-limited — retry later or use a different token.');
  if (
    low.includes('early eof') ||
    low.includes('ssl_read') ||
    low.includes('unexpected eof') ||
    low.includes('rpc failed') ||
    low.includes('signal: killed') ||
    low.includes('timed out') ||
    low.includes('timeout') ||
    low.includes('connection reset') ||
    low.includes('broken pipe')
  )
    return tr(
      '网络中断 / 超时，clone 未拉完。点同步重试；反复失败请检查 manager 容器到该 host 的连通性（大陆访问 github 常不稳，可换 gitee 镜像）。',
      'Network interrupted / timeout — clone did not finish. Retry sync; if it keeps failing, check the manager container’s connectivity to the host (github from mainland is often unstable — a gitee mirror is more reliable).',
    );
  if (low.includes('could not resolve host') || low.includes('name or service not known'))
    return tr('DNS 解析失败：无法解析该 host。检查容器 DNS / 出口策略。', 'DNS resolution failed: cannot resolve the host. Check the container DNS / egress policy.');
  if (low.includes('connection refused'))
    return tr('连接被拒：检查防火墙 / 出口代理。', 'Connection refused: check the firewall / egress proxy.');
  return '';
}

export default function KnowledgeReposPage() {
  const { tr } = useI18n();
  const [items, setItems] = useState<KnowledgeRepo[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<KnowledgeRepo | null>(null);
  const [deleting, setDeleting] = useState<KnowledgeRepo | null>(null);
  const [syncingID, setSyncingID] = useState<number | null>(null);
  const [credentialsRefresh, setCredentialsRefresh] = useState(0);

  const fetchAll = useCallback(async (silent = false) => {
    if (silent) setRefreshing(true);
    else setLoading(true);
    try {
      const r = await listRepos();
      // Hide the platform-vendor builtin vault row (seeded internally,
      // driven by the "Sync built-in vault" button on the Knowledge
      // page) — the Repos page is for user-managed git knowledge
      // sources, not platform-shipped content. isBuiltinVault() keys off
      // the server is_builtin flag so this filter doesn't break on a URL
      // scheme change the way the old ongridio/vault substring did.
      setItems((r.items ?? []).filter((row) => !isBuiltinVault(row)));
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void fetchAll();
    const timer = window.setInterval(() => { if (!document.hidden) void fetchAll(true); }, 5000);
    return () => window.clearInterval(timer);
  }, [fetchAll]);

  const onSync = async (id: number) => {
    setSyncingID(id);
    setErr(null);
    try {
      await syncRepo(id);
      await fetchAll(true);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSyncingID(null);
    }
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <div className="flex items-center gap-2 text-xs text-zinc-500">
              <Link to="/knowledge" className="inline-flex items-center gap-1 text-zinc-400 hover:text-zinc-200">
                <ChevronLeft size={12} /> {tr('返回知识库', 'Back to Knowledge')}
              </Link>
            </div>
            <h1 className="mt-1 text-base font-semibold text-zinc-100">{tr('代码仓库', 'Code repos')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr(
                '新增后自动同步，每 5 分钟检查更新；拉取全部分支、Tag 和完整历史。',
                'Syncs automatically after adding, then checks every 5 minutes for all branches, tags and full history.',
              )}
            </p>
          </div>
          <div className="flex gap-2">
            <Button variant="outline" size="sm"
              type="button"
              onClick={() => { void fetchAll(true); setCredentialsRefresh((value) => value + 1); }}
              disabled={loading || refreshing}
              className="inline-flex items-center gap-1.5 px-2.5 py-1.5"
            >
              <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
              {tr('刷新', 'Refresh')}
            </Button>
            <Button variant="primary" size="sm"
              type="button"
              onClick={() => setCreating(true)}
              className="inline-flex items-center gap-1.5 px-2.5 py-1.5 font-medium text-accent-fg"
            >
              <Plus size={12} /> {tr('添加仓库', 'Add repo')}
            </Button>
          </div>
        </div>
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-6">
        {err && (
          <div className="mb-4 rounded-lg border border-red-500/40 bg-red-500/5 px-4 py-3 text-sm text-red-300">
            {err}
          </div>
        )}

        <SSHIdentitiesCard refreshKey={credentialsRefresh} />

        {loading ? (
          <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : items.length === 0 ? (
          <div className="flex h-60 flex-col items-center justify-center gap-2 text-zinc-500">
            <GitBranch size={28} className="text-zinc-600" />
            <div className="text-sm">{tr('还没添加仓库', 'No repos added yet')}</div>
            <Button variant="primary" size="sm"
              type="button"
              onClick={() => setCreating(true)}
              className="mt-1 inline-flex items-center gap-1 px-3 py-1.5 font-medium text-accent-fg"
            >
              <Plus size={12} /> {tr('添加仓库', 'Add repo')}
            </Button>
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            {items.map((r) => (
              <RepoCard
                key={r.id}
                repo={r}
                syncing={syncingID === r.id || !!r.syncing}
                onSync={() => void onSync(r.id)}
                onDelete={() => setDeleting(r)}
                onEdit={() => setEditing(r)}
              />
            ))}
          </div>
        )}
      </div>

      {(creating || editing) && (
        <RepoCreator
          repo={editing ?? undefined}
          onClose={() => { setCreating(false); setEditing(null); }}
          onCreated={() => {
            setCreating(false);
            setEditing(null);
            void fetchAll(true);
          }}
        />
      )}
      {deleting && (
        <DeleteRepoDialog
          repo={deleting}
          onClose={() => setDeleting(null)}
          onDone={() => {
            setDeleting(null);
            void fetchAll(true);
          }}
        />
      )}
    </main>
  );
}

function RepoCard({
  repo,
  syncing,
  onSync,
  onDelete,
  onEdit,
}: {
  repo: KnowledgeRepo;
  syncing: boolean;
  onSync: () => void;
  onDelete: () => void;
  onEdit: () => void;
}) {
  const { tr } = useI18n();
  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0 w-full sm:flex-1">
          <h2 className="break-words text-2xl font-semibold tracking-tight text-text">{repositoryName(repo.url)}</h2>
          <Hint content={repo.url}><div className="mt-1 truncate font-mono text-xs text-zinc-500">{repo.url}</div></Hint>
          {repo.description && (
            <p className="mt-2 break-words text-sm text-text-muted">{repo.description}</p>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="outline" size="sm" onClick={onEdit} disabled={syncing}>
            <Pencil size={11} /> {tr('编辑', 'Edit')}
          </Button>
          <Hint content={tr('立即检查更新；文档未变化时跳过索引', 'Check now; skip indexing when documents are unchanged')}><Button variant="outline" size="sm"
            type="button"
            onClick={onSync}
            disabled={syncing}
            className="inline-flex items-center gap-1 px-2 py-1"

          >
            <RefreshCw size={11} className={cn(syncing && 'animate-spin')} />
            {syncing ? tr('同步中…', 'Syncing…') : tr('同步', 'Sync')}
          </Button></Hint>
          <Hint content={tr('移除', 'Remove')}><Button variant="dangerGhost" size="sm"
            type="button"
            onClick={onDelete}

            className="p-1"
          >
            <Trash2 size={11} />
          </Button></Hint>
        </div>
      </div>
      <details className="mt-4 border-t border-border pt-3 text-xs text-text-muted">
        <summary className="w-fit cursor-pointer select-none rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-indigo-500">{tr('同步详情', 'Sync details')}
          <span className="ml-3 inline-flex items-center gap-1.5">
            <span aria-hidden className={cn('h-1.5 w-1.5 rounded-full', repo.last_sync_error ? 'bg-red-500' : repo.source_synced_at && repo.history_complete ? 'bg-emerald-500' : 'bg-zinc-500')} />
            {syncing ? tr('同步中', 'Syncing') : repo.last_sync_error ? tr('同步失败，将自动重试', 'Sync failed; will retry automatically') : repo.source_synced_at && repo.history_complete ? tr('完整历史已同步', 'Full history synced') : tr('等待完整同步', 'Awaiting full sync')}
          </span>
        </summary>
        <div className="mt-3 space-y-2">
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {tr('文档索引分支 ', 'Document indexing ref ')}<span className="font-mono text-zinc-300">{repo.branch}</span>
            {repo.last_synced_at && (
              <>
                {' · '}
                {tr(`上次同步 ${fullDateTime(repo.last_synced_at)}`, `Last sync ${fullDateTime(repo.last_synced_at)}`)}
              </>
            )}
            {!repo.last_synced_at && <span className="ml-2 text-text-muted">{tr('等待重建索引', 'Awaiting reindex')}</span>}
            {repo.last_synced_at && repo.file_count > 0 && (
              <>
                {' · '}
                {tr(`文件 ${repo.file_count}`, `${repo.file_count} files`)}
              </>
            )}
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-text-muted">
            <span>Commit {repo.source_synced_at ? (repo.commit_count ?? 0).toLocaleString() : '—'}</span>
            <span>Tag {repo.source_synced_at ? (repo.tag_count ?? 0).toLocaleString() : '—'}</span>
            <span>{tr('分支', 'Branches')} {repo.source_synced_at ? (repo.branch_count ?? 0).toLocaleString() : '—'}</span>
          </div>
          {repo.source_synced_at && <p className="mt-1 text-xs text-text-faint">
            {tr('源码最近拉取：', 'Source last fetched: ')}{fullDateTime(repo.source_synced_at)}
            {' · '}{tr('数量为本地快照，不代表远端此刻没有新提交。', 'Counts reflect the local snapshot; newer remote commits may exist.')}
          </p>}
        </div>
      </details>
      {repo.last_sync_error && (
        <div className="mt-2 rounded-md border border-red-500/30 bg-red-500/5 px-2 py-1.5 text-[11px] text-red-300">
          <div className="font-medium">
            {tr('上次同步失败', 'Last sync failed')}
            {(() => {
              const hint = gitErrorHint(repo.last_sync_error!, repo.url, tr);
              return hint ? `：${hint}` : '';
            })()}
          </div>
          <details className="mt-1">
            <summary className="cursor-pointer text-red-300/70">{tr('原始输出', 'raw output')}</summary>
            <pre className="mt-1 whitespace-pre-wrap break-all text-[10px] text-red-200/70">{repo.last_sync_error}</pre>
          </details>
        </div>
      )}
    </section>
  );
}

function RepoCreator({ repo, onClose, onCreated }: { repo?: KnowledgeRepo; onClose: () => void; onCreated: () => void }) {
  const { tr } = useI18n();
  const [url, setUrl] = useState(repo?.url ?? '');
  const [branch, setBranch] = useState(repo?.branch ?? 'main');
  const [description, setDescription] = useState(repo?.description ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      const input = { branch: branch.trim() || 'main', description: description.trim() };
      if (repo) await updateRepo(repo.id, input);
      else await createRepo({ url: url.trim(), ...input });
      onCreated();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={() => { if (!submitting) onClose(); }}
      title={repo ? tr('编辑仓库', 'Edit repository') : tr('添加 git 仓库', 'Add git repo')}
      footer={
        <>
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="px-3 py-1.5"
          >
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" size="sm"
            type="button"
            onClick={() => void submit()}
            disabled={submitting || url.trim() === ''}
            className="px-3 py-1.5 font-medium text-accent-fg"
          >
            {submitting ? tr('验证并保存中…', 'Verifying and saving…') : tr('验证并保存', 'Verify and save')}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-xs text-zinc-300">
        <p className="text-text-muted">{tr('保存前验证仓库访问权限与分支/Tag，成功后自动同步并更新文档索引。完整拉取结果可在仓库卡片查看。', 'Access and branch/tag are verified before saving. Sync and document indexing then run automatically; the card shows the full fetch result.')}</p>
        {err && <div role="alert" className="whitespace-pre-wrap break-words rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-red-300">{gitErrorHint(err, url, tr) || tr('验证或保存失败，请检查仓库地址、凭证和分支/Tag。', 'Verification or saving failed. Check the repository URL, credentials and branch/tag.')}<div className="mt-1">{err}</div></div>}
        <Label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('仓库 URL *', 'Repo URL *')}</div>
          <Input
            type="text"
            aria-label={tr('仓库 URL *', 'Repo URL *')}
            value={url}
            disabled={!!repo || submitting}
            maxLength={512}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://github.com/your-org/runbooks.git  /  git@gitlab.company.internal:team/repo.git"
            className="w-full font-mono"
          />
          <div className="mt-1 text-[11px] text-zinc-500">
            {tr(
              'HTTPS（公开仓库）或 SSH（git@host:owner/repo）都行。SSH 私库需要先在上方"凭证 · SSH key"配一条 hosts 匹配的 key。不要把 token 嵌进 URL —— 会被 git argv / 日志 / DB 列泄漏。',
              'HTTPS (public) or SSH (git@host:owner/repo) both work. For SSH private repos, configure a matching SSH key in "Credentials · SSH key" above first. Do NOT embed tokens in the URL — they leak via git argv / logs / DB columns.',
            )}
          </div>
        </Label>
        <Label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('文档索引分支或 Tag', 'Document indexing branch or tag')}</div>
          <Input
            type="text"
            aria-label={tr('文档索引分支或 Tag', 'Document indexing branch or tag')}
            value={branch}
            disabled={submitting}
            maxLength={128}
            onChange={(e) => setBranch(e.target.value)}
            placeholder="main"
            className="w-full font-mono"
          />
        </Label>
        <Label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('说明（可选）', 'Description (optional)')}</div>
          <Input
            type="text"
            aria-label={tr('说明（可选）', 'Description (optional)')}
            value={description}
            disabled={submitting}
            maxLength={512}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={tr('一句话说这个仓库装什么', "One-liner describing what this repo holds")}
            className="w-full"
          />
        </Label>
        <div className="rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-[11px] text-zinc-500">
          {tr('仅索引：', 'Indexed only: ')}<span className="font-mono">.md / .txt / .rst / .yaml / .yml / .toml / .json</span>
          {tr('；忽略 ', '; ignored: ')}<span className="font-mono">.git / vendor / node_modules / dist / build</span>
          {tr('。单文件 ≤256KiB；单仓库 ≤2000 文件。', '. Per-file ≤256 KiB; per-repo ≤2000 files.')}
        </div>
      </div>
    </Modal>
  );
}

function DeleteRepoDialog({ repo, onClose, onDone }: { repo: KnowledgeRepo; onClose: () => void; onDone: () => void }) {
  const { tr } = useI18n();
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await deleteRepo(repo.id);
      onDone();
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
      title={tr('移除仓库', 'Remove repo')}
      footer={
        <>
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5"
          >
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="danger" size="sm"
            type="button"
            onClick={() => void submit()}
            disabled={submitting}
            className="px-3 py-1.5 font-medium"
          >
            {submitting ? tr('删除中…', 'Deleting…') : tr('删除', 'Delete')}
          </Button>
        </>
      }
    >
      <div className="text-xs text-zinc-300">
        {err && <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-red-300">{err}</div>}
        <p>
          {tr('移除 ', 'Remove ')}<span className="font-mono text-zinc-100">{repo.url}</span>?
        </p>
        <p className="mt-2 text-zinc-500">
          {tr('会同时删除所有由本仓库导入的知识文档；本地 clone 也会清掉。', 'All knowledge docs imported from this repo will be deleted, and the local clone removed.')}
        </p>
      </div>
    </Modal>
  );
}

// SSHIdentitiesCard — phase 1. Manages stored SSH private
// keys + the hosts they auth against. Lives in this page so all git
// auth config (HTTPS PAT card above + SSH keys here) is one stop.
function SSHIdentitiesCard({ refreshKey }: { refreshKey: number }) {
  const { confirmAction, dialog } = useDialogs();
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<SSHIdentity[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await listSSHIdentities();
      setItems(r.items ?? []);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh, refreshKey]);

  const onDelete = async (id: number) => {
    if (!(await confirmAction(tr('删除该 SSH 凭证？后续指向其 hosts 的仓库会同步失败。', 'Delete this SSH identity? Subsequent syncs to its hosts will fail.')))) return;
    try {
      await deleteSSHIdentity(id);
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    }
  };

  return (
    <>{dialog}<section className="mb-4 rounded-xl border border-zinc-800 bg-zinc-900/40">
      <button
        type="button"
        onClick={() => { setOpen((v) => !v); if (!open && err) void refresh(); }}
        aria-expanded={open}
        className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left"
      >
        <div className="flex items-center gap-2 text-sm text-zinc-200">
          <KeyRound size={14} className="text-zinc-400" />
          {tr('凭证 · SSH key', 'Credentials · SSH key')}
          <span className="text-[11px] text-zinc-500">
            {loading ? tr('加载中…', 'Loading…') : err ? tr('查询失败', 'Query failed') : items.length > 0
              ? tr(`已配置 ${items.length} 条`, `${items.length} configured`)
              : tr('未配置', 'None')}
          </span>
        </div>
        <span className="text-[11px] text-zinc-500">{open ? tr('收起', 'Hide') : tr('展开', 'Show')}</span>
      </button>
      {open && (
        <div className="space-y-3 border-t border-zinc-800/60 px-4 py-3">
          {err && <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-[11px] text-red-300">{err}</div>}
          {loading ? (
            <div className="text-[11px] text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : err ? null : items.length === 0 ? (
            <div className="text-[11px] text-zinc-500">
              {tr(
                '还没有 SSH 凭证。添加 SSH 风格的仓库（git@host:owner/repo）之前需要先在这里配置一条。',
                'No SSH identities yet. To clone an ssh-style repo (git@host:owner/repo), add one here first.',
              )}
            </div>
          ) : (
            <ul className="space-y-2">
              {items.map((id) => (
                <li
                  key={id.id}
                  className="flex items-center justify-between gap-3 rounded-md border border-zinc-800/60 bg-zinc-950/40 px-3 py-2"
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm text-zinc-100">{id.name}</span>
                      <span className="font-mono text-[10px] text-zinc-500">{id.fingerprint}</span>
                    </div>
                    <div className="mt-0.5 text-[11px] text-zinc-500">
                      hosts:{' '}
                      {id.hosts.length === 0 ? (
                        <span className="text-red-300">{tr('未配置', 'none')}</span>
                      ) : (
                        id.hosts.map((h, idx) => (
                          <span key={h} className="font-mono text-zinc-300">
                            {idx > 0 && ', '}
                            {h}
                          </span>
                        ))
                      )}
                    </div>
                    {id.last_used_at && (
                      <div className="mt-0.5 text-[10px] text-zinc-600">
                        {tr(`上次使用 ${fullDateTime(id.last_used_at)}`, `Last used ${fullDateTime(id.last_used_at)}`)}
                      </div>
                    )}
                  </div>
                  <Button variant="plain" size="sm"
                    type="button"
                    onClick={() => void onDelete(id.id)}
                    className="inline-flex shrink-0 items-center gap-1 px-2 py-1 border border-red-500/40 bg-red-500/10 text-red-300 hover:bg-red-500/20"
                  >
                    <Trash2 size={11} /> {tr('删除', 'Delete')}
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <Button variant="outline" size="sm"
            type="button"
            onClick={() => setAdding(true)}
            className="inline-flex items-center gap-1 px-2.5 py-1.5"
          >
            <Plus size={12} /> {tr('添加 SSH 凭证', 'Add SSH identity')}
          </Button>
          <p className="text-[11px] text-zinc-500">
            {tr(
              '建议为 ongrid 单独生成一对 ed25519 deploy key（无 passphrase），公钥粘到 GitHub/GitLab/Gitea 的 Deploy keys 列表。这里粘私钥。',
              'Recommended: generate a dedicated ed25519 deploy key (no passphrase) for ongrid; paste the public key into the host\'s Deploy keys list; paste the private key here.',
            )}
          </p>
        </div>
      )}
      {adding && (
        <AddSSHIdentityModal
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false);
            void refresh();
          }}
        />
      )}
    </section></>
  );
}

// AddSSHIdentityModal — two-mode form. Mode "generate" is the
// recommended path: manager creates an ed25519 keypair, persists it,
// and shows the public key once for the admin to paste into the host's
// Deploy keys. Mode "paste" is the escape hatch for existing keys that
// already have a public side registered somewhere.
function AddSSHIdentityModal({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: () => void;
}) {
  const { tr } = useI18n();
  const [mode, setMode] = useState<'generate' | 'paste'>('generate');
  const [name, setName] = useState('');
  const [privateKey, setPrivateKey] = useState('');
  const [hosts, setHosts] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // After successful generate, show the resulting public key so the
  // admin can copy it; cleared on close.
  const [generated, setGenerated] = useState<SSHIdentity | null>(null);

  const parseHostsInput = () =>
    hosts.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);

  const canSubmit =
    name.trim() &&
    hosts.trim() &&
    (mode === 'generate' || privateKey.trim()) &&
    !busy;

  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    setErr(null);
    try {
      if (mode === 'generate') {
        const row = await generateSSHIdentity({
          name: name.trim(),
          hosts: parseHostsInput(),
        });
        setGenerated(row);
        // Don't call onSaved yet — admin still needs to copy the
        // public key. onSaved triggers when the dialog is dismissed.
      } else {
        await createSSHIdentity({
          name: name.trim(),
          private_key: privateKey,
          hosts: parseHostsInput(),
        });
        onSaved();
      }
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const closeAndRefresh = () => {
    setGenerated(null);
    onSaved();
  };

  // Post-generate view — show the public key prominently with copy
  // affordance + reminder to paste into the host's Deploy keys list.
  if (generated) {
    return (
      <Modal
        open
        onClose={closeAndRefresh}
        title={tr('SSH 凭证已创建', 'SSH identity created')}
        size="md"
        footer={
          <Button variant="primary" size="sm"
            type="button"
            onClick={closeAndRefresh}
            className="inline-flex items-center px-3 py-1.5 font-medium text-accent-fg"
          >
            {tr('我已复制', 'Copied — done')}
          </Button>
        }
      >
        <div className="space-y-3 text-sm text-zinc-300">
          <p>
            {tr('已生成 ed25519 密钥对：', 'ed25519 keypair generated: ')}
            <span className="font-mono text-zinc-100">{generated.name}</span>
          </p>
          <p className="text-[11px] text-zinc-500">
            {tr(
              '把下面这一行公钥复制粘贴到目标仓库的 Deploy keys 列表（GitHub: 仓库 → Settings → Deploy keys → Add deploy key；GitLab: 项目 → Settings → Repository → Deploy keys）。Read-only 即可。',
              "Copy the public key line below into the target repo's Deploy keys page (GitHub: Repo → Settings → Deploy keys → Add deploy key; GitLab: Project → Settings → Repository → Deploy keys). Read-only is enough.",
            )}
          </p>
          <pre className="select-all whitespace-pre-wrap break-all rounded-md border border-zinc-800 bg-zinc-950/80 px-3 py-2 font-mono text-[11px] text-zinc-100">
            {generated.public_key}
          </pre>
          <p className="text-[11px] text-zinc-500">
            {tr('指纹：', 'Fingerprint: ')}
            <span className="font-mono text-zinc-400">{generated.fingerprint}</span>
          </p>
          <p className="text-[11px] text-amber-300/80">
            {tr(
              '私钥已落库，无法 reveal 出来。如果需要在多台 ongrid 之间共享同一把 key，请用"粘贴现有私钥"模式分别添加。',
              "The private key is stored on the server and cannot be revealed. If you need to share the same key across multiple ongrid deployments, add it via the 'paste existing key' mode on each.",
            )}
          </p>
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('添加 SSH 凭证', 'Add SSH identity')}
      size="md"
      footer={
        <>
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="inline-flex items-center px-3 py-1.5"
          >
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" size="sm"
            type="button"
            disabled={!canSubmit}
            onClick={() => void submit()}
            className="inline-flex items-center gap-1 px-3 py-1.5 font-medium text-accent-fg"
          >
            {busy
              ? tr('处理中…', 'Working…')
              : mode === 'generate'
                ? tr('生成密钥对', 'Generate keypair')
                : tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {/* Mode picker — generate first, paste second; the labelling
            steers admins toward the safer auto-gen flow. */}
        <div className="inline-flex rounded-md border border-zinc-800 bg-zinc-950/40 p-0.5 text-[11px]">
          <Button variant="subtle" size="sm"
            type="button"
            onClick={() => setMode('generate')}
            className={cn(
              'rounded px-2.5 py-1',
              mode === 'generate' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300',
            )}
          >
            {tr('manager 生成（推荐）', 'Generate on manager (recommended)')}
          </Button>
          <Button variant="subtle" size="sm"
            type="button"
            onClick={() => setMode('paste')}
            className={cn(
              'rounded px-2.5 py-1',
              mode === 'paste' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300',
            )}
          >
            {tr('粘贴现有私钥', 'Paste existing key')}
          </Button>
        </div>

        <Label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">{tr('名称', 'Name')}</span>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('如 github-personal、corp-gitlab', 'e.g. github-personal, corp-gitlab')}
            className="w-full"
          />
        </Label>
        <Label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">
            {tr('hosts（空格 / 逗号分隔；支持通配 * ?）', 'hosts (space or comma separated; * ? globs supported)')}
          </span>
          <Input
            value={hosts}
            onChange={(e) => setHosts(e.target.value)}
            placeholder="github.com gitlab.company.internal"
            className="w-full font-mono"
          />
        </Label>
        {mode === 'paste' && (
          <Label className="block">
            <span className="mb-1 block text-[11px] text-zinc-500">
              {tr('私钥（PEM；无 passphrase）', 'Private key (PEM, no passphrase)')}
            </span>
            <Textarea
              value={privateKey}
              onChange={(e) => setPrivateKey(e.target.value)}
              placeholder={`-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----`}
              rows={9}
              className="w-full font-mono"
            />
            <span className="mt-1 block text-[11px] text-zinc-500">
              {tr(
                '建议 ed25519，且不要 passphrase（manager 是无头进程，无法交互输入解锁）。私钥落库后只能删除重建，无法 reveal。',
                'ed25519 recommended, passphrase-less (manager runs headless; cannot prompt for an unlock). Once saved the key cannot be revealed back — rotate by delete + recreate.',
              )}
            </span>
          </Label>
        )}
        {mode === 'generate' && (
          <div className="rounded-md border border-zinc-800/60 bg-zinc-950/40 px-3 py-2 text-[11px] text-zinc-400">
            {tr(
              'manager 将生成一对 ed25519 密钥（无 passphrase）。私钥直接落库，不会显示；公钥在下一步显示给你复制到 Deploy keys。',
              'Manager will generate an ed25519 keypair (no passphrase). The private key stays on the server; the public key will be shown in the next step so you can paste it into Deploy keys.',
            )}
          </div>
        )}
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-[11px] text-red-300">{err}</div>
        )}
      </div>
    </Modal>
  );
}
