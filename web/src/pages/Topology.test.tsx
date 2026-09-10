import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { server } from '@/test/msw-server';
import type { TopologyNode } from '@/api/topology';
import TopologyPage from './Topology';

vi.mock('@/store/auth', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/store/auth')>(),
  useAuth: () => 'admin',
}));
vi.mock('@/components/topology/Graph', () => ({
  TopologyGraph: ({ nodes, onSelect }: { nodes: TopologyNode[]; onSelect: (node: TopologyNode) => void }) =>
    <>{nodes.map((node) => <button key={node.id} onClick={() => onSelect(node)}>{node.name}</button>)}</>,
}));

beforeEach(() => localStorage.setItem('ongrid-locale', 'zh-CN'));

it.each(['图谱', '节点 + 关系'])('%s keeps synced and cluster membership relations read-only from either endpoint', async (tab) => {
  const user = userEvent.setup();
  const nodes = [
    { id: 1, name: 'test-host', type: 'device' },
    { id: 2, name: 'test-cluster', type: 'cluster' },
    { id: 3, name: 'test-service', type: 'service' },
    { id: 4, name: 'reported-service', type: 'service', props: { source: 'apm', service_name: 'reported-service', service_namespace: 'shop', environment: 'prod' } },
  ];
  const relations = [
    { id: 1, src_id: 1, dst_id: 2, type: 'member_of', props: { source: 'manual' } },
    ...['edge_enrollment', 'kubernetes', 'network_discovery'].map((source, i) =>
      ({ id: i + 2, src_id: 1, dst_id: 3, type: 'connected_to', props: { source } })),
    { id: 5, src_id: 1, dst_id: 3, type: 'depends_on', props: {} },
    { id: 6, src_id: 4, dst_id: 1, type: 'deployed_on', props: { source: 'apm' } },
  ];
  server.use(
    http.get('/api/v1/topology/nodes', () => HttpResponse.json({ items: nodes, total: nodes.length })),
    http.get('/api/v1/topology/nodes/:id', ({ params }) => HttpResponse.json(nodes.find((node) => node.id === Number(params.id)))),
    http.get('/api/v1/topology/node-types', () => HttpResponse.json({ items: [] })),
    http.get('/api/v1/topology/relation-types', () => HttpResponse.json({ items: [] })),
    http.get('/api/v1/topology/relations', ({ request }) => {
      const id = Number(new URL(request.url).searchParams.get('src_or_dst_id'));
      const items = relations.filter((rel) => !id || rel.src_id === id || rel.dst_id === id);
      return HttpResponse.json({ items, total: items.length });
    }),
  );
  render(<MemoryRouter><TopologyPage /></MemoryRouter>);
  if (tab !== '图谱') await user.click(screen.getByRole('tab', { name: tab }));
  await user.click(await screen.findByText('test-host'));
  const list = await screen.findByRole('list');
  expect(await within(list).findAllByText('托管')).toHaveLength(5);
  const manualRow = within(list).getByText('depends_on').closest('li')!;
  expect(await within(manualRow).findByRole('button', { name: '删除关系' })).toBeInTheDocument();
  expect(within(list).getAllByRole('button', { name: '删除关系' })).toHaveLength(1);
  await user.click(screen.getByRole('button', { name: '关闭' }));
  await user.click(screen.getByText('test-cluster'));
  const clusterList = await screen.findByRole('list');
  expect(await within(clusterList).findByText('托管')).toHaveAttribute('title', '请在集群内管理成员关系。');
  expect(within(clusterList).queryByRole('button', { name: '删除关系' })).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: '关闭' }));
  await user.click(screen.getByText('reported-service'));
  expect(await screen.findByText('此服务及其设备关联由遥测上报自动维护，不能手动修改。')).toBeInTheDocument();
  const serviceList = await screen.findByRole('list');
  expect(await within(serviceList).findByText('托管')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '删除关系' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '删除节点' })).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: '查看服务' })).toHaveAttribute('href', '/apm?service_name=reported-service&service_namespace=shop&environment=prod');
});
