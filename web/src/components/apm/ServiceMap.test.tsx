import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { expect, it, vi } from 'vitest';
import type { Edge, Node, NodeChange, EdgeChange } from '@xyflow/react';
import type { ReactNode } from 'react';
import type { ApmList, ApmDependencies } from '@/api/apm';
import { ServiceMap, trafficLevel } from './ServiceMap';
vi.mock('@xyflow/react', () => ({ MarkerType: { ArrowClosed: 'arrowclosed' }, Position: { Left: 'left', Right: 'right' }, Background: () => null, Controls: () => null,
  ReactFlow: ({ nodes, edges, onNodeClick, onEdgeClick, onNodesChange, onEdgesChange }: { nodes: Node[]; edges: Edge[]; onNodeClick: (event: unknown, node: Node) => void; onEdgeClick: (event: unknown, edge: Edge) => void; onNodesChange: (changes: NodeChange[]) => void; onEdgesChange: (changes: EdgeChange[]) => void }) => <div>
    {nodes.map(node => <button key={node.id} className={node.className} aria-label={node.ariaLabel} onClick={e => onNodeClick(e, node)}>{node.data.label as ReactNode}</button>)}
    {edges.map(edge => <button key={edge.id} aria-label={edge.ariaLabel} onClick={e => onEdgeClick(e, edge)} onKeyDown={e => { if (e.key === 'Enter') { onEdgesChange([{ type: 'select', id: edge.id, selected: true }]); onNodesChange(nodes.filter(n => n.selected).map(n => ({ type: 'select', id: n.id, selected: false }))); } }}>{edge.label as ReactNode}</button>)}</div>,
}));
it('keeps fixed traffic decades and distinguishes no data from zero', () => {
  expect([null, 0, 0.01, 9.9, 10, 100, 1000, 1e9].map(trafficLevel)).toEqual([-1, 0, 1, 1, 2, 3, 4, 4]);
});
it('separates identities and sampled traffic, and preserves map context in drilldown', () => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  const identity = (name: string, environment = 'production') => ({ service_name: name, service_namespace: 'trade', environment });
  const list = { items: [
    { identity: identity('orders'), rps: 1500, error_rate: 2, p95_ms: 80 },
    { identity: identity('orders', 'staging'), rps: 2, error_rate: 0, p95_ms: 20 },
    { identity: identity('sampled'), rps: 9999, metric_source: 'tempo_spanmetrics', error_rate: 0 },
  ], total: 3 } as ApmList;
  const dependencies: ApmDependencies = { items: [{ client: identity('orders'), server: identity('mysql'), connection_type: 'database', rps: 30, error_rate: 1, p95_ms: 6 }], truncated: false };
  render(<MemoryRouter><ServiceMap list={list} dependencies={dependencies} params={new URLSearchParams('tab=map&start=2026-09-10T00:00:00Z&end=2026-09-10T01:00:00Z')} loading={false} /></MemoryRouter>);
  expect(screen.getByRole('button', { name: 'sampled · production · trade' })).toHaveClass('traffic--1');
  const order = screen.getByRole('button', { name: 'orders · production · trade' });
  expect(order).toHaveClass('traffic-4');
  expect(screen.getByRole('button', { name: 'orders · staging · trade' })).toHaveClass('traffic-1');
  fireEvent.click(order);
  const link = new URL(screen.getByRole('link', { name: '查看服务' }).getAttribute('href')!, 'http://localhost');
  expect(link.searchParams.get('environment')).toBe('production');
  expect(link.searchParams.has('tab')).toBe(false);
  expect(new URLSearchParams(link.searchParams.get('list_query')!).get('tab')).toBe('map');
  fireEvent.keyDown(screen.getByRole('button', { name: 'orders → mysql' }), { key: 'Enter' });
  expect(within(screen.getByRole('complementary')).getByText('30 /s')).toBeInTheDocument();
  expect(within(screen.getByRole('complementary')).getByText('观测调用速率')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: '关闭详情' }));
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument();
  fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'missing' } });
  expect(screen.getByText('没有匹配的服务')).toBeInTheDocument();
});
