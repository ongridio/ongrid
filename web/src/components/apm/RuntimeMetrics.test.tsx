import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { ApmRuntime } from '@/api/apm';
import { RuntimeMetrics } from './RuntimeMetrics';

vi.mock('recharts', () => ({
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => children,
  LineChart: ({ data, children }: { data: unknown; children: React.ReactNode }) => <div><div data-testid="points">{JSON.stringify(data)}</div>{children}</div>,
  CartesianGrid: () => null, Line: ({ name }: { name: string }) => <div data-testid="series" data-name={name} />, Tooltip: () => null, XAxis: () => null, YAxis: () => null,
}));

describe('Runtime metrics', () => {
  it('filters only the clicked chart and restores all lines on a second click', async () => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    const user = userEvent.setup();
    render(<RuntimeMetrics data={{ items: ['process_cpu_cores', 'process_threads'].flatMap((name) => [
      { name, unit: 'count', value: 1, instance_id: 'python:1', version: 'v1', points: [{ timestamp: 1, value: 1 }] },
      { name, unit: 'count', value: 2, instance_id: 'python:2', version: 'v1', points: [{ timestamp: 1, value: 2 }] },
    ]) } as ApmRuntime} />);
    const cpu = screen.getByRole('region', { name: 'CPU 使用量' });
    const threads = screen.getByRole('region', { name: '线程数' });
    await act(async () => { await user.click(within(cpu).getByRole('button', { name: /python:2.*只显示此曲线/ })); });
    expect(within(cpu).getAllByTestId('series')).toHaveLength(1);
    expect(within(cpu).getByTestId('series')).toHaveAttribute('data-name', 'python:2 · v1');
    expect(within(threads).getAllByTestId('series')).toHaveLength(2);
    await act(async () => { await user.click(within(cpu).getByRole('button', { name: /python:2.*显示全部曲线/ })); });
    expect(within(cpu).getAllByTestId('series')).toHaveLength(2);
  });

  it('shows Python process resources above the independent runtime empty state', () => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    render(<RuntimeMetrics data={{ items: [
      { name: 'process_cpu_cores', unit: 'cores', value: 0.25, instance_id: 'python:21', version: '', points: [{ timestamp: 1, value: 0.25 }] },
      { name: 'process_resident_memory_bytes', unit: 'bytes', value: 10485760, instance_id: 'python:21', version: '', points: [{ timestamp: 1, value: 10485760 }] },
      { name: 'process_io_read_bytes_per_second', unit: 'bytes_per_second', value: 0, instance_id: 'python:21', version: '', points: [{ timestamp: 1, value: 0 }] },
    ] } as ApmRuntime} />);
    const resources = screen.getByRole('region', { name: '基础资源' });
    const runtime = screen.getByRole('region', { name: '运行时监控' });
    expect(within(resources).getByText('0.25 核')).toBeInTheDocument();
    expect(within(resources).getByText('10 MiB')).toBeInTheDocument();
    expect(within(resources).getByText('0 MiB/s')).toBeInTheDocument();
    expect(within(runtime).getByText('暂无运行时指标')).toBeInTheDocument();
    expect(within(runtime).queryByText('10 MiB')).not.toBeInTheDocument();
    expect(resources.compareDocumentPosition(runtime) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
  it.each(['zh-CN', 'en-US'])('keeps chart and legend units consistent and missing GC distinct from zero in %s', (locale) => {
    localStorage.setItem('ongrid-locale', locale);
    render(<RuntimeMetrics data={{ items: [
      { name: 'nodejs_eventloop_utilization_ratio', unit: 'ratio', value: 0.25, instance_id: 'node-1', version: '', points: [{ timestamp: 1, value: 0.25 }] },
      { name: 'go_config_gogc_percent', unit: 'percent', value: 100, instance_id: 'go-1', version: '', points: [] },
      { name: 'go_gc_cycles_per_second', unit: 'per_second', value: 0.5, instance_id: 'go-1', version: '', points: [] },
      { name: 'go_memory_allocation_bytes_per_second', unit: 'bytes_per_second', value: 1048576, instance_id: 'go-1', version: 'v1', points: [{ timestamp: 1, value: 1048576 }] },
      { name: 'go_gc_mean_duration_seconds', unit: 'seconds', value: null, instance_id: 'go-1', version: 'v1', points: [{ timestamp: 1, value: 0.002 }, { timestamp: 2, value: null }] },
      { name: 'jvm_heap_memory_used_bytes', unit: 'bytes', value: 2097152, instance_id: 'java-1', version: 'v2', points: [{ timestamp: 1, value: 2097152 }] },
      { name: 'jvm_non_heap_memory_used_bytes', unit: 'bytes', value: 0, instance_id: 'java-1', version: 'v2', points: [{ timestamp: 1, value: 0 }] },
    ] } as ApmRuntime} />);
    const allocation = screen.getByRole('region', { name: /Go 内存分配速率|Go memory allocation rate/ });
    expect(within(allocation).getByText('1 MiB/s')).toBeInTheDocument();
    expect(within(allocation).getByTestId('points')).toHaveTextContent('"v0":1');
    const gc = screen.getByRole('region', { name: /Go GC 平均暂停时间|Go GC mean pause duration/ });
    expect(within(gc).getByTestId('points')).toHaveTextContent('"v0":2');
    expect(within(gc).getByTestId('points')).toHaveTextContent('"v0":null');
    expect(within(gc).getByText('—')).toBeInTheDocument();
    expect(screen.getByText('2 MiB')).toBeInTheDocument();
    expect(screen.getByText('0 MiB')).toBeInTheDocument();
    expect(screen.getByText('25 %')).toBeInTheDocument();
    expect(screen.getByText('100 %')).toBeInTheDocument();
    expect(screen.getByText('0.5 /s')).toBeInTheDocument();
    expect(screen.getAllByText('java-1 · v2')).toHaveLength(2);
  });
});
