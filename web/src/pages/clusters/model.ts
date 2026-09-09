import type { Device } from "@/api/devices";
import type { EdgeEnrollmentProfile } from "@/api/edges";
import type { TopologyNode, TopologyRelation } from "@/api/topology";

export type ClusterSummary = {
  cluster: TopologyNode;
  members: Device[];
  memberRelations: TopologyRelation[];
  externalRelations: TopologyRelation[];
  profiles: EdgeEnrollmentProfile[];
  online: number;
  offline: number;
  activeProfiles: number;
  lastMemberSeenAt?: string;
};

export function isDeviceCluster(node: TopologyNode): boolean {
  return node.type === "cluster" && node.props?.source !== "kubernetes";
}

export function buildClusterSummaries(
  nodes: TopologyNode[],
  devices: Device[],
  relations: TopologyRelation[],
  profiles: EdgeEnrollmentProfile[],
): ClusterSummary[] {
  const devicesByNodeID = new Map<number, Device>();
  for (const device of devices) {
    if (device.node_id) devicesByNodeID.set(device.node_id, device);
  }

  return nodes
    .filter((node) => node.type === "cluster")
    .map((cluster) => {
      const memberRelations = relations.filter(
        (relation) =>
          relation.type === "member_of" && relation.dst_id === cluster.id,
      );
      const memberRelationIDs = new Set(
        memberRelations.map((relation) => relation.id),
      );
      const externalRelations = relations.filter(
        (relation) =>
          (relation.src_id === cluster.id || relation.dst_id === cluster.id) &&
          !memberRelationIDs.has(relation.id),
      );
      const members = memberRelations
        .map((relation) => devicesByNodeID.get(relation.src_id))
        .filter((device): device is Device => Boolean(device))
        .sort((left, right) =>
          (left.name || left.hostname || "").localeCompare(
            right.name || right.hostname || "",
          ),
        );
      const clusterProfiles = profiles.filter(
        (profile) =>
          profile.assignment_mode === "cluster" &&
          profile.cluster_node_id === cluster.id,
      );
      const online = members.filter((device) => device.online === true).length;
      const lastMemberSeenAt = latestMemberSeenAt(members);
      return {
        cluster,
        members,
        memberRelations,
        externalRelations,
        profiles: clusterProfiles,
        online,
        offline: members.length - online,
        activeProfiles: clusterProfiles.filter(
          (profile) => profile.status === "active",
        ).length,
        lastMemberSeenAt,
      };
    })
    .sort((left, right) => left.cluster.name.localeCompare(right.cluster.name));
}

function latestMemberSeenAt(members: Device[]): string | undefined {
  return members.reduce<string | undefined>((latest, device) => {
    const candidate = device.last_seen_at ?? device.updated_at;
    if (!candidate || Number.isNaN(Date.parse(candidate))) return latest;
    if (!latest || Date.parse(candidate) > Date.parse(latest)) return candidate;
    return latest;
  }, undefined);
}

export function clusterMembershipByDeviceNode(
  clusters: TopologyNode[],
  relations: TopologyRelation[],
): Map<number, TopologyNode> {
  const clustersByID = new Map(
    clusters.filter(isDeviceCluster).map((cluster) => [cluster.id, cluster]),
  );
  const out = new Map<number, TopologyNode>();
  for (const relation of relations) {
    if (relation.type !== "member_of") continue;
    const cluster = clustersByID.get(relation.dst_id);
    if (cluster && !out.has(relation.src_id)) {
      out.set(relation.src_id, cluster);
    }
  }
  return out;
}

export function relationSource(relation: TopologyRelation): string {
  return typeof relation.props?.source === "string"
    ? relation.props.source
    : "manual";
}
