// NodeNeighbors — embeddable card listing every relation involving
// the given topology node. Used by the EdgeDetail "Topology" tab and
// the Incident "影响面" panel; cheap GET (relations + parallel
// hydrate of the other-side names) keeps it dependency-free.
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Loader2, Share2 } from 'lucide-react';
import { ApiError } from '@/api/client';
import { useI18n } from '@/i18n/locale';
import {
  getNode,
  listRelations,
  listRelationTypes,
  type RelationType,
  type TopologyNode,
  type TopologyRelation,
} from '@/api/topology';

type Props = {
  /** Topology node id (NOT the device id). Pass null to render the empty hint. */
  nodeID: number | null;
  /** Max relations to fetch + render. Default 200 covers virtually every device. */
  limit?: number;
};

export function NodeNeighbors({ nodeID, limit = 200 }: Props) {
  const { tr } = useI18n();
  const [center, setCenter] = useState<TopologyNode | null>(null);
  const [neighbors, setNeighbors] = useState<TopologyRelation[]>([]);
  const [nodeMap, setNodeMap] = useState<Map<number, TopologyNode>>(new Map());
  const [relTypes, setRelTypes] = useState<Map<string, RelationType>>(new Map());
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  const fetchAll = useCallback(async () => {
    if (!nodeID) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const [c, rel, rt] = await Promise.all([
        getNode(nodeID),
        listRelations({ src_or_dst_id: nodeID, limit }),
        listRelationTypes(),
      ]);
      setCenter(c);
      setNeighbors(rel.items ?? []);
      const tMap = new Map<string, RelationType>();
      for (const t of rt.items ?? []) tMap.set(t.name, t);
      setRelTypes(tMap);
      const ids = new Set<number>();
      for (const r of rel.items ?? []) {
        ids.add(r.src_id === nodeID ? r.dst_id : r.src_id);
      }
      const got = await Promise.all([...ids].map((id) => getNode(id).catch(() => null)));
      const map = new Map<number, TopologyNode>();
      for (const n of got) {
        if (n) map.set(n.id, n);
      }
      setNodeMap(map);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [nodeID, limit]);

  useEffect(() => {
    fetchAll();
  }, [fetchAll]);

  if (!nodeID) {
    return (
      <div className="rounded-lg border border-dashed border-zinc-800 bg-zinc-900/30 px-4 py-6 text-center text-xs text-zinc-500">
        {tr(
          '设备拓扑信息尚未就绪，请稍后刷新。',
          'Device topology is not ready yet. Refresh shortly.',
        )}
      </div>
    );
  }

  if (loading) {
    return (
      <div className="flex items-center gap-2 text-xs text-zinc-500">
        <Loader2 size={12} className="animate-spin" />
        {tr('加载邻居关系…', 'Loading neighbours…')}
      </div>
    );
  }

  if (err) {
    return (
      <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
        {err}
      </div>
    );
  }

  if (!center) return null;

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2 text-xs">
        <div className="text-zinc-400">
          {tr('中心节点：', 'Center: ')}
          <span className="ml-1 font-medium text-zinc-100">{center.name}</span>
          <span className="ml-1.5 rounded bg-zinc-800 px-1 py-0.5 font-mono text-[10px] text-zinc-300">
            {center.type}
          </span>
          <span className="ml-1 text-zinc-600">#{center.id}</span>
        </div>
        <Link
          to="/topology"
          className="inline-flex items-center gap-1 rounded-md border border-zinc-800 px-2 py-0.5 text-[11px] text-zinc-400 hover:bg-zinc-800"
        >
          <Share2 size={11} /> {tr('在图谱中查看', 'Open in graph')}
        </Link>
      </div>
      {neighbors.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-800 px-3 py-4 text-center text-xs text-zinc-500">
          {tr(
            '暂无关联关系。',
            'No relationships yet.',
          )}
        </div>
      ) : (
        <ul className="space-y-1.5">
          {neighbors.map((rel) => (
            <NeighborRow
              key={rel.id}
              rel={rel}
              centerID={nodeID}
              other={nodeMap.get(rel.src_id === nodeID ? rel.dst_id : rel.src_id) ?? null}
              tag={relTypes.get(rel.type)?.semantics_tag ?? 'annotation'}
              propagates={relTypes.get(rel.type)?.propagates_failure ?? false}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

const TAG_COLOR: Record<string, string> = {
  hard_dep: 'text-red-300',
  runtime_dep: 'text-orange-300',
  traffic: 'text-amber-300',
  redundancy: 'text-emerald-300',
  observation: 'text-sky-300',
  aggregation: 'text-violet-300',
  annotation: 'text-zinc-400',
};

function NeighborRow({
  rel,
  centerID,
  other,
  tag,
  propagates,
}: {
  rel: TopologyRelation;
  centerID: number;
  other: TopologyNode | null;
  tag: string;
  propagates: boolean;
}) {
  const { tr } = useI18n();
  const outgoing = rel.src_id === centerID;
  const arrow = outgoing ? '→' : '←';
  const otherID = outgoing ? rel.dst_id : rel.src_id;
  return (
    <li className="flex items-center gap-2 rounded-md border border-zinc-800/60 bg-zinc-900/30 px-2.5 py-1.5 text-[11px]">
      <span className={`font-mono ${TAG_COLOR[tag] ?? 'text-zinc-500'}`}>{arrow}</span>
      <span className={`rounded bg-zinc-800 px-1 py-0.5 font-mono ${TAG_COLOR[tag] ?? 'text-zinc-300'}`}>
        {rel.type === 'collects_from' ? tr('采集关系', 'Collection') : rel.type}
      </span>
      {rel.type === 'collects_from' && <span className="text-zinc-500">{rel.props?.enabled === false ? tr('轮询已停用', 'Polling disabled') : tr('SNMP 采集', 'SNMP collection')}</span>}
      {propagates && (
        <span className="rounded bg-amber-500/15 px-1 py-0.5 text-[10px] text-amber-300">
          {tr('传故障', 'propagates')}
        </span>
      )}
      <span className="min-w-0 flex-1 truncate text-zinc-200">
        {other?.name ?? `node #${otherID}`}
        {other && <span className="ml-1 text-zinc-500">[{other.type}]</span>}
      </span>
    </li>
  );
}
