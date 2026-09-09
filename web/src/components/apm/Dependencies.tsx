import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { Background, Controls, ReactFlow, type Node, type Edge } from '@xyflow/react';
import dagre from '@dagrejs/dagre';
import '@xyflow/react/dist/style.css';
import { type ApmDependencies, serviceParams, type ServiceIdentity } from '@/api/apm';
import { useI18n } from '@/i18n/locale';

export function Dependencies({ data, params }: { data: ApmDependencies; params: URLSearchParams }) {
  const { tr } = useI18n();
  const { nodes, edges } = useMemo(() => {
    const identities = new Map<string, ServiceIdentity>();
    const external = new Set<string>();
    const key = (id: ServiceIdentity) => JSON.stringify([id.service_name, id.service_namespace, id.environment]);
    const graph = new dagre.graphlib.Graph()
      .setGraph({ rankdir: 'LR', ranksep: 90 })
      .setDefaultEdgeLabel(() => ({}));
    const current: ServiceIdentity = {
      service_name: params.get('service_name') || '',
      service_namespace: params.get('service_namespace') || '',
      environment: params.get('environment') || '',
    };
    if (current.service_name) {
      identities.set(key(current), current);
      graph.setNode(key(current), { width: 220, height: 90 });
    }
    const edges: Edge[] = data.items.map((edge, index) => {
      for (const id of [edge.client, edge.server]) {
        identities.set(key(id), id);
        graph.setNode(key(id), { width: 220, height: 90 });
      }
      graph.setEdge(key(edge.client), key(edge.server));
      if (edge.connection_type === 'virtual_node') external.add(key(edge.client));
      if (edge.connection_type === 'database') external.add(key(edge.server));
      return {
        id: String(index),
        source: key(edge.client),
        target: key(edge.server),
        label: `${edge.rps?.toFixed(2) ?? '—'} /s`,
        style: { stroke: '#71717a' },
      };
    });
    dagre.layout(graph);
    const nodes: Node[] = [...identities].map(([id, identity]) => ({
      id,
      position: { x: graph.node(id).x - 110, y: graph.node(id).y - 45 },
      data: {
        label: external.has(id) ? (
          <span>
            {identity.service_name}
            <br />
            {tr('推断的外部依赖', 'Inferred external dependency')}
          </span>
        ) : (
          <Link to={`/apm/service?${serviceParams(params, identity)}`}>
            <strong>{identity.service_name}</strong>
            <br />
            {identity.environment || tr('未设置', 'Unset')} ·{' '}
            {identity.service_namespace || tr('未设置', 'Unset')}
          </Link>
        ),
      },
      style: {
        width: 220,
        minHeight: 90,
        background: 'rgb(var(--card))',
        color: 'rgb(var(--text))',
        border: '1px solid rgb(var(--border))',
      },
    }));
    return { nodes, edges };
  }, [data, params, tr]);
  return (
    <>
      <p className="text-xs text-zinc-500">
        {tr(
          '所有入口类型的观测调用关系；采样或 Span 配对缺失会使关系不完整。',
          'Observed calls across entry types; sampling or missing span pairs can leave gaps.',
        )}
      </p>
      {nodes.length > 0 && (
        <div className="my-4 h-80 rounded-lg border border-zinc-800">
          <ReactFlow nodes={nodes} edges={edges} fitView fitViewOptions={{ maxZoom: 1 }} nodesDraggable={false} nodesConnectable={false}>
            <Background />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>
      )}
      {data.items.length > 0 && <div className="overflow-auto">
        <table className="w-full text-left text-xs">
          <thead className="text-zinc-500">
            <tr>
              {[
                tr('调用方 → 被调用方', 'Caller → callee'),
                tr('类型', 'Type'),
                'RPS',
                tr('错误率', 'Error rate'),
                'P95 (ms)',
              ].map((label) => (
                <th className="p-3" key={label}>
                  {label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-800">
            {data.items.map((edge, i) => (
              <tr key={i}>
                <td className="p-3">
                  {[edge.client, edge.server].map((id, j) => (
                    <span key={j}>
                      {j > 0 && ' → '}
                      {(j === 0 && edge.connection_type === 'virtual_node') ||
                      (j === 1 && edge.connection_type === 'database') ? (
                        <span>{id.service_name}</span>
                      ) : (
                        <Link className="underline" to={`/apm/service?${serviceParams(params, id)}`}>
                          {id.service_name} ({id.environment || '∅'}/{id.service_namespace || '∅'})
                        </Link>
                      )}
                    </span>
                  ))}
                </td>
                <td>
                  {edge.connection_type === 'virtual_node'
                    ? tr('推断的外部调用', 'Inferred external call')
                    : edge.connection_type === 'database'
                      ? tr('数据库', 'Database')
                      : edge.connection_type || tr('服务', 'Service')}
                </td>
                <td>{edge.rps?.toFixed(2) ?? '—'}</td>
                <td>{edge.error_rate == null ? '—' : `${edge.error_rate.toFixed(2)}%`}</td>
                <td>{edge.p95_ms?.toFixed(1) ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>}
      {data.items.length === 0 && (
        <p className="py-8 text-center text-zinc-500">
          {tr('暂未观测到可识别的服务依赖', 'No identifiable service dependencies observed')}
        </p>
      )}
      {data.truncated && (
        <p role="status">
          {tr('仅显示流量最高的 200 条关系，请缩小范围。', 'Showing the top 200 edges; narrow the scope.')}
        </p>
      )}
    </>
  );
}
