import { useI18n } from '@/i18n/locale';

import type { PoolEntry } from '../../../api/discovery';
import { Drawer } from '../../Drawer';

// The provider's report, as it arrived.
//
// IT IS A DRAWER AND NOT A TABLE COLUMN because it is the one view that should
// NOT be normalised: an operator debugging a mapping needs the provider's own
// field names, including the ones no model attribute was mapped from.

export function RawDataDrawer({ entry, onClose }: { entry: PoolEntry | null; onClose(): void }) {
  const { tr } = useI18n();

  return (
    <Drawer
      open={entry !== null}
      onClose={onClose}
      size="lg"
      title={tr('原始发现数据', 'Raw discovery data')}
      description={
        entry
          ? tr(
              `唯一值 ${entry.unique_value}${entry.adr_name ? ` · 规则 ${entry.adr_name}` : ''}`,
              `Unique value ${entry.unique_value}${entry.adr_name ? ` · rule ${entry.adr_name}` : ''}`,
            )
          : undefined
      }
    >
      {entry && (
        <div className="flex flex-col gap-3">
          <dl className="grid grid-cols-2 gap-2 text-xs">
            <div>
              <dt className="text-text-faint">{tr('上报采集器', 'Reported by')}</dt>
              <dd className="text-text-muted">{entry.oneagent_name || entry.oneagent_id || '—'}</dd>
            </div>
            <div>
              <dt className="text-text-faint">{tr('状态', 'Status')}</dt>
              <dd className="text-text-muted">{entry.is_accept ? tr('已接受', 'Accepted') : tr('待接受', 'Pending')}</dd>
            </div>
            <div>
              <dt className="text-text-faint">{tr('实例 ID', 'Instance')}</dt>
              <dd className="text-text-muted">{entry.ci_id ?? '—'}</dd>
            </div>
            <div>
              <dt className="text-text-faint">{tr('接受人', 'Accepted by')}</dt>
              <dd className="text-text-muted">{entry.accept_by || '—'}</dd>
            </div>
          </dl>
          <pre className="max-h-[60vh] overflow-auto rounded-md border border-border bg-zinc-950/60 p-3 font-mono text-[11px] leading-relaxed text-text-muted">
            {JSON.stringify(entry.instance ?? {}, null, 2)}
          </pre>
        </div>
      )}
    </Drawer>
  );
}
