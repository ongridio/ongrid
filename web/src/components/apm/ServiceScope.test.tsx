import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { expect, it, vi } from 'vitest';
import { queryApm } from '@/api/apm';
import { request } from '@/api/client';
import { ServiceSwitcher } from './ServiceSwitcher';
import { ServiceProfiles } from './ServiceProfiles';

vi.mock('@/api/apm', async (original) => ({ ...await original<object>(), queryApm: vi.fn().mockResolvedValue({ items: [], total: 0 }) }));
vi.mock('@/api/client', () => ({ request: vi.fn().mockResolvedValue({}) }));
vi.mock('@/pages/DailyTools', () => ({ NativeFlamegraph: () => null }));

it('does not restrict service discovery to the old service instance/version', async () => {
  render(<MemoryRouter><ServiceSwitcher params={new URLSearchParams({ service_name: 'orders', environment: 'prod', service_namespace: 'trade', service_version: 'v1', instance_id: 'orders-1', start: '2026-09-10T00:00:00Z', end: '2026-09-10T01:00:00Z' })} navigationState={null} /></MemoryRouter>);
  fireEvent.click(screen.getByRole('button', { name: /切换服务|Switch service/ }));
  await waitFor(() => expect(queryApm).toHaveBeenCalled());
  const query = vi.mocked(queryApm).mock.calls[0][1];
  expect(query.get('instance_id')).toBeNull();
  expect(query.get('service_version')).toBeNull();
});

it('includes the selected version in the historical profile request', async () => {
  render(<MemoryRouter><ServiceProfiles params={new URLSearchParams({ service_name: 'orders', environment: 'prod', service_namespace: 'trade', service_version: 'v2', start: '2026-09-10T00:00:00Z', end: '2026-09-10T01:00:00Z' })} instances={[{ instance_id: 'orders-1', device_id: '42', version: 'v2', cluster_id: '', pod: '' }]} loading={false} refresh={0} /></MemoryRouter>);
  await waitFor(() => expect(request).toHaveBeenCalled());
  const query = new URL(String(vi.mocked(request).mock.calls[0][1]), 'http://localhost').searchParams;
  expect(query.get('service_version')).toBe('v2');
});
