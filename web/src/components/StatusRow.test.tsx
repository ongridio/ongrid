import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { StatusRow } from './StatusRow';

describe('StatusRow', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/edges', () =>
        HttpResponse.json({
          items: [
            {
              id: 27,
              name: 'k8s:kind-local:node',
              status: 'online',
              roles: [],
              access_key_id: 'ak-node',
              last_seen_at: '2026-07-10T09:00:00Z',
              device_id: 33,
            },
            {
              id: 28,
              name: 'k8s:kind-local:controller',
              status: 'online',
              roles: [],
              access_key_id: 'ak-controller',
              last_seen_at: '2026-07-10T09:00:00Z',
              device_id: null,
            },
          ],
          total: 2,
        }),
      ),
      http.get('/api/v1/k8s/edge-attachments', () =>
        HttpResponse.json({
          items: [
            {
              edge_id: 27,
              cluster_id: 22,
              cluster_name: 'kind-local',
              cluster_mode: 'full-node',
              node_name: 'kind-local-control-plane',
              kind: 'k8s-node',
            },
            {
              edge_id: 28,
              cluster_id: 22,
              cluster_name: 'kind-local',
              cluster_mode: 'full-node',
              node_name: 'kind-local-control-plane',
              kind: 'k8s-controller',
            },
          ],
          total: 2,
        }),
      ),
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/usage/today', () => HttpResponse.json({ total_tokens: 0 })),
    );
  });

  it('不把 K8s Controller Edge 计为在线设备', async () => {
    render(
      <MemoryRouter>
        <StatusRow />
      </MemoryRouter>,
    );

    expect(await screen.findByText('1/1 在线设备')).toBeInTheDocument();
    expect(screen.queryByText('2/2 在线设备')).not.toBeInTheDocument();
  });
});
