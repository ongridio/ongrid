import { beforeEach, describe, expect, it, vi } from 'vitest';

import { listKubernetesEdgeAttachments } from '@/api/kubernetes';

import { loadK8sEdgeAttachments } from './edgeAttachments';

vi.mock('@/api/kubernetes', () => ({
  listKubernetesEdgeAttachments: vi.fn(),
}));

const listAttachmentsMock = vi.mocked(listKubernetesEdgeAttachments);

describe('loadK8sEdgeAttachments', () => {
  beforeEach(() => {
    listAttachmentsMock.mockReset();
  });

  it('loads every page of managed Edge attachments', async () => {
    const firstPage = Array.from({ length: 500 }, (_, index) => ({
      edge_id: index + 1,
      cluster_id: 1,
      cluster_name: 'prod',
      cluster_mode: 'full-node',
      node_name: `node-${index + 1}`,
      kind: 'k8s-node' as const,
    }));
    listAttachmentsMock
      .mockResolvedValueOnce({ items: firstPage, total: 501, limit: 500, offset: 0 })
      .mockResolvedValueOnce({
        items: [{
          edge_id: 501,
          cluster_id: 1,
          cluster_name: 'prod',
          cluster_mode: 'full-node',
          node_name: 'node-501',
          kind: 'k8s-node',
        }],
        total: 501,
        limit: 500,
        offset: 500,
      });

    const attachments = await loadK8sEdgeAttachments();

    expect(listAttachmentsMock).toHaveBeenNthCalledWith(1, { limit: 500, offset: 0 });
    expect(listAttachmentsMock).toHaveBeenNthCalledWith(2, { limit: 500, offset: 500 });
    expect(Object.keys(attachments)).toHaveLength(501);
    expect(attachments[501]?.[0]).toMatchObject({ clusterName: 'prod', nodeName: 'node-501' });
  });
});
