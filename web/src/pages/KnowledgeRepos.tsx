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
  RefreshCw,
  Trash2,
} from 'lucide-react';
import { cn } from '@/lib/cn';
import { fullDateTime } from '@/lib/format';
import { Modal } from '@/components/Modal';
import {
  createRepo,
  createSSHIdentity,
  createHTTPSCredential,
  deleteRepo,
  deleteSSHIdentity,
  deleteHTTPSCredential,
  generateSSHIdentity,
  isBuiltinVault,
  listRepos,
  listSSHIdentities,
  listHTTPSCredentials,
  syncRepo,
  updateHTTPSCredential,
  type KnowledgeRepo,
  type SSHIdentity,
  type HTTPSCredential,
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
  if (low.includes('no https credential configured for host=')) {
    const m = raw.match(/no HTTPS credential configured for host=([^;\s]*)/i);
    const host = m?.[1] ?? '';
    return host
      ? tr(
          `私有仓库需要凭证：尚未为 host=${host} 配置 HTTPS 凭证。请在上方『凭证 · HTTPS』卡片添加一条 hosts 匹配的凭证。`,
          `This private repo needs a credential: no HTTPS credential configured for host=${host}. Add one with a matching host in the "Credentials · HTTPS" card above.`,
        )
      : tr(
          '私有仓库需要凭证：尚未为该 host 配置 HTTPS 凭证。请在上方『凭证 · HTTPS』卡片添加一条 hosts 匹配的凭证。',
          'This private repo needs a credential: no HTTPS credential configured for this host. Add one with a matching host in the "Credentials · HTTPS" card above.',
        );
  }
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
  const [deleting, setDeleting] = useState<KnowledgeRepo | null>(null);
  const [syncingID, setSyncingID] = useState<number | null>(null);

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
                '添加的 git 仓库 · sync 后 .md / .yaml / .json 文件会进知识库供 LLM 检索',
                'Added git repos · after sync, .md / .yaml / .json files enter the knowledge base for LLM retrieval',
              )}
            </p>
          </div>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => fetchAll(true)}
              disabled={loading || refreshing}
              className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-40"
            >
              <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
              {tr('刷新', 'Refresh')}
            </button>
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="inline-flex items-center gap-1.5 rounded-md bg-accent px-2.5 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90"
            >
              <Plus size={12} /> {tr('添加仓库', 'Add repo')}
            </button>
          </div>
        </div>
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-6">
        {err && (
          <div className="mb-4 rounded-lg border border-red-500/40 bg-red-500/5 px-4 py-3 text-sm text-red-300">
            {err}
          </div>
        )}

        <HTTPSCredentialsCard />

        <SSHIdentitiesCard />

        {loading ? (
          <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : items.length === 0 ? (
          <div className="flex h-60 flex-col items-center justify-center gap-2 text-zinc-500">
            <GitBranch size={28} className="text-zinc-600" />
            <div className="text-sm">{tr('还没添加仓库', 'No repos added yet')}</div>
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="mt-1 inline-flex items-center gap-1 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90"
            >
              <Plus size={12} /> {tr('添加仓库', 'Add repo')}
            </button>
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            {items.map((r) => (
              <RepoCard
                key={r.id}
                repo={r}
                syncing={syncingID === r.id}
                onSync={() => void onSync(r.id)}
                onDelete={() => setDeleting(r)}
              />
            ))}
          </div>
        )}
      </div>

      {creating && (
        <RepoCreator
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
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
}: {
  repo: KnowledgeRepo;
  syncing: boolean;
  onSync: () => void;
  onDelete: () => void;
}) {
  const { tr } = useI18n();
  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate font-mono text-sm text-zinc-100" title={repo.url}>
            {repo.url}
          </div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {tr('分支 ', 'Branch ')}<span className="font-mono text-zinc-300">{repo.branch}</span>
            {repo.last_synced_at && (
              <>
                {' · '}
                {tr(`上次同步 ${fullDateTime(repo.last_synced_at)}`, `Last sync ${fullDateTime(repo.last_synced_at)}`)}
              </>
            )}
            {repo.file_count > 0 && (
              <>
                {' · '}
                {tr(`文件 ${repo.file_count}`, `${repo.file_count} files`)}
              </>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <button
            type="button"
            onClick={onSync}
            disabled={syncing}
            className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800 disabled:opacity-50"
            title={tr('git pull + 重建索引', 'git pull + rebuild index')}
          >
            <RefreshCw size={11} className={cn(syncing && 'animate-spin')} />
            {syncing ? tr('同步中…', 'Syncing…') : tr('同步', 'Sync')}
          </button>
          <button
            type="button"
            onClick={onDelete}
            title={tr('移除', 'Remove')}
            className="rounded p-1 text-zinc-500 hover:bg-red-900/30 hover:text-red-300"
          >
            <Trash2 size={11} />
          </button>
        </div>
      </div>
      {repo.description && (
        <p className="mt-2 text-xs text-zinc-400">{repo.description}</p>
      )}
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

function RepoCreator({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { tr } = useI18n();
  const [url, setUrl] = useState('');
  const [branch, setBranch] = useState('main');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await createRepo({ url: url.trim(), branch: branch.trim() || 'main', description: description.trim() || undefined });
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
      onClose={onClose}
      title={tr('添加 git 仓库', 'Add git repo')}
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
            onClick={() => void submit()}
            disabled={submitting || url.trim() === ''}
            className="rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
          >
            {submitting ? tr('保存中…', 'Saving…') : tr('保存（保存后再点同步）', 'Save (then click Sync)')}
          </button>
        </>
      }
    >
      <div className="space-y-3 text-xs text-zinc-300">
        {err && <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-red-300">{err}</div>}
        <label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('仓库 URL *', 'Repo URL *')}</div>
          <input
            type="text"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://github.com/your-org/runbooks.git  /  git@gitlab.company.internal:team/repo.git"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
          <div className="mt-1 text-[11px] text-zinc-500">
            {tr(
              'HTTPS（公开仓库）或 SSH（git@host:owner/repo）都行。SSH 私库需要先在上方"凭证 · SSH key"配一条 hosts 匹配的 key。不要把 token 嵌进 URL —— 会被 git argv / 日志 / DB 列泄漏。',
              'HTTPS (public) or SSH (git@host:owner/repo) both work. For SSH private repos, configure a matching SSH key in "Credentials · SSH key" above first. Do NOT embed tokens in the URL — they leak via git argv / logs / DB columns.',
            )}
            {' '}
            {tr(
              'HTTPS 私库需先在上方『凭证 · HTTPS』配一条 hosts 匹配的凭证；同样不要把 token 嵌进 URL。',
              'For private HTTPS repos, configure a matching credential in "Credentials · HTTPS" above first; likewise never embed the token in the URL.',
            )}
          </div>
        </label>
        <label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('分支', 'Branch')}</div>
          <input
            type="text"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            placeholder="main"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('说明（可选）', 'Description (optional)')}</div>
          <input
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={tr('一句话说这个仓库装什么', "One-liner describing what this repo holds")}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </label>
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
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={submitting}
            className="rounded-md bg-red-500 px-3 py-1.5 text-xs font-medium text-white hover:bg-red-600 disabled:opacity-50"
          >
            {submitting ? tr('删除中…', 'Deleting…') : tr('删除', 'Delete')}
          </button>
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
function SSHIdentitiesCard() {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<SSHIdentity[]>([]);
  const [loading, setLoading] = useState(false);
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
    if (open) void refresh();
  }, [open, refresh]);

  const onDelete = async (id: number) => {
    if (!window.confirm(tr('删除该 SSH 凭证？后续指向其 hosts 的仓库会同步失败。', 'Delete this SSH identity? Subsequent syncs to its hosts will fail.'))) return;
    try {
      await deleteSSHIdentity(id);
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    }
  };

  return (
    <section className="mb-4 rounded-xl border border-zinc-800 bg-zinc-900/40">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left"
      >
        <div className="flex items-center gap-2 text-sm text-zinc-200">
          <KeyRound size={14} className="text-zinc-400" />
          {tr('凭证 · SSH key', 'Credentials · SSH key')}
          <span className="text-[11px] text-zinc-500">
            {items.length > 0
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
          ) : items.length === 0 ? (
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
                  className="flex items-start justify-between gap-3 rounded-md border border-zinc-800/60 bg-zinc-950/40 px-3 py-2"
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
                  <button
                    type="button"
                    onClick={() => void onDelete(id.id)}
                    className="inline-flex shrink-0 items-center gap-1 rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1 text-[11px] text-red-300 hover:bg-red-500/20"
                  >
                    <Trash2 size={11} /> {tr('删除', 'Delete')}
                  </button>
                </li>
              ))}
            </ul>
          )}
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-200 hover:bg-zinc-800"
          >
            <Plus size={12} /> {tr('添加 SSH 凭证', 'Add SSH identity')}
          </button>
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
    </section>
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
          <button
            type="button"
            onClick={closeAndRefresh}
            className="inline-flex items-center rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white"
          >
            {tr('我已复制', 'Copied — done')}
          </button>
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
          <button
            type="button"
            onClick={onClose}
            className="inline-flex items-center rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-200 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            disabled={!canSubmit}
            onClick={() => void submit()}
            className="inline-flex items-center gap-1 rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-40"
          >
            {busy
              ? tr('处理中…', 'Working…')
              : mode === 'generate'
                ? tr('生成密钥对', 'Generate keypair')
                : tr('保存', 'Save')}
          </button>
        </>
      }
    >
      <div className="space-y-3">
        {/* Mode picker — generate first, paste second; the labelling
            steers admins toward the safer auto-gen flow. */}
        <div className="inline-flex rounded-md border border-zinc-800 bg-zinc-950/40 p-0.5 text-[11px]">
          <button
            type="button"
            onClick={() => setMode('generate')}
            className={cn(
              'rounded px-2.5 py-1',
              mode === 'generate' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300',
            )}
          >
            {tr('manager 生成（推荐）', 'Generate on manager (recommended)')}
          </button>
          <button
            type="button"
            onClick={() => setMode('paste')}
            className={cn(
              'rounded px-2.5 py-1',
              mode === 'paste' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300',
            )}
          >
            {tr('粘贴现有私钥', 'Paste existing key')}
          </button>
        </div>

        <label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">{tr('名称', 'Name')}</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('如 github-personal、corp-gitlab', 'e.g. github-personal, corp-gitlab')}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">
            {tr('hosts（空格 / 逗号分隔；支持通配 * ?）', 'hosts (space or comma separated; * ? globs supported)')}
          </span>
          <input
            value={hosts}
            onChange={(e) => setHosts(e.target.value)}
            placeholder="github.com gitlab.company.internal"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        {mode === 'paste' && (
          <label className="block">
            <span className="mb-1 block text-[11px] text-zinc-500">
              {tr('私钥（PEM；无 passphrase）', 'Private key (PEM, no passphrase)')}
            </span>
            <textarea
              value={privateKey}
              onChange={(e) => setPrivateKey(e.target.value)}
              placeholder={`-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----`}
              rows={9}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-[11px] text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
            />
            <span className="mt-1 block text-[11px] text-zinc-500">
              {tr(
                '建议 ed25519，且不要 passphrase（manager 是无头进程，无法交互输入解锁）。私钥落库后只能删除重建，无法 reveal。',
                'ed25519 recommended, passphrase-less (manager runs headless; cannot prompt for an unlock). Once saved the key cannot be revealed back — rotate by delete + recreate.',
              )}
            </span>
          </label>
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

// HTTPSCredentialsCard — manages stored HTTPS PAT credentials + the
// host patterns they cover. Mirrors SSHIdentitiesCard; goes in the
// same credentials section of the page so operators configure all
// git auth in one spot.
function HTTPSCredentialsCard() {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<HTTPSCredential[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<HTTPSCredential | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await listHTTPSCredentials();
      setItems(r.items ?? []);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (open) void refresh();
  }, [open, refresh]);

  const onDelete = async (id: number) => {
    if (!window.confirm(tr('删除该 HTTPS 凭证？后续指向其 hosts 的仓库会同步失败。', 'Delete this HTTPS credential? Subsequent syncs to its hosts will fail.'))) return;
    try {
      await deleteHTTPSCredential(id);
      await refresh();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    }
  };

  return (
    <section className="mb-4 rounded-xl border border-zinc-800 bg-zinc-900/40">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left"
      >
        <div className="flex items-center gap-2 text-sm text-zinc-200">
          <KeyRound size={14} className="text-zinc-400" />
          {tr('凭证 · HTTPS', 'Credentials · HTTPS')}
          <span className="text-[11px] text-zinc-500">
            {items.length > 0
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
          ) : items.length === 0 ? (
            <div className="text-[11px] text-zinc-500">
              {tr(
                '还没有 HTTPS 凭证。私有 HTTPS 仓库（https://gitlab.example.com/...）需要先在这里配置一条 hosts 匹配的凭证。',
                'No HTTPS credentials yet. A private HTTPS repo (https://gitlab.example.com/...) needs a matching credential configured here first.',
              )}
            </div>
          ) : (
            <ul className="space-y-2">
              {items.map((cred) => (
                <li
                  key={cred.id}
                  className="flex items-start justify-between gap-3 rounded-md border border-zinc-800/60 bg-zinc-950/40 px-3 py-2"
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm text-zinc-100">{cred.name}</span>
                      <span className="font-mono text-[10px] text-zinc-500">{cred.username}</span>
                      {cred.has_token ? (
                        <span className="rounded-sm bg-emerald-500/15 px-1.5 py-0.5 text-[10px] text-emerald-300">
                          {tr('已配置', 'configured')}
                        </span>
                      ) : (
                        <span className="rounded-sm bg-zinc-700/60 px-1.5 py-0.5 text-[10px] text-zinc-400">
                          {tr('未配置', 'none')}
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 text-[11px] text-zinc-500">
                      hosts:{' '}
                      {cred.hosts.length === 0 ? (
                        <span className="text-red-300">{tr('未配置', 'none')}</span>
                      ) : (
                        cred.hosts.map((h, idx) => (
                          <span key={h} className="font-mono text-zinc-300">
                            {idx > 0 && ', '}
                            {h}
                          </span>
                        ))
                      )}
                    </div>
                    {cred.last_used_at && (
                      <div className="mt-0.5 text-[10px] text-zinc-600">
                        {tr(`上次使用 ${fullDateTime(cred.last_used_at)}`, `Last used ${fullDateTime(cred.last_used_at)}`)}
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 items-center gap-1.5">
                    <button
                      type="button"
                      onClick={() => { setEditing(cred); setAdding(true); }}
                      className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
                    >
                      {tr('编辑', 'Edit')}
                    </button>
                    <button
                      type="button"
                      onClick={() => void onDelete(cred.id)}
                      className="inline-flex items-center gap-1 rounded-md border border-red-500/40 bg-red-500/10 px-2 py-1 text-[11px] text-red-300 hover:bg-red-500/20"
                    >
                      <Trash2 size={11} /> {tr('删除', 'Delete')}
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          )}
          <button
            type="button"
            onClick={() => { setEditing(null); setAdding(true); }}
            className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-200 hover:bg-zinc-800"
          >
            <Plus size={12} /> {tr('添加 HTTPS 凭证', 'Add HTTPS credential')}
          </button>
        </div>
      )}
      {adding && (
        <AddHTTPSCredentialModal
          editing={editing}
          onClose={() => { setAdding(false); setEditing(null); }}
          onSaved={() => {
            setAdding(false);
            setEditing(null);
            void refresh();
          }}
        />
      )}
    </section>
  );
}

// AddHTTPSCredentialModal — single-form modal for creating and editing
// HTTPS PAT credentials. No generate/paste dual-mode (unlike SSH).
// Token is write-only: type=password + autoComplete=off; editing mode
// leaves token blank (no pre-fill — HTTPSCredential type has no token
// field, so there is nothing to pre-fill).
function AddHTTPSCredentialModal({
  onClose,
  onSaved,
  editing,
}: {
  onClose: () => void;
  onSaved: () => void;
  editing?: HTTPSCredential | null;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState(editing?.name ?? '');
  const [hosts, setHosts] = useState(editing?.hosts.join(', ') ?? '');
  const [username, setUsername] = useState(editing?.username ?? 'oauth2');
  // Token is never pre-filled — HTTPSCredential DTO has no token field.
  // In edit mode, leaving blank means "keep existing token" (server semantics).
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const parseHostsInput = () =>
    hosts.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean);

  const isEditing = editing != null;

  const canSubmit =
    name.trim() &&
    hosts.trim() &&
    (isEditing || token.trim()) &&  // new: token required; edit: optional
    !busy;

  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    setErr(null);
    try {
      const parsed = parseHostsInput();
      const uname = username.trim() || 'oauth2';
      if (isEditing) {
        await updateHTTPSCredential(editing.id, {
          name: name.trim(),
          hosts: parsed,
          username: uname,
          token,  // empty string = keep existing (nil-means-keep on server)
        });
      } else {
        await createHTTPSCredential({
          name: name.trim(),
          hosts: parsed,
          username: uname,
          token,
        });
      }
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={isEditing ? tr('编辑 HTTPS 凭证', 'Edit HTTPS credential') : tr('添加 HTTPS 凭证', 'Add HTTPS credential')}
      size="md"
      footer={
        <>
          <button
            type="button"
            onClick={onClose}
            className="inline-flex items-center rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-200 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            disabled={!canSubmit}
            onClick={() => void submit()}
            className="inline-flex items-center gap-1 rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-40"
          >
            {busy ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </button>
        </>
      }
    >
      <div className="space-y-3">
        <label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">{tr('名称', 'Name')}</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('如 gitlab-org、github-org', 'e.g. gitlab-org, github-org')}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">
            {tr('hosts（空格 / 逗号分隔；支持通配 * ?）', 'hosts (space or comma separated; * ? globs supported)')}
          </span>
          <input
            value={hosts}
            onChange={(e) => setHosts(e.target.value)}
            placeholder="gitlab.example.com github.com"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">
            {tr('用户名（GitLab PAT 通常填 oauth2）', 'Username (GitLab PAT typically uses oauth2)')}
          </span>
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="oauth2"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-[11px] text-zinc-500">
            {tr('Token（PAT）', 'Token (PAT)')}
          </span>
          <input
            type="password"
            autoComplete="off"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={isEditing ? tr('留空 = 不修改', 'Leave blank to keep existing') : tr('粘贴 Personal Access Token', 'Paste Personal Access Token')}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
          />
          <span className="mt-1 block text-[11px] text-zinc-500">
            {isEditing
              ? tr('留空 = 不修改已存 token；填入新值 = 轮换。', 'Leave blank to keep the stored token; enter a new value to rotate.')
              : tr('Personal Access Token（PAT），只写不读；保存后不回显。', 'Personal Access Token (PAT); write-only, never echoed back after save.')}
          </span>
        </label>
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-[11px] text-red-300">{err}</div>
        )}
      </div>
    </Modal>
  );
}
