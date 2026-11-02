import { useAssistantPage } from '@/lib/assistantPage';
// PageView — full-screen viewer for a hosted serve_page artifact, opened in a
// NEW TAB from the 产物 list's 打开 button. Pages are private (authed): we fetch
// the HTML with the bearer (token in localStorage, shared across tabs) and
// render it in a sandboxed iframe srcdoc that fills the whole tab. srcdoc
// inherits the SPA document's UTF-8 encoding, so Chinese renders correctly —
// unlike the old blob: URL approach which dropped the charset and mojibake'd.
import { useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';

import { fetchPageHTML } from '@/api/pages';

export default function PageView() {
  const { id } = useParams<{ id: string }>();
  const [html, setHtml] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!id) return;
    let alive = true;
    setHtml(null);
    setFailed(false);
    fetchPageHTML(id)
      .then((h) => alive && setHtml(h))
      .catch(() => alive && setFailed(true));
    return () => {
      alive = false;
    };
  }, [id]);

  useAssistantPage('产物查看器', 'Artifact viewer', {
    artifact_id: id,
    state: failed ? "failed" : html == null ? "loading" : "ready",
  });

  if (failed) {
    return (
      <div className="flex h-screen w-screen items-center justify-center bg-white text-sm text-zinc-500">
        页面加载失败 / failed to load
      </div>
    );
  }
  if (html == null) {
    return (
      <div className="flex h-screen w-screen items-center justify-center bg-white text-sm text-zinc-400">
        <Loader2 size={16} className="mr-2 animate-spin" /> 加载中…
      </div>
    );
  }
  return (
    <iframe
      title="page"
      srcDoc={html}
      sandbox="allow-popups allow-downloads"
      className="h-screen w-screen border-0 bg-white"
    />
  );
}
