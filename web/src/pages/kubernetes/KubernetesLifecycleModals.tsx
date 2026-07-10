import { useEffect, useState } from 'react';
import { Check, Clipboard, Trash2 } from 'lucide-react';

import type { KubernetesCluster } from '@/api/kubernetes';
import { Modal } from '@/components/Modal';
import { Button, Chip } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';
import {
  isKubernetesClusterRecentlyActive,
  kubernetesUninstallCommand,
  kubernetesUpgradeCommand,
} from './model';

function ClusterIdentity({ cluster }: { cluster: KubernetesCluster }) {
  const { tr } = useI18n();
  const statusTone = cluster.status === 'online'
    ? 'success'
    : cluster.status === 'degraded'
      ? 'warning'
      : cluster.status === 'offline'
        ? 'default'
        : 'info';
  return (
    <>
      <span className="text-zinc-500">{tr('集群', 'Cluster')}</span>
      <Chip tone="accent">{cluster.name}</Chip>
      <Chip tone="accent">{cluster.mode || 'full-node'}</Chip>
      <Chip tone={statusTone}>{cluster.status || 'unknown'}</Chip>
    </>
  );
}

function useCopyCommand(command: string) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    setCopied(false);
  }, [command]);
  async function copy() {
    await navigator.clipboard?.writeText(command);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }
  return { copied, copy };
}

function CommandBlock({ label, command }: { label: string; command: string }) {
  const { tr } = useI18n();
  const { copied, copy } = useCopyCommand(command);
  return (
    <div>
      <div className="mb-1 flex items-center justify-between gap-2 text-zinc-500">
        <span>{label}</span>
        <Button onClick={() => void copy()}>
          {copied ? <Check size={12} /> : <Clipboard size={12} />}
          {copied ? tr('已复制', 'Copied') : tr('复制', 'Copy')}
        </Button>
      </div>
      <pre className="max-h-72 overflow-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 text-[11px] leading-5 text-zinc-300">
        {command}
      </pre>
    </div>
  );
}

export function UninstallCommandModal({
  cluster,
  onClose,
}: {
  cluster: KubernetesCluster | null;
  onClose(): void;
}) {
  const { tr } = useI18n();
  if (!cluster) return null;
  return (
    <Modal open onClose={onClose} title={tr('Helm 卸载命令', 'Helm uninstall command')} size="lg">
      <div className="space-y-3 text-xs">
        <ClusterIdentity cluster={cluster} />
        <div className="rounded-md border border-amber-500/20 bg-amber-500/10 px-2 py-1.5 text-amber-200">
          {tr(
            '先在目标 Kubernetes 集群执行卸载命令，确认资源清理后再删除 Ongrid 侧集群记录。',
            'Run the uninstall command in the target Kubernetes cluster before deleting the Ongrid cluster record.',
          )}
        </div>
        <CommandBlock label={tr('卸载命令', 'Uninstall command')} command={kubernetesUninstallCommand(cluster)} />
      </div>
    </Modal>
  );
}

export function UpgradeCommandModal({
  cluster,
  onClose,
}: {
  cluster: KubernetesCluster | null;
  onClose(): void;
}) {
  const { tr } = useI18n();
  if (!cluster) return null;
  return (
    <Modal open onClose={onClose} title={tr('Helm 升级命令', 'Helm upgrade command')} size="lg">
      <div className="space-y-3 text-xs">
        <ClusterIdentity cluster={cluster} />
        <div className="rounded-md border border-sky-500/20 bg-sky-500/10 px-2 py-1.5 text-sky-200">
          {tr(
            '在目标 Kubernetes 集群执行该命令，会复用现有 Helm values，并从 manager 内置镜像仓库拉取新版镜像后滚动升级 Controller 与 Node Edge。',
            'Run this in the target Kubernetes cluster. It reuses existing Helm values, pulls the new image from the manager-hosted registry, and rolls both Controller and Node Edge agents.',
          )}
        </div>
        <CommandBlock label={tr('升级命令', 'Upgrade command')} command={kubernetesUpgradeCommand(cluster)} />
      </div>
    </Modal>
  );
}

export function DeleteClusterModal({
  cluster,
  deleting,
  onClose,
  onDelete,
}: {
  cluster: KubernetesCluster | null;
  deleting: boolean;
  onClose(): void;
  onDelete(cluster: KubernetesCluster): void;
}) {
  const { tr } = useI18n();
  if (!cluster) return null;
  const active = isKubernetesClusterRecentlyActive(cluster);

  return (
    <Modal
      open
      onClose={onClose}
      title={tr(`删除 Kubernetes 集群 ${cluster.name}`, `Delete Kubernetes cluster ${cluster.name}`)}
      size="lg"
      footer={
        <>
          <Button onClick={onClose} disabled={deleting}>{tr('取消', 'Cancel')}</Button>
          <Button variant="danger" onClick={() => onDelete(cluster)} disabled={deleting}>
            <Trash2 size={12} />
            {deleting
              ? tr('删除中…', 'Deleting…')
              : active
                ? tr('确认已卸载，删除记录', 'Confirm uninstalled, delete record')
                : tr('确认删除', 'Delete')}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-xs">
        <div className="flex flex-wrap items-center gap-2">
          <ClusterIdentity cluster={cluster} />
          <Chip tone={active ? 'warning' : 'default'}>
            {active ? tr('仍在上报', 'active') : tr('离线或陈旧', 'offline/stale')}
          </Chip>
        </div>
        <div className={cn(
          'rounded-md border px-3 py-2 leading-5',
          active
            ? 'border-amber-500/20 bg-amber-500/10 text-amber-200'
            : 'border-zinc-800 bg-zinc-950/40 text-zinc-400',
        )}>
          {active
            ? tr(
                '该集群仍在线或最近有上报。建议先在目标 Kubernetes 集群执行卸载命令，否则集群内 controller / node edge 会继续运行并反复重试上报。',
                'This cluster is online or recently reported. Run the uninstall command in the target Kubernetes cluster first, otherwise the controller / node edge will keep running and retrying reports.',
              )
            : tr(
                '该集群当前离线或同步时间已陈旧，可以删除 Ongrid 侧记录；如果目标集群仍存在，建议先执行卸载命令清理组件。',
                'This cluster is offline or stale, so deleting the Ongrid record is allowed. If the target cluster still exists, run the uninstall command first to clean up components.',
              )}
        </div>
        <CommandBlock label={tr('卸载命令', 'Uninstall command')} command={kubernetesUninstallCommand(cluster)} />
        <div className="rounded-md border border-red-500/20 bg-red-500/10 px-3 py-2 leading-5 text-red-200">
          {tr(
            '删除记录会移除 Ongrid 侧集群、快照、接入 token 和拓扑镜像；它不会自动进入目标 Kubernetes 集群卸载 Helm release。',
            'Deleting the record removes the Ongrid cluster, snapshots, enrollment token, and topology mirror. It does not uninstall the Helm release from the target Kubernetes cluster.',
          )}
        </div>
      </div>
    </Modal>
  );
}
