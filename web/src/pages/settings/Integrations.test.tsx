import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import SettingsIntegrations from './Integrations';
import {
  getLogBackend,
  getLogBackendConnectionCheck,
  saveLogBackend,
  selectLogBackend,
  selectLokiLogBackend,
  startLogBackendConnectionCheck,
  testLogBackend,
  type LogBackend,
  type LogBackendConnectionCheck,
} from '@/api/logs';
import { openObservabilityUrl } from '@/lib/drilldown';

vi.mock('@/api/settings', () => ({
  listSettings: vi.fn(async () => ({ items: [], total: 0 })),
  setSetting: vi.fn(async () => undefined),
  revealSetting: vi.fn(async () => ({ value: '' })),
  testGrafanaConnection: vi.fn(async () => ({})),
  syncGrafana: vi.fn(async () => ({})),
  testPromConnection: vi.fn(async () => ({})),
  testLokiConnection: vi.fn(async () => ({})),
  testTempoConnection: vi.fn(async () => ({})),
  testWebSearchConnection: vi.fn(async () => ({})),
}));

vi.mock('@/api/logs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/logs')>();
  return {
    ...actual,
    getLogBackend: vi.fn(),
    getLogBackendConnectionCheck: vi.fn(),
    saveLogBackend: vi.fn(async () => ({})),
    selectLogBackend: vi.fn(async () => ({})),
    selectLokiLogBackend: vi.fn(),
    startLogBackendConnectionCheck: vi.fn(),
    testLogBackend: vi.fn(),
  };
});

vi.mock('@/lib/drilldown', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/drilldown')>();
  return {
    ...actual,
    openObservabilityUrl: vi.fn(async () => undefined),
  };
});

vi.mock('@/api/edges', () => ({
  listEdges: vi.fn(async () => ({ items: [], total: 0 })),
}));

vi.mock('@/api/integrations', () => ({
  getPluginCounts: vi.fn(async () => ({ counts: {} })),
}));

function backend(status: LogBackend['status'], currentBackend: LogBackend['current_backend'] = 'elasticsearch'): LogBackend {
  return {
    id: 7,
    name: 'external-elasticsearch',
    type: 'elasticsearch',
    status,
    generation: 1,
    current_backend: currentBackend,
    current_backend_id: 7,
    write_endpoints: ['https://es.example.com:9200'],
    query_endpoint: 'https://es.example.com:9200',
    dataset: 'ongrid.system',
    namespace: 'prod',
    index_pattern: 'logs-ongrid.*.otel-prod',
    write_credential_ref: 'write-key',
    query_credential_ref: 'query-key',
    has_custom_ca: false,
    tls_insecure: false,
    created_at: '2026-08-20T00:00:00Z',
    updated_at: '2026-08-20T00:00:00Z',
  };
}

describe('SettingsIntegrations log backend presentation', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(testLogBackend).mockResolvedValue({
      status: 'ok',
      detected_version: '8.16.3',
      tested_at: '2026-08-21T00:00:00Z',
    });
    localStorage.setItem('ongrid-locale', 'zh-CN');
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
      configurable: true,
      value: vi.fn(),
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('selects Loki immediately and leaves device verification to the separate action', async () => {
    let currentBackend = backend('selected');
    const activeLoki = {
      ...currentBackend,
      status: 'unselected' as const,
      current_backend: 'loki' as const,
      current_backend_id: 0,
      ended_at: '2026-08-21T00:00:00Z',
      assignments: [],
    };
    vi.mocked(getLogBackend).mockImplementation(async () => currentBackend);
    vi.mocked(selectLokiLogBackend).mockImplementation(async () => {
      currentBackend = activeLoki;
      return currentBackend;
    });

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    const lokiTab = await screen.findByRole('tab', { name: /Loki/ });
    await act(async () => user.click(lokiTab));
    const loki = screen.getByRole('region', { name: 'Loki 日志后端配置' });
    await act(async () => user.click(within(loki).getByRole('button', { name: '设为当前' })));

    await waitFor(() => expect(selectLokiLogBackend).toHaveBeenCalledOnce());
    expect(await screen.findByText(/Loki 已设为当前日志后端；可点击“检查设备连接”验证在线设备。/)).toBeVisible();
    expect(screen.getByRole('tab', { name: /Loki/ })).toHaveAttribute('aria-selected', 'true');
    expect(within(loki).getByRole('button', { name: '设为当前' })).toBeDisabled();
    expect(within(loki).getByRole('button', { name: '检查设备连接' })).toBeEnabled();
    expect(startLogBackendConnectionCheck).not.toHaveBeenCalled();
    expect(screen.queryByText(/正在验证当前在线/)).not.toBeInTheDocument();

    await act(async () => user.click(screen.getByRole('tab', { name: /Elasticsearch/ })));
    await screen.findByRole('heading', { name: 'Elasticsearch 配置' });

    expect(screen.queryByText('高级配置 · Elasticsearch Data Stream')).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: /日志数据集/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: /环境标识/ })).not.toBeInTheDocument();
  });

  it('keeps data stream naming internal when an existing configuration is saved', async () => {
    const saved = backend('unselected', 'loki');
    vi.mocked(getLogBackend).mockResolvedValue(saved);
    vi.mocked(saveLogBackend).mockResolvedValue(saved);

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await act(async () => user.click(await screen.findByRole('tab', { name: /Elasticsearch/ })));
    const elasticsearch = await screen.findByRole('region', { name: 'Elasticsearch 日志后端配置' });
    const queryEndpoint = within(elasticsearch).getByRole('textbox', { name: /Manager 查询 endpoint/ });
    fireEvent.change(queryEndpoint, { target: { value: 'https://es-query.example.com:9200' } });
    await act(async () => user.click(within(elasticsearch).getByRole('button', { name: '保存' })));

    await waitFor(() => expect(saveLogBackend).toHaveBeenCalledOnce());
    expect(vi.mocked(saveLogBackend).mock.calls[0][0]).toMatchObject({
      dataset: 'ongrid.system',
      namespace: 'prod',
      query_endpoint: 'https://es-query.example.com:9200',
    });
  });

  it('selects an unselected Elasticsearch configuration directly', async () => {
    const unselected = backend('unselected', 'loki');
    const selected = { ...unselected, status: 'selected' as const, current_backend: 'elasticsearch' as const };
    vi.mocked(getLogBackend).mockResolvedValue(unselected);
    vi.mocked(selectLogBackend).mockResolvedValue(selected);

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await act(async () => user.click(await screen.findByRole('tab', { name: /Elasticsearch/ })));
    await screen.findByRole('heading', { name: 'Elasticsearch 配置' });
    const elasticsearch = screen.getByRole('region', { name: 'Elasticsearch 日志后端配置' });
    expect(screen.queryByRole('combobox', { name: '真实写探针 Edge' })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: '仅灰度，不自动全量' })).not.toBeInTheDocument();
    expect(within(elasticsearch).getByRole('button', { name: '保存' })).toBeDisabled();
    expect(within(elasticsearch).getByRole('button', { name: '设为当前' })).toBeEnabled();
    await act(async () => user.click(within(elasticsearch).getByRole('button', { name: '设为当前' })));
    await waitFor(() => expect(selectLogBackend).toHaveBeenCalledWith(7));
    expect(saveLogBackend).not.toHaveBeenCalled();
  });

  it('switches immediately after the Manager validation and offers a separate device check', async () => {
    const unselected = backend('unselected', 'loki');
    const selected = {
      ...unselected,
      status: 'selected' as const,
      current_backend: 'elasticsearch' as const,
      current_backend_id: 7,
      cutover_at: '2026-08-20T01:00:00Z',
    };
    let currentBackend = unselected;
    vi.mocked(getLogBackend).mockImplementation(async () => currentBackend);
    vi.mocked(selectLogBackend).mockImplementation(async () => {
      currentBackend = selected;
      return selected;
    });

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await act(async () => user.click(await screen.findByRole('tab', { name: /Elasticsearch/ })));
    const elasticsearch = await screen.findByRole('region', { name: 'Elasticsearch 日志后端配置' });
    await act(async () => user.click(within(elasticsearch).getByRole('button', { name: '设为当前' })));
    await waitFor(() => expect(screen.getByRole('tabpanel')).toHaveTextContent('Elasticsearch 已设为当前日志后端'));
    expect(within(elasticsearch).getByRole('button', { name: '检查设备连接' })).toBeEnabled();
    expect(startLogBackendConnectionCheck).not.toHaveBeenCalled();
  });

  it('shows only the verified/online device count while the connection check converges', async () => {
    const pollCallbacks: Array<() => void> = [];
    vi.spyOn(window, 'setInterval').mockImplementation(((handler: TimerHandler) => {
      if (typeof handler === 'function') pollCallbacks.push(() => handler());
      return pollCallbacks.length;
    }) as typeof window.setInterval);
    vi.mocked(getLogBackend).mockResolvedValue(backend('selected'));
    const pending: LogBackendConnectionCheck = {
      backend_id: 7,
      backend: 'elasticsearch',
      generation: 3,
      observed_at: '2026-08-21T00:00:00Z',
      total: 2,
      online: 1,
      verified: 0,
      pending: 1,
      failed: 0,
      offline: 1,
      all_online_verified: false,
      edges: [
        { edge_id: 42, edge_name: 'edge-online', online: true, status: 'pending', desired_generation: 3, applied_generation: 0 },
        { edge_id: 43, edge_name: 'edge-offline', online: false, status: 'offline', desired_generation: 3, applied_generation: 0 },
      ],
    };
    const verified: LogBackendConnectionCheck = {
      ...pending,
      verified: 1,
      pending: 0,
      all_online_verified: true,
      edges: [
        { edge_id: 42, edge_name: 'edge-online', online: true, status: 'verified', desired_generation: 3, applied_generation: 3 },
        pending.edges[1],
      ],
    };
    vi.mocked(startLogBackendConnectionCheck).mockResolvedValue(pending);
    vi.mocked(getLogBackendConnectionCheck).mockResolvedValue(verified);

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const elasticsearch = await screen.findByRole('region', { name: 'Elasticsearch 日志后端配置' });
    await act(async () => userEvent.click(within(elasticsearch).getByRole('button', { name: '检查设备连接' })));
    await waitFor(() => expect(startLogBackendConnectionCheck).toHaveBeenCalledWith(7));
    expect(within(elasticsearch).getByText('在线设备验证 0/1')).toBeVisible();
    expect(within(elasticsearch).queryByText('edge-online')).not.toBeInTheDocument();
    expect(within(elasticsearch).queryByText('edge-offline')).not.toBeInTheDocument();
    await waitFor(() => expect(pollCallbacks.length).toBeGreaterThan(0));

    await act(async () => {
      pollCallbacks.forEach((poll) => poll());
      await Promise.resolve();
    });

    await waitFor(() => expect(within(elasticsearch).getByText('在线设备验证 1/1')).toBeVisible());
  });

  it('checks the selected Loki device path and advances the visual progress to completion', async () => {
    const pollCallbacks: Array<() => void> = [];
    vi.spyOn(window, 'setInterval').mockImplementation(((handler: TimerHandler) => {
      if (typeof handler === 'function') pollCallbacks.push(() => handler());
      return pollCallbacks.length;
    }) as typeof window.setInterval);
    vi.mocked(getLogBackend).mockResolvedValue(backend('unselected', 'loki'));
    const pending: LogBackendConnectionCheck = {
      backend_id: 0,
      backend: 'loki',
      generation: 51,
      observed_at: '2026-08-21T00:00:00Z',
      total: 3,
      online: 2,
      verified: 0,
      pending: 2,
      failed: 0,
      offline: 1,
      all_online_verified: false,
      edges: [],
    };
    const verified: LogBackendConnectionCheck = {
      ...pending,
      verified: 2,
      pending: 0,
      all_online_verified: true,
    };
    vi.mocked(startLogBackendConnectionCheck).mockResolvedValue(pending);
    vi.mocked(getLogBackendConnectionCheck).mockResolvedValue(verified);

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const loki = await screen.findByRole('region', { name: 'Loki 日志后端配置' });
    await act(async () => userEvent.click(within(loki).getByRole('button', { name: '检查设备连接' })));
    await waitFor(() => expect(startLogBackendConnectionCheck).toHaveBeenCalledWith());
    expect(within(loki).getByText('在线设备验证 0/2')).toBeVisible();
    expect(within(loki).getByRole('progressbar', { name: '在线设备验证进度' })).toHaveAttribute('aria-valuenow', '0');
    await waitFor(() => expect(pollCallbacks.length).toBeGreaterThan(0));

    await act(async () => {
      pollCallbacks.forEach((poll) => poll());
      await Promise.resolve();
    });

    await waitFor(() => expect(within(loki).getByText('在线设备验证 2/2')).toBeVisible());
    expect(within(loki).getByText('在线设备已全部验证')).toBeVisible();
    expect(within(loki).getByRole('progressbar', { name: '在线设备验证进度' })).toHaveAttribute('aria-valuenow', '2');
  });

  it('opens the selected Elasticsearch datasource in Grafana Explore', async () => {
    vi.mocked(getLogBackend).mockResolvedValue(backend('selected'));

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const elasticsearch = await screen.findByRole('region', { name: 'Elasticsearch 日志后端配置' });
    const openButton = within(elasticsearch).getByRole('button', { name: '在 Grafana 中查看日志' });
    await waitFor(() => expect(openButton).toBeEnabled());
    await act(async () => userEvent.click(openButton));

    await waitFor(() => expect(openObservabilityUrl).toHaveBeenCalledOnce());
    const target = new URL(vi.mocked(openObservabilityUrl).mock.calls[0][0]);
    const panes = JSON.parse(target.searchParams.get('panes') ?? '{}');
    expect(panes.og.datasource).toBe('ongrid-elasticsearch');
    expect(panes.og.queries[0].datasource).toEqual({ type: 'elasticsearch', uid: 'ongrid-elasticsearch' });
  });

  it('opens all Loki log sources in Grafana Explore', async () => {
    vi.mocked(getLogBackend).mockResolvedValue(backend('unselected', 'loki'));

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const loki = await screen.findByRole('region', { name: 'Loki 日志后端配置' });
    const openButton = within(loki).getByRole('button', { name: '在 Grafana 中查看日志' });
    await waitFor(() => expect(openButton).toBeEnabled());
    await act(async () => userEvent.click(openButton));

    await waitFor(() => expect(openObservabilityUrl).toHaveBeenCalledOnce());
    const target = new URL(vi.mocked(openObservabilityUrl).mock.calls[0][0]);
    const panes = JSON.parse(target.searchParams.get('panes') ?? '{}');
    expect(panes.og.datasource).toBe('ongrid-loki');
    expect(panes.og.queries[0].datasource).toEqual({ type: 'loki', uid: 'ongrid-loki' });
    expect(panes.og.queries[0].expr).toBe('{ongrid_source=~".+"}');
  });

  it('tests an unselected Elasticsearch configuration without selecting it', async () => {
    vi.mocked(getLogBackend).mockResolvedValue(backend('unselected', 'loki'));

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await act(async () => user.click(await screen.findByRole('tab', { name: /Elasticsearch/ })));
    const elasticsearch = await screen.findByRole('region', { name: 'Elasticsearch 日志后端配置' });
    await act(async () => user.click(within(elasticsearch).getByRole('button', { name: '测试连接' })));

    await waitFor(() => expect(testLogBackend).toHaveBeenCalledWith(7));
    expect(selectLogBackend).not.toHaveBeenCalled();
    expect(screen.getByText(/连接测试通过；查询\/写入端点及 API Key 权限有效（Elasticsearch 8\.16\.3）/)).toBeVisible();
  });

  it('keeps shared log actions ordered and exposes device checks for both backends', async () => {
    vi.mocked(getLogBackend).mockResolvedValue(backend('unselected', 'loki'));

    render(
      <MemoryRouter initialEntries={['/settings/integrations?focus=logs']}>
        <SettingsIntegrations />
      </MemoryRouter>,
    );

    const allExpected = ['保存', '测试连接', '设为当前', '检查设备连接', '在 Grafana 中查看日志'];
    const actionOrder = (region: HTMLElement) => within(region)
      .getAllByRole('button')
      .map((button) => button.textContent?.trim() ?? '')
      .filter((label) => allExpected.includes(label));

    const loki = await screen.findByRole('region', { name: 'Loki 日志后端配置' });
    expect(actionOrder(loki)).toEqual(allExpected);

    const user = userEvent.setup();
    await act(async () => user.click(screen.getByRole('tab', { name: /Elasticsearch/ })));
    const elasticsearch = await screen.findByRole('region', { name: 'Elasticsearch 日志后端配置' });
    expect(actionOrder(elasticsearch)).toEqual(allExpected);
  });
});
