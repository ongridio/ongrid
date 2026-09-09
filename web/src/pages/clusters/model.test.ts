import { describe, expect, it } from "vitest";
import type { Device } from "@/api/devices";
import type { EdgeEnrollmentProfile } from "@/api/edges";
import type { TopologyNode, TopologyRelation } from "@/api/topology";
import {
  buildClusterSummaries,
  clusterMembershipByDeviceNode,
} from "./model";

const clusters: TopologyNode[] = [
  {
    id: 501,
    type: "cluster",
    name: "prod-hosts",
    props: { source: "manual" },
    created_at: "2026-07-31T00:00:00Z",
    updated_at: "2026-07-31T01:00:00Z",
  },
  {
    id: 901,
    type: "cluster",
    name: "k8s-prod",
    props: { source: "kubernetes" },
    created_at: "2026-07-31T00:00:00Z",
    updated_at: "2026-07-31T01:00:00Z",
  },
];

const devices: Device[] = [
  { id: 19, node_id: 119, name: "host-a", online: true },
  { id: 20, node_id: 120, name: "host-b", online: false },
];

const relations: TopologyRelation[] = [
  {
    id: 701,
    src_id: 119,
    dst_id: 501,
    type: "member_of",
    props: { source: "edge_enrollment" },
    created_at: "2026-07-31T01:00:00Z",
  },
  {
    id: 702,
    src_id: 120,
    dst_id: 501,
    type: "member_of",
    props: { source: "manual" },
    created_at: "2026-07-31T01:00:00Z",
  },
  {
    id: 704,
    src_id: 501,
    dst_id: 801,
    type: "depends_on",
    created_at: "2026-07-31T01:00:00Z",
  },
];

const profiles: EdgeEnrollmentProfile[] = [
  {
    id: 31,
    name: "prod rollout",
    assignment_mode: "cluster",
    cluster_node_id: 501,
    expires_at: "2026-08-01T00:00:00Z",
    max_uses: 100,
    used_count: 2,
    status: "active",
    created_at: "2026-07-31T00:00:00Z",
  },
  {
    id: 32,
    name: "standalone rollout",
    assignment_mode: "batch_only",
    expires_at: "2026-08-01T00:00:00Z",
    max_uses: 10,
    used_count: 0,
    status: "active",
    created_at: "2026-07-31T00:00:00Z",
  },
];

describe("device cluster model", () => {
  it("includes both cluster enrollment methods and summarizes members and profiles", () => {
    const result = buildClusterSummaries(
      clusters,
      devices,
      relations,
      profiles,
    );

    expect(result).toHaveLength(2);
    expect(result.some((item) => item.cluster.id === 901)).toBe(true);
    result.sort((a,b) => a.cluster.id - b.cluster.id);
    expect(result[0].cluster.id).toBe(501);
    expect(result[0].members.map((device) => device.id)).toEqual([19, 20]);
    expect(result[0].online).toBe(1);
    expect(result[0].offline).toBe(1);
    expect(result[0].activeProfiles).toBe(1);
    expect(result[0].externalRelations.map((relation) => relation.id)).toEqual([
      704,
    ]);
  });

  it("maps each device node to its non-Kubernetes cluster", () => {
    const result = clusterMembershipByDeviceNode(clusters, [
      ...relations,
      {
        id: 703,
        src_id: 121,
        dst_id: 901,
        type: "member_of",
        created_at: "2026-07-31T01:00:00Z",
      },
    ]);

    expect(result.get(119)?.id).toBe(501);
    expect(result.get(120)?.id).toBe(501);
    expect(result.has(121)).toBe(false);
  });
});
