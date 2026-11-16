import { TopologyGraph } from '@/components/topology/Graph';
import type { TopologyNode, TopologyRelation } from '@/api/topology';

import type { TopologyGraph as CMDBGraph } from '../../api/topology';

// The CMDB graph, drawn by the console's existing topology surface.
//
// THE TWO SHAPES ARE DIFFERENT ON PURPOSE. The CMDB service knows instance ids,
// models and edges; the shared graph component knows a display node and a typed
// relation. Mapping at this boundary keeps the CMDB module from owning a second
// React Flow implementation, and keeps the shared component from learning about
// CI models.
export function TopoGraph({ graph }: { graph: CMDBGraph }) {
  const nodes: TopologyNode[] = graph.nodes.map((node) => ({
    id: node.id,
    type: String(node.type_id),
    name: node.name,
    props: { type_id: node.type_id, icon: graph.type2meta?.[String(node.type_id)] ?? '' },
    created_at: '',
    updated_at: '',
  }));
  const relations: TopologyRelation[] = graph.links.map((link, index) => ({
    id: index + 1,
    src_id: link.from,
    dst_id: link.to,
    type: 'cmdb',
    props: null,
    created_at: '',
    updated_at: '',
  }));

  return (
    <div className="h-[calc(100vh-16rem)] min-h-[24rem] w-full">
      <TopologyGraph
        nodes={nodes}
        relations={relations}
        relationTypes={[]}
        selectedID={null}
        onSelect={() => {}}
      />
    </div>
  );
}
