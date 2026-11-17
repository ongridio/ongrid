import { useEffect, useRef, useState } from 'react';
import { Modal } from '@/components/Modal';
import { Button, Input } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { downloadSavedResult, getSavedResult, readSavedResult, type ResultPage, type SavedResult } from '@/api/toolResults';

export function SavedToolResult({ sessionId, resultId }: { sessionId: string; resultId: string }) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const [meta, setMeta] = useState<SavedResult>();
  const [page, setPage] = useState<ResultPage>();
  const [keyword, setKeyword] = useState('');
  const [activeKeyword, setActiveKeyword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const requestRef = useRef<AbortController>();
  useEffect(() => () => requestRef.current?.abort(), [sessionId, resultId]);

  async function load(query: string, cursor?: string) {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setBusy(true); setError('');
    try {
      const metadata = await getSavedResult(sessionId, resultId, controller.signal);
      if (controller.signal.aborted) return;
      setMeta(metadata);
      if (metadata.storage_status !== 'ready') { setPage(undefined); return; }
      const next = await readSavedResult(sessionId, resultId, query, cursor, controller.signal);
      if (!controller.signal.aborted) { setPage(next); setActiveKeyword(query); }
    } catch (e) {
      if (!controller.signal.aborted) setError(e instanceof Error ? e.message : tr('读取失败', 'Read failed'));
    } finally { if (!controller.signal.aborted) setBusy(false); }
  }
  async function download() {
    const controller = new AbortController(); requestRef.current = controller;
    setBusy(true); setError('');
    try {
      const blob = await downloadSavedResult(sessionId, resultId, controller.signal);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a'); link.href = url; link.download = `tool-result-${resultId}.txt`; link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : tr('下载失败', 'Download failed')); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  }
  return <>
    <Button variant="link" size="sm" onClick={() => { setOpen(true); setKeyword(''); void load(''); }}>{tr('查看保存的结果', 'View saved result')}</Button>
    <Modal open={open} onClose={() => { requestRef.current?.abort(); setOpen(false); }} title={tr('保存的工具结果', 'Saved tool result')} size="xl">
      <div className="min-w-0 space-y-3">
        {meta && <p className="text-xs text-zinc-400">
          {meta.captured_bytes.toLocaleString()} {tr('字节', 'bytes')} · {meta.capture_status === 'complete' ? tr('本次响应已完整保存', 'This response was saved in full') : tr('仅保存了部分响应', 'Only part of the response was captured')}
        </p>}
        {meta && meta.storage_status !== 'ready' ? <p role="status" className="text-sm text-amber-500">{tr('原文已过期或暂不可用。', 'The original result has expired or is unavailable.')}</p> : <>
          <form className="flex flex-wrap gap-2" onSubmit={e => { e.preventDefault(); void load(keyword.trim()); }}>
            <Input className="min-w-0 flex-1" aria-label={tr('搜索结果', 'Search result')} placeholder={tr('输入关键词', 'Enter a keyword')} value={keyword} maxLength={512} onChange={e => setKeyword(e.target.value)} />
            <Button type="submit" variant="outline" disabled={busy}>{tr('搜索', 'Search')}</Button>
            <Button variant="outline" disabled={busy || !meta} onClick={() => void download()}>{meta?.capture_status === 'complete' ? tr('下载原文', 'Download original') : tr('下载已采集内容', 'Download captured content')}</Button>
          </form>
          {page && <>
            <p className="text-xs text-zinc-500">{tr('当前范围', 'Current range')} {page.scan_start.toLocaleString()}–{page.scan_end.toLocaleString()} · {page.scan_complete ? tr('已到扫描末尾', 'End of scan') : tr('尚未扫描完整结果', 'Result not fully scanned')}</p>
            {page.items.length ? <pre className="max-h-[50vh] overflow-auto whitespace-pre-wrap break-all rounded border border-zinc-800 bg-zinc-950 p-3 text-xs leading-5 text-zinc-300">{page.items.map(item => item.data_json ?? item.text ?? '').join('\n')}</pre> : <p className="text-sm text-zinc-400">{tr('当前扫描范围内没有匹配项。', 'No matches in the scanned range.')}</p>}
            {page.notice && <p className="text-xs text-amber-500">{page.notice}</p>}
            {page.next_cursor && <Button variant="outline" disabled={busy} onClick={() => void load(activeKeyword, page.next_cursor)}>{tr('继续读取', 'Continue reading')}</Button>}
          </>}
        </>}
        {busy && <p role="status" className="text-xs text-zinc-500">{tr('正在读取…', 'Reading…')}</p>}
        {error && <p role="alert" className="break-words text-sm text-red-500">{error}</p>}
      </div>
    </Modal>
  </>;
}
