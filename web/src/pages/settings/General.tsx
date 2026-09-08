import { Switch } from '@/components/ui/Switch';
import { useCallback, useEffect, useState } from 'react';
import { Loader2, Network } from 'lucide-react';
import { listSettings, setSetting } from '@/api/settings';
import { useI18n } from '@/i18n/locale';

const CATEGORY = 'platform';
const KEY = 'network_discovery_enabled';

// Match the manager's fail-open default: only an explicit false disables
// admission. This keeps the displayed state correct for old or malformed rows.
function isDiscoveryEnabled(value?: string): boolean {
  return value?.trim().toLowerCase() !== 'false';
}

export default function SettingsGeneral() {
  const { tr } = useI18n();
  const [enabled, setEnabled] = useState(true);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const response = await listSettings(CATEGORY);
      const row = response.items.find((item) => item.key === KEY);
      // Missing row follows the Manager default: enabled for new installs.
      setEnabled(isDiscoveryEnabled(row?.value));
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const onToggle = useCallback(async (next: boolean) => {
    setSaving(true);
    setError(null);
    setEnabled(next);
    try {
      await setSetting(CATEGORY, KEY, next ? 'true' : 'false', false);
    } catch (err) {
      setEnabled(!next);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }, []);

  return (
    <div className="space-y-4">
      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-5">
        <div className="mb-3 flex items-center gap-2">
          <Network size={14} className="text-zinc-400" />
          <h2 className="text-sm font-medium text-zinc-100">{tr('网络发现', 'Network discovery')}</h2>
        </div>
        <p className="mb-4 text-xs leading-relaxed text-zinc-500">
          {tr(
            '控制平台是否接收 Edge 上报的默认网关、ARP 邻居和 LLDP 邻居。关闭后不会再创建或更新候选设备，已有候选和正式网络设备不会被删除。',
            'Controls whether the platform accepts gateway, ARP-neighbor, and LLDP-neighbor reports from Edges. When disabled, no candidates are created or updated; existing candidates and verified network devices remain.',
          )}
        </p>

        {loading ? (
          <div className="flex h-10 items-center text-xs text-zinc-500">
            <Loader2 size={13} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
          </div>
        ) : (
          <div className="flex items-center justify-between gap-4 rounded-lg border border-zinc-800 bg-zinc-950/40 px-4 py-3">
            <div>
              <div className="text-[13px] font-medium text-zinc-200">
                {tr('启用网络发现', 'Enable network discovery')}
              </div>
              <div className="mt-0.5 text-[11px] text-zinc-500">
                {enabled
                  ? tr('当前：接收所有已启用 Edge 的发现上报', 'Current: accept discovery reports from enabled Edges')
                  : tr('当前：忽略新的网络发现上报', 'Current: ignore new network discovery reports')}
              </div>
            </div>
            <Switch checked={enabled} disabled={saving} onCheckedChange={(next) => void onToggle(next)} aria-label={tr('启用网络发现', 'Enable network discovery')} />
          </div>
        )}

        {error && <div className="mt-3 text-xs text-red-400">{error}</div>}

        <p className="mt-4 text-[11px] leading-relaxed text-zinc-600">
          {tr(
            '新安装的 Edge 默认开启本地采集。若只想停用某一台主机，可将其 /etc/ongrid-edge/ongrid-edge.env 中的 ONGRID_NETWORK_DISCOVERY_ENABLED 设为 false 后重启服务。',
            'New Edges collect locally by default. To stop one host only, set ONGRID_NETWORK_DISCOVERY_ENABLED=false in /etc/ongrid-edge/ongrid-edge.env and restart its service.',
          )}
        </p>
      </section>
    </div>
  );
}
