import { listKubernetesEdgeAttachments } from '@/api/kubernetes';

export type K8sEdgeAttachment = {
  kind: 'k8s-controller' | 'k8s-controller-runtime' | 'k8s-node';
  clusterId: number;
  clusterName: string;
  clusterMode: string;
  nodeName?: string;
};

export type K8sEdgeAttachmentMap = Record<number, K8sEdgeAttachment[]>;

export async function loadK8sEdgeAttachments(): Promise<K8sEdgeAttachmentMap> {
  const limit = 500;
  const out = new Map<number, K8sEdgeAttachment[]>();
  for (let offset = 0; ; offset += limit) {
    const response = await listKubernetesEdgeAttachments({ limit, offset });
    const items = response.items ?? [];
    for (const item of items) {
      addK8sEdgeAttachment(out, item.edge_id, {
        kind: item.kind,
        clusterId: item.cluster_id,
        clusterName: item.cluster_name,
        clusterMode: item.cluster_mode,
        nodeName: item.node_name,
      });
    }
    if (items.length < limit || offset + items.length >= response.total) break;
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
