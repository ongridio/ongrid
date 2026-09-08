import { Hint } from '@/components/ui/Tooltip';
import { useCallback, useEffect, useState, type ReactNode } from "react";
import {
  CheckCircle2,
  Clock3,
  History,
  Loader2,
  RefreshCw,
  RotateCw,
  XCircle,
} from "lucide-react";
import {
  getEdgeUpgradeJob,
  listEdgeUpgradeJobs,
  retryEdgeUpgradeJob,
  type EdgeUpgradeJob,
  type EdgeUpgradeJobDetail,
  type EdgeUpgradeJobItemStatus,
  type EdgeUpgradeJobStatus,
} from "@/api/edges";
import { Modal } from "@/components/Modal";
import { Button, Card, Chip, EmptyState, PaginationFooter } from "@/components/ui";
import { useI18n } from "@/i18n/locale";
import { fullDateTime, relativeTime } from "@/lib/format";

const HISTORY_POLL_MS = 5_000;
const PAGE_SIZE = 20;

type Props = {
  clusterID: number;
  isAdmin: boolean;
  refreshKey: number;
};

export function ClusterUpgradeHistory({
  clusterID,
  isAdmin,
  refreshKey,
}: Props) {
  const { tr } = useI18n();
  const [jobs, setJobs] = useState<EdgeUpgradeJob[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const [detail, setDetail] = useState<EdgeUpgradeJobDetail | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [retrying, setRetrying] = useState(false);

  const load = useCallback(
    async (silent = false) => {
      if (silent) setRefreshing(true);
      else setLoading(true);
      try {
        const response = await listEdgeUpgradeJobs({
          clusterNodeId: clusterID,
          page: page + 1,
          pageSize: PAGE_SIZE,
        });
        setJobs(response.items ?? []);
        setTotal(response.total ?? 0);
        setError(null);
      } catch (loadError) {
        setError(
          (loadError as Error).message ||
            tr("加载升级记录失败", "Failed to load upgrade history"),
        );
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [clusterID, page, tr],
  );

  const loadDetail = useCallback(
    async (id: number, silent = false) => {
      if (!silent) setDetail(null);
      try {
        const response = await getEdgeUpgradeJob(id);
        setDetail(response);
        setDetailError(null);
      } catch (loadError) {
        setDetailError(
          (loadError as Error).message ||
            tr("加载升级详情失败", "Failed to load upgrade details"),
        );
      }
    },
    [tr],
  );

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  useEffect(() => {
    setPage(0);
  }, [clusterID]);

  const hasActiveJobs = jobs.some((job) => !isTerminal(job.status));

  useEffect(() => {
    if (!hasActiveJobs) return;
    const timer = window.setInterval(() => void load(true), HISTORY_POLL_MS);
    return () => window.clearInterval(timer);
  }, [hasActiveJobs, load]);

  useEffect(() => {
    if (selectedID == null) return;
    void loadDetail(selectedID);
  }, [loadDetail, selectedID]);

  useEffect(() => {
    if (
      selectedID == null ||
      (detail != null && isTerminal(detail.job.status))
    ) {
      return;
    }
    const timer = window.setInterval(
      () => void loadDetail(selectedID, true),
      HISTORY_POLL_MS,
    );
    return () => window.clearInterval(timer);
  }, [detail, loadDetail, selectedID]);

  async function retry() {
    if (!detail || retrying) return;
    setRetrying(true);
    setDetailError(null);
    try {
      await retryEdgeUpgradeJob(detail.job.id);
      await Promise.all([load(true), loadDetail(detail.job.id, true)]);
    } catch (retryError) {
      setDetailError(
        (retryError as Error).message ||
          tr("重试升级失败", "Failed to retry upgrade"),
      );
    } finally {
      setRetrying(false);
    }
  }

  return (
    <>
      <Card className="p-0">
        <div className="flex items-center justify-between border-b border-zinc-800/60 px-4 py-3">
          <div>
            <h2 className="text-sm font-medium text-zinc-100">
              {tr("升级记录", "Upgrade history")}
            </h2>
            <p className="mt-0.5 text-[11px] text-zinc-500">
              {tr(
                `${total} 条任务 · Manager 后台持续确认升级结果`,
                `${total} jobs · Manager verifies rollout results in the background`,
              )}
            </p>
          </div>
          <Button disabled={refreshing} onClick={() => void load(true)}>
            <RefreshCw size={13} className={refreshing ? "animate-spin" : ""} />
            {tr("刷新", "Refresh")}
          </Button>
        </div>

        {error && (
          <div className="border-b border-red-500/20 bg-red-500/5 px-4 py-2.5 text-xs text-red-600 dark:text-red-300">
            {error}
          </div>
        )}
        {loading ? (
          <div className="flex items-center justify-center gap-2 px-4 py-10 text-xs text-zinc-500">
            <Loader2 size={14} className="animate-spin" />
            {tr("正在加载升级记录…", "Loading upgrade history…")}
          </div>
        ) : jobs.length === 0 ? (
          <EmptyState
            icon={History}
            title={tr("暂无升级记录", "No upgrade history")}
            hint={tr(
              "从该集群发起批量升级后，任务和逐设备结果会保存在这里。",
              "Batch upgrades started from this cluster and their per-device results appear here.",
            )}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="border-b border-zinc-800/60 bg-zinc-950/30 text-[11px] uppercase tracking-wide text-zinc-500">
                <tr>
                  <th className="px-4 py-2.5 font-medium">
                    {tr("任务", "Job")}
                  </th>
                  <th className="px-4 py-2.5 font-medium">
                    {tr("目标版本", "Target")}
                  </th>
                  <th className="px-4 py-2.5 font-medium">
                    {tr("进度", "Progress")}
                  </th>
                  <th className="px-4 py-2.5 font-medium">
                    {tr("状态", "Status")}
                  </th>
                  <th className="px-4 py-2.5 font-medium">
                    {tr("创建时间", "Created")}
                  </th>
                  <th className="px-4 py-2.5 text-right font-medium">
                    {tr("操作", "Actions")}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/60">
                {jobs.map((job) => (
                  <tr
                    key={job.id}
                    className="cursor-pointer hover:bg-zinc-900/40"
                    onClick={() => setSelectedID(job.id)}
                  >
                    <td className="px-4 py-3 font-medium text-zinc-100">
                      #{job.id}
                    </td>
                    <td className="px-4 py-3 font-mono text-zinc-400">
                      {job.target_version}
                    </td>
                    <td className="px-4 py-3 text-zinc-400">
                      <div>
                        {tr(
                          `${job.succeeded} 成功 · ${job.failed} 失败 · ${job.pending} 处理中`,
                          `${job.succeeded} succeeded · ${job.failed} failed · ${job.pending} pending`,
                        )}
                      </div>
                      {job.total_batches > 0 && (
                        <div className="mt-1 text-[11px] text-zinc-500">
                          {job.current_batch > 0
                            ? tr(
                                `第 ${job.current_batch}/${job.total_batches} 批`,
                                `Batch ${job.current_batch}/${job.total_batches}`,
                              )
                            : tr(
                                `共 ${job.total_batches} 批`,
                                `${job.total_batches} batches total`,
                              )}
                        </div>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <JobStatusChip status={job.status} />
                    </td>
                    <Hint content={fullDateTime(job.created_at)}><td
                      className="whitespace-nowrap px-4 py-3 text-zinc-500"

                    >
                      {relativeTime(job.created_at)}
                    </td></Hint>
                    <td className="px-4 py-3 text-right">
                      <Button onClick={() => setSelectedID(job.id)}>
                        {tr("查看", "View")}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <PaginationFooter
          page={page}
          pageSize={PAGE_SIZE}
          shown={jobs.length}
          total={total}
          loading={loading || refreshing}
          className="px-4"
          onPageChange={setPage}
        />
      </Card>

      <Modal
        open={selectedID != null}
        onClose={() => {
          setSelectedID(null);
          setDetail(null);
          setDetailError(null);
        }}
        title={
          detail
            ? tr(`升级任务 #${detail.job.id}`, `Upgrade job #${detail.job.id}`)
            : tr("升级任务", "Upgrade job")
        }
        size="lg"
        footer={
          <>
            {detail &&
              isAdmin &&
              detail.job.failed > 0 &&
              isTerminal(detail.job.status) && (
                <Button disabled={retrying} onClick={() => void retry()}>
                  {retrying ? (
                    <Loader2 size={13} className="animate-spin" />
                  ) : (
                    <RotateCw size={13} />
                  )}
                  {tr(
                    `重试 ${detail.job.failed} 台失败设备`,
                    `Retry ${detail.job.failed} failed devices`,
                  )}
                </Button>
              )}
            <Button variant="primary" onClick={() => setSelectedID(null)}>
              {tr("关闭", "Close")}
            </Button>
          </>
        }
      >
        <UpgradeJobDetailView detail={detail} error={detailError} />
      </Modal>
    </>
  );
}

function UpgradeJobDetailView({
  detail,
  error,
}: {
  detail: EdgeUpgradeJobDetail | null;
  error: string | null;
}) {
  const { tr } = useI18n();
  if (error) {
    return (
      <div className="rounded-lg border border-red-500/20 bg-red-500/5 px-3 py-3 text-xs text-red-600 dark:text-red-300">
        {error}
      </div>
    );
  }
  if (!detail) {
    return (
      <div className="flex items-center justify-center gap-2 py-10 text-xs text-zinc-500">
        <Loader2 size={14} className="animate-spin" />
        {tr("正在加载…", "Loading…")}
      </div>
    );
  }
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-6">
        <DetailMetric label={tr("总数", "Total")} value={detail.job.total} />
        <DetailMetric
          label={tr("成功", "Succeeded")}
          value={detail.job.succeeded}
          tone="success"
        />
        <DetailMetric
          label={tr("失败", "Failed")}
          value={detail.job.failed}
          tone={detail.job.failed > 0 ? "danger" : "default"}
        />
        <DetailMetric
          label={tr("跳过", "Skipped")}
          value={detail.job.skipped}
        />
        <DetailMetric
          label={tr("处理中", "Pending")}
          value={detail.job.pending}
          tone={detail.job.pending > 0 ? "info" : "default"}
        />
        <DetailMetric
          label={tr("滚动批次", "Rollout batch")}
          value={
            detail.job.total_batches > 0
              ? `${detail.job.current_batch}/${detail.job.total_batches}`
              : "—"
          }
          tone={detail.job.pending > 0 ? "info" : "default"}
        />
      </div>
      <div className="overflow-hidden rounded-lg border border-zinc-200 dark:border-zinc-800/60">
        <div className="divide-y divide-zinc-200 dark:divide-zinc-800/60">
          {detail.items.map((item) => (
            <div
              key={item.id}
              className="flex items-start justify-between gap-4 px-3 py-2.5"
            >
              <div className="min-w-0">
                <div className="truncate text-xs font-medium text-zinc-700 dark:text-zinc-200">
                  {item.device_name ||
                    item.edge_name ||
                    `Edge #${item.edge_id}`}
                </div>
                <div className="mt-0.5 text-[11px] text-zinc-500">
                  {item.batch_number > 0 && (
                    <>
                      {tr(
                        `第 ${item.batch_number} 批`,
                        `batch ${item.batch_number}`,
                      )}{" "}
                      ·{" "}
                    </>
                  )}
                  {item.arch || "—"} ·{" "}
                  {item.from_version || tr("未知版本", "unknown version")} →{" "}
                  {item.target_version} ·{" "}
                  {tr(`第 ${item.attempt} 次`, `attempt ${item.attempt}`)}
                </div>
                {item.error_message && (
                  <div className="mt-1 break-words text-[11px] leading-4 text-red-600 dark:text-red-300">
                    {item.error_message}
                  </div>
                )}
              </div>
              <ItemStatusChip status={item.status} />
            </div>
          ))}
        </div>
      </div>
      <div className="text-[11px] leading-5 text-zinc-500">
        {tr(
          `创建于 ${fullDateTime(detail.job.created_at)}${detail.job.finished_at ? ` · 完成于 ${fullDateTime(detail.job.finished_at)}` : ""}`,
          `Created ${fullDateTime(detail.job.created_at)}${detail.job.finished_at ? ` · finished ${fullDateTime(detail.job.finished_at)}` : ""}`,
        )}
      </div>
    </div>
  );
}

function JobStatusChip({ status }: { status: EdgeUpgradeJobStatus }) {
  const { tr } = useI18n();
  const config = {
    queued: { label: tr("排队中", "Queued"), tone: "info" as const },
    running: { label: tr("执行中", "Running"), tone: "info" as const },
    succeeded: { label: tr("已完成", "Succeeded"), tone: "success" as const },
    partial_failed: {
      label: tr("部分失败", "Partial failure"),
      tone: "warning" as const,
    },
    failed: { label: tr("失败", "Failed"), tone: "danger" as const },
  }[status];
  return <Chip tone={config.tone}>{config.label}</Chip>;
}

function ItemStatusChip({ status }: { status: EdgeUpgradeJobItemStatus }) {
  const { tr } = useI18n();
  const config: Record<
    EdgeUpgradeJobItemStatus,
    {
      label: string;
      tone: "default" | "success" | "warning" | "danger" | "info";
      icon?: ReactNode;
    }
  > = {
    queued: {
      label: tr("排队中", "Queued"),
      tone: "default",
      icon: <Clock3 size={10} />,
    },
    dispatching: {
      label: tr("正在下发", "Dispatching"),
      tone: "info",
      icon: <Loader2 size={10} className="animate-spin" />,
    },
    waiting_registration: {
      label: tr("等待重连", "Waiting"),
      tone: "info",
      icon: <Loader2 size={10} className="animate-spin" />,
    },
    succeeded: {
      label: tr("成功", "Succeeded"),
      tone: "success",
      icon: <CheckCircle2 size={10} />,
    },
    failed: {
      label: tr("失败", "Failed"),
      tone: "danger",
      icon: <XCircle size={10} />,
    },
    timed_out: {
      label: tr("确认超时", "Timed out"),
      tone: "warning",
      icon: <Clock3 size={10} />,
    },
    skipped: { label: tr("已跳过", "Skipped"), tone: "default" },
  };
  const current = config[status];
  return (
    <Chip tone={current.tone} className="shrink-0">
      {current.icon}
      {current.label}
    </Chip>
  );
}

function DetailMetric({
  label,
  value,
  tone = "default",
}: {
  label: string;
  value: number | string;
  tone?: "default" | "success" | "danger" | "info";
}) {
  const valueClass = {
    default: "text-zinc-700 dark:text-zinc-200",
    success: "text-emerald-600 dark:text-emerald-300",
    danger: "text-red-600 dark:text-red-300",
    info: "text-indigo-600 dark:text-indigo-300",
  }[tone];
  return (
    <div className="rounded-lg border border-zinc-200 px-3 py-2.5 dark:border-zinc-800/60">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div className={`mt-1 text-lg font-semibold tabular-nums ${valueClass}`}>
        {value}
      </div>
    </div>
  );
}

function isTerminal(status: EdgeUpgradeJobStatus) {
  return (
    status === "succeeded" || status === "partial_failed" || status === "failed"
  );
}
