// Settings → About. Product identity: version (live from the manager), the
// public GitHub repo, and the license. Lives here because the brand mark in the
// sidebar no longer shows the version.
import { useEffect, useState } from 'react';
import { Github, ExternalLink, Scale, Tag } from 'lucide-react';
import { useI18n } from '@/i18n/locale';
import { getManagerVersion } from '@/api/version';
import { OngridLogo } from '@/components/OngridLogo';

const GITHUB_URL = 'https://github.com/ongridio/ongrid';
const LICENSE = 'AGPL-3.0-only';
const LICENSE_URL = 'https://github.com/ongridio/ongrid/blob/main/LICENSE';

export default function About() {
  const { tr } = useI18n();
  const [version, setVersion] = useState('');

  useEffect(() => {
    let cancelled = false;
    void getManagerVersion()
      .then((r) => {
        if (!cancelled) setVersion((r.manager_version || '').trim());
      })
      .catch(() => {
        if (!cancelled) setVersion('');
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className="mx-auto max-w-2xl">
      <div className="rounded-xl border border-zinc-200 bg-white p-6 dark:border-zinc-800/60 dark:bg-zinc-900/40">
        <div className="flex items-center gap-3">
          <OngridLogo size={44} className="shrink-0" />
          <div>
            <h2 className="text-base font-semibold text-zinc-900 dark:text-zinc-100">Ongrid</h2>
            <p className="text-xs text-zinc-500">{tr('AIOps 平台', 'AIOps platform')}</p>
          </div>
        </div>

        <dl className="mt-5 divide-y divide-zinc-200 text-sm dark:divide-zinc-800/60">
          <Row icon={<Tag size={14} />} label={tr('版本', 'Version')}>
            <span className="font-mono text-zinc-800 dark:text-zinc-200">{version || '—'}</span>
          </Row>
          <Row icon={<Github size={14} />} label={tr('源码', 'Source')}>
            <a
              href={GITHUB_URL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-indigo-600 hover:underline dark:text-indigo-300"
            >
              github.com/ongridio/ongrid <ExternalLink size={12} />
            </a>
          </Row>
          <Row icon={<Scale size={14} />} label={tr('许可证', 'License')}>
            <a
              href={LICENSE_URL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-indigo-600 hover:underline dark:text-indigo-300"
            >
              {LICENSE} <ExternalLink size={12} />
            </a>
          </Row>
        </dl>

        <p className="mt-5 text-xs leading-relaxed text-zinc-500">
          {tr(
            '云端 AI 智能体 + 边缘隧道的一体化运维平台。版本与升级命令见「设置 → 升级」。',
            'A unified AIOps platform: a cloud AI agent + an edge tunnel. For upgrade commands see Settings → Upgrade.',
          )}
        </p>
      </div>
    </div>
  );
}

function Row({ icon, label, children }: { icon: React.ReactNode; label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between py-2.5">
      <span className="flex items-center gap-2 text-zinc-500">
        {icon} {label}
      </span>
      <span>{children}</span>
    </div>
  );
}
