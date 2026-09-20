import { listNodes, listRelations, type TopologyNode, type TopologyRelation } from "@/api/topology";

function indexTopologyClusters(
  clusters: TopologyNode[],
  relations: TopologyRelation[],
): Map<number, TopologyNode[]> {
  const clustersByID = new Map(
    clusters.map((cluster) => [cluster.id, cluster]),
  );
  const out = new Map<number, TopologyNode[]>();
  for (const relation of relations) {
    if (relation.type !== "member_of") continue;
    const cluster = clustersByID.get(relation.dst_id);
    if (!cluster) continue;
    const memberships = out.get(relation.src_id) ?? [];
    if (!memberships.some((item) => item.id === cluster.id)) {
      memberships.push(cluster);
      memberships.sort((a, b) => a.name.localeCompare(b.name));
    }
    out.set(relation.src_id, memberships);
  }
  return out;
}

export async function loadTopologyClusters(): Promise<Map<number, TopologyNode[]>> {
  const [clusterResp, relationResp] = await Promise.all([
    listNodes({ type: "cluster" }),
    listRelations({ type: "member_of" }),
  ]);
  return indexTopologyClusters(
    clusterResp.items ?? [],
    relationResp.items ?? [],
  );
}
