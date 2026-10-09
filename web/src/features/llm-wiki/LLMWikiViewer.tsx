import { useEffect, useMemo, useState } from 'react';
import { ArrowUpRight, Trash2 } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Modal } from '@/components/Modal';
import { localizedPath } from '@/api/knowledge';
import { fullDateTime } from '@/lib/format';
import { splitFrontmatter } from '@/lib/frontmatter';
import { useI18n } from '@/i18n/locale';
import { getLLMWikiNode, getLLMWikiNodePreview } from './api';
import { type LLMWikiNode } from './types';

type RawPreview =
  | { kind: 'pdf'; url: string }
  | { kind: 'docx'; text: string };

type WikiReference = {
  path: string;
  node_id?: string;
  title?: string;
};

export function LLMWikiFileViewer({
  file,
  onClose,
  onOpenFile,
  onDelete,
}: {
  file: LLMWikiNode;
  onClose: () => void;
  onOpenFile: (file: LLMWikiNode) => void;
  onDelete: () => void;
}) {
  const { tr } = useI18n();
  const [detail, setDetail] = useStateWithReset<LLMWikiNode | null>(null, file.id);
  const [error, setError] = useStateWithReset<string | null>(null, file.id);
  const [preview, setPreview] = useStateWithReset<RawPreview | null>(null, file.id);
  const [previewError, setPreviewError] = useStateWithReset<string | null>(null, file.id);
  const previewKind = rawPreviewKind(file);

  useEffect(() => {
    let cancelled = false;
    void getLLMWikiNode(file.id)
      .then((node) => { if (!cancelled) setDetail(node); })
      .catch((reason: unknown) => { if (!cancelled) setError((reason as Error).message); });
    return () => { cancelled = true; };
  }, [file.id, setDetail, setError]);

  useEffect(() => {
    let cancelled = false;
    let objectURL: string | null = null;
    if (!previewKind) return () => { cancelled = true; };
    void getLLMWikiNodePreview(file.id)
      .then(async (blob) => {
        if (cancelled) return;
        if (previewKind === 'pdf') {
          objectURL = URL.createObjectURL(blob);
          setPreview({ kind: 'pdf', url: objectURL });
        } else {
          setPreview({ kind: 'docx', text: await blob.text() });
        }
      })
      .catch((reason: unknown) => { if (!cancelled) setPreviewError((reason as Error).message); });
    return () => {
      cancelled = true;
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [file.id, previewKind, setPreview, setPreviewError]);

  return (
    <Modal
      open
      onClose={onClose}
      title={file.name}
      size="xl"
      resizable
      footer={
        <div className="flex items-center justify-end gap-2">
          {file.layer === 'raw' && (
            <button type="button" onClick={onDelete} className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-1.5 text-xs text-red-300 hover:bg-red-500/20">
              <Trash2 size={12} className="mr-1 inline" />{tr('删除文件', 'Delete file')}
            </button>
          )}
          <button type="button" onClick={onClose} className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800">{tr('关闭', 'Close')}</button>
        </div>
      }
    >
      <div className="space-y-3">
        <div className="font-mono text-[11px] text-zinc-500">{localizedPath(file.relative_path)}</div>
        {detail && <div className="text-xs text-zinc-500">{tr('更新时间', 'Updated')}：{fullDateTime(detail.updated_at)}</div>}
        {detail?.layer === 'raw' ? (
          <WikiReferences
            title={tr(`关联 (${detail.metadata?.wiki_files?.length ?? 0})`, `Related (${detail.metadata?.wiki_files?.length ?? 0})`)}
            references={detail.metadata?.wiki_files}
            emptyLabel={tr('暂无关联 Wiki', 'No related Wiki pages')}
            layer="wiki"
            onOpenFile={onOpenFile}
          />
        ) : (
          detail && <div className="space-y-3">
            <WikiSourceReferences metadata={detail.metadata} onOpenFile={onOpenFile} />
          </div>
        )}
        <div className="rounded-md border border-zinc-800 bg-zinc-950/40 p-4">
          {error ? <div className="text-xs text-red-300">{error}</div> : detail ? previewKind ? <RawPreviewView kind={previewKind} preview={preview} error={previewError} fileName={file.name} tr={tr} /> : <WikiDocBody content={detail.content ?? ''} omitMetaKeys={detail.layer === 'wiki' ? ['id', 'type', 'language', 'source_versions', 'title', 'source_file'] : undefined} /> : <div className="text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>}
        </div>
      </div>
    </Modal>
  );
}

// This tiny hook keeps the viewer reset logic local to the feature. It avoids
// showing the previous file while the detail request for a linked file runs.
function useStateWithReset<T>(initial: T, resetKey: string): [T, (value: T) => void] {
  const [value, setValue] = useState(initial);
  useEffect(() => setValue(initial), [initial, resetKey]);
  return [value, setValue];
}

function rawPreviewKind(file: LLMWikiNode): 'pdf' | 'docx' | null {
  if (file.layer !== 'raw') return null;
  const extension = file.relative_path.toLowerCase().split('.').pop();
  return extension === 'pdf' || extension === 'docx' ? extension : null;
}

function RawPreviewView({ kind, preview, error, fileName, tr }: { kind: 'pdf' | 'docx'; preview: RawPreview | null; error: string | null; fileName: string; tr: (zh: string, en: string) => string }) {
  if (error) return <div className="text-xs text-red-300">{error}</div>;
  if (!preview) return <div className="text-xs text-zinc-500">{tr('加载预览…', 'Loading preview…')}</div>;
  if (kind === 'pdf' && preview.kind === 'pdf') return <iframe title={fileName} src={preview.url} className="h-[min(70vh,720px)] w-full rounded border border-zinc-800 bg-white" />;
  if (kind === 'docx' && preview.kind === 'docx') return <pre className="max-h-[min(70vh,720px)] overflow-auto whitespace-pre-wrap text-sm leading-6 text-zinc-300">{preview.text}</pre>;
  return null;
}

function WikiReferences({ title, references, emptyLabel, layer, onOpenFile }: { title: string; references?: WikiReference[]; emptyLabel: string; layer: LLMWikiNode['layer']; onOpenFile: (file: LLMWikiNode) => void }) {
  return (
    <div className="rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-2">
      <div className="mb-1 text-[11px] font-medium text-zinc-400">{title}</div>
      {references?.length ? <div className="flex flex-wrap gap-1.5">{references.map((reference) => <WikiReferenceButton key={reference.node_id ?? reference.path} reference={reference} layer={layer} onOpenFile={onOpenFile} />)}</div> : <div className="text-xs text-zinc-500">{emptyLabel}</div>}
    </div>
  );
}

function WikiReferenceButton({ reference, layer, onOpenFile }: { reference: WikiReference; layer: LLMWikiNode['layer']; onOpenFile: (file: LLMWikiNode) => void }) {
  const target = reference.node_id ? {
    id: reference.node_id,
    parent_id: '',
    layer,
    kind: 'file' as const,
    name: reference.path.split('/').pop() || reference.path,
    relative_path: reference.path,
    has_children: false,
    child_count: 0,
    document_count: 1,
  } : null;
  const content = <><span className="truncate text-zinc-300">{reference.title || reference.path}</span>{reference.title && <span className="truncate font-mono text-[11px] text-zinc-500">{reference.path}</span>}{target && <ArrowUpRight size={12} className="shrink-0 text-zinc-500" />}</>;
  if (!target) return <div className="flex min-w-0 items-center gap-1.5 text-xs">{content}</div>;
  return <button type="button" onClick={() => onOpenFile(target)} aria-label={reference.path} className="flex min-w-0 max-w-full shrink-0 items-center gap-1.5 rounded-full border border-zinc-800 px-2 py-1 text-left text-xs hover:border-zinc-600 hover:bg-zinc-900">{content}</button>;
}

function WikiSourceReferences({ metadata, onOpenFile }: { metadata?: LLMWikiNode['metadata']; onOpenFile: (file: LLMWikiNode) => void }) {
  const { tr } = useI18n();
  const references: WikiReference[] = metadata?.source_files?.length
    ? metadata.source_files.map(({ path, node_id }) => ({ path, node_id }))
    : metadata?.source_file ? [{ path: metadata.source_file, node_id: metadata.source_node_id }] : [];
  if (references.length === 0) return null;
  return <WikiReferences title={tr(`来源 (${references.length})`, `Sources (${references.length})`)} references={references} emptyLabel={tr('暂无关联 Source', 'No related sources')} layer="raw" onOpenFile={onOpenFile} />;
}

function WikiDocBody({ content, omitMetaKeys }: { content: string; omitMetaKeys?: readonly string[] }) {
  const fm = useMemo(() => splitFrontmatter(content), [content]);
  return (
    <div className="md-body text-sm text-zinc-200">
      {fm && <dl className="mb-4 space-y-1 border-b border-zinc-800 pb-3">{fm.meta.filter(([key]) => !omitMetaKeys?.includes(key)).map(([key, value]) => <div key={key} className="flex items-baseline gap-2 text-xs"><dt className="shrink-0 font-mono text-[11px] text-zinc-500">{key}</dt>{Array.isArray(value) ? <dd className="flex flex-wrap gap-1">{value.map((item) => <span key={item} className="rounded bg-zinc-800/60 px-1.5 py-0.5 text-[11px] text-zinc-400">{item}</span>)}</dd> : <dd className="text-zinc-400">{value}</dd>}</div>)}</dl>}
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{fm ? fm.body : content}</ReactMarkdown>
    </div>
  );
}
