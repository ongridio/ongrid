import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { Background, Controls, MarkerType, Position, ReactFlow, type Edge, type Node, type ReactFlowInstance, type NodeChange, type EdgeChange } from '@xyflow/react';
import dagre from '@dagrejs/dagre';
import { ArrowUpRight, Box, Database, X } from 'lucide-react';
import { Button, Card, EmptyState, Input } from '@/components/ui';
import { serviceParams, type ApmDependencies, type ApmList, type ServiceIdentity } from '@/api/apm';
import { useI18n } from '@/i18n/locale';
import '@xyflow/react/dist/style.css';
import './ServiceMap.css';

const identityKey = (id: ServiceIdentity) => JSON.stringify([id.environment, id.service_namespace, id.service_name]);
// Fixed decades keep the visual meaning stable across refreshes and scopes.
export const trafficLevel = (rps: number | null | undefined) => rps == null || !Number.isFinite(rps) || rps < 0 ? -1 : rps === 0 ? 0 : Math.min(4, Math.max(1, Math.floor(Math.log10(rps)) + 1));
const number = (n: number | null | undefined, suffix = '') => n == null ? '—' : `${n.toLocaleString(undefined, { maximumFractionDigits: 2 })}${suffix}`;

export function ServiceMap({ list, dependencies, params, loading }: { list?: ApmList; dependencies?: ApmDependencies; params: URLSearchParams; loading: boolean }) {
  const { tr } = useI18n();
  const flow = useRef<ReactFlowInstance | null>(null);
  const [selected, setSelected] = useState('');
  const [search, setSearch] = useState('');
  const onSelectionChanges = useCallback((changes: (NodeChange | EdgeChange)[]) => {
    setSelected(current => {
      const active = changes.find(change => change.type === 'select' && change.selected);
      if (active && active.type === 'select') return active.id;
      return changes.some(change => change.type === 'select' && !change.selected && change.id === current) ? '' : current;
    });
  }, []);
  const graph = useMemo(() => {
    const services = new Map((list?.items || []).map(s => [identityKey(s.identity), s]));
    const identities = new Map((list?.items || []).map(s => [identityKey(s.identity), s.identity]));
    const external = new Set<string>();
    const edges = (dependencies?.items || []).map(e => {
      const source = identityKey(e.client), target = identityKey(e.server);
      identities.set(source, e.client); identities.set(target, e.server);
      if (e.connection_type === 'database') external.add(target);
      if (e.connection_type === 'virtual_node') external.add(source);
      return { id: JSON.stringify([source, target, e.connection_type]), source, target, value: e };
    });
    const layout = new dagre.graphlib.Graph().setGraph({ rankdir: 'LR', ranksep: 100, nodesep: 40 }).setDefaultEdgeLabel(() => ({}));
    [...identities.keys()].sort().forEach(id => layout.setNode(id, { width: 220, height: 108 }));
    [...edges].sort((a,b) => a.id.localeCompare(b.id)).forEach(e => layout.setEdge(e.source, e.target));
    dagre.layout(layout);
    return { services, identities, external, edges, layout, topologyKey: JSON.stringify([[...identities.keys()].sort(), edges.map(e => e.id).sort()]) };
  }, [list, dependencies]);
  const selectedIdentity = graph.identities.get(selected);
  const selectedService = graph.services.get(selected);
  const selectedEdge = graph.edges.find(e => e.id === selected);
  const focus = new Set<string>();
  if (selectedIdentity) {
    focus.add(selected);
    graph.edges.filter(e => e.source === selected || e.target === selected).forEach(e => { focus.add(e.source); focus.add(e.target); });
  } else if (selectedEdge) { focus.add(selectedEdge.source); focus.add(selectedEdge.target); }
  const focusedIds = JSON.stringify([...focus].sort());
  useEffect(() => {
    if (focusedIds === '[]') return;
    // Fit after React Flow observes the narrower canvas beside the sidebar.
    let frame = requestAnimationFrame(() => { frame = requestAnimationFrame(() => { void flow.current?.fitView({ nodes: (JSON.parse(focusedIds) as string[]).map(id => ({ id })), maxZoom: 1, padding: 0.2 }); }); });
    return () => cancelAnimationFrame(frame);
  }, [focusedIds]);
  const matches = new Set([...graph.identities].filter(([, id]) => id.service_name.toLowerCase().includes(search.toLowerCase())).map(([id]) => id));
  const emphasized = (id: string) => (!focus.size || focus.has(id)) && (!search || matches.has(id));
  const nodes: Node[] = [...graph.identities].map(([id, identity]) => {
    const s = graph.services.get(id), sampled = s?.metric_source === 'tempo_spanmetrics';
    const external = graph.external.has(id) && !s;
    const level = trafficLevel(sampled ? null : s?.rps);
    const Icon = external ? Database : Box;
    return { id, selected: selected === id, position: { x: graph.layout.node(id).x - 110, y: graph.layout.node(id).y - 54 },
      sourcePosition: Position.Right, targetPosition: Position.Left,
      className: `service-map-node traffic-${level} ${external || sampled ? 'inferred' : ''}`,
      style: { width: 220, opacity: emphasized(id) ? 1 : 0.25, borderColor: selected === id ? 'rgb(var(--accent))' : undefined },
      ariaLabel: `${identity.service_name} · ${identity.environment} · ${identity.service_namespace}`,
      data: { label: <div className="text-left">
        <div className="flex items-center gap-2"><Icon size={14} className="shrink-0" /><strong className="truncate" title={identity.service_name}>{identity.service_name}</strong>{s?.error_rate != null && s.error_rate > 0 && <span className="ml-auto h-2 w-2 shrink-0 rounded-full bg-red-500" title={tr('存在失败请求', 'Failed requests observed')} />}</div>
        <div className="mt-1 truncate text-xs text-text-muted">{external ? tr('外部依赖 · 推断', 'External · inferred') : `${identity.environment || '∅'} / ${identity.service_namespace || '∅'}`}</div>
        <div className="mt-3 flex justify-between text-xs"><span className="font-mono font-semibold">{number(s?.rps)} {sampled ? tr('样本/s', 'samples/s') : 'req/s'}</span><span className="text-text-muted">{tr('错误', 'Errors')} {number(s?.error_rate, '%')}</span></div>
      </div> },
    };
  });
  const edges: Edge[] = graph.edges.map(e => {
    const active = e.id === selected || e.source === selected || e.target === selected;
    return { id: e.id, selected: selected === e.id, source: e.source, target: e.target,
      markerEnd: { type: MarkerType.ArrowClosed, color: active ? '#6366f1' : '#64748b' },
      label: active ? `${number(e.value.rps)} ${tr('观测调用/s', 'observed calls/s')}` : undefined,
      animated: active && (e.value.rps || 0) > 0,
      style: { stroke: active ? '#6366f1' : '#64748b', strokeWidth: 1 + Math.max(0, trafficLevel(e.value.rps)), opacity: focus.size && !active ? 0.15 : 0.7 },
      labelStyle: { fill: 'rgb(var(--text))', fontSize: 11 }, labelBgStyle: { fill: 'rgb(var(--card))' },
      ariaLabel: `${e.value.client.service_name} → ${e.value.server.service_name}`,
    };
  });
  return <Card className="service-map overflow-hidden !p-0">
    <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border p-4">
      <div><h2 className="text-sm font-semibold">{tr('服务地图', 'Service map')}</h2><p className="mt-1 text-xs text-text-muted">{graph.identities.size} {tr('个节点', 'nodes')} · {graph.edges.length} {tr('条调用关系', 'call relationships')}</p></div>
      <Input type="search" className="w-full sm:w-56" aria-label={tr('在地图中定位服务', 'Find a service in the map')} placeholder={tr('搜索服务，回车定位…', 'Find service, Enter to focus…')} value={search} onChange={e => { setSearch(e.target.value); setSelected(''); }} onKeyDown={e => { if (e.key === 'Enter' && search && matches.size) void flow.current?.fitView({ nodes: [...matches].map(id => ({ id })), maxZoom: 1, padding: 0.3 }); }} />
      <div className="flex flex-wrap items-center gap-2 text-xs text-text-muted" aria-label={tr('节点请求量图例', 'Node request rate legend')}><span>req/s</span>{['0', '<10', '10–99', '100–999', '≥1k'].map((label,i) => <span key={label} className="flex items-center gap-1"><i className={`traffic-swatch traffic-${i}`} />{label}</span>)}<span>· {tr('灰色：无全量指标', 'Gray: no full metrics')}</span></div>
    </div>
    {!list && !dependencies && !loading ? <EmptyState title={tr('服务地图数据不可用', 'Service map data unavailable')} /> : nodes.length === 0 ? <EmptyState title={loading ? tr('正在加载服务地图…', 'Loading service map…') : tr('当前范围暂无服务', 'No services in this scope')} /> : <div className={`service-map-body ${selectedIdentity || selectedEdge ? 'has-selection' : ''}`}>
      <div className="service-map-canvas"><ReactFlow onNodesChange={onSelectionChanges} onEdgesChange={onSelectionChanges} multiSelectionKeyCode={null} deleteKeyCode={null} ariaLabelConfig={{ 'controls.ariaLabel': tr('地图控制', 'Map controls'), 'controls.zoomIn.ariaLabel': tr('放大', 'Zoom in'), 'controls.zoomOut.ariaLabel': tr('缩小', 'Zoom out'), 'controls.fitView.ariaLabel': tr('适应画布', 'Fit view'), 'node.a11yDescription.default': tr('按回车查看服务，按 Escape 取消选择。', 'Press Enter to inspect the service, Escape to deselect.'), 'edge.a11yDescription.default': tr('按回车查看调用关系，按 Escape 取消选择。', 'Press Enter to inspect calls, Escape to deselect.') }} key={graph.topologyKey} onInit={instance => { flow.current = instance; }} nodes={nodes} edges={edges} fitView fitViewOptions={{ maxZoom: 1, padding: 0.2 }} minZoom={0.1} nodesDraggable={false} nodesConnectable={false} onNodeClick={(_,node) => setSelected(node.id)} onEdgeClick={(_,edge) => setSelected(edge.id)} onPaneClick={() => setSelected('')}><Background gap={24} size={1} /><Controls showInteractive={false} /></ReactFlow></div>
      {(selectedIdentity || selectedEdge) && <aside className="space-y-5 border-l border-border p-5" aria-label={tr('地图详情', 'Map details')}>
        <div className="flex items-start justify-between gap-2"><h3 className="break-all text-sm font-semibold">{selectedIdentity?.service_name || `${selectedEdge?.value.client.service_name} → ${selectedEdge?.value.server.service_name}`}</h3><Button variant="subtle" size="icon" aria-label={tr('关闭详情', 'Close details')} onClick={() => setSelected('')}><X size={14} /></Button></div>
        <p className="text-xs text-text-muted">{selectedIdentity ? `${selectedIdentity.environment || '∅'} / ${selectedIdentity.service_namespace || '∅'}` : tr('Trace 观测调用关系，可能受采样影响', 'Trace-observed calls; sampling may affect coverage')}</p>
        <dl className="divide-y divide-border text-sm">{[
          [selectedEdge ? tr('观测调用速率', 'Observed call rate') : selectedService?.metric_source === 'tempo_spanmetrics' ? tr('Trace 样本速率', 'Trace sample rate') : tr('请求速率', 'Request rate'), number((selectedEdge?.value || selectedService)?.rps, ' /s')],
          [tr('错误率', 'Error rate'), number((selectedEdge?.value || selectedService)?.error_rate, '%')],
          [selectedEdge ? tr('调用侧 P95', 'Caller P95') : 'P95', number((selectedEdge?.value || selectedService)?.p95_ms, ' ms')],
        ].map(([label,value]) => <div key={label} className="flex justify-between gap-3 py-3"><dt className="text-text-muted">{label}</dt><dd className="font-mono">{value}</dd></div>)}</dl>
        {selectedIdentity && (!graph.external.has(selected) || selectedService) && <Link className="flex items-center gap-1 text-sm text-indigo-500" to={`/apm/service?${serviceParams(params, selectedIdentity)}`}>{tr('查看服务', 'View service')}<ArrowUpRight size={14} /></Link>}
        {selectedIdentity && !selectedService && <p className="text-xs text-text-muted">{tr('仅观测到调用关系，当前未加载此节点的请求指标。', 'Only the call relationship is available; request metrics for this node are not loaded.')}</p>}
      </aside>}
    </div>}
    <div className="space-y-1 border-t border-border px-4 py-3 text-xs text-text-muted">
      <p>{tr('节点深浅表示服务请求量；线宽与箭头表示观测调用量及方向。点击节点或连线查看详情。', 'Node shading shows request rate; line width and arrows show observed call volume and direction. Select a node or edge for details.')}</p>
      <p>{tr('连线来自 Trace，采样或配对缺失会使关系不完整；没有连线不代表没有调用。', 'Edges come from traces; sampling or missing span pairs can leave gaps. No edge does not mean no calls.')}</p>
      {(!dependencies || !list) && !loading && <p role="status">{tr('部分数据未加载，请查看页面错误提示并刷新。', 'Some data is unavailable; check the page error and refresh.')}</p>}
      {(dependencies?.truncated || (list?.total || 0) > (list?.items.length || 0)) && <p role="status">{tr('当前展示最多 100 个服务的指标及 200 条高流量关系，请缩小环境或命名空间范围。', 'Showing metrics for up to 100 services and 200 busiest relationships; narrow the environment or namespace.')}</p>}
      {search && matches.size === 0 && <p role="status">{tr('没有匹配的服务', 'No matching service')}</p>}
    </div>
  </Card>;
}
