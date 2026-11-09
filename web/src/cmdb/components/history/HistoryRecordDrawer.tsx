import { useI18n } from '@/i18n/locale';
import { fullDateTime } from '@/lib/format';

import type {
  AttributeRecord,
  RelationRecord,
  TriggerHistoryItem,
  TypeHistoryItem,
} from '../../api/history';
import { Drawer } from '../Drawer';

// One write's changes, in full.
//
// THE TABLE SHOWS A SUMMARY because a write can change a dozen attributes; the
// drawer is where the whole record lives, including the operation's origin,
// ticket and reason — the fields a person needs when they are asking "why did
// this change" rather than "what changed".

export type HistorySelection =
  | { kind: 'attributes'; record: AttributeRecord }
  | { kind: 'relations'; record: RelationRecord }
  | { kind: 'types'; item: TypeHistoryItem }
  | { kind: 'triggers'; item: TriggerHistoryItem };

function Meta({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] text-text-faint">{label}</dt>
      <dd className="truncate text-xs text-text-muted">{value || '—'}</dd>
    </div>
  );
}

export function HistoryRecordDrawer({
  selection,
  userLabel,
  onClose,
}: {
  selection: HistorySelection | null;
  userLabel(uid?: number): string;
  onClose(): void;
}) {
  const { tr } = useI18n();

  return (
    <Drawer
      open={selection !== null}
      onClose={onClose}
      size="lg"
      title={tr('变更详情', 'Change details')}
      description={tr('一次写入的全部变更，以及它的来源。', 'Every change in one write, and where it came from.')}
    >
      {selection?.kind === 'attributes' && (
        <div className="flex flex-col gap-4">
          <dl className="grid grid-cols-2 gap-3">
            <Meta label={tr('时间', 'Time')} value={fullDateTime(selection.record.created_at)} />
            <Meta label={tr('用户', 'User')} value={userLabel(selection.record.uid)} />
            <Meta label={tr('模型', 'Model')} value={selection.record.type_alias || selection.record.type_name || ''} />
            <Meta label={tr('来源', 'Origin')} value={selection.record.origin ?? ''} />
            <Meta label={tr('工单', 'Ticket')} value={selection.record.ticket_id ?? ''} />
            <Meta label={tr('原因', 'Reason')} value={selection.record.reason ?? ''} />
          </dl>
          <section>
            <h3 className="mb-2 text-xs font-semibold text-text">{tr('变更明细', 'Changed attributes')}</h3>
            <ul className="flex flex-col gap-2">
              {selection.record.changes.map((change) => (
                <li key={change.id} className="rounded-md border border-border px-3 py-2">
                  <div className="flex flex-wrap items-center gap-2 text-xs">
                    <span className="font-medium text-text">{change.attr_alias || change.attr_name || `#${change.attr_id}`}</span>
                    <span className="text-text-faint">#{change.ci_id}</span>
                    {change.ci_name && <span className="text-text-muted">{change.ci_name}</span>}
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-2 font-mono text-[11px]">
                    <code className="rounded bg-zinc-900/60 px-1.5 py-0.5 text-text-muted">{change.old || '—'}</code>
                    <span className="text-text-faint">→</span>
                    <code className="rounded bg-zinc-900/60 px-1.5 py-0.5 text-emerald-300">{change.new || '—'}</code>
                  </div>
                </li>
              ))}
            </ul>
          </section>
        </div>
      )}

      {selection?.kind === 'relations' && (
        <div className="flex flex-col gap-4">
          <dl className="grid grid-cols-2 gap-3">
            <Meta label={tr('时间', 'Time')} value={fullDateTime(selection.record.created_at)} />
            <Meta label={tr('用户', 'User')} value={userLabel(selection.record.uid)} />
            <Meta label={tr('模型', 'Model')} value={selection.record.type_alias || selection.record.type_name || ''} />
            <Meta label={tr('来源', 'Origin')} value={selection.record.origin ?? ''} />
          </dl>
          <section>
            <h3 className="mb-2 text-xs font-semibold text-text">{tr('变更明细', 'Changed relations')}</h3>
            <ul className="flex flex-col gap-2">
              {selection.record.changes.map((change) => (
                <li key={change.id} className="flex flex-wrap items-center gap-2 rounded-md border border-border px-3 py-2 text-xs">
                  <span className="text-text">{change.first_ci_name || `#${change.first_ci_id}`}</span>
                  <span className="text-text-faint">→</span>
                  <span className="text-text">{change.second_ci_name || `#${change.second_ci_id}`}</span>
                  <span className="text-text-faint">
                    {tr(`关系类型 #${change.relation_type_id ?? '—'}`, `relation type #${change.relation_type_id ?? '—'}`)}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        </div>
      )}

      {selection?.kind === 'types' && (
        <div className="flex flex-col gap-4">
          <dl className="grid grid-cols-2 gap-3">
            <Meta label={tr('时间', 'Time')} value={fullDateTime(selection.item.created_at)} />
            <Meta label={tr('用户', 'User')} value={userLabel(selection.item.uid)} />
            <Meta label={tr('模型', 'Model')} value={selection.item.type_alias || selection.item.type_name || ''} />
            <Meta label={tr('属性', 'Attribute')} value={selection.item.attr_alias || selection.item.attr_name || ''} />
          </dl>
          <section>
            <h3 className="mb-2 text-xs font-semibold text-text">{tr('变更明细', 'Change')}</h3>
            <pre className="max-h-[50vh] overflow-auto rounded-md border border-border bg-zinc-950/60 p-3 font-mono text-[11px] text-text-muted">
              {JSON.stringify(selection.item.change ?? {}, null, 2)}
            </pre>
          </section>
        </div>
      )}

      {selection?.kind === 'triggers' && (
        <div className="flex flex-col gap-4">
          <dl className="grid grid-cols-2 gap-3">
            <Meta label={tr('时间', 'Time')} value={fullDateTime(selection.item.created_at)} />
            <Meta label={tr('用户', 'User')} value={userLabel(selection.item.uid)} />
            <Meta label={tr('模型', 'Model')} value={selection.item.type_name ?? ''} />
            <Meta label={tr('资源', 'Instance')} value={selection.item.ci_name || `#${selection.item.ci_id}`} />
            <Meta label={tr('触发器', 'Trigger')} value={selection.item.trigger_name ?? ''} />
            <Meta label={tr('结果', 'Result')} value={selection.item.is_ok ? tr('成功', 'Succeeded') : tr('失败', 'Failed')} />
          </dl>
          <section>
            <h3 className="mb-2 text-xs font-semibold text-text">{tr('变更明细', 'Delivery')}</h3>
            <pre className="max-h-[40vh] overflow-auto rounded-md border border-border bg-zinc-950/60 p-3 font-mono text-[11px] text-text-muted">
              {JSON.stringify({ notify: selection.item.notify, webhook: selection.item.webhook }, null, 2)}
            </pre>
          </section>
        </div>
      )}
    </Drawer>
  );
}
