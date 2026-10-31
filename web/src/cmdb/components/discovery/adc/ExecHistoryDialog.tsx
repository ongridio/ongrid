import { useCallback, useEffect, useState } from 'react';
import { Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { PaginationFooter } from '@/components/ui/PaginationFooter';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';

import { describeError } from '../../../api/http';
import { clearExecHistories, listExecHistories, type ExecHistory } from '../../../api/discovery';
import { useToast } from '../../Toast';

// The model's discovery log.
//
// A LINE IS A RUN'S OWN WORDS, not a rendered event: the agent's report, the
// accept, and the failures between them are written by the components that saw
// them, and the console shows the text it was given. Filtering that text into
// columns would lose exactly the detail a person opens the dialog for.

const PAGE_SIZE = 20;

export function ExecHistoryDialog({
  open,
  typeID,
  typeLabel,
  onClose,
}: {
  open: boolean;
  typeID: number | null;
  typeLabel: string;
  onClose(): void;
}) {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [rows, setRows] = useState<ExecHistory[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(
    async (nextPage: number) => {
      if (typeID == null) return;
      setLoading(true);
      setErr(null);
      try {
        const got = await listExecHistories(typeID, nextPage, PAGE_SIZE);
        setRows(got.items ?? []);
        setTotal(got.total ?? 0);
      } catch (error) {
        setErr(describeError(error, tr('无法读取执行日志', 'Could not load the discovery log')));
      } finally {
        setLoading(false);
      }
    },
    [typeID, tr],
  );

  useEffect(() => {
    if (!open) return;
    setPage(1);
    void load(1);
  }, [open, load]);

  const clear = async () => {
    if (typeID == null) return;
    const ok = await confirmAction(
      tr(
        `清空「${typeLabel}」的全部执行日志？已经写入的实例不会受影响。`,
        `Clear every log line for “${typeLabel}”? Instances already written are not affected.`,
      ),
    );
    if (!ok) return;
    setClearing(true);
    try {
      await clearExecHistories(typeID);
      toast.success(tr('已清空执行日志', 'Discovery log cleared'));
      setPage(1);
      await load(1);
    } catch (error) {
      toast.error(describeError(error, tr('清空失败', 'Could not clear the log')));
    } finally {
      setClearing(false);
    }
  };

  return (
    <>
      <Modal
        open={open}
        onClose={onClose}
        size="lg"
        title={tr(`执行日志 · ${typeLabel}`, `Discovery log · ${typeLabel}`)}
        footer={
          <div className="flex flex-wrap justify-between gap-2">
            <Button
              variant="dangerGhost"
              type="button"
              onClick={() => void clear()}
              disabled={clearing || total === 0}
              className="inline-flex items-center gap-1.5"
            >
              <Trash2 size={12} aria-hidden="true" />
              {clearing ? tr('清空中…', 'Clearing…') : tr('清空日志', 'Clear log')}
            </Button>
            <Button variant="outline" type="button" onClick={onClose}>
              {tr('关闭', 'Close')}
            </Button>
          </div>
        }
      >
        {err && (
          <p role="alert" className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
            {err}
          </p>
        )}
        {loading && rows.length === 0 ? (
          <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
        ) : rows.length === 0 ? (
          <p className="py-6 text-center text-xs text-text-faint">{tr('没有执行日志', 'No discovery log yet')}</p>
        ) : (
          <ol className="flex max-h-[55vh] flex-col gap-2 overflow-y-auto">
            {rows.map((row) => (
              <li key={row.id} className="rounded-md border border-border px-3 py-2">
                <p className="whitespace-pre-wrap break-words font-mono text-[11px] text-text-muted">{row.stdout}</p>
                {row.created_at && <p className="mt-1 text-[10px] text-text-faint">{row.created_at}</p>}
              </li>
            ))}
          </ol>
        )}
        <PaginationFooter
          page={page - 1}
          pageSize={PAGE_SIZE}
          shown={rows.length}
          total={total}
          loading={loading}
          onPageChange={(next) => {
            const target = next + 1;
            setPage(target);
            void load(target);
          }}
        />
      </Modal>
      {dialog}
    </>
  );
}
