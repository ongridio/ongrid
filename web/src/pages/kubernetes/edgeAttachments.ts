import { listKubernetesClusters, listKubernetesNodes } from '@/api/kubernetes';

export type K8sEdgeAttachment = {
  kind: 'k8s-controller' | 'k8s-controller-runtime' | 'k8s-node';
  clusterId: number;
  clusterName: string;
  clusterMode: string;
  nodeName?: string;
};

export type K8sEdgeAttachmentMap = Record<number, K8sEdgeAttachment[]>;

export async function loadK8sEdgeAttachments(): Promise<K8sEdgeAttachmentMap> {
  const clustersOut = await listKubernetesClusters({ limit: 100 });
  const clusters = clustersOut.items ?? [];
  const out = new Map<number, K8sEdgeAttachment[]>();

  for (const cluster of clusters) {
    if (cluster.controller_edge_id) {
      addK8sEdgeAttachment(out, cluster.controller_edge_id, {
        kind: 'k8s-controller',
        clusterId: cluster.id,
        clusterName: cluster.name,
        clusterMode: cluster.mode,
      });
    }
  }

  const nodeLoads = await Promise.allSettled(
    clusters.map(async (cluster) => ({
      cluster,
      nodes: (await listKubernetesNodes(cluster.id)).items ?? [],
    })),
  );
  for (const result of nodeLoads) {
    if (result.status !== 'fulfilled') continue;
    const controllerNodeName = result.value.cluster.controller_node_name?.trim();
    for (const node of result.value.nodes) {
      if (!node.edge_id) continue;
      addK8sEdgeAttachment(out, node.edge_id, {
        kind: 'k8s-node',
        clusterId: result.value.cluster.id,
        clusterName: result.value.cluster.name,
        clusterMode: result.value.cluster.mode,
        nodeName: node.node_name,
      });
      if (controllerNodeName && node.node_name === controllerNodeName) {
        addK8sEdgeAttachment(out, node.edge_id, {
          kind: 'k8s-controller-runtime',
          clusterId: result.value.cluster.id,
          clusterName: result.value.cluster.name,
          clusterMode: result.value.cluster.mode,
          nodeName: node.node_name,
        });
      }
    }
  }

  return Object.fromEntries(out.entries());
}

export function isK8sControllerEdge(attachments: K8sEdgeAttachment[]) {
  return attachments.some((item) => item.kind === 'k8s-controller');
}

export function filterVisibleDeviceEdges<T extends { id: number }>(
  edges: T[],
  attachments: K8sEdgeAttachmentMap,
) {
  return edges.filter((edge) => !isK8sControllerEdge(attachments[edge.id] ?? []));
}

export function isK8sManagedEdge(attachments: K8sEdgeAttachment[]) {
  return attachments.some((item) => item.kind === 'k8s-node' || item.kind === 'k8s-controller-runtime');
}

export function uniqueAttachmentClusters(attachments: K8sEdgeAttachment[]): K8sEdgeAttachment[] {
  const out = new Map<number, K8sEdgeAttachment>();
  for (const item of attachments) {
    if (!out.has(item.clusterId)) out.set(item.clusterId, item);
  }
  return [...out.values()];
}

function addK8sEdgeAttachment(
  out: Map<number, K8sEdgeAttachment[]>,
  edgeID: number,
  attachment: K8sEdgeAttachment,
) {
  const items = out.get(edgeID) ?? [];
  if (
    !items.some(
      (item) =>
        item.kind === attachment.kind &&
        item.clusterId === attachment.clusterId &&
        item.nodeName === attachment.nodeName,
    )
  ) {
    items.push(attachment);
  }
  out.set(edgeID, items);
}
