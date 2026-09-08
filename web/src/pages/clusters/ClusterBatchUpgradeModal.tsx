import { Label } from '@/components/ui';
import { Checkbox } from '@/components/ui/Checkbox';
import { useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  CheckCircle2,
  Loader2,
  PackageOpen,
  XCircle,
} from "lucide-react";
import {
  createEdgeUpgradeJob,
  type EdgeUpgradeBundle,
  type EdgeUpgradeJob,
} from "@/api/edges";
import { Modal } from "@/components/Modal";
import { Button, Chip } from "@/components/ui";
import { useI18n } from "@/i18n/locale";
import { formatBytes } from "@/lib/format";
import {
  groupUpgradeTargets,
  type ClusterUpgradePlan,
  type ClusterUpgradeTarget,
} from "./upgrade";

type Props = {
  open: boolean;
  clusterID: number;
  clusterName: string;
  managerVersion: string;
  plan: ClusterUpgradePlan;
  forcePlan: ClusterUpgradePlan;
  bundles: EdgeUpgradeBundle[];
  bundleCatalogError: string | null;
  onClose(): void;
  onFinished(): void | Promise<void>;
};

export function ClusterBatchUpgradeModal({
  open,
  clusterID,
  clusterName,
  managerVersion,
  plan,
  forcePlan,
  bundles,
  bundleCatalogError,
  onClose,
  onFinished,
}: Props) {
  const { tr } = useI18n();
  const [forceReinstall, setForceReinstall] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submittedJob, setSubmittedJob] = useState<EdgeUpgradeJob | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setForceReinstall(false);
    setSubmitting(false);
    setSubmittedJob(null);
    setSubmitError(null);
  }, [open]);

  const selectedPlan = forceReinstall ? forcePlan : plan;
  const architectureGroups = useMemo(
    () => [...groupUpgradeTargets(selectedPlan.targets).entries()],
    [selectedPlan.targets],
  );
  const skipped =
    selectedPlan.upToDate.length +
    selectedPlan.missingBundle.length +
    selectedPlan.offline.length +
    selectedPlan.unlinked.length +
    selectedPlan.unsupported.length;
  const preflightBlocked = Boolean(bundleCatalogError) || !managerVersion;

  async function submit() {
    if (submitting || selectedPlan.targets.length === 0 || preflightBlocked)
      return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const job = await createEdgeUpgradeJob({
        edge_ids: selectedPlan.targets.map((target) => target.edge.id),
        target_version: managerVersion,
        cluster_node_id: clusterID,
        force_reinstall: forceReinstall,
      });
      setSubmittedJob(job);
      await onFinished();
    } catch (error) {
      setSubmitError(
        (error as Error).message ||
          tr("创建升级任务失败", "Failed to create upgrade job"),
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={submitting ? () => undefined : onClose}
      title={tr(`批量升级 · ${clusterName}`, `Batch upgrade · ${clusterName}`)}
      size="lg"
      footer={
        submittedJob ? (
          <Button variant="primary" onClick={onClose}>
            {tr("关闭并在升级记录中查看", "Close and view in upgrade history")}
          </Button>
        ) : (
          <>
            <Button disabled={submitting} onClick={onClose}>
              {tr("取消", "Cancel")}
            </Button>
            <Button
              variant="primary"
              disabled={
                submitting ||
                selectedPlan.targets.length === 0 ||
                preflightBlocked
              }
              onClick={() => void submit()}
            >
              {submitting ? (
                <Loader2 size={13} className="animate-spin" />
              ) : (
                <PackageOpen size={13} />
              )}
              {submitting
                ? tr("正在创建后台任务…", "Creating background job…")
                : tr(
                    `升级 ${selectedPlan.targets.length} 台设备`,
                    `Upgrade ${selectedPlan.targets.length} devices`,
                  )}
            </Button>
          </>
        )
      }
    >
      {submittedJob ? (
        <UpgradeJobCreated job={submittedJob} />
      ) : (
        <UpgradePreflight
          plan={selectedPlan}
          defaultPlan={plan}
          managerVersion={managerVersion}
          bundles={bundles}
          bundleCatalogError={bundleCatalogError}
          architectureGroups={architectureGroups}
          skipped={skipped}
          forceReinstall={forceReinstall}
          onForceReinstallChange={setForceReinstall}
          submitError={submitError}
        />
      )}
    </Modal>
  );
}

function UpgradeJobCreated({ job }: { job: EdgeUpgradeJob }) {
  const { tr } = useI18n();
  return (
    <div className="space-y-4">
      <div className="flex items-start gap-3 rounded-lg border border-emerald-500/20 bg-emerald-500/5 px-3 py-3">
        <CheckCircle2 size={16} className="mt-0.5 shrink-0 text-emerald-500" />
        <div>
          <div className="text-sm font-medium text-zinc-700 dark:text-zinc-200">
            {tr(`升级任务 #${job.id} 已创建`, `Upgrade job #${job.id} created`)}
          </div>
          <p className="mt-1 text-xs leading-5 text-zinc-500">
            {tr(
              "Manager 会在后台继续下发并确认设备重新注册。现在可以关闭弹窗或离开页面，不会中断升级。",
              "Manager continues dispatch and re-registration verification in the background. You can close this dialog or leave the page without interrupting the rollout.",
            )}
          </p>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
        <PreflightMetric
          label={tr("目标设备", "Targets")}
          count={job.total}
          tone="info"
        />
        <PreflightMetric
          label={tr("等待处理", "Pending")}
          count={job.pending}
          tone="warning"
        />
        <PreflightMetric
          label={tr("已完成", "Completed")}
          count={job.succeeded}
          tone="success"
        />
        <PreflightMetric
          label={tr("滚动批次", "Rolling batches")}
          value={`${job.total_batches} × ≤${job.batch_size}`}
          tone="info"
        />
        <PreflightMetric
          label={tr("目标版本", "Target version")}
          value={job.target_version}
          tone="default"
        />
      </div>
    </div>
  );
}

function UpgradePreflight({
  plan,
  defaultPlan,
  managerVersion,
  bundles,
  bundleCatalogError,
  architectureGroups,
  skipped,
  forceReinstall,
  onForceReinstallChange,
  submitError,
}: {
  plan: ClusterUpgradePlan;
  defaultPlan: ClusterUpgradePlan;
  managerVersion: string;
  bundles: EdgeUpgradeBundle[];
  bundleCatalogError: string | null;
  architectureGroups: Array<
    [ClusterUpgradeTarget["packageArch"], ClusterUpgradeTarget[]]
  >;
  skipped: number;
  forceReinstall: boolean;
  onForceReinstallChange(value: boolean): void;
  submitError: string | null;
}) {
  const { tr } = useI18n();
  return (
    <div className="space-y-4">
      <div className="flex items-start gap-3 rounded-lg border border-amber-500/20 bg-amber-500/5 px-3 py-2.5">
        <AlertTriangle size={15} className="mt-0.5 shrink-0 text-amber-500" />
        <p className="text-xs leading-5 text-zinc-500 dark:text-zinc-400">
          {tr(
            "仅升级在线且已关联 Host Edge 的 Linux 设备。Manager 默认每批最多处理 10 台，当前批次全部成功、失败或超时后才会继续下一批，并以设备重新注册和目标版本作为成功条件。",
            "Only online Linux devices linked to a Host Edge are upgraded. Manager processes up to 10 devices per batch and starts the next batch only after every device in the current batch succeeds, fails, or times out. Success requires re-registration with the target version.",
          )}
        </p>
      </div>

      {(bundleCatalogError || submitError) && (
        <div className="flex items-start gap-3 rounded-lg border border-red-500/20 bg-red-500/5 px-3 py-2.5">
          <XCircle size={15} className="mt-0.5 shrink-0 text-red-500" />
          <div>
            <div className="text-xs font-medium text-red-600 dark:text-red-300">
              {bundleCatalogError
                ? tr(
                    "无法验证升级制品，已阻止升级",
                    "Artifact verification failed; upgrade blocked",
                  )
                : tr("创建升级任务失败", "Failed to create upgrade job")}
            </div>
            <div className="mt-1 break-words text-[11px] leading-5 text-zinc-500">
              {bundleCatalogError || submitError}
            </div>
          </div>
        </div>
      )}

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
        <PreflightMetric
          label={tr("可升级", "Eligible")}
          count={plan.targets.length}
          tone="success"
        />
        <PreflightMetric
          label={tr("已是目标版本", "Up to date")}
          count={plan.upToDate.length}
          tone={plan.upToDate.length > 0 ? "info" : "default"}
        />
        <PreflightMetric
          label={tr("制品缺失", "Artifact missing")}
          count={plan.missingBundle.length}
          tone={plan.missingBundle.length > 0 ? "danger" : "default"}
        />
        <PreflightMetric
          label={tr("离线", "Offline")}
          count={plan.offline.length}
          tone={plan.offline.length > 0 ? "warning" : "default"}
        />
        <PreflightMetric
          label={tr("未关联 Edge", "No Edge")}
          count={plan.unlinked.length}
          tone={plan.unlinked.length > 0 ? "warning" : "default"}
        />
        <PreflightMetric
          label={tr("架构不支持", "Unsupported")}
          count={plan.unsupported.length}
          tone={plan.unsupported.length > 0 ? "warning" : "default"}
        />
      </div>

      <div className="overflow-hidden rounded-lg border border-zinc-200 bg-white dark:border-zinc-800/60 dark:bg-zinc-950/30">
        <div className="flex items-center justify-between border-b border-zinc-200 px-3 py-2.5 dark:border-zinc-800/60">
          <span className="text-xs font-medium text-zinc-700 dark:text-zinc-300">
            {tr("升级制品", "Upgrade artifacts")}
          </span>
          <span className="text-[11px] text-zinc-500">
            {tr("目标版本", "Target version")}{" "}
            {managerVersion || tr("未知", "Unknown")}
          </span>
        </div>
        <div className="divide-y divide-zinc-200 dark:divide-zinc-800/60">
          {bundles.map((bundle) => (
            <div
              key={`${bundle.arch}-${bundle.version}`}
              className="flex items-center justify-between gap-3 px-3 py-2.5"
            >
              <div className="min-w-0">
                <div className="font-mono text-xs text-zinc-700 dark:text-zinc-300">
                  {bundle.arch}
                </div>
                <div className="mt-0.5 truncate text-[11px] text-zinc-500">
                  {bundle.available
                    ? `${formatBytes(bundle.bytes)} · sha256 ${bundle.sha256?.slice(0, 12)}…`
                    : bundle.error || tr("制品不可用", "Artifact unavailable")}
                </div>
              </div>
              <Chip tone={bundle.available ? "success" : "danger"}>
                {bundle.available ? tr("就绪", "Ready") : tr("缺失", "Missing")}
              </Chip>
            </div>
          ))}
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border border-zinc-200 bg-white dark:border-zinc-800/60 dark:bg-zinc-950/30">
        <div className="border-b border-zinc-200 px-3 py-2.5 text-xs font-medium text-zinc-700 dark:border-zinc-800/60 dark:text-zinc-300">
          {tr("本次升级范围", "Upgrade scope")}
        </div>
        {architectureGroups.length > 0 ? (
          <div className="divide-y divide-zinc-200 dark:divide-zinc-800/60">
            {architectureGroups.map(([arch, targets]) => (
              <div
                key={arch}
                className="flex items-center justify-between px-3 py-2.5 text-xs"
              >
                <span className="font-mono text-zinc-700 dark:text-zinc-300">
                  {arch}
                </span>
                <Chip tone="info">
                  {tr(`${targets.length} 台设备`, `${targets.length} devices`)}
                </Chip>
              </div>
            ))}
          </div>
        ) : (
          <div className="px-3 py-7 text-center text-xs text-zinc-500">
            {tr("当前没有可升级设备", "No devices are currently eligible")}
          </div>
        )}
      </div>

      {defaultPlan.upToDate.length > 0 && (
        <Label className="flex cursor-pointer items-start gap-3 rounded-lg border border-zinc-200 px-3 py-2.5 dark:border-zinc-800/60">
          <Checkbox

            checked={forceReinstall}
            onCheckedChange={(checkedValue) => onForceReinstallChange(checkedValue)}
            className="mt-0.5 h-4 w-4"
          />
          <span>
            <span className="block text-xs font-medium text-zinc-700 dark:text-zinc-300">
              {tr(
                "强制重新安装同版本设备",
                "Force reinstall same-version devices",
              )}
            </span>
            <span className="mt-1 block text-[11px] leading-5 text-zinc-500">
              {tr(
                `默认跳过 ${defaultPlan.upToDate.length} 台已是目标版本的设备。仅在修复损坏安装时启用。`,
                `${defaultPlan.upToDate.length} up-to-date devices are skipped by default. Enable only to repair a damaged installation.`,
              )}
            </span>
          </span>
        </Label>
      )}

      {skipped > 0 && (
        <details className="rounded-lg border border-zinc-200 px-3 py-2.5 text-xs dark:border-zinc-800/60">
          <summary className="cursor-pointer text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200">
            {tr(
              `查看将跳过的 ${skipped} 台设备`,
              `View ${skipped} skipped devices`,
            )}
          </summary>
          <div className="mt-3 space-y-3 border-t border-zinc-200 pt-3 dark:border-zinc-800/60">
            <SkippedTargets
              label={tr("已是目标版本", "Already at target version")}
              targets={plan.upToDate}
            />
            <SkippedTargets
              label={tr(
                "对应架构制品不可用",
                "Artifact unavailable for architecture",
              )}
              targets={plan.missingBundle}
            />
            <SkippedDevices
              label={tr("离线", "Offline")}
              devices={plan.offline}
            />
            <SkippedDevices
              label={tr("未关联 Edge", "No Edge")}
              devices={plan.unlinked}
            />
            <SkippedDevices
              label={tr("系统或架构不支持", "Unsupported OS or architecture")}
              devices={plan.unsupported}
            />
          </div>
        </details>
      )}
    </div>
  );
}

function PreflightMetric({
  label,
  count,
  value,
  tone,
}: {
  label: string;
  count?: number;
  value?: string;
  tone: "default" | "success" | "warning" | "danger" | "info";
}) {
  const valueClass = {
    default: "text-zinc-700 dark:text-zinc-200",
    success: "text-emerald-600 dark:text-emerald-300",
    warning: "text-amber-600 dark:text-amber-300",
    danger: "text-red-600 dark:text-red-300",
    info: "text-indigo-600 dark:text-indigo-300",
  }[tone];
  return (
    <div className="rounded-lg border border-zinc-200 bg-white px-3 py-2.5 dark:border-zinc-800/60 dark:bg-zinc-950/30">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div
        className={`mt-1 truncate text-lg font-semibold tabular-nums ${valueClass}`}
      >
        {value ?? count ?? 0}
      </div>
    </div>
  );
}

function SkippedTargets({
  label,
  targets,
}: {
  label: string;
  targets: ClusterUpgradeTarget[];
}) {
  return (
    <SkippedDevices
      label={label}
      devices={targets.map((target) => target.device)}
    />
  );
}

function SkippedDevices({
  label,
  devices,
}: {
  label: string;
  devices: Array<{ id: number; name: string; hostname?: string }>;
}) {
  if (devices.length === 0) return null;
  return (
    <div>
      <div className="text-[11px] font-medium text-zinc-500">{label}</div>
      <div className="mt-1 flex flex-wrap gap-1">
        {devices.map((device) => (
          <Chip key={device.id}>
            {device.name || device.hostname || `#${device.id}`}
          </Chip>
        ))}
      </div>
    </div>
  );
}
