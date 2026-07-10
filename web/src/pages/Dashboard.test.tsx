import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import DashboardPage from './Dashboard';
import { server } from '@/test/msw-server';

describe('DashboardPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/edges', () =>
        HttpResponse.json({
          items: [
            {
              id: 23,
              name: 'k8s:kind-local:controller',
              status: 'online',
              roles: [],
              access_key_id: 'ak-controller',
              last_seen_at: '2026-06-29T10:00:00Z',
              host_info: null,
              device_id: null,
              agent_version: null,
            },
            {
              id: 24,
              name: 'k8s:kind-local:ongrid-k8s-control-plane',
              status: 'online',
              roles: [],
              access_key_id: 'ak-node',
              last_seen_at: '2026-06-29T10:00:00Z',
              host_info: { hostname: 'ongrid-k8s-control-plane', ip_address: '10.0.0.5' },
              device_id: 31,
              agent_version: 'v0.9.0',
            },
          ],
          total: 2,
        }),
      ),
      http.get('/api/v1/k8s/edge-attachments', () =>
        HttpResponse.json({
          items: [
            { edge_id: 23, cluster_id: 1, cluster_name: 'kind-local', cluster_mode: 'full-node', node_name: 'ongrid-k8s-control-plane', kind: 'k8s-controller' },
            { edge_id: 24, cluster_id: 1, cluster_name: 'kind-local', cluster_mode: 'full-node', node_name: 'ongrid-k8s-control-plane', kind: 'k8s-node' },
            { edge_id: 24, cluster_id: 1, cluster_name: 'kind-local', cluster_mode: 'full-node', node_name: 'ongrid-k8s-control-plane', kind: 'k8s-controller-runtime' },
          ],
          total: 3,
        }),
      ),
      http.get('/api/v1/metrics/query_range', () =>
        HttpResponse.json({
          resolution: '1h',
          from: '2026-06-28T10:00:00Z',
          to: '2026-06-29T10:00:00Z',
          matrix: [],
        }),
      ),
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/alerts/incidents', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/usage/today', () => HttpResponse.json({ total_tokens: 0 })),
    );
  });

  it('统计在线设备时不把 K8s Controller Edge 当作设备', async () => {
    render(
      <MemoryRouter>
        <DashboardPage />
      </MemoryRouter>,
    );

    const card = (await screen.findByText('在线设备')).closest('.rounded-xl') as HTMLElement;
    expect(card).not.toBeNull();

    await waitFor(() => {
      expect(within(card).getByText('1')).toBeInTheDocument();
      expect(within(card).getByText('/ 1')).toBeInTheDocument();
    });
    expect(screen.getByText('1 台 →')).toBeInTheDocument();
  });
});
