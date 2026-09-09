import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import type { Node, Edge } from '@xyflow/react';
import { Dependencies } from './Dependencies';

vi.mock('@xyflow/react', () => ({
  ReactFlow: ({ nodes, edges }: { nodes: Node[]; edges: Edge[] }) => (
    <div data-testid="graph" data-edges={edges.length}>
      {nodes.map((node) => <div key={node.id}>{node.data.label as React.ReactNode}</div>)}
    </div>
  ),
  Background: () => null,
  Controls: () => null,
}));

describe('service dependency nodes', () => {
  it('keeps the current service without edges and deduplicates it when edges arrive', () => {
    const current = { environment: 'dev', service_namespace: 'shop', service_name: 'orders' };
    const params = new URLSearchParams(current);
    const { rerender } = render(<MemoryRouter><Dependencies data={{ items: [], truncated: false }} params={params} /></MemoryRouter>);
    expect(screen.getByRole('link', { name: /orders/ })).toBeInTheDocument();
    expect(screen.getByTestId('graph')).toHaveAttribute('data-edges', '0');
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    rerender(<MemoryRouter><Dependencies data={{ items: [{ client: current, server: { ...current, service_name: 'payments' }, rps: 1, error_rate: 0, p95_ms: 10, connection_type: '' }], truncated: false }} params={params} /></MemoryRouter>);
    expect(screen.getByTestId('graph').children).toHaveLength(2);
    expect(screen.getByTestId('graph')).toHaveAttribute('data-edges', '1');
  });
});
