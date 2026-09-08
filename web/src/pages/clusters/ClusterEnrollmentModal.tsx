import { Label, Input } from '@/components/ui';
import { useEffect, useState } from "react";
import { Check, Copy, Network } from "lucide-react";
import { Modal } from "@/components/Modal";
import { Button, Chip } from "@/components/ui";
import {
  createEdgeEnrollmentProfile,
  type CreateEdgeEnrollmentProfileResponse,
} from "@/api/edges";
import type { TopologyNode } from "@/api/topology";
import { useI18n } from "@/i18n/locale";
import { cn } from "@/lib/cn";

type Props = {
  open: boolean;
  cluster: TopologyNode;
  onClose(): void;
  onCreated(profile: CreateEdgeEnrollmentProfileResponse["profile"]): void;
};

export function ClusterEnrollmentModal({
  open,
  cluster,
  onClose,
  onCreated,
}: Props) {
  const { tr } = useI18n();
  const [name, setName] = useState("");
  const [expiresInHours, setExpiresInHours] = useState(24);
  const [maxUses, setMaxUses] = useState(100);
  const [created, setCreated] =
    useState<CreateEdgeEnrollmentProfileResponse | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setName(cluster.name);
    setExpiresInHours(24);
    setMaxUses(100);
    setCreated(null);
    setPending(false);
    setError(null);
  }, [cluster.id, cluster.name, open]);

  async function createProfile() {
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError(tr("请输入安装批次名称", "Enter an installation batch name"));
      return;
    }
    if (expiresInHours < 1 || expiresInHours > 168) {
      setError(
        tr(
          "有效期需在 1 到 168 小时之间",
          "Validity must be between 1 and 168 hours",
        ),
      );
      return;
    }
    if (maxUses < 1 || maxUses > 10000) {
      setError(
        tr(
          "设备数需在 1 到 10000 之间",
          "Device count must be between 1 and 10000",
        ),
      );
      return;
    }

    setPending(true);
    setError(null);
    try {
      const result = await createEdgeEnrollmentProfile({
        name: trimmedName,
        assignment_mode: "cluster",
        cluster_node_id: cluster.id,
        expires_in_hours: expiresInHours,
        max_uses: maxUses,
      });
      setCreated(result);
      onCreated(result.profile);
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
      title={tr("安装设备到集群", "Install devices into cluster")}
      size="lg"
      footer={
        created ? (
          <Button variant="primary" onClick={onClose}>
            {tr("我已保存命令", "I've saved the command")}
          </Button>
        ) : (
          <>
            <Button onClick={onClose}>{tr("取消", "Cancel")}</Button>
            <Button
              variant="primary"
              disabled={pending}
              onClick={() => void createProfile()}
            >
              {pending
                ? tr("生成中…", "Generating…")
                : tr("生成安装命令", "Generate command")}
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="space-y-4">
          <div className="rounded-lg border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
            {tr(
              "令牌只显示一次。每台主机执行后会领取独立凭证，并自动归属当前集群。",
              "The token is shown once. Every host receives independent credentials and joins this cluster automatically.",
            )}
          </div>
          <EnrollmentCommand token={created.enrollment_token} />
        </div>
      ) : (
        <div className="space-y-5">
          <div className="flex items-center justify-between rounded-lg border border-zinc-800 bg-zinc-900 px-3 py-2.5">
            <div className="flex min-w-0 items-center gap-2.5">
              <Network size={16} className="shrink-0 text-indigo-400" />
              <div className="min-w-0">
                <div className="text-[11px] text-zinc-500">
                  {tr("目标集群", "Target cluster")}
                </div>
                <div className="truncate text-sm text-zinc-100">
                  {cluster.name}
                </div>
              </div>
            </div>
            <Chip tone="accent">#{cluster.id}</Chip>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <Label className="block text-[11px] text-zinc-500 sm:col-span-2">
              {tr("安装批次名称", "Installation batch name")}
              <Input
                autoFocus
                maxLength={128}
                value={name}
                onChange={(event) => setName(event.target.value)}
                className="mt-1 w-full"
              />
            </Label>
            <Label className="block text-[11px] text-zinc-500">
              {tr("有效期（小时）", "Validity (hours)")}
              <Input
                type="number"
                min={1}
                max={168}
                value={expiresInHours}
                onChange={(event) =>
                  setExpiresInHours(Number(event.target.value))
                }
                className="mt-1 w-full"
              />
            </Label>
            <Label className="block text-[11px] text-zinc-500">
              {tr("最多安装设备数", "Maximum devices")}
              <Input
                type="number"
                min={1}
                max={10000}
                value={maxUses}
                onChange={(event) => setMaxUses(Number(event.target.value))}
                className="mt-1 w-full"
              />
            </Label>
          </div>
        </div>
      )}

      {error && (
        <div
          role="alert"
          className="mt-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
        >
          {error}
        </div>
      )}
    </Modal>
  );
}

function EnrollmentCommand({ token }: { token: string }) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  const host =
    typeof window === "undefined" ? "ongrid.example.com" : window.location.host;
  const hostname =
    typeof window === "undefined"
      ? "ongrid.example.com"
      : window.location.hostname;
  const tunnelAddr = `${hostname.includes(":") ? `[${hostname}]` : hostname}:40012`;
  const command =
    `curl -k -sSL https://${host}/install.sh | bash -s -- ` +
    `--enrollment-token=${token} ` +
    `--server-edge-addr=${tunnelAddr} ` +
    `--server-http-addr=${host} --tls-insecure`;
  const display =
    `curl -k -sSL https://${host}/install.sh | bash -s -- \\\n` +
    `  --enrollment-token=${token} \\\n` +
    `  --server-edge-addr=${tunnelAddr} \\\n` +
    `  --server-http-addr=${host} \\\n` +
    "  --tls-insecure";

  return (
    <div>
      <div className="mb-1 flex items-center justify-between">
        <span className="text-[11px] uppercase tracking-wider text-zinc-500">
          {tr("在每台目标主机执行", "Run on every target host")}
        </span>
        <Button variant="subtle" size="sm"
          type="button"
          aria-label={tr("复制安装命令", "Copy installation command")}
          onClick={() => {
            navigator.clipboard
              .writeText(command)
              .then(() => {
                setCopied(true);
                window.setTimeout(() => setCopied(false), 2000);
              })
              .catch(() => undefined);
          }}
          className={cn(
            "inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs",
            copied
              ? "bg-emerald-500/15 text-emerald-300"
              : "bg-zinc-800 text-zinc-300 hover:bg-zinc-700",
          )}
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? tr("已复制", "Copied") : tr("复制单行", "Copy one-liner")}
        </Button>
      </div>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-2 font-mono text-[11px] leading-relaxed text-zinc-200">
        {display}
      </pre>
      <p className="mt-2 text-[11px] text-zinc-500">
        {tr(
          "默认命令兼容自签名证书；正式环境配置可信证书后应移除 -k 和 --tls-insecure。",
          "The default command supports self-signed certificates. Remove -k and --tls-insecure after configuring a trusted production certificate.",
        )}
      </p>
    </div>
  );
}
