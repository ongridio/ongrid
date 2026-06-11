// Knowledge page — folder-tree view of the RAG knowledge base.
//
// Layout:
//   header: title + buttons (同步内置知识库 / 刷新 / 新建文档)
//   search bar: full-width, hits a ?path_prefix scoped /knowledge/search
//   left sidebar: folder tree built from GET /knowledge/paths split on "/"
//   right pane: doc grid filtered by selected folder (path_prefix)
//
// Tree rendering: paths are flat strings like "网络/DNS"; we parse each
// into nodes and merge into a tree by walking the segments. Click any
// folder to set the path_prefix filter; click "全部" to clear.
import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type ReactNode } from 'react';
import {
  BookOpen,
  ChevronRight,
  Copy,
  DownloadCloud,
  Eye,
  Folder,
  FolderOpen,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  Upload,
} from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import { cn } from '@/lib/cn';
import { fullDateTime } from '@/lib/format';
import { Modal } from '@/components/Modal';
import { Button, Card, EmptyState, PageHeader } from '@/components/ui';
import {
  createDoc,
  deleteDoc,
  listDocs,
  localizedDocTitle,
  localizedPath,
  localizedPathSegment,
  moveDoc,
  searchKnowledge,
  syncVault,
  updateDoc,
  uploadDoc,
  type KnowledgeDoc,
  type PathRow,
  type SearchHit,
} from '@/api/knowledge';
import { ApiError } from '@/api/client';
import { useI18n } from '@/i18n/locale';

// Drag-and-drop payload key for relocating an org doc onto a folder (ADR-029).
// Carries the doc id; folder rows in the 组织 tree are the drop targets.
const DOC_DND_MIME = 'application/x-ongrid-doc-id';

type TreeNode = {
  name: string;
  path: string; // cumulative breadcrumb (matches payload.path_prefixes)
  count: number; // doc count of this exact path
  subtreeCount: number; // doc count rolled up over the whole subtree
  children: Map<string, TreeNode>;
};

function buildTree(rows: PathRow[]): TreeNode {
  const root: TreeNode = {
    name: '__root__',
    path: '',
    count: 0,
    subtreeCount: 0,
    children: new Map(),
  };
  for (const r of rows) {
    if (!r.path) continue;
    const parts = r.path.split('/');
    let cur = root;
    let cumulative = '';
    for (let i = 0; i < parts.length; i++) {
      const seg = parts[i];
      cumulative = cumulative ? `${cumulative}/${seg}` : seg;
      let child = cur.children.get(seg);
      if (!child) {
        child = { name: seg, path: cumulative, count: 0, subtreeCount: 0, children: new Map() };
        cur.children.set(seg, child);
      }
      child.subtreeCount += r.count;
      if (i === parts.length - 1) child.count += r.count;
      cur = child;
    }
    root.subtreeCount += r.count;
  }
  return root;
}

export default function KnowledgePage() {
  const { tr } = useI18n();
  const [items, setItems] = useState<KnowledgeDoc[]>([]);
  // Two trees by source (ADR-028): 'builtin' = the read-only platform vault
  // (source_type=vault); 'org' = the organization's own content
  // (upload + manual), full CRUD. Each scope renders its own folder tree.
  const [sourceScope, setSourceScope] = useState<'builtin' | 'org'>('org');
  const [activePath, setActivePath] = useState<string>(''); // '' = 全部
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<KnowledgeDoc | 'create' | null>(null);
  const [deleting, setDeleting] = useState<KnowledgeDoc | null>(null);

  const [query, setQuery] = useState('');
  const [hits, setHits] = useState<SearchHit[]>([]);
  const [searching, setSearching] = useState(false);

  // Built-in vault sync state. The vault is platform content synced via the
  // dedicated /knowledge/vault/sync endpoint — it is NOT a repo row, so the
  // "同步内置知识库" button is always available and needs no repo lookup.
  // lastVaultSync caches the most recent file count for the tooltip.
  const [syncingBuiltin, setSyncingBuiltin] = useState(false);
  const [syncErr, setSyncErr] = useState<string | null>(null);
  const [lastVaultSync, setLastVaultSync] = useState<{
    count: number;
    at: string;
    source: 'cloud' | 'embedded';
  } | null>(null);
  // Transient "✓ synced" banner so a successful click has a visible result
  // (the count alone is easy to miss when it doesn't change). Cleared after
  // a few seconds or on the next action.
  const [syncOk, setSyncOk] = useState<{ count: number; source: 'cloud' | 'embedded' } | null>(null);

  const fetchAll = useCallback(async (silent = false) => {
    if (silent) setRefreshing(true);
    else setLoading(true);
    try {
      // The folder tree is derived per-scope from the docs themselves
      // (each source has its own tree), so we no longer need the aggregate
      // /knowledge/paths endpoint here.
      const docsR = await listDocs();
      setItems(docsR.items ?? []);
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

  const onSyncBuiltin = useCallback(async () => {
    setSyncingBuiltin(true);
    setSyncErr(null);
    setSyncOk(null);
    try {
      const res = await syncVault();
      setLastVaultSync({ count: res.file_count, at: res.synced_at, source: res.source });
      setSyncOk({ count: res.file_count, source: res.source });
      await fetchAll(true);
      window.setTimeout(() => setSyncOk(null), 6000);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      setSyncErr(msg);
    } finally {
      setSyncingBuiltin(false);
    }
  }, [fetchAll]);

  // File upload (ADR-028) → 组织知识库 (source_type=upload). Accepts the
  // selected .md/.txt files, uploads each, then jumps to the org scope so
  // the operator sees what they just added.
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const onUploadFiles = useCallback(
    async (files: FileList | null) => {
      if (!files || files.length === 0) return;
      setUploading(true);
      setErr(null);
      try {
        for (const f of Array.from(files)) {
          await uploadDoc(f);
        }
        setSourceScope('org');
        setActivePath('');
        await fetchAll(true);
      } catch (e) {
        setErr((e as Error).message || 'upload failed');
      } finally {
        setUploading(false);
        if (fileInputRef.current) fileInputRef.current.value = '';
      }
    },
    [fetchAll],
  );

  const isBuiltin = (d: KnowledgeDoc) => d.source_type === 'vault';

  // Both source groups are shown side-by-side in the sidebar (two trees, no
  // toggle). builtin = vault; org = everything else (upload + manual).
  const builtinItems = useMemo(() => items.filter(isBuiltin), [items]);
  const orgItems = useMemo(() => items.filter((d) => !isBuiltin(d)), [items]);

  // Each group builds its own folder tree from its own docs' paths.
  const treeFromItems = (docs: KnowledgeDoc[]): TreeNode => {
    const m = new Map<string, number>();
    for (const d of docs) {
      const p = d.path ?? '';
      if (p) m.set(p, (m.get(p) ?? 0) + 1);
    }
    const rows: PathRow[] = [...m.entries()].map(([path, count]) => ({ path, count }));
    return buildTree(rows);
  };
  const builtinTree = useMemo(() => treeFromItems(builtinItems), [builtinItems]);
  const orgTree = useMemo(() => treeFromItems(orgItems), [orgItems]);

  const scopedItems = sourceScope === 'builtin' ? builtinItems : orgItems;

  // Right pane shows docs in the selected group, filtered to the selected
  // folder (exact path OR a descendant of it).
  const visibleDocs = useMemo(() => {
    if (!activePath) return scopedItems;
    const prefix = `${activePath}/`;
    return scopedItems.filter((d) => d.path === activePath || (d.path ?? '').startsWith(prefix));
  }, [scopedItems, activePath]);

  // Picking a folder in either tree selects that group + path.
  const pickFolder = useCallback((scope: 'builtin' | 'org', path: string) => {
    setSourceScope(scope);
    setActivePath(path);
  }, []);

  // Drag-drop: relocate an org doc into a folder (ADR-029). Optimism isn't
  // worth it here — moves re-embed server-side — so we just await + refetch.
  const onMoveDoc = useCallback(
    async (docId: string, targetPath: string) => {
      setErr(null);
      try {
        await moveDoc(docId, targetPath);
        await fetchAll(true);
      } catch (e) {
        setErr(e instanceof ApiError ? e.message : (e as Error).message);
      }
    },
    [fetchAll],
  );

  const runSearch = async () => {
    if (!query.trim()) {
      setHits([]);
      return;
    }
    setSearching(true);
    try {
      const r = await searchKnowledge(query.trim(), {
        limit: 10,
        pathPrefix: activePath || undefined,
      });
      setHits(r.items ?? []);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSearching(false);
    }
  };

  const counts = useMemo(() => {
    let builtin = 0;
    let org = 0;
    for (const d of items) {
      if (isBuiltin(d)) builtin++;
      else org++;
    }
    return { builtin, org, total: items.length };
  }, [items]);

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('知识库', 'Knowledge base')}
        subtitle={
          <>
            {tr(`共 ${counts.total} 条 · 组织 ${counts.org} · 内置 ${counts.builtin}`, `${counts.total} total · ${counts.org} org · ${counts.builtin} built-in`)}
            {activePath && (
              <>
                {tr(' · 当前目录 ', ' · current folder ')}
                <span className="text-zinc-300">{localizedPath(activePath)}</span>
              </>
            )}
          </>
        }
        actions={
          <>
            {/* Group-scoped actions (upload / new / sync) live on the
                sidebar section headers now; the toolbar keeps only refresh. */}
            <input
              ref={fileInputRef}
              type="file"
              accept=".md,.markdown,.txt,.text,.pdf,.docx"
              multiple
              className="hidden"
              onChange={(e) => void onUploadFiles(e.target.files)}
            />
            <Button
              onClick={() => fetchAll(true)}
              disabled={loading || refreshing}
              variant="ghost"
            >
              <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
              {tr('刷新', 'Refresh')}
            </Button>
          </>
        }
      />

      {syncErr && (
        <div className="mx-6 mt-3 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
          <div className="font-medium">
            {tr('内置知识库同步失败', 'Built-in vault sync failed')}
          </div>
          <div className="mt-0.5 break-all text-amber-200/80">{syncErr}</div>
        </div>
      )}

      {syncOk && (
        <div className="mx-6 mt-3 rounded-md border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-300">
          {syncOk.source === 'cloud'
            ? tr(
                `✓ 已从云端同步内置知识库 · ${syncOk.count} 篇`,
                `✓ Synced built-in vault from cloud · ${syncOk.count} docs`,
              )
            : tr(
                `✓ 已同步内置知识库 · ${syncOk.count} 篇（云端不可达，使用离线内置版）`,
                `✓ Synced built-in vault · ${syncOk.count} docs (cloud unreachable — used offline baseline)`,
              )}
        </div>
      )}

      <div className="border-b border-zinc-800/60 px-6 py-2.5">
        <div className="flex items-center gap-2">
          <label className="relative block flex-1 max-w-2xl">
            <span className="sr-only">{tr('检索', 'Search')}</span>
            <Search size={12} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void runSearch();
              }}
              placeholder={
                activePath
                  ? tr(`在「${activePath}」内检索（同 query_knowledge 工具）`, `Search within "${activePath}" (same as the query_knowledge tool)`)
                  : tr('试搜：LLM 看到的命中结果（同 query_knowledge 工具）', "Try a search — see what the LLM would (same as query_knowledge tool)")
              }
              className="w-full rounded-md border border-zinc-800/60 bg-zinc-950/40 py-1.5 pl-8 pr-2 text-xs text-zinc-200 placeholder:text-zinc-500 focus:border-zinc-700 focus:outline-none"
            />
          </label>
          <button
            type="button"
            onClick={() => void runSearch()}
            disabled={searching || !query.trim()}
            className="rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
          >
            {searching ? tr('检索中…', 'Searching…') : tr('检索', 'Search')}
          </button>
          {hits.length > 0 && (
            <span className="text-[11px] text-zinc-500">{tr(`命中 ${hits.length} 条`, `${hits.length} hit(s)`)}</span>
          )}
        </div>
        {hits.length > 0 && (
          <div className="mt-3 grid gap-2">
            {hits.map((h) => (
              <SearchHitCard key={h.doc.id} hit={h} />
            ))}
          </div>
        )}
      </div>

      <div className="flex flex-1 overflow-hidden">
        <aside className="hidden w-60 shrink-0 overflow-y-auto border-r border-zinc-800 bg-zinc-950/40 px-2 py-3 md:block">
          <ScopeTreeSection
            label={tr('组织知识库', 'Organization')}
            accent="text-sky-300"
            tree={orgTree}
            total={orgItems.length}
            active={sourceScope === 'org'}
            activePath={activePath}
            onPick={(p) => pickFolder('org', p)}
            onMoveDoc={onMoveDoc}
            actions={
              <>
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation();
                    fileInputRef.current?.click();
                  }}
                  disabled={uploading}
                  title={tr('上传文件 (.md/.txt/.pdf/.docx)', 'Upload file (.md/.txt/.pdf/.docx)')}
                  className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 disabled:opacity-50"
                >
                  <Upload size={13} className={cn(uploading && 'animate-pulse')} />
                </button>
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation();
                    setSourceScope('org');
                    setEditing('create');
                  }}
                  title={tr('新建文档', 'New doc')}
                  className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
                >
                  <Plus size={13} />
                </button>
              </>
            }
          />
          <div className="my-2 border-t border-zinc-800/60" />
          <ScopeTreeSection
            label={tr('内置知识库', 'Built-in')}
            accent="text-amber-300"
            tree={builtinTree}
            total={builtinItems.length}
            active={sourceScope === 'builtin'}
            activePath={activePath}
            onPick={(p) => pickFolder('builtin', p)}
            actions={
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  void onSyncBuiltin();
                }}
                disabled={syncingBuiltin}
                title={
                  lastVaultSync
                    ? tr(
                        `上次同步 ${fullDateTime(lastVaultSync.at)} · ${lastVaultSync.count} 篇 · ${lastVaultSync.source === 'cloud' ? '来源云端' : '离线内置'}`,
                        `Last synced ${fullDateTime(lastVaultSync.at)} · ${lastVaultSync.count} docs · ${lastVaultSync.source === 'cloud' ? 'from cloud' : 'offline baseline'}`,
                      )
                    : tr('从云端同步内置知识库（github.com/ongridio/vault，连不上则用离线内置版）', 'Sync built-in vault from cloud (github.com/ongridio/vault; offline baseline if unreachable)')
                }
                className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 disabled:opacity-50"
              >
                <DownloadCloud size={13} className={cn(syncingBuiltin && 'animate-pulse')} />
              </button>
            }
          />
        </aside>
        <div className="flex-1 overflow-y-auto px-6 py-6">
          {err && (
            <div className="mb-4 rounded-lg border border-red-500/40 bg-red-500/5 px-4 py-3 text-sm text-red-300">
              {err}
            </div>
          )}
          {loading ? (
            <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : visibleDocs.length === 0 ? (
            <EmptyState
              icon={BookOpen}
              title={
                activePath
                  ? tr(`「${activePath}」里还没有文档`, `No docs in "${activePath}" yet`)
                  : sourceScope === 'builtin'
                    ? tr('内置知识库为空', 'Built-in knowledge base is empty')
                    : tr('组织知识库还没有内容', 'No organization docs yet')
              }
              action={
                sourceScope === 'builtin' ? (
                  <Button variant="ghost" onClick={() => void onSyncBuiltin()} disabled={syncingBuiltin}>
                    <DownloadCloud size={12} /> {tr('从云端同步内置知识库', 'Sync built-in vault from cloud')}
                  </Button>
                ) : (
                  <div className="flex items-center gap-2">
                    <Button variant="ghost" onClick={() => fileInputRef.current?.click()} disabled={uploading}>
                      <Upload size={12} /> {tr('上传文件', 'Upload')}
                    </Button>
                    <Button variant="primary" onClick={() => setEditing('create')}>
                      <Plus size={12} /> {tr('新建文档', 'New doc')}
                    </Button>
                  </div>
                )
              }
            />
          ) : (
            <div className="flex flex-col gap-1.5">
              {visibleDocs.map((d) => (
                <DocCard
                  key={d.id}
                  doc={d}
                  onEdit={() => setEditing(d)}
                  onDelete={() => setDeleting(d)}
                />
              ))}
            </div>
          )}
        </div>
      </div>

      {editing && (
        <DocEditor
          mode={editing === 'create' ? 'create' : 'edit'}
          existing={editing === 'create' ? null : editing}
          defaultPath={editing === 'create' ? activePath : undefined}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void fetchAll(true);
          }}
        />
      )}
      {deleting && (
        <DeleteDocDialog
          doc={deleting}
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

// ScopeTreeSection renders one labeled source group's folder tree (no own
// <aside> — the parent stacks two of these). Highlights are shown only when
// this section's scope is the active one; the other section stays visible
// and browsable but unhighlighted.
function ScopeTreeSection({
  label,
  accent,
  tree,
  total,
  active,
  activePath,
  onPick,
  onMoveDoc,
  actions,
}: {
  label: string;
  accent: string;
  tree: TreeNode;
  total: number;
  active: boolean;
  activePath: string;
  onPick: (p: string) => void;
  // When set (org tree only), folders + the group root accept dropped doc
  // cards to relocate them (ADR-029). Built-in tree leaves this undefined.
  onMoveDoc?: (docId: string, path: string) => void;
  // Group-scoped action icons (upload/new for org, sync for built-in)
  // rendered on the right of the header.
  actions?: ReactNode;
}) {
  // Feed FolderNode a path that matches nothing when this scope isn't
  // active, so only the selected tree shows a highlight.
  const childActivePath = active ? activePath : '';
  const [rootDragOver, setRootDragOver] = useState(false);
  const acceptDrop = (e: DragEvent) => {
    if (!onMoveDoc || !e.dataTransfer.types.includes(DOC_DND_MIME)) return false;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    return true;
  };
  return (
    <div>
      {/* The section header IS the group root — clicking it shows the whole
          group (no separate "全部" row); folders are the next level down.
          It's also the drop target for "move to root". */}
      <div className="flex items-center gap-1">
        <button
          type="button"
          onClick={() => onPick('')}
          onDragOver={onMoveDoc ? (e) => setRootDragOver(acceptDrop(e)) : undefined}
          onDragLeave={onMoveDoc ? () => setRootDragOver(false) : undefined}
          onDrop={
            onMoveDoc
              ? (e) => {
                  setRootDragOver(false);
                  const id = e.dataTransfer.getData(DOC_DND_MIME);
                  if (id) onMoveDoc(id, '');
                }
              : undefined
          }
          className={cn(
            'flex min-w-0 flex-1 items-center justify-between gap-2 rounded-md px-2 py-1.5 text-xs',
            rootDragOver
              ? 'ring-1 ring-sky-400/70 bg-sky-500/10'
              : active && activePath === ''
                ? 'bg-zinc-800'
                : 'hover:bg-zinc-900',
          )}
        >
          <span className={cn('flex items-center gap-1.5 truncate font-semibold', accent)}>
            <BookOpen size={12} />
            {label}
          </span>
          <span className="text-[10px] text-zinc-500">{total}</span>
        </button>
        {actions && <div className="flex shrink-0 items-center gap-0.5 pr-1">{actions}</div>}
      </div>
      <div className="mt-0.5">
        {[...tree.children.values()]
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((n) => (
            <FolderNode key={n.path} node={n} depth={0} activePath={childActivePath} onPick={onPick} onMoveDoc={onMoveDoc} />
          ))}
      </div>
    </div>
  );
}

function FolderNode({
  node,
  depth,
  activePath,
  onPick,
  onMoveDoc,
}: {
  node: TreeNode;
  depth: number;
  activePath: string;
  onPick: (p: string) => void;
  onMoveDoc?: (docId: string, path: string) => void;
}) {
  const { tr } = useI18n();
  const isActive = activePath === node.path;
  const isAncestor = activePath.startsWith(`${node.path}/`);
  const [expanded, setExpanded] = useState(isActive || isAncestor);
  const [dragOver, setDragOver] = useState(false);
  useEffect(() => {
    if (isActive || isAncestor) setExpanded(true);
  }, [isActive, isAncestor]);

  const hasChildren = node.children.size > 0;
  const Icon = expanded ? FolderOpen : Folder;
  const canDrop = (e: DragEvent) =>
    !!onMoveDoc && e.dataTransfer.types.includes(DOC_DND_MIME);

  return (
    <div>
      <div
        onDragOver={
          onMoveDoc
            ? (e) => {
                if (canDrop(e)) {
                  e.preventDefault();
                  e.dataTransfer.dropEffect = 'move';
                  setDragOver(true);
                }
              }
            : undefined
        }
        onDragLeave={onMoveDoc ? () => setDragOver(false) : undefined}
        onDrop={
          onMoveDoc
            ? (e) => {
                setDragOver(false);
                if (!canDrop(e)) return;
                e.preventDefault();
                const id = e.dataTransfer.getData(DOC_DND_MIME);
                if (id) onMoveDoc(id, node.path);
              }
            : undefined
        }
        className={cn(
          'group flex items-center gap-1 rounded-md px-1.5 py-1 text-xs',
          dragOver
            ? 'ring-1 ring-sky-400/70 bg-sky-500/10 text-zinc-100'
            : isActive
              ? 'bg-zinc-800 text-zinc-100'
              : 'text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200',
        )}
        style={{ paddingLeft: 6 + depth * 12 }}
      >
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          disabled={!hasChildren}
          className={cn(
            'flex h-4 w-4 shrink-0 items-center justify-center rounded text-zinc-500',
            hasChildren ? 'hover:text-zinc-200' : 'opacity-0',
          )}
          aria-label={expanded ? tr('折叠', 'Collapse') : tr('展开', 'Expand')}
        >
          <ChevronRight
            size={11}
            className={cn('transition-transform', expanded && 'rotate-90')}
          />
        </button>
        <button
          type="button"
          onClick={() => onPick(node.path)}
          className="flex flex-1 items-center justify-between gap-1.5 truncate"
          title={localizedPath(node.path)}
        >
          <span className="flex items-center gap-1.5 truncate">
            <Icon size={12} className="shrink-0" />
            <span className="truncate">{localizedPathSegment(node.name)}</span>
          </span>
          <span className="text-[10px] text-zinc-500">{node.subtreeCount}</span>
        </button>
      </div>
      {expanded && hasChildren && (
        <div>
          {[...node.children.values()]
            .sort((a, b) => a.name.localeCompare(b.name))
            .map((c) => (
              <FolderNode
                key={c.path}
                node={c}
                depth={depth + 1}
                activePath={activePath}
                onPick={onPick}
                onMoveDoc={onMoveDoc}
              />
            ))}
        </div>
      )}
    </div>
  );
}

function DocCard({
  doc,
  onEdit,
  onDelete,
}: {
  doc: KnowledgeDoc;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { tr } = useI18n();
  // Editable = the org's own content (manual paste / uploaded file).
  // Read-only = platform vault + (legacy) repo docs — regenerated on sync.
  const editable = doc.source_type === 'manual' || doc.source_type === 'upload';
  const sourceBadge =
    doc.source_type === 'vault'
      ? { label: tr('内置', 'built-in'), cls: 'text-amber-300' }
      : doc.source_type === 'upload'
        ? { label: tr('上传', 'upload'), cls: 'text-sky-300' }
        : doc.source_type === 'repo'
          ? { label: 'repo', cls: 'text-emerald-300' }
          : { label: tr('手动', 'manual'), cls: 'text-indigo-300' };
  return (
    <Card
      compact
      // Org docs (manual/upload) are draggable onto a folder in the 组织 tree
      // to relocate them (ADR-029). The drop targets read DOC_DND_MIME.
      draggable={editable}
      onDragStart={
        editable
          ? (e) => {
              e.dataTransfer.setData(DOC_DND_MIME, doc.id);
              e.dataTransfer.effectAllowed = 'move';
            }
          : undefined
      }
      className="flex cursor-pointer flex-col py-2.5 transition-colors hover:bg-zinc-800/40"
      onClick={onEdit}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium text-zinc-100" title={localizedDocTitle(doc)}>
            {localizedDocTitle(doc)}
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[10px] uppercase tracking-wider text-zinc-500">
            <span className={sourceBadge.cls}>{sourceBadge.label}</span>
            {doc.path && (
              <span className="rounded bg-zinc-800/60 px-1.5 py-0.5 font-normal normal-case text-zinc-300">
                {localizedPath(doc.path)}
              </span>
            )}
          </div>
          {doc.tags && doc.tags.length > 0 && (
            <div className="mt-1.5 flex flex-wrap gap-1">
              {doc.tags.map((t) => (
                <span
                  key={t}
                  className="rounded bg-zinc-800/60 px-1.5 py-0.5 text-[10px] text-zinc-400"
                >
                  {t}
                </span>
              ))}
            </div>
          )}
          {doc.url && (
            <div className="mt-1 truncate font-mono text-[10px] text-zinc-600" title={doc.url}>
              {doc.url}
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {editable ? (
            <>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onEdit();
                }}
                title={tr('编辑', 'Edit')}
                className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200"
              >
                <Pencil size={11} />
              </button>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onDelete();
                }}
                title={tr('删除', 'Delete')}
                className="rounded p-1 text-zinc-500 hover:bg-red-900/30 hover:text-red-300"
              >
                <Trash2 size={11} />
              </button>
            </>
          ) : (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                onEdit();
              }}
              title={tr('查看', 'View')}
              className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200"
            >
              <Eye size={11} />
            </button>
          )}
        </div>
      </div>
    </Card>
  );
}

function SearchHitCard({ hit }: { hit: SearchHit }) {
  return (
    <div className="rounded-md border border-zinc-800/60 bg-zinc-950/40 px-3 py-2">
      <div className="flex items-center justify-between text-[11px] text-zinc-500">
        <div className="flex items-center gap-2">
          <span className="text-zinc-100 font-medium">{localizedDocTitle(hit.doc)}</span>
          {hit.doc.path && (
            <span className="rounded bg-zinc-800/60 px-1.5 py-0.5 text-[10px] text-zinc-400">
              {localizedPath(hit.doc.path)}
            </span>
          )}
          {hit.doc.source_type === 'repo' && (
            <span className="rounded bg-emerald-500/10 px-1.5 py-0.5 text-[10px] text-emerald-300">
              repo
            </span>
          )}
        </div>
        <span>score {hit.score.toFixed(2)}</span>
      </div>
      <div className="mt-1 line-clamp-3 whitespace-pre-wrap break-words text-[12px] text-zinc-300">
        {hit.doc.content}
      </div>
    </div>
  );
}

function DocEditor({
  mode,
  existing,
  defaultPath,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  existing: KnowledgeDoc | null;
  defaultPath?: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { tr } = useI18n();
  const [title, setTitle] = useState(existing?.title ?? '');
  const [titleEN, setTitleEN] = useState(existing?.title_en ?? '');
  const [content, setContent] = useState(existing?.content ?? '');
  const [url, setUrl] = useState(existing?.url ?? '');
  const [path, setPath] = useState(existing?.path ?? defaultPath ?? '');
  const [tagsText, setTagsText] = useState((existing?.tags ?? []).join(', '));
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  // Vault (built-in) and repo docs are read-only — both are regenerated
  // on sync, so in-place edits would be silently lost (the backend PATCH
  // rejects them too). Users can still open them to read the rendered
  // body; the modal switches to a markdown viewer instead of the form.
  // "复制为组织文档" forks the body into an editable org doc (manual),
  // which is the supported way to customize built-in content.
  const sourceReadOnly =
    existing?.source_type === 'repo' || existing?.source_type === 'vault';
  const [forked, setForked] = useState(false);
  const readOnly = mode === 'edit' && sourceReadOnly && !forked;
  // An uploaded file's url is its identity (the filename); it can't be
  // re-pointed from the editor — the backend keeps it fixed. Show it but
  // lock the field so editing it isn't silently ignored.
  const urlLocked = mode === 'edit' && existing?.source_type === 'upload';

  useEffect(() => {
    if (mode === 'edit' && existing && !content) {
      setLoading(true);
      void (async () => {
        try {
          const r = await import('@/api/knowledge').then((m) => m.getDoc(existing.id));
          setContent(r.content ?? '');
        } catch (e) {
          setErr(e instanceof ApiError ? e.message : (e as Error).message);
        } finally {
          setLoading(false);
        }
      })();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, existing?.id]);

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      const tags = tagsText
        .split(/[,，]/)
        .map((t) => t.trim())
        .filter(Boolean);
      const cleanPath = path
        .split('/')
        .map((s) => s.trim())
        .filter(Boolean)
        .join('/');
      if (mode === 'create' || forked) {
        // forked: a read-only vault/repo doc copied into the org scope —
        // lands as a brand-new manual doc, the original stays untouched.
        await createDoc({
          title: title.trim(),
          title_en: titleEN.trim() || undefined,
          content: content.trim(),
          url: forked ? undefined : url.trim() || undefined,
          path: cleanPath || undefined,
          tags: tags.length ? tags : undefined,
        });
      } else if (existing) {
        await updateDoc(existing.id, {
          title: title.trim(),
          title_en: titleEN.trim() || undefined,
          content: content.trim(),
          path: cleanPath || undefined,
          tags: tags.length ? tags : undefined,
        });
      }
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  // Read-only viewer for vault/repo docs — render markdown body, show
  // metadata in a header strip. Manual/upload docs continue to the editor
  // form below.
  if (readOnly) {
    const isVault = existing?.source_type === 'vault';
    return (
      <Modal
        open
        onClose={onClose}
        title={existing ? localizedDocTitle(existing) : tr('文档', 'Document')}
        footer={
          <>
            <button
              type="button"
              onClick={onClose}
              className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
            >
              {tr('关闭', 'Close')}
            </button>
            <button
              type="button"
              onClick={() => {
                // The copy is a fresh org doc — drop the source url
                // (builtin://… / repo path) so it isn't carried over.
                setUrl('');
                setForked(true);
              }}
              disabled={loading || !content}
              title={
                isVault
                  ? tr('内置文档随同步更新、不可直接修改；复制一份到组织知识库后即可编辑', 'Built-in docs are refreshed on sync and not directly editable; copy into the org knowledge base to edit')
                  : tr('repo 文档随同步更新、不可直接修改；复制一份到组织知识库后即可编辑', 'Repo docs are refreshed on sync and not directly editable; copy into the org knowledge base to edit')
              }
              className="flex items-center gap-1.5 rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
            >
              <Copy size={11} />
              {tr('复制为组织文档', 'Copy as org doc')}
            </button>
          </>
        }
      >
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-zinc-500">
            {isVault ? (
              <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-amber-300">{tr('内置', 'built-in')}</span>
            ) : (
              <span className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-emerald-300">repo</span>
            )}
            {existing?.path && (
              <span className="rounded bg-zinc-800/60 px-1.5 py-0.5 text-zinc-300">{localizedPath(existing.path)}</span>
            )}
            {existing?.tags?.map((t) => (
              <span key={t} className="rounded bg-zinc-800/60 px-1.5 py-0.5 text-zinc-400">
                {t}
              </span>
            ))}
            {existing?.url &&
              (existing.url.startsWith('http') ? (
                <a
                  href={existing.url}
                  target="_blank"
                  rel="noreferrer"
                  className="ml-auto truncate font-mono text-[10px] text-zinc-500 hover:text-zinc-300 underline-offset-2 hover:underline"
                  title={existing.url}
                >
                  {existing.url}
                </a>
              ) : (
                <span className="ml-auto truncate font-mono text-[10px] text-zinc-500" title={existing.url}>
                  {existing.url}
                </span>
              ))}
          </div>
          <div className="max-h-[60vh] overflow-y-auto rounded-md border border-zinc-800 bg-zinc-950/40 p-4">
            {loading ? (
              <div className="text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
            ) : err ? (
              <div className="text-xs text-red-300">{err}</div>
            ) : content ? (
              <div className="md-body text-sm text-zinc-200">
                <ReactMarkdown>{content}</ReactMarkdown>
              </div>
            ) : (
              <div className="text-xs text-zinc-500">{tr('该文档没有正文内容', 'This document has no body content')}</div>
            )}
          </div>
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={
        mode === 'create'
          ? tr('新建知识文档', 'New knowledge doc')
          : forked
            ? tr(`复制「${existing ? localizedDocTitle(existing) : ''}」为组织文档`, `Copy "${existing ? localizedDocTitle(existing) : ''}" as org doc`)
            : tr(`编辑 ${existing ? localizedDocTitle(existing) : ''}`, `Edit ${existing ? localizedDocTitle(existing) : ''}`)
      }
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
            disabled={submitting || title.trim() === '' || content.trim() === ''}
            className="rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
          >
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </button>
        </>
      }
    >
      <div className="space-y-3 text-xs text-zinc-300">
        {err && (
          <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-red-300">{err}</div>
        )}
        <label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('标题 *', 'Title *')}</div>
          <input
            type="text"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={tr('例：nginx 重启 SOP', 'e.g. nginx restart SOP')}
            maxLength={256}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">
            {tr('英文标题（可选；en-US locale 下显示）', 'English title (optional; shown in en-US locale)')}
          </div>
          <input
            type="text"
            value={titleEN}
            onChange={(e) => setTitleEN(e.target.value)}
            placeholder={tr('例：nginx restart SOP', 'e.g. nginx restart SOP')}
            maxLength={256}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </label>
        <div className="grid grid-cols-2 gap-2">
          <label className="block">
            <div className="mb-1 text-[11px] text-zinc-500">{tr('目录路径（用 / 分隔）', 'Folder path (separated by /)')}</div>
            <input
              type="text"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder={tr('网络/DNS', 'Network/DNS')}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </label>
          <label className="block">
            <div className="mb-1 text-[11px] text-zinc-500">{tr('标签（逗号分隔）', 'Tags (comma separated)')}</div>
            <input
              type="text"
              value={tagsText}
              onChange={(e) => setTagsText(e.target.value)}
              placeholder="dns, resolv"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </label>
        </div>
        <label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">
            {urlLocked ? tr('来源文件（不可修改）', 'Source file (read-only)') : tr('来源 URL（可选）', 'Source URL (optional)')}
          </div>
          <input
            type="text"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            readOnly={urlLocked}
            placeholder="https://wiki.internal/runbook/nginx-restart"
            className={cn(
              'w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none',
              urlLocked && 'cursor-not-allowed text-zinc-500',
            )}
          />
        </label>
        <label className="block">
          <div className="mb-1 text-[11px] text-zinc-500">{tr('内容（markdown）*', 'Content (markdown) *')}</div>
          <textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            placeholder={tr('支持 markdown。LLM 通过 query_knowledge 检索时按语义匹配 title + content。', 'Markdown supported. The LLM does semantic match over title + content via the query_knowledge tool.')}
            className="h-72 w-full resize-y rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </label>
      </div>
    </Modal>
  );
}

function DeleteDocDialog({
  doc,
  onClose,
  onDone,
}: {
  doc: KnowledgeDoc;
  onClose: () => void;
  onDone: () => void;
}) {
  const { tr } = useI18n();
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await deleteDoc(doc.id);
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
      title={tr(`删除 ${localizedDocTitle(doc)}`, `Delete ${localizedDocTitle(doc)}`)}
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
        {err && (
          <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-red-300">
            {err}
          </div>
        )}
        <p>{tr('确定删除这条文档？', 'Delete this document?')}</p>
      </div>
    </Modal>
  );
}
