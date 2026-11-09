import { useCallback, useEffect, useRef, useState } from 'react';
import { RefreshCw, ScrollText } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { PageHeader } from '@/components/ui/PageHeader';
import { PaginationFooter } from '@/components/ui/PaginationFooter';
import { Select } from '@/components/ui/Select';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import { describeError } from '../api/http';
import {
  acceptPool,
  deletePool,
  getPoolCounters,
  listPool,
  listPoolAttributes,
  listPoolCITypes,
  type PoolAttribute,
  type PoolCounters,
  type PoolEntry,
  type TargetTypeCount,
} from '../api/discovery';
import { CounterStrip } from '../components/discovery/adc/CounterStrip';
import { ExecHistoryDialog } from '../components/discovery/adc/ExecHistoryDialog';
import { PoolTable } from '../components/discovery/adc/PoolTable';
import { PoolTree } from '../components/discovery/adc/PoolTree';
import { RawDataDrawer } from '../components/discovery/adc/RawDataDrawer';
import { useToast } from '../components/Toast';

// 自动发现池 — the sightings an agent reported, before and after acceptance.
//
// THE POOL IS A QUEUE WITH A MEMORY. Accepting writes an instance; deleting a
// row does not delete that instance, because it has had a life of its own since.
// An accepted row that the provider changes becomes pending again: what a person
// accepted is no longer what the provider reports.

const PAGE_SIZE = 20;

type StatusFilter = 'all' | 'pending' | 'accepted';

export default function DiscoveryPoolPage() {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [types, setTypes] = useState<TargetTypeCount[]>([]);
  const [typesLoading, setTypesLoading] = useState(true);
  const [typesErr, setTypesErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<number | null>(null);

  const [counters, setCounters] = useState<PoolCounters | null>(null);
  const [attributes, setAttributes] = useState<PoolAttribute[]>([]);
  const [entries, setEntries] = useState<PoolEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState<StatusFilter>('all');
  const [loading, setLoading] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [accepting, setAccepting] = useState<number | null>(null);

  const [raw, setRaw] = useState<PoolEntry | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);

  // A RESPONSE BELONGS TO THE MODEL THAT ASKED FOR IT. Switching models
  // invalidates every in-flight counter, column and row request; without this
  // a slow previous response could repaint its rows under the new model's name
  // and an accept would then act on the wrong model's sighting.
  const requestSeq = useRef(0);

  const loadTypes = useCallback(async () => {
    setTypesLoading(true);
    setTypesErr(null);
    try {
      const got = await listPoolCITypes();
      setTypes(got);
      setSelected((current) => {
        if (current != null && got.some((item) => item.type_id === current)) return current;
        return got[0]?.type_id ?? null;
      });
    } catch (error) {
      setTypesErr(describeError(error, tr('无法读取发现模型', 'Could not load the discovery models')));
    } finally {
      setTypesLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    void loadTypes();
  }, [loadTypes]);

  const loadCounters = useCallback(
    async (typeID: number, seq: number) => {
      try {
        const got = await getPoolCounters(typeID);
        if (seq !== requestSeq.current) return;
        setCounters(got);
      } catch (error) {
        if (seq !== requestSeq.current) return;
        // A FAILED COUNTER IS NOT A FAILED POOL: the table is still usable, and
        // replacing the whole page with an error over a summary would hide it.
        toast.error(describeError(error, tr('无法读取发现池统计', 'Could not load the pool counters')));
      }
    },
    [toast, tr],
  );

  const loadAttributes = useCallback(
    async (typeID: number, seq: number) => {
      try {
        const got = await listPoolAttributes(typeID);
        if (seq !== requestSeq.current) return;
        setAttributes(got);
      } catch (error) {
        if (seq !== requestSeq.current) return;
        setAttributes([]);
        toast.error(describeError(error, tr('无法读取发现池列', 'Could not load the pool columns')));
      }
    },
    [toast, tr],
  );

  const loadEntries = useCallback(
    async (
      typeID: number,
      nextPage: number,
      nextStatus: StatusFilter,
      seq: number,
      quiet = false,
    ) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        const got = await listPool({
          type_id: typeID,
          page: nextPage,
          page_size: PAGE_SIZE,
          is_accept: nextStatus === 'all' ? undefined : nextStatus === 'accepted',
        });
        if (seq !== requestSeq.current) return;
        setEntries(got.items ?? []);
        setTotal(got.total ?? 0);
      } catch (error) {
        if (seq !== requestSeq.current) return;
        setErr(describeError(error, tr('无法读取发现池', 'Could not load the discovery pool')));
      } finally {
        if (seq === requestSeq.current) {
          setLoading(false);
          setRefreshing(false);
        }
      }
    },
    [tr],
  );

  useEffect(() => {
    if (selected == null) {
      requestSeq.current += 1;
      setEntries([]);
      setTotal(0);
      setCounters(null);
      setAttributes([]);
      setErr(null);
      setLoading(false);
      setRefreshing(false);
      return;
    }
    const seq = ++requestSeq.current;
    setPage(1);
    // CLEAR THE PREVIOUS MODEL BEFORE THE REQUEST, so a slow or failed load
    // cannot leave its rows and columns under the new model's title.
    setEntries([]);
    setTotal(0);
    setCounters(null);
    setAttributes([]);
    setErr(null);
    void loadCounters(selected, seq);
    void loadAttributes(selected, seq);
    void loadEntries(selected, 1, status, seq);
  }, [selected, status, loadCounters, loadAttributes, loadEntries]);

  const selectedType = types.find((item) => item.type_id === selected) ?? null;
  const labelOf = (type: TargetTypeCount | null) =>
    type ? type.alias || type.name || `#${type.type_id}` : '';

  const refresh = () => {
    if (selected == null) return;
    const seq = requestSeq.current;
    void loadCounters(selected, seq);
    void loadAttributes(selected, seq);
    void loadEntries(selected, page, status, seq, true);
  };

  const accept = async (row: PoolEntry) => {
    const seq = requestSeq.current;
    const typeID = selected;
    setAccepting(row.id);
    try {
      await acceptPool(row.id);
      toast.success(tr(`已接受 ${row.unique_value}`, `Accepted ${row.unique_value}`));
      if (typeID != null && seq === requestSeq.current) {
        await Promise.all([
          loadCounters(typeID, seq),
          loadEntries(typeID, page, status, seq, true),
        ]);
      }
    } catch (error) {
      toast.error(describeError(error, tr('接受失败', 'Could not accept it')));
    } finally {
      setAccepting(null);
    }
  };

  const remove = async (row: PoolEntry) => {
    const ok = await confirmAction(
      tr(
        `删除发现记录「${row.unique_value}」？已经接受的实例不会被删除。`,
        `Delete the sighting “${row.unique_value}”? The instance it became is not removed.`,
      ),
    );
    if (!ok) return;
    const seq = requestSeq.current;
    const typeID = selected;
    try {
      await deletePool(row.id);
      toast.success(tr('已删除', 'Deleted'));
      if (typeID != null && seq === requestSeq.current) {
        await Promise.all([
          loadCounters(typeID, seq),
          loadEntries(typeID, page, status, seq, true),
        ]);
      }
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('自动发现池', 'Discovery pool')}
        subtitle={tr(
          '采集器上报的候选资源。接受后写入 CMDB；未接受的记录不会影响现有实例。',
          'Candidate resources reported by collectors. Accepting writes them into the CMDB; a pending row never touches an existing instance.',
        )}
      />

      <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-4 overflow-y-auto p-6 lg:flex-row">
        <PoolTree
          types={types}
          selected={selected}
          loading={typesLoading}
          error={typesErr}
          onSelect={setSelected}
          onRefresh={() => void loadTypes()}
        />

        <div className="flex min-w-0 flex-1 flex-col gap-3">
          {selectedType == null ? (
            <Card>
              <EmptyState
                title={tr('选择一个模型', 'Choose a model')}
                hint={tr(
                  '左侧列出已经有发现目标的模型。',
                  'The list on the left shows the models that already have discovery targets.',
                )}
              />
            </Card>
          ) : (
            <>
              {counters ? (
                <CounterStrip counters={counters} />
              ) : (
                <p className="text-xs text-text-muted">{tr('加载统计…', 'Loading counters…')}</p>
              )}

              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="min-w-0 text-xs text-text-muted">
                  {tr(
                    `「${labelOf(selectedType)}」的发现记录。`,
                    `Sightings for “${labelOf(selectedType)}”.`,
                  )}
                </p>
                <div className="flex flex-wrap items-center gap-2">
                  <Select
                    label={tr('状态', 'Status')}
                    value={status}
                    onValueChange={(value) => setStatus(value as StatusFilter)}
                    options={[
                      { value: 'all', label: tr('全部', 'All') },
                      { value: 'pending', label: tr('待接受', 'Pending') },
                      { value: 'accepted', label: tr('已接受', 'Accepted') },
                    ]}
                    className="w-40"
                  />
                  <Button
                    variant="outline"
                    size="sm"
                    type="button"
                    onClick={() => setHistoryOpen(true)}
                    className="inline-flex items-center gap-1.5"
                  >
                    <ScrollText size={12} aria-hidden="true" />
                    {tr('执行日志', 'Discovery log')}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    type="button"
                    onClick={refresh}
                    disabled={loading || refreshing}
                    className="inline-flex items-center gap-1.5"
                  >
                    <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} aria-hidden="true" />
                    {tr('刷新', 'Refresh')}
                  </Button>
                </div>
              </div>

              <Card>
                <PoolTable
                  rows={entries}
                  attributes={attributes}
                  loading={loading}
                  error={err}
                  acceptingID={accepting}
                  onRetry={refresh}
                  onAccept={(row) => void accept(row)}
                  onDelete={(row) => void remove(row)}
                  onViewRaw={setRaw}
                />
                <PaginationFooter
                  page={page - 1}
                  pageSize={PAGE_SIZE}
                  shown={entries.length}
                  total={total}
                  loading={loading}
                  onPageChange={(next) => {
                    if (selected == null) return;
                    const target = next + 1;
                    setPage(target);
                    void loadEntries(selected, target, status, requestSeq.current);
                  }}
                />
              </Card>
            </>
          )}
        </div>
      </div>

      <RawDataDrawer entry={raw} onClose={() => setRaw(null)} />
      <ExecHistoryDialog
        open={historyOpen}
        typeID={selected}
        typeLabel={labelOf(selectedType)}
        onClose={() => setHistoryOpen(false)}
      />
      {dialog}
    </div>
  );
}
