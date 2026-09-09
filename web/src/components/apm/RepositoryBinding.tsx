import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { GitBranch } from 'lucide-react';
import { deleteRepositoryBinding, getRepositoryBinding, saveRepositoryBinding, type RepositoryBinding, type ServiceIdentity } from '@/api/apm';
import { isBuiltinVault, listRepos, repositoryName, type KnowledgeRepo } from '@/api/knowledge';
import { Modal } from '@/components/Modal';
import { Button, Input, Label, Select } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export function RepositoryBindingButton({ identity, canEdit }: { identity: ServiceIdentity; canEdit: boolean }) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const [binding, setBinding] = useState<RepositoryBinding | null>(null);
  const [repos, setRepos] = useState<KnowledgeRepo[]>([]);
  const [repoID, setRepoID] = useState('');
  const [directory, setDirectory] = useState('');
  const [pattern, setPattern] = useState('{version}');
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState('');
  const [retry, setRetry] = useState(0);
  const scope = JSON.stringify(identity);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setLoaded(false);
    setError('');
    Promise.all([getRepositoryBinding(JSON.parse(scope)), open ? listRepos() : Promise.resolve(null)]).then(([value, data]) => {
      if (!active) return;
      setBinding(value);
      setRepoID(value?.repo_id || '');
      setDirectory(value?.source_directory || '');
      setPattern(value?.tag_pattern || '{version}');
      if (data) setRepos((data.items || []).filter((repo) => !isBuiltinVault(repo)));
      setLoaded(true);
    }).catch((e: Error) => { if (active) setError(e.message); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [open, scope, retry]);

  async function save(remove = false) {
    setBusy(true);
    setError('');
    try {
      if (remove) { await deleteRepositoryBinding(identity); setBinding(null); }
      else setBinding(await saveRepositoryBinding(identity, { repo_id: repoID, source_directory: directory, tag_pattern: pattern }));
      setOpen(false);
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  }

  return <>
    <Button variant="subtle" size="sm" className="max-w-full" title={binding?.repo_url} onClick={() => setOpen(true)}><GitBranch size={14} className="shrink-0" /><span className="truncate">{binding?.repo_missing ? tr('仓库已移除', 'Repository removed') : binding?.repo_url ? repositoryName(binding.repo_url) : error && !open ? tr('仓库加载失败', 'Repository unavailable') : tr('绑定仓库', 'Bind repository')}</span></Button>
    <Modal open={open} onClose={() => { if (!busy) setOpen(false); }} title={tr('绑定源码仓库', 'Bind source repository')} size="md" footer={<>
      {canEdit && binding && <Button variant="dangerGhost" disabled={busy || !loaded} onClick={() => void save(true)}>{tr('解除绑定', 'Unbind')}</Button>}
      <Button variant="outline" disabled={busy} onClick={() => setOpen(false)}>{tr('取消', 'Cancel')}</Button>
      {canEdit && <Button disabled={busy || !loaded || !repos.some((repo) => String(repo.id) === repoID)} onClick={() => void save()}>{busy ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}</Button>}
    </>}>
      <div className="space-y-4 text-sm">
        <div><p className="font-medium">{identity.service_name}</p><p className="text-xs text-zinc-500">{identity.environment || tr('未设置环境', 'Unset environment')} / {identity.service_namespace || tr('未设置命名空间', 'Unset namespace')}</p></div>
        {loading && <p role="status" className="text-zinc-500">{tr('加载中…', 'Loading…')}</p>}
        {error && <div role="alert" className="text-red-500">{error}{!loaded && <Button variant="ghost" size="sm" onClick={() => setRetry((value) => value + 1)}>{tr('重试', 'Retry')}</Button>}</div>}
        {loaded && <>
          {binding?.repo_missing && <p role="alert" className="text-amber-500">{tr('原仓库已移除，请重新选择或解除绑定。', 'The bound repository was removed. Select another or unbind.')}</p>}
          <Label className="block space-y-1"><span>{tr('代码仓库', 'Repository')}</span><Select className="w-full max-w-full" aria-label={tr('代码仓库', 'Repository')} value={repoID} onValueChange={setRepoID} disabled={!canEdit || busy} options={[{ value: '', label: tr('请选择仓库', 'Select a repository') }, ...repos.map((repo) => ({ value: String(repo.id), label: repositoryName(repo.url) + (repos.filter((item) => repositoryName(item.url) === repositoryName(repo.url)).length > 1 ? ` (#${repo.id})` : '') }))]} /></Label>
          {repoID && <p className="break-all font-mono text-xs text-text-muted">{repos.find((repo) => String(repo.id) === repoID)?.url}</p>}
          {repos.length === 0 && <p className="text-zinc-500">{tr('还没有可绑定的仓库。', 'No repositories are available.')}</p>}
          <Link to="/knowledge/repos" className="inline-block text-xs text-indigo-500 hover:underline">{tr('管理代码仓库', 'Manage repositories')}</Link>
          <Label className="block space-y-1"><span>{tr('源码目录', 'Source directory')}</span><Input aria-label={tr('源码目录', 'Source directory')} value={directory} onChange={(e) => setDirectory(e.target.value)} disabled={!canEdit || busy} placeholder={tr('留空表示仓库根目录', 'Empty means repository root')} maxLength={512} /></Label>
          <Label className="block space-y-1"><span>{tr('版本 Tag 规则', 'Version tag pattern')}</span><Input aria-label={tr('版本 Tag 规则', 'Version tag pattern')} value={pattern} onChange={(e) => setPattern(e.target.value)} disabled={!canEdit || busy} placeholder="{version}" maxLength={128} /></Label>
          <p className="text-xs text-zinc-500">{tr('用 {version} 代入出错实例的 service.version，例如 v{version} → v1.0.0。对应 Tag 需已同步，缺失时停止源码定位。', 'Replace {version} with the failing instance’s service.version, e.g. v{version} → v1.0.0. The tag must be synced; source lookup stops when it is missing.')}</p>
          {!canEdit && <p className="text-xs text-zinc-500">{tr('仅管理员可修改绑定。', 'Only administrators can edit this binding.')}</p>}
        </>}
      </div>
    </Modal>
  </>;
}
