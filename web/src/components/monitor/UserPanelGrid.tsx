import { Button } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
// UserPanelGrid — renders user-authored monitor panels (CRUD via the
// /v1/monitor/panels endpoints). Same chrome / chart / threshold logic
// as the four built-in panels, plus edit / delete overlays on hover and
// a role-filter status badge so the user can see which panels are
// honoring the current role filter.
import { useEffect, useState } from 'react';
import { Pencil, Trash2 } from 'lucide-react';
import { type MonitorPanel } from '@/api/monitorPanels';
import { type GrafanaPanel } from '@/api/grafana';
import { PromQLPanel } from '@/components/PromQLPanel';
import { injectDeviceIDFilter, referencesDeviceID } from '@/lib/promql';
import { useI18n } from '@/i18n/locale';

// userPanelToGrafanaPanel reshapes a wire row into the GrafanaPanel
// shape PromQLPanel consumes. If a role filter is active and the user's
// PromQL references device_id, splice in `device_id=~"a|b"`. If the
// query doesn't mention device_id at all, leave it alone — those
// queries are likely service- or fleet-aggregate views, and silently
// injecting would change their meaning. The grid surfaces a "全集群"
// chip so the user knows the filter doesn't apply.
function userPanelToGrafanaPanel(p: MonitorPanel, filteredIDs: number[] | null): GrafanaPanel {
  const expr =
    filteredIDs !== null && referencesDeviceID(p.promql)
      ? injectDeviceIDFilter(p.promql, filteredIDs)
      : p.promql;
  return {
    id: 100000 + p.id, // offset so user ids don't collide with hardcoded 1..4
    type: p.type,
    title: p.title,
    gridPos: { x: 0, y: 0, w: 12, h: 8 },
    targets: [
      {
        expr,
        legendFormat: p.legend || undefined,
        refId: 'A',
      },
    ],
    fieldConfig: { defaults: { unit: p.unit || undefined } },
  };
}

export function UserPanelGrid({
  panels,
  range,
  fromMs,
  toMs,
  tick,
  grafanaBase,
  dashboardUid,
  orgId,
  onOpenPanel,
  onEdit,
  onDelete,
  filteredDeviceIDs,
}: {
  panels: MonitorPanel[];
  range: string;
  fromMs: number;
  toMs: number;
  tick: number;
  grafanaBase: string;
  dashboardUid: string;
  orgId: string;
  onOpenPanel: (panelId: number) => void;
  onEdit(panel: MonitorPanel): void;
  onDelete(panel: MonitorPanel): void;
  filteredDeviceIDs: number[] | null;
}) {
  const { tr } = useI18n();
  const [narrow, setNarrow] = useState<boolean>(
    typeof window !== 'undefined' ? window.innerWidth < 1200 : false,
  );
  useEffect(() => {
    const onResize = () => setNarrow(window.innerWidth < 1200);
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  return (
    <div
      className="grid gap-3"
      style={{
        gridTemplateColumns: narrow ? '1fr' : 'repeat(24, minmax(0, 1fr))',
        gridAutoRows: '36px',
      }}
    >
      {panels.map((p) => {
        const w = 12;
        const h = 8;
        const style = narrow
          ? { gridColumn: '1 / -1', gridRow: `span ${h}`, minHeight: 220 }
          : { gridColumn: `span ${w}`, gridRow: `span ${h}`, minHeight: 180 };
        const isFiltered = filteredDeviceIDs !== null && referencesDeviceID(p.promql);
        const isUnfilteredButRoleSet = filteredDeviceIDs !== null && !referencesDeviceID(p.promql);
        return (
          <div key={p.id} style={style} className="relative min-w-0 group">
            <PromQLPanel
              panel={userPanelToGrafanaPanel(p, filteredDeviceIDs)}
              range={range}
              tick={tick}
              grafanaBase={grafanaBase}
              dashboardUid={dashboardUid}
              orgId={orgId}
              fromMs={fromMs}
              toMs={toMs}
              onOpenInGrafana={onOpenPanel}
            />
            {isFiltered && (
              <Hint content={tr('本面板按角色筛选过设备', 'This panel is filtered by role')}><div
                className="absolute left-2 top-2 rounded-md border border-emerald-700/40 bg-emerald-900/30 px-1.5 py-0.5 text-[10px] text-emerald-200"

              >
                {tr('已筛选', 'Filtered')}
              </div></Hint>
            )}
            {isUnfilteredButRoleSet && (
              <Hint content={tr("该面板的 PromQL 未引用 device_id，角色筛选不影响它", "This panel's PromQL does not reference device_id, so the role filter has no effect")}><div
                className="absolute left-2 top-2 rounded-md border border-zinc-700/60 bg-zinc-900/60 px-1.5 py-0.5 text-[10px] text-zinc-400"

              >
                {tr('全集群', 'Cluster-wide')}
              </div></Hint>
            )}
            <div className="absolute right-2 top-2 flex items-center gap-1 opacity-0 transition-opacity group-hover:opacity-100">
              <Hint content={tr('编辑面板', 'Edit panel')}><Button variant="outline" size="sm"
                type="button"
                onClick={() => onEdit(p)}

                className="p-1"
              >
                <Pencil size={12} />
              </Button></Hint>
              <Hint content={tr('删除面板', 'Delete panel')}><Button variant="dangerGhost" size="sm"
                type="button"
                onClick={() => onDelete(p)}

                className="p-1"
              >
                <Trash2 size={12} />
              </Button></Hint>
            </div>
            {p.last_sync_error && (
              <Hint content={p.last_sync_error}><div
                className="absolute bottom-2 left-2 right-2 rounded-md border border-amber-700/40 bg-amber-900/30 px-2 py-1 text-[10px] text-amber-200"

              >
                {tr('Grafana 同步失败 — 本地仍可用', 'Grafana sync failed — local copy still works')}
              </div></Hint>
            )}
          </div>
        );
      })}
    </div>
  );
}
