import { useCallback, useEffect, useMemo, useState } from 'react';
import { ArrowLeft, BookOpen, Eye, FolderSync, ListTodo, Loader2, Play, RefreshCw, Trash2 } from 'lucide-react';
import { ApiError } from '@/api/client';
import { localizedPath } from '@/api/knowledge';
import { Modal } from '@/components/Modal';
import { Button, Card, EmptyState } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import { compileLLMWiki, deleteLLMWikiNode, listLLMWikiTree, syncLLMWikiSources } from './api';
import { LLMWikiJobs } from './LLMWikiJobs';
import { LLMWikiTree } from './LLMWikiTree';
import { LLMWikiFileViewer } from './LLMWikiViewer';
import { type LLMWikiNode } from './types';

type WikiLayer = LLMWikiNode['layer'];

export function LLMWikiNavigation({ active, onOpen, onDocumentCount, onChanged, onJobsChanged, activeDirectory, activeLayer, jobsRefreshKey, refreshKey }: { active: boolean; onOpen: (directory: LLMWikiNode | null, layer: 'all' | WikiLayer) => void; onDocumentCount: (count: number) => void; onChanged: () => void; onJobsChanged: () => void; activeDirectory: LLMWikiNode | null; activeLayer: 'all' | WikiLayer; jobsRefreshKey: number; refreshKey: number }) {
  const { tr } = useI18n();
  const [nodes, setNodes] = useState<LLMWikiNode[]>([]);
  const [documentCounts, setDocumentCounts] = useState<Partial<Record<WikiLayer, number>>>({});
  const [showJobs, setShowJobs] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [compiling, setCompiling] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const results = await Promise.all([listLLMWikiTree('raw'), listLLMWikiTree('wiki')]);
      setNodes(results.flatMap((result) => result.items ?? []));
      const counts = results.map((result) => {
        const fileCount = (result.items ?? []).filter((node) => node.kind === 'file').length;
        return fileCount > 0 ? fileCount : (result.document_count ?? 0);
      });
      setDocumentCounts({ raw: counts[0], wiki: counts[1] });
      onDocumentCount(counts[1]);
    } catch {
      // Wiki is optional; the traditional Knowledge page remains usable when
      // its storage or worker is temporarily unavailable.
    }
  }, [onDocumentCount]);
  useEffect(() => { void load(); }, [load, refreshKey]);
  const rootSelected = active && activeDirectory === null && activeLayer === 'all';
  return (
    <div>
      <div className="flex items-center gap-1">
        <Button variant="subtle" size="sm" type="button" aria-current={rootSelected ? 'page' : undefined} onClick={() => onOpen(null, 'all')} className={cn('flex min-w-0 flex-1 items-center justify-between gap-2 rounded-md px-2 py-1.5 text-xs', rootSelected ? 'bg-zinc-800' : 'hover:bg-zinc-900')}>
          <span className="flex items-center gap-1.5 font-semibold text-indigo-300"><BookOpen size={12} /> LLM Wiki</span>
          {(documentCounts.raw ?? 0) + (documentCounts.wiki ?? 0) > 0 && <span className="text-[10px] text-zinc-500">{documentCounts.wiki ?? 0}</span>}
        </Button>
        <div className="flex shrink-0 items-center gap-0.5 pr-1">
          <Hint content={tr('从组织知识库同步', 'Sync from organization knowledge')}>
            <Button variant="subtle" size="sm" type="button" aria-label={tr('同步组织知识库', 'Sync organization knowledge')} disabled={syncing} onClick={() => { setSyncing(true); setSyncError(null); void syncLLMWikiSources().then(onChanged).catch((error: Error) => setSyncError(error.message)).finally(() => setSyncing(false)); }} className="p-1">
              {syncing ? <Loader2 size={13} className="animate-spin" /> : <FolderSync size={13} />}
            </Button>
          </Hint>
          <Hint content={tr('刷新 Wiki', 'Refresh Wiki')}>
            <Button variant="subtle" size="sm" type="button" aria-label={tr('刷新 Wiki', 'Refresh Wiki')} onClick={onChanged} className="p-1"><RefreshCw size={13} /></Button>
          </Hint>
          <Hint content={tr('编译全部', 'Compile all')}>
            <Button variant="subtle" size="sm" type="button" aria-label={tr('编译全部', 'Compile all')} disabled={compiling} onClick={() => { setCompiling(true); setShowJobs(true); setSyncError(null); void compileLLMWiki().then(() => { onJobsChanged(); onChanged(); }).catch((error: Error) => setSyncError(error.message)).finally(() => setCompiling(false)); }} className="p-1">
              {compiling ? <Loader2 size={13} className="animate-spin" /> : <Play size={13} />}
            </Button>
          </Hint>
          <Hint content={tr('编译任务', 'Compile jobs')}>
            <Button variant="subtle" size="sm" type="button" aria-label={tr('编译任务', 'Compile jobs')} aria-expanded={showJobs} onClick={() => setShowJobs((current) => !current)} className={cn('p-1', showJobs && 'bg-zinc-800')}>
              <ListTodo size={13} />
            </Button>
          </Hint>
        </div>
      </div>
      {syncError && <div className="mx-2 mt-1 text-[10px] text-red-300">{syncError}</div>}
      <div className="mt-0.5">
        <LLMWikiTree layers={['raw', 'wiki']} nodes={nodes} documentCounts={documentCounts} activeDirectory={activeDirectory} activeLayer={activeLayer} onSelectDirectory={(dir, ly) => onOpen(dir, ly)} />
      </div>
      {showJobs && <LLMWikiJobs refreshKey={jobsRefreshKey} onPublished={onChanged} />}
    </div>
  );
}

export function LLMWikiPane({ onExit, hideSidebar, initialDirectory, initialLayer, externalRefreshKey = 0 }: { onExit?: () => void; hideSidebar?: boolean; initialDirectory?: LLMWikiNode | null; initialLayer?: 'all' | WikiLayer; externalRefreshKey?: number }) {
  const { tr } = useI18n();
  const [nodes, setNodes] = useState<LLMWikiNode[]>([]);
  const [documentCounts, setDocumentCounts] = useState<Partial<Record<WikiLayer, number>>>({});
  const [directory, setDirectory] = useState<LLMWikiNode | null>(initialDirectory ?? null);
  const [layer, setLayer] = useState<'all' | WikiLayer>(initialLayer ?? 'all');
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [viewing, setViewing] = useState<LLMWikiNode | null>(null);
  const [deleting, setDeleting] = useState<LLMWikiNode | null>(null);
  const [compilingIds, setCompilingIds] = useState<Set<string>>(new Set());

  useEffect(() => {
    setDirectory(initialDirectory ?? null);
    setLayer(initialLayer ?? 'all');
  }, [initialDirectory, initialLayer]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const results = await Promise.all([
        listLLMWikiTree('raw'),
        listLLMWikiTree('wiki'),
      ]);
      setNodes(results.flatMap((result) => result.items ?? []));
      setDocumentCounts({ raw: results[0].document_count, wiki: results[1].document_count });
      setError(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load, refreshKey, externalRefreshKey]);

  const refresh = useCallback(() => {
    setRefreshing(true);
    setRefreshKey((key) => key + 1);
  }, []);

  const compileFile = useCallback(async (file: LLMWikiNode) => {
    if (!file.source_id) return;
    setCompilingIds((prev) => new Set(prev).add(file.id));
    try {
      await compileLLMWiki(false, [file.source_id!]);
      refresh();
    } catch {
      // compilation triggered; status shown in jobs panel
    } finally {
      setCompilingIds((prev) => {
        const next = new Set(prev);
        next.delete(file.id);
        return next;
      });
    }
  }, [refresh]);

  const files = useMemo(() => nodes.filter((node) => node.kind === 'file'), [nodes]);
  // 收集当前目录及全部子目录 ID，目录视图据此展示整棵子树里的文件。
  const descendantIds = useMemo(() => {
    if (!directory) return null;
    const folders = nodes.filter((node) => node.kind === 'folder' && node.layer === directory.layer);
    const byParent = new Map<string, typeof folders>();
    for (const f of folders) {
      const list = byParent.get(f.parent_id) ?? [];
      list.push(f);
      byParent.set(f.parent_id, list);
    }
    const ids = new Set<string>([directory.id]);
    const stack = [directory.id];
    while (stack.length > 0) {
      const pid = stack.pop()!;
      for (const child of byParent.get(pid) ?? []) {
        if (!ids.has(child.id)) {
          ids.add(child.id);
          stack.push(child.id);
        }
      }
    }
    return ids;
  }, [nodes, directory]);
  const visibleFiles = useMemo(() => {
    if (directory && descendantIds) return files.filter((file) => file.layer === directory.layer && descendantIds.has(file.parent_id));
    return layer === 'all' ? files : files.filter((file) => file.layer === layer);
  }, [directory, descendantIds, files, layer]);

  // 外层知识库页面已经提供统一文件树时，这里只渲染文件列表与弹窗。
  if (hideSidebar) {
    return (
      <>
        <LLMWikiFileList
          files={visibleFiles}
          directory={directory}
          loading={loading}
          error={error}
          onViewFile={setViewing}
          onDeleteFile={setDeleting}
          onCompileFile={compileFile}
        />
        {viewing && <LLMWikiFileViewer file={viewing} onClose={() => setViewing(null)} onOpenFile={setViewing} onDelete={() => { setViewing(null); setDeleting(viewing); }} />}
        {deleting && <DeleteLLMWikiFileDialog file={deleting} onClose={() => setDeleting(null)} onDone={() => { setDeleting(null); refresh(); }} />}
      </>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 overflow-hidden">
      <aside className="w-64 shrink-0 overflow-y-auto border-r border-zinc-800/60 px-2 py-3">
        <div className="mb-2 flex items-center justify-between px-2">
          <div className="flex items-center gap-2 text-xs font-medium text-zinc-100">{onExit && <button type="button" onClick={onExit} aria-label={tr('返回知识库', 'Back to knowledge')} title={tr('返回知识库', 'Back to knowledge')} className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"><ArrowLeft size={13} /></button>}LLM Wiki</div>
          <div className="flex items-center gap-1">
            <button type="button" aria-label={tr('刷新 Wiki', 'Refresh Wiki')} onClick={refresh} disabled={refreshing} title={tr('刷新 Wiki', 'Refresh Wiki')} className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 disabled:opacity-50"><RefreshCw size={13} className={cn(refreshing && 'animate-spin')} /></button>
          </div>
        </div>
        <LLMWikiTree nodes={nodes} documentCounts={documentCounts} activeDirectory={directory} activeLayer={layer} onSelectDirectory={(next, nextLayer) => { setDirectory(next); setLayer(nextLayer); }} />
        <LLMWikiJobs refreshKey={refreshKey} onPublished={refresh} />
      </aside>
      <div className="min-w-0 flex-1 overflow-y-auto px-6 py-6">
        <LLMWikiFileList
          files={visibleFiles}
          directory={directory}
          loading={loading}
          error={error}
          onViewFile={setViewing}
          onDeleteFile={setDeleting}
          onCompileFile={compileFile}
        />
      </div>
      {viewing && <LLMWikiFileViewer file={viewing} onClose={() => setViewing(null)} onOpenFile={setViewing} onDelete={() => { setViewing(null); setDeleting(viewing); }} />}
      {deleting && <DeleteLLMWikiFileDialog file={deleting} onClose={() => setDeleting(null)} onDone={() => { setDeleting(null); refresh(); }} />}
    </div>
  );
}

function LLMWikiFileList({
  files,
  directory,
  loading,
  error,
  onViewFile,
  onDeleteFile,
  onCompileFile,
}: {
  files: LLMWikiNode[];
  directory: LLMWikiNode | null;
  loading: boolean;
  error: string | null;
  onViewFile: (file: LLMWikiNode) => void;
  onDeleteFile: (file: LLMWikiNode) => void;
  onCompileFile: (file: LLMWikiNode) => void;
}) {
  const { tr } = useI18n();
  if (loading) return <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>;
  if (error) return <div className="rounded-md border border-red-500/30 bg-red-500/5 px-3 py-2 text-xs text-red-300">{error}</div>;
  if (files.length === 0) return <EmptyState icon={BookOpen} title={directory ? tr('此目录暂无文件', 'No files in this directory') : tr('LLM Wiki 还没有内容', 'LLM Wiki has no content yet')} />;
  return <div className="flex flex-col gap-1.5">{files.map((file) => <WikiFileCard key={file.id} file={file} onView={onViewFile} onDelete={onDeleteFile} onCompile={onCompileFile} />)}</div>;
}

function WikiFileCard({ file, onView, onDelete, onCompile }: { file: LLMWikiNode; onView: (file: LLMWikiNode) => void; onDelete: (file: LLMWikiNode) => void; onCompile: (file: LLMWikiNode) => void }) {
  const { tr } = useI18n();
  const label = file.layer === 'raw' ? 'Raw' : 'Wiki';
  return <Card key={file.id} compact onClick={() => onView(file)} className="flex cursor-pointer flex-col py-2.5 transition-colors hover:bg-zinc-800/40"><div className="flex items-center justify-between gap-3"><div className="min-w-0"><div className="truncate text-sm font-medium text-zinc-100">{file.layer === 'raw' ? file.name : file.name.replace(/\.md$/i, '')}</div><div className="mt-0.5 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500"><span className={file.layer === 'raw' ? 'text-amber-300' : 'text-sky-300'}>{label}</span><span className="rounded bg-zinc-800/60 px-1.5 py-0.5 font-normal normal-case text-zinc-300">{file.layer}</span></div><div className="mt-1 truncate font-mono text-[10px] text-zinc-600">{localizedPath(file.relative_path)}</div></div><div className="flex shrink-0 items-center gap-1"><Eye size={15} className="text-zinc-500" />{file.layer === 'raw' && <button type="button" onClick={(event) => { event.stopPropagation(); onCompile(file); }} aria-label={tr(`编译 ${file.name}`, `Compile ${file.name}`)} title={tr('编译此文件', 'Compile this file')} className="rounded p-1 text-zinc-500 hover:bg-indigo-900/30 hover:text-indigo-300"><Play size={13} /></button>}{file.layer === 'raw' && <button type="button" onClick={(event) => { event.stopPropagation(); onDelete(file); }} aria-label={tr(`删除 ${file.name}`, `Delete ${file.name}`)} title={tr('删除 Raw 文件', 'Delete raw file')} className="rounded p-1 text-zinc-500 hover:bg-red-900/30 hover:text-red-300"><Trash2 size={13} /></button>}</div></div></Card>;
}

function DeleteLLMWikiFileDialog({ file, onClose, onDone }: { file: LLMWikiNode; onClose: () => void; onDone: () => void }) {
  const { tr } = useI18n();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submit = async () => {
    setSubmitting(true);
    setError(null);
    try {
      await deleteLLMWikiNode(file.id);
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };
  return <Modal open onClose={onClose} title={tr(`删除 ${file.name}`, `Delete ${file.name}`)} footer={<><button type="button" onClick={onClose} className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800">{tr('取消', 'Cancel')}</button><button type="button" onClick={() => void submit()} disabled={submitting} className="rounded-md bg-red-500 px-3 py-1.5 text-xs font-medium text-white hover:bg-red-600 disabled:opacity-50">{submitting ? tr('删除中…', 'Deleting…') : tr('确认删除', 'Delete')}</button></>}><div className="text-xs text-zinc-300">{error && <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-red-300">{error}</div>}<p>{tr('删除 Raw 文件后，当前 Wiki 构建与搜索索引也会失效，需要重新编译。此操作不可恢复。', 'Deleting this raw file also invalidates the current Wiki build and search index. A new compile is required. This cannot be undone.')}</p><p className="mt-2 font-mono text-[11px] text-zinc-500">{localizedPath(file.relative_path)}</p></div></Modal>;
}
