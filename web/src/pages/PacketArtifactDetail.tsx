import { useAssistantPage } from '@/lib/assistantPage';
import { useCallback, useEffect, useState } from 'react';
import { ArrowLeft, Download, FileCode2, Loader2 } from 'lucide-react';
import { Link, useParams } from 'react-router-dom';

import { Button, EmptyState, PageHeader } from '@/components/ui';
import { downloadPacketCapture, getPacketCaptureArtifact, packetCaptureArtifactID, type PacketCapture } from '@/api/packetCaptures';
import { PacketArtifactViewer } from '@/pages/Pages';
import { useI18n } from '@/i18n/locale';

export default function PacketArtifactDetailPage() {
  const { tr } = useI18n();
  const { artifactID = '' } = useParams();
  const [capture, setCapture] = useState<PacketCapture | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [downloading, setDownloading] = useState(false);

  useEffect(() => {
    let alive = true;
    void (async () => {
      try {
        const artifact = await getPacketCaptureArtifact(artifactID);
        if (alive) setCapture(artifact);
      } catch (err) {
        if (alive) setError(err instanceof Error ? err.message : String(err));
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => { alive = false; };
  }, [artifactID]);

  const download = useCallback(async () => {
    if (!capture) return;
    setDownloading(true);
    try {
      await downloadPacketCapture(capture);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setDownloading(false);
    }
  }, [capture]);

  useAssistantPage('抓包产物', 'Packet artifact', {
    artifact_id: artifactID,
    state: capture?.state,
    loading,
  });

  return (
    <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-hidden">
      <PageHeader
        title={capture?.title || tr('数据包产物', 'Packet artifact')}
        subtitle={capture ? `${packetCaptureArtifactID(capture)} · ${capture.interface_name}` : tr('逐包查看、协议树和原始字节', 'Packet list, protocol tree, and raw bytes')}
        actions={
          <Link to="/pages?tab=packets" className="inline-flex h-8 items-center gap-1.5 rounded-md border border-zinc-700 px-2.5 text-xs text-zinc-300 transition-colors hover:border-zinc-600 hover:bg-zinc-800">
            <ArrowLeft size={13} /> {tr('返回产物', 'Back to artifacts')}
          </Link>
        }
      />
      <div className="min-h-0 flex-1 overflow-hidden px-6 py-4">
        {error ? <div className="mb-4 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-xs text-red-400">{error}</div> : null}
        {loading ? (
          <div className="py-20 text-center text-xs text-zinc-500"><Loader2 size={16} className="mx-auto mb-2 animate-spin" />{tr('加载中…', 'Loading…')}</div>
        ) : !capture ? (
          <EmptyState icon={FileCode2} title={tr('找不到数据包产物', 'Packet artifact not found')} hint={tr('该产物可能已过期或不存在。', 'This artifact may have expired or does not exist.')} />
        ) : (
          <PacketArtifactViewer capture={capture} downloading={downloading} onDownload={() => void download()} tr={tr} />
        )}
      </div>
    </main>
  );
}
