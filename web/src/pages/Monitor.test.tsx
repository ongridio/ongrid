import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { expect, it, vi } from 'vitest';
import MonitorPage from './Monitor';

vi.mock('@/components/monitor/PanelGrid', () => ({ PanelGrid: ({ fromMs, toMs }: { fromMs: number; toMs: number }) => <output data-testid="panel-window">{fromMs},{toMs}</output> }));
vi.mock('@/api/edges', async () => ({ ...await vi.importActual<typeof import('@/api/edges')>('@/api/edges'), listEdges: vi.fn(async () => []) }));
vi.mock('@/api/monitorPanels', () => ({ listMonitorPanels: vi.fn(async () => []), createMonitorPanel: vi.fn(), deleteMonitorPanel: vi.fn(), updateMonitorPanel: vi.fn() }));
vi.mock('@/lib/drilldown', () => ({ fetchGrafanaRootURL: vi.fn(async () => ''), openObservabilityUrl: vi.fn() }));

it('applies custom bounds to panels, keeps them fixed on refresh and returns to a rolling preset', async () => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  render(<MemoryRouter initialEntries={['/monitor?refresh=0']}><MonitorPage /></MemoryRouter>);
  const original = screen.getByTestId('panel-window').textContent;
  fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
  fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-09-08T01:00:00' } });
  fireEvent.change(screen.getByLabelText('结束时间'), { target: { value: '2026-09-08T02:00:00' } });
  expect(screen.getByTestId('panel-window')).toHaveTextContent(original!);
  fireEvent.click(screen.getByRole('button', { name: '应用' }));
  const selected = `${Date.parse('2026-09-08T01:00:00')},${Date.parse('2026-09-08T02:00:00')}`;
  await waitFor(() => expect(screen.getByTestId('panel-window')).toHaveTextContent(selected));
  fireEvent.click(screen.getByRole('button', { name: '刷新' }));
  expect(screen.getByTestId('panel-window')).toHaveTextContent(selected);
  fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
  fireEvent.click(screen.getByRole('button', { name: '15 分钟' }));
  const [start, end] = screen.getByTestId('panel-window').textContent!.split(',').map(Number);
  expect(end - start).toBe(900000);
  expect(end).toBeGreaterThan(Date.now() - 10000);
});
