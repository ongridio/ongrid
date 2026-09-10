import { render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { ApmRuntime } from '@/api/apm';
import { RuntimeMetrics } from './RuntimeMetrics';

vi.mock('recharts', () => ({
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => children,
  LineChart: ({ data }: { data: unknown }) => <div data-testid="points">{JSON.stringify(data)}</div>,
  CartesianGrid: () => null, Line: () => null, Tooltip: () => null, XAxis: () => null, YAxis: () => null,
}));

describe('Runtime metrics', () => {
  it.each(['zh-CN', 'en-US'])('keeps chart and legend units consistent and missing GC distinct from zero in %s', (locale) => {
    localStorage.setItem('ongrid-locale', locale);
    render(<RuntimeMetrics data={{ items: [
      { name: 'go_memory_allocation_bytes_per_second', unit: 'bytes_per_second', value: 1048576, instance_id: 'go-1', version: 'v1', points: [{ timestamp: 1, value: 1048576 }] },
      { name: 'go_gc_mean_duration_seconds', unit: 'seconds', value: null, instance_id: 'go-1', version: 'v1', points: [{ timestamp: 1, value: 0.002 }, { timestamp: 2, value: null }] },
      { name: 'jvm_heap_memory_used_bytes', unit: 'bytes', value: 2097152, instance_id: 'java-1', version: 'v2', points: [{ timestamp: 1, value: 2097152 }] },
      { name: 'jvm_non_heap_memory_used_bytes', unit: 'bytes', value: 0, instance_id: 'java-1', version: 'v2', points: [{ timestamp: 1, value: 0 }] },
    ] } as ApmRuntime} />);
    const allocation = screen.getByRole('region', { name: /Go 内存分配速率|Go memory allocation rate/ });
    expect(within(allocation).getByText('1 MiB/s')).toBeInTheDocument();
    expect(within(allocation).getByTestId('points')).toHaveTextContent('"v0":1');
    const gc = screen.getByRole('region', { name: /Go GC/ });
    expect(within(gc).getByTestId('points')).toHaveTextContent('"v0":2');
    expect(within(gc).getByTestId('points')).toHaveTextContent('"v0":null');
    expect(within(gc).getByText('—')).toBeInTheDocument();
    expect(screen.getByText('2 MiB')).toBeInTheDocument();
    expect(screen.getByText('0 MiB')).toBeInTheDocument();
    expect(screen.getAllByText('java-1 · v2')).toHaveLength(2);
  });
});
