import { Label, Input, Textarea } from '@/components/ui';
import { useEffect, useMemo, useState } from "react";
import { Check, HardDrive, Search } from "lucide-react";
import type { Device } from "@/api/devices";
import {
  createNode,
  createRelation,
  updateNode,
  type TopologyNode,
} from "@/api/topology";
import { Modal } from "@/components/Modal";
import { Button, Chip } from "@/components/ui";
import { useI18n } from "@/i18n/locale";
import { cn } from "@/lib/cn";

type CreateProps = {
  open: boolean;
  onClose(): void;
  onCreated(cluster: TopologyNode): void;
};

export function CreateDeviceClusterModal({
  open,
  onClose,
  onCreated,
}: CreateProps) {
  const { tr } = useI18n();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setName("");
    setDescription("");
    setPending(false);
    setError(null);
  }, [open]);

  async function submit() {
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError(tr("请输入集群名称", "Enter a cluster name"));
      return;
    }
    setPending(true);
    setError(null);
    try {
      const cluster = await createNode({
        type: "cluster",
        name: trimmedName,
        props: {
          source: "manual",
          ...(description.trim() ? { description: description.trim() } : {}),
        },
      });
      onCreated(cluster);
    } catch (err) {
      setError((err as Error).message || tr("创建失败", "Failed to create"));
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={tr("新建设备集群", "Create device cluster")}
      footer={
        <>
          <Button onClick={onClose}>{tr("取消", "Cancel")}</Button>
          <Button
            variant="primary"
            disabled={pending}
            onClick={() => void submit()}
          >
            {pending
              ? tr("创建中…", "Creating…")
              : tr("创建集群", "Create cluster")}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Label className="block text-[11px] text-zinc-500">
          {tr("集群名称", "Cluster name")}
          <Input
            autoFocus
            maxLength={128}
            value={name}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") void submit();
            }}
            className="mt-1 w-full"
          />
        </Label>
        <Label className="block text-[11px] text-zinc-500">
          {tr("说明（可选）", "Description (optional)")}
          <Textarea
            rows={3}
            maxLength={500}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            className="mt-1 w-full resize-none"
          />
        </Label>
        {error && <InlineError message={error} />}
      </div>
    </Modal>
  );
}

type RenameProps = {
  cluster: TopologyNode | null;
  onClose(): void;
  onRenamed(name: string): void;
};

export function RenameDeviceClusterModal({
  cluster,
  onClose,
  onRenamed,
}: RenameProps) {
  const { tr } = useI18n();
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!cluster) return;
    setName(cluster.name);
    setPending(false);
    setError(null);
  }, [cluster]);

  async function submit() {
    if (!cluster) return;
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError(tr("请输入集群名称", "Enter a cluster name"));
      return;
    }
    setPending(true);
    setError(null);
    try {
      await updateNode(cluster.id, { name: trimmedName });
      onRenamed(trimmedName);
    } catch (err) {
      setError((err as Error).message || tr("保存失败", "Failed to save"));
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      open={Boolean(cluster)}
      onClose={onClose}
      title={tr("重命名集群", "Rename cluster")}
      footer={
        <>
          <Button onClick={onClose}>{tr("取消", "Cancel")}</Button>
          <Button
            variant="primary"
            disabled={pending}
            onClick={() => void submit()}
          >
            {pending ? tr("保存中…", "Saving…") : tr("保存", "Save")}
          </Button>
        </>
      }
    >
      <Label className="block text-[11px] text-zinc-500">
        {tr("集群名称", "Cluster name")}
        <Input
          autoFocus
          maxLength={128}
          value={name}
          onChange={(event) => setName(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") void submit();
          }}
          className="mt-1 w-full"
        />
      </Label>
      {error && <InlineError message={error} />}
    </Modal>
  );
}

type AddMembersProps = {
  open: boolean;
  cluster: TopologyNode;
  devices: Device[];
  onClose(): void;
  onAdded(): void;
};

export function AddClusterMembersModal({
  open,
  cluster,
  devices,
  onClose,
  onAdded,
}: AddMembersProps) {
  const { tr } = useI18n();
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    setSelected(new Set());
    setPending(false);
    setError(null);
  }, [open]);

  const visible = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    if (!normalized) return devices;
    return devices.filter((device) =>
      [device.name, device.hostname, device.ip_address]
        .filter(Boolean)
        .some((value) => value!.toLocaleLowerCase().includes(normalized)),
    );
  }, [devices, query]);

  function toggle(deviceID: number) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(deviceID)) next.delete(deviceID);
      else next.add(deviceID);
      return next;
    });
  }

  async function submit() {
    const selectedDevices = devices.filter((device) => selected.has(device.id));
    if (selectedDevices.length === 0) return;
    setPending(true);
    setError(null);
    try {
      await Promise.all(
        selectedDevices.map((device) =>
          createRelation({
            src_id: device.node_id!,
            dst_id: cluster.id,
            type: "member_of",
            props: { source: "manual", device_id: device.id },
          }),
        ),
      );
      onAdded();
    } catch (err) {
      setError(
        (err as Error).message || tr("添加失败", "Failed to add members"),
      );
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={tr("添加集群成员", "Add cluster members")}
      size="lg"
      footer={
        <>
          <Button onClick={onClose}>{tr("取消", "Cancel")}</Button>
          <Button
            variant="primary"
            disabled={pending || selected.size === 0}
            onClick={() => void submit()}
          >
            {pending
              ? tr("添加中…", "Adding…")
              : tr(
                  `添加 ${selected.size} 台设备`,
                  `Add ${selected.size} device(s)`,
                )}
          </Button>
        </>
      }
    >
      <div className="relative mb-3">
        <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
        <Input
          autoFocus
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder={tr(
            "搜索名称、主机名或 IP",
            "Search name, hostname, or IP",
          )}
          className="w-full pl-8 pr-3"
        />
      </div>

      {devices.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-800 px-4 py-10 text-center">
          <HardDrive size={22} className="mx-auto mb-2 text-zinc-600" />
          <p className="text-sm text-zinc-500">
            {tr("没有可添加的普通设备", "No eligible devices")}
          </p>
          <p className="mt-1 text-xs text-zinc-600">
            {tr(
              "设备必须已有拓扑节点，且未归属其他集群或 Kubernetes。",
              "Devices need a topology node and must not belong to another cluster or Kubernetes.",
            )}
          </p>
        </div>
      ) : visible.length === 0 ? (
        <div className="py-10 text-center text-xs text-zinc-500">
          {tr("没有匹配的设备", "No matching devices")}
        </div>
      ) : (
        <div className="max-h-80 space-y-1 overflow-y-auto pr-1">
          {visible.map((device) => {
            const checked = selected.has(device.id);
            return (
              <button
                key={device.id}
                type="button"
                aria-pressed={checked}
                onClick={() => toggle(device.id)}
                className={cn(
                  "flex w-full items-center gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors",
                  checked
                    ? "border-indigo-500/40 bg-indigo-500/10"
                    : "border-transparent hover:bg-zinc-800/60",
                )}
              >
                <span
                  className={cn(
                    "flex h-4 w-4 shrink-0 items-center justify-center rounded border",
                    checked
                      ? "border-indigo-400 bg-indigo-500 text-white"
                      : "border-zinc-700 bg-zinc-950",
                  )}
                >
                  {checked && <Check size={11} />}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs font-medium text-zinc-200">
                    {device.name || device.hostname || `#${device.id}`}
                  </span>
                  <span className="mt-0.5 block truncate text-[11px] text-zinc-500">
                    {[device.hostname, device.ip_address]
                      .filter(Boolean)
                      .join(" · ") || "—"}
                  </span>
                </span>
                <Chip tone={device.online ? "success" : "default"}>
                  {device.online ? tr("在线", "Online") : tr("离线", "Offline")}
                </Chip>
              </button>
            );
          })}
        </div>
      )}
      {error && <InlineError message={error} />}
    </Modal>
  );
}

type DeleteProps = {
  cluster: TopologyNode | null;
  blockedReason?: string;
  deleting: boolean;
  onClose(): void;
  onDelete(): void;
};

export function DeleteDeviceClusterModal({
  cluster,
  blockedReason,
  deleting,
  onClose,
  onDelete,
}: DeleteProps) {
  const { tr } = useI18n();
  return (
    <Modal
      open={Boolean(cluster)}
      onClose={onClose}
      title={tr("删除设备集群", "Delete device cluster")}
      footer={
        <>
          <Button onClick={onClose}>{tr("取消", "Cancel")}</Button>
          <Button
            variant="danger"
            disabled={deleting || Boolean(blockedReason)}
            onClick={onDelete}
          >
            {deleting
              ? tr("删除中…", "Deleting…")
              : tr("删除集群", "Delete cluster")}
          </Button>
        </>
      }
    >
      {blockedReason ? (
        <div className="rounded-lg border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
          {blockedReason}
        </div>
      ) : (
        <p className="text-sm leading-6 text-zinc-300">
          {tr(
            `确认删除空集群“${cluster?.name ?? ""}”？此操作不会删除任何设备。`,
            `Delete the empty cluster “${cluster?.name ?? ""}”? No devices will be deleted.`,
          )}
        </p>
      )}
    </Modal>
  );
}

function InlineError({ message }: { message: string }) {
  return (
    <div
      role="alert"
      className="mt-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
    >
      {message}
    </div>
  );
}
