import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import DailyToolsPage from './DailyTools';
import { server } from '@/test/msw-server';

vi.mock('@/components/XTerminal', async () => {
  const React = await vi.importActual<typeof import('react')>('react');
  return {
    XTerminal: ({ attachRef }: { attachRef(api: { write(data: string): void; writeln(line: string): void; clear(): void; fit(): void; focus(): void; dispose(): void }): void }) => {
      const [text, setText] = React.useState('');
      React.useEffect(() => {
        attachRef({
          write: (data: string) => setText((prev) => `${prev}${String(data)}`),
          writeln: (line: string) => setText((prev) => `${prev}${String(line)}\n`),
          clear: () => setText(''),
          fit: () => {},
          focus: () => {},
          dispose: () => {},
        });
      }, [attachRef]);
      return <pre data-testid="xterminal">{text}</pre>;
    },
  };
});

vi.stubGlobal('ResizeObserver', class ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
});

const edges = [
  { id: 1, name: 'edge-001', status: 'online', roles: [], access_key_id: 'ak-1', last_seen_at: null, device_id: 11 },
  { id: 2, name: 'edge-002', status: 'online', roles: [], access_key_id: 'ak-2', last_seen_at: null, device_id: 12 },
  { id: 3, name: 'edge-003', status: 'offline', roles: [], access_key_id: 'ak-3', last_seen_at: null, device_id: 13 },
];

function sse(event: string, data: unknown) {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

async function openEdgePicker() {
  await screen.findByRole('button', { name: /#1 edge-001|选择 Edge/ });
  await userEvent.click(screen.getByRole('button', { name: /#1 edge-001|选择 Edge/ }));
  await screen.findByLabelText('搜索 Edge');
}

async function selectEdge(label = '选择 #1 edge-001') {
  await openEdgePicker();
  await userEvent.click(screen.getByLabelText(label));
}

describe('DailyToolsPage', () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem('ongrid-locale', 'zh-CN');
    let uuidSeq = 0;
    vi.spyOn(crypto, 'randomUUID').mockImplementation(() => `11111111-1111-4111-8111-${String(++uuidSeq).padStart(12, '0')}` as `${string}-${string}-${string}-${string}-${string}`);
    server.use(
      http.get('/api/v1/edges', () => HttpResponse.json({ items: edges, total: edges.length })),
      http.get('/api/v1/operator-runs/netns', () => HttpResponse.json({ edge_id: 1, namespaces: ['blue'] })),
    );
  });

  it('初始状态不默认选择 Edge，也不预填探测目标', async () => {
    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    expect(await screen.findByRole('button', { name: '选择 Edge' })).toBeInTheDocument();
    expect(screen.getByLabelText('目标 Host / IP')).toHaveValue('');
    expect(screen.getByRole('button', { name: /^执行$/ })).toBeDisabled();
  });

  it('恢复未清空的运行历史', async () => {
    localStorage.setItem('ongrid-daily-tools-runs-v1', JSON.stringify({
      runs: [{
        id: 'saved-run-1', tool: 'ping', title: 'Ping saved.example', target: 'saved.example', status: 'success', startedAt: '2026-08-16T00:00:00Z',
        results: [{ edgeID: 1, edgeLabel: '#1 edge-001', status: 'success' }],
        logs: [{ id: 'saved-log-1', ts: '2026-08-16T00:00:01Z', stream: 'status', message: 'finished: success' }],
      }],
      captureRuns: [{
        id: 'saved-capture-1', status: 'ready', title: '保存的抓包', target: 'tcp port 443', edgeLabels: ['#1 edge-001'], startedAt: '2026-08-16T00:00:00Z',
        captureIDs: [41], link: '/pages?tab=packets', members: [{ id: 41, edgeLabel: '#1 edge-001', state: 'ready', capturedPackets: 8, capturedBytes: 512 }],
        logs: [],
      }],
    }));

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    expect(await screen.findByText('Ping saved.example')).toBeInTheDocument();
    expect(screen.getByText('保存的抓包')).toBeInTheDocument();
    expect(screen.getByText('8')).toBeInTheDocument();
  });

  it('对已选在线 Edge 并行执行 Ping 并展示矩阵结果', async () => {
    const calls: Array<{ edge_ids?: number[]; command?: string; args?: Record<string, unknown>; timeout_ms?: number }> = [];
    server.use(
      http.post('/api/v1/operator-runs', async ({ request }) => {
        const body = await request.json() as { edge_ids?: number[]; command?: string; args?: Record<string, unknown>; timeout_ms?: number };
        calls.push(body);
        return HttpResponse.json({
          id: 'operator-run-1',
          command: 'ping',
          title: 'Ping 101.34.63.91',
          status: 'running',
          edge_ids: [1, 2],
          started_at: '2026-08-16T00:00:00Z',
        });
      }),
      http.get('/api/v1/operator-runs/operator-run-1/events', () => HttpResponse.text([
        sse('created', { id: 'e1', type: 'created', ts: '2026-08-16T00:00:00Z', run_id: 'operator-run-1', status: 'running', message: '$ ping -c 4 -W 3 101.34.63.91' }),
        sse('edge_running', { id: 'e2', type: 'edge_running', ts: '2026-08-16T00:00:00Z', run_id: 'operator-run-1', edge_id: 1, status: 'running' }),
        sse('stdout', { id: 'e3', type: 'stdout', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-1', edge_id: 1, stream: 'stdout', message: '4 packets transmitted, 4 received, 0% packet loss\nrtt min/avg/max/mdev = 1.000/12.000/30.000/1.000 ms' }),
        sse('edge_done', { id: 'e4', type: 'edge_done', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-1', edge_id: 1, status: 'success', message: 'completed', duration_ms: 12 }),
        sse('stdout', { id: 'e5', type: 'stdout', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-1', edge_id: 2, stream: 'stdout', message: '4 packets transmitted, 4 received, 0% packet loss\nrtt min/avg/max/mdev = 1.000/21.000/30.000/1.000 ms' }),
        sse('edge_done', { id: 'e6', type: 'edge_done', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-1', edge_id: 2, status: 'success', message: 'completed', duration_ms: 21 }),
        sse('done', { id: 'e7', type: 'done', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-1', status: 'success', message: 'finished: success' }),
      ].join(''), { headers: { 'Content-Type': 'text/event-stream' } })),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    await selectEdge();
    await userEvent.click(screen.getByLabelText('选择 #2 edge-002'));
    await userEvent.type(screen.getByLabelText('目标 Host / IP'), '101.34.63.91');
    await userEvent.click(screen.getByRole('button', { name: /^执行$/ }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].edge_ids).toEqual([1, 2]);
    expect(calls[0]).toMatchObject({ command: 'ping', args: { host: '101.34.63.91', count: 4, timeout_ms: 3000 }, timeout_ms: 3000 });
    await waitFor(() => expect(screen.getAllByText('Ping 101.34.63.91').length).toBeGreaterThan(0));
    expect(screen.getAllByText('#1 edge-001').length).toBeGreaterThan(0);
    expect(screen.getAllByText('#2 edge-002').length).toBeGreaterThan(0);
    expect(screen.getByText('12.000ms')).toBeInTheDocument();
    expect(screen.getByText('21.000ms')).toBeInTheDocument();
    expect(screen.getAllByText(/\$ ping -c 4 -W 3 101\.34\.63\.91/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/4 packets transmitted/).length).toBeGreaterThanOrEqual(2);
  });

  it('执行日志不会给 stdout 空行重复添加 Edge 前缀', async () => {
    server.use(
      http.get('/api/v1/edges', () => HttpResponse.json({ items: [edges[0]], total: 1 })),
      http.post('/api/v1/operator-runs', async () => HttpResponse.json({
        id: 'operator-run-blank-line',
        command: 'ping',
        title: 'Ping 101.34.63.91',
        status: 'running',
        edge_ids: [1],
        started_at: '2026-08-16T00:00:00Z',
      })),
      http.get('/api/v1/operator-runs/operator-run-blank-line/events', () => HttpResponse.text([
        sse('stdout', { id: 'e1', type: 'stdout', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-blank-line', edge_id: 1, stream: 'stdout', message: 'line one\n\nline two\n' }),
        sse('done', { id: 'e2', type: 'done', ts: '2026-08-16T00:00:02Z', run_id: 'operator-run-blank-line', status: 'success', message: 'finished: success' }),
      ].join(''), { headers: { 'Content-Type': 'text/event-stream' } })),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    await selectEdge();
    await userEvent.type(screen.getByLabelText('目标 Host / IP'), '101.34.63.91');
    await userEvent.click(screen.getByRole('button', { name: /^执行$/ }));

    await waitFor(() => expect(screen.getAllByText('Ping 101.34.63.91').length).toBeGreaterThan(0));
    const terminalText = screen.getByTestId('xterminal').textContent ?? '';
    expect((terminalText.match(/\[#1 edge-001\]/g) ?? [])).toHaveLength(2);
    expect(terminalText).toContain('line one');
    expect(terminalText).toContain('line two');
  });

  it('TCP 工具会把 Host 输入中的端口拆分为 host 和 port 参数', async () => {
    const calls: Array<{ command?: string; args?: Record<string, unknown> }> = [];
    server.use(
      http.get('/api/v1/edges', () => HttpResponse.json({ items: [edges[0]], total: 1 })),
      http.post('/api/v1/operator-runs', async ({ request }) => {
        const body = await request.json() as { command?: string; args?: Record<string, unknown> };
        calls.push(body);
        return HttpResponse.json({
          id: 'operator-run-tcp',
          command: 'tcp',
          title: 'TCP 101.34.63.91:443',
          status: 'running',
          edge_ids: [1],
          started_at: '2026-08-16T00:00:00Z',
        });
      }),
      http.get('/api/v1/operator-runs/operator-run-tcp/events', () => HttpResponse.text([
        sse('done', { id: 'e1', type: 'done', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-tcp', status: 'success', message: 'finished: success' }),
      ].join(''), { headers: { 'Content-Type': 'text/event-stream' } })),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    await selectEdge();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '工具' }), 'tcp');
    const hostInput = screen.getByLabelText('目标 Host / IP');
    await userEvent.type(hostInput, '101.34.63.91:443');
    await userEvent.click(screen.getByRole('button', { name: /^执行$/ }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({ command: 'tcp', args: { host: '101.34.63.91', port: 443 } });
  });

  it('HTTP 工具会传递跳过 TLS 和网络命名空间参数', async () => {
    const calls: Array<{ command?: string; args?: Record<string, unknown> }> = [];
    server.use(
      http.get('/api/v1/edges', () => HttpResponse.json({ items: [edges[0]], total: 1 })),
      http.post('/api/v1/operator-runs', async ({ request }) => {
        const body = await request.json() as { command?: string; args?: Record<string, unknown> };
        calls.push(body);
        return HttpResponse.json({
          id: 'operator-run-http',
          command: 'http',
          title: 'HTTP https://101.34.63.91/healthz',
          status: 'running',
          edge_ids: [1],
          started_at: '2026-08-16T00:00:00Z',
        });
      }),
      http.get('/api/v1/operator-runs/operator-run-http/events', () => HttpResponse.text([
        sse('done', { id: 'e1', type: 'done', ts: '2026-08-16T00:00:01Z', run_id: 'operator-run-http', status: 'success', message: 'finished: success' }),
      ].join(''), { headers: { 'Content-Type': 'text/event-stream' } })),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    await selectEdge();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '工具' }), 'http');
    await userEvent.type(screen.getByLabelText('URL'), 'https://101.34.63.91/healthz');
    await userEvent.click(screen.getByLabelText('跳过 TLS 检测'));
    await screen.findByRole('option', { name: 'blue' });
    await userEvent.selectOptions(await screen.findByLabelText('网络命名空间'), 'blue');
    await userEvent.click(screen.getByRole('button', { name: /^执行$/ }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({
      command: 'http',
      args: {
        url: 'https://101.34.63.91/healthz',
        method: 'HEAD',
        timeout_ms: 5000,
        skip_tls: true,
        namespace: 'blue',
      },
    });
    await waitFor(() => expect(screen.getByTestId('xterminal').textContent).toContain('ip netns exec blue curl -I -X HEAD --max-time 5 -k https://101.34.63.91/healthz'));
  });

  it('抓包作为快捷任务展示并支持停止', async () => {
    const cancelCalls: string[] = [];
    const captureCalls: Array<{ targets?: Array<{ network_namespace?: string }> }> = [];
    server.use(
      http.post('/api/v1/packet-capture-sessions', async ({ request }) => {
        captureCalls.push(await request.json() as { targets?: Array<{ network_namespace?: string }> });
        return HttpResponse.json({
        session: {
          id: 'pcap-session-1',
          source: 'api',
          pcap_count: 2,
          state: 'collecting',
          title: '日常工具抓包',
          description: '',
          canonical_filter: 'tcp and port 443',
          duration_seconds: 60,
          planned_start_at: '2026-08-16T00:00:00Z',
          clock_quality: 'unknown',
          created_at: '2026-08-16T00:00:00Z',
          updated_at: '2026-08-16T00:00:00Z',
        },
        captures: [{ id: 41 }, { id: 42 }],
        member_errors: [],
        });
      }),
      http.post('/api/v1/packet-capture-sessions/pcap-session-1/stop', () => {
        cancelCalls.push('session');
        return HttpResponse.json({ id: 'pcap-session-1', state: 'collecting' });
      }),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    await selectEdge();
    await userEvent.selectOptions(await screen.findByLabelText('网络命名空间'), 'blue');
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '工具' }), 'capture');
    await userEvent.click(screen.getByRole('button', { name: '开始' }));

    await waitFor(() => expect(captureCalls).toHaveLength(1));
    expect(captureCalls[0].targets).toEqual([{ device_id: 11, interface: 'eth0', network_namespace: 'blue' }]);

    const panel = await screen.findByText('抓包快捷任务');
    expect(panel).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '打开数据包' })).toHaveAttribute('href', '/artifacts/packet-sessions/pcap-session-1');
    expect(screen.getAllByText(/\$ ip netns exec blue tcpdump -U -n -q -i eth0 -s 1514 -w <artifact>\.pcap/).length).toBeGreaterThan(0);
    expect(within(panel.closest('section') as HTMLElement).getByRole('button', { name: '取消并丢弃' })).toBeInTheDocument();

    await userEvent.click(within(panel.closest('section') as HTMLElement).getByRole('button', { name: '停止并保存' }));
    await waitFor(() => expect(cancelCalls).toEqual(['session']));
    expect(screen.getAllByText(/已发送停止并保存请求，正在上传已有数据包。/).length).toBeGreaterThan(0);
  });

  it('抓包快捷任务会轮询成员状态并实时更新卡片', async () => {
    server.use(
      http.post('/api/v1/packet-capture-sessions', async () => HttpResponse.json({
        session: {
          id: 'pcap-session-1',
          source: 'api',
          pcap_count: 2,
          state: 'collecting',
          title: '日常工具抓包',
          description: '',
          canonical_filter: 'tcp and port 443',
          duration_seconds: 60,
          planned_start_at: '2026-08-16T00:00:00Z',
          clock_quality: 'unknown',
          created_at: '2026-08-16T00:00:00Z',
          updated_at: '2026-08-16T00:00:00Z',
        },
        captures: [
          { id: 41, edge_id: 1, device_id: 11, state: 'capturing', captured_packets: 0, captured_bytes: 0 },
          { id: 42, edge_id: 2, device_id: 12, state: 'capturing', captured_packets: 0, captured_bytes: 0 },
        ],
        member_errors: [],
      })),
      http.post('/api/v1/packet-capture-sessions/pcap-session-1/refresh', () => HttpResponse.json({
        session: {
          id: 'pcap-session-1',
          source: 'api',
          pcap_count: 2,
          state: 'ready',
          title: '日常工具抓包',
          description: '',
          canonical_filter: 'tcp and port 443',
          duration_seconds: 60,
          planned_start_at: '2026-08-16T00:00:00Z',
          clock_quality: 'unknown',
          created_at: '2026-08-16T00:00:00Z',
          updated_at: '2026-08-16T00:00:02Z',
        },
        captures: [
          { id: 41, edge_id: 1, device_id: 11, state: 'ready', captured_packets: 12, captured_bytes: 2048 },
          { id: 42, edge_id: 2, device_id: 12, state: 'ready', captured_packets: 7, captured_bytes: 1024 },
        ],
      })),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    await selectEdge();
    await userEvent.click(screen.getByLabelText('选择 #2 edge-002'));
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '工具' }), 'capture');
    await userEvent.click(screen.getByRole('button', { name: '开始' }));

    expect(await screen.findByText(/capture 41/)).toBeInTheDocument();

    await waitFor(() => expect(screen.getAllByText('ready').length).toBeGreaterThanOrEqual(2), { timeout: 3000 });
    expect(screen.getByText('12')).toBeInTheDocument();
    expect(screen.getByText('2.0 KB')).toBeInTheDocument();
    expect(screen.getAllByText(/capture_id=41 capturing -> ready/).length).toBeGreaterThan(0);
  });

  it('抓包轮询未完成时不会为同一任务发起重叠刷新', async () => {
    let refreshCalls = 0;
    let releaseRefresh!: () => void;
    const refreshGate = new Promise<void>((resolve) => {
      releaseRefresh = resolve;
    });
    localStorage.setItem('ongrid-daily-tools-runs-v1', JSON.stringify({
      runs: [],
      captureRuns: [{
        id: 'saved-capture-running',
        sessionID: 'pcap-session-in-flight',
        status: 'capturing',
        title: '并发轮询测试',
        target: 'tcp',
        edgeLabels: ['#1 edge-001'],
        startedAt: '2026-08-16T00:00:00Z',
        captureIDs: [41],
        link: '/artifacts/packet-sessions/pcap-session-in-flight',
        members: [{ id: 41, edgeLabel: '#1 edge-001', state: 'capturing' }],
        logs: [],
      }],
    }));
    server.use(
      http.post('/api/v1/packet-capture-sessions/pcap-session-in-flight/refresh', async () => {
        refreshCalls++;
        await refreshGate;
        return HttpResponse.json({
          session: { id: 'pcap-session-in-flight', state: 'ready' },
          captures: [{ id: 41, edge_id: 1, device_id: 11, state: 'ready', captured_packets: 1, captured_bytes: 64 }],
        });
      }),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    expect(await screen.findByText('并发轮询测试')).toBeInTheDocument();
    await waitFor(() => expect(refreshCalls).toBe(1), { timeout: 2500 });
    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 1700));
    });
    expect(refreshCalls).toBe(1);
    await act(async () => {
      releaseRefresh();
    });
    await waitFor(() => expect(screen.getByText('ready')).toBeInTheDocument());
  });

  it('抓包会展示成员创建失败原因', async () => {
    server.use(
      http.post('/api/v1/packet-capture-sessions', async () => HttpResponse.json({
        session: {
          id: 'pcap-session-empty',
          source: 'api',
          pcap_count: 0,
          state: 'failed',
          title: '日常工具抓包',
          description: '',
          canonical_filter: 'tcp and port 443',
          duration_seconds: 60,
          planned_start_at: '2026-08-16T00:00:00Z',
          clock_quality: 'unknown',
          created_at: '2026-08-16T00:00:00Z',
          updated_at: '2026-08-16T00:00:00Z',
        },
        captures: [],
        member_errors: ['device 11: packet capture: unsupported filter "tcp or udp"'],
      })),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);

    await selectEdge();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '工具' }), 'capture');
    await userEvent.click(screen.getByRole('button', { name: '开始' }));

    expect((await screen.findAllByText(/device 11: packet capture/)).length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: '停止' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: '打开数据包' })).toHaveAttribute('href', '/artifacts/packet-sessions/pcap-session-empty');
  });

  it('通过 otelcol-contrib pprof receiver 采集应用 profiling 并自动展示', async () => {
    let saved: { enabled?: boolean; spec?: Record<string, unknown> } = {};
    let readsAfterStart = 0;
    let downloaded = false;
    const flamegraphResponse = {
      flamebearer: {
        names: ['total', 'ongrid-edge.main', 'db.query'],
        levels: [[0, 100, 0, 0], [0, 100, 35, 1], [0, 65, 65, 2]],
        numTicks: 100,
        maxSelf: 65,
      },
      metadata: { format: 'single' as const, units: 'samples' },
    };
    server.use(
      http.get('/api/v1/edges/1/plugins', () => HttpResponse.json({ items: [{
        plugin_name: 'profiles',
        enabled: saved.enabled === true,
        spec: saved.spec ?? {},
        ...(saved.enabled ? { health: readsAfterStart++ === 0
          ? { state: 'crashed', reported_at: new Date(Date.now() - 60_000).toISOString() }
          : { state: 'running', started_at: new Date(Date.now() - 2000).toISOString(), reported_at: new Date().toISOString() } } : {}),
      }] })),
      http.put('/api/v1/edges/1/plugins/profiles', async ({ request }) => {
        saved = await request.json() as typeof saved;
        if (saved.enabled && saved.spec) saved.spec = { ...saved.spec, expires_at: new Date(Date.now() + 1500).toISOString() };
        readsAfterStart = 0;
        return HttpResponse.json({
          plugin_name: 'profiles',
          enabled: saved.enabled,
          spec: saved.spec,
          health: { state: 'crashed', reported_at: new Date(Date.now() - 60_000).toISOString() },
        });
      }),
      http.get('/api/v1/profiles/flamegraph', ({ request }) => {
        const url = new URL(request.url);
        expect(url.searchParams.get('service')).toBe('ongrid-edge');
        expect(url.searchParams.get('kind')).toBe('heap');
        expect(url.searchParams.get('range')).toBe('15m');
        return HttpResponse.json(flamegraphResponse);
      }),
      http.get('/api/v1/profiles/download', ({ request }) => {
        const url = new URL(request.url);
        expect(url.searchParams.get('service')).toBe('ongrid-edge');
        expect(url.searchParams.get('kind')).toBe('heap');
        expect(url.searchParams.get('range')).toBe('15m');
        downloaded = true;
        return new HttpResponse(new Uint8Array([1, 2, 3]), { headers: { 'Content-Type': 'application/octet-stream' } });
      }),
    );

    render(<MemoryRouter><DailyToolsPage /></MemoryRouter>);
    const toolSelect = screen.getByRole('combobox', { name: '工具' });
    expect(within(toolSelect).getByRole('option', { name: 'Ping' })).toBeInTheDocument();
    expect(within(toolSelect).queryByRole('option', { name: '应用性能分析' })).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '工具类型' }), 'performance');
    expect(within(toolSelect).getByRole('option', { name: '应用性能分析' })).toBeInTheDocument();
    await selectEdge();
    const profileType = await screen.findByRole('combobox', { name: '分析类型' });
    expect(profileType).toHaveValue('heap');
    expect(screen.queryByRole('combobox', { name: '分析进程' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('service.name')).not.toBeInTheDocument();
    expect(screen.getByLabelText('采集 URL')).toHaveValue('http://127.0.0.1:6060/debug/pprof/heap');
    expect(screen.getByText(/默认采集当前 Edge 的 ongrid-edge/)).toBeInTheDocument();

    await userEvent.selectOptions(profileType, 'cpu');
    expect(screen.queryByRole('combobox', { name: '分析进程' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('采集 URL')).toHaveValue('http://127.0.0.1:6060/debug/pprof/profile?seconds=30');
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '采样时长' }), '60');
    expect(screen.getByLabelText('采集 URL')).toHaveValue('http://127.0.0.1:6060/debug/pprof/profile?seconds=60');
    await userEvent.selectOptions(profileType, 'heap');
    await userEvent.selectOptions(screen.getByRole('combobox', { name: '采样时长' }), '30');

    expect(screen.queryByLabelText('采样频率（Hz）')).not.toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: '采样时长' })).toHaveValue('30');
    await userEvent.click(screen.getByRole('button', { name: '开始采样' }));
    await waitFor(() => expect(saved.enabled).toBe(true));
    await waitFor(() => expect(screen.getAllByText('采样中')).not.toHaveLength(0), { timeout: 3000 });
    expect(saved.spec).toMatchObject({
      mode: 'pprof',
      duration_seconds: 30,
      runtime_target: {
        url: 'http://127.0.0.1:6060/debug/pprof/heap',
        profile_type: 'heap',
        service_name: 'ongrid-edge',
        collection_interval_seconds: 10,
      },
    });

    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: vi.fn(() => 'blob:profile') });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() });
    const clickDownload = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    await userEvent.click(screen.getByRole('button', { name: '下载数据' }));
    await waitFor(() => expect(downloaded).toBe(true));
    clickDownload.mockRestore();

    const viewer = await screen.findByRole('region', { name: '应用性能分析图' }, { timeout: 3000 });
    const frame = await within(viewer).findByRole('button', { name: /ongrid-edge\.main/ });
    expect(frame).not.toHaveAttribute('title');
    await userEvent.hover(frame);
    expect(within(viewer).getByRole('tooltip')).toHaveTextContent('ongrid-edge.main');
    expect(within(viewer).getByRole('tooltip')).toHaveTextContent('Total: 100 samples (100%)');
    expect(within(viewer).getByRole('tooltip')).toHaveTextContent('Self: 35 samples');
    await userEvent.unhover(frame);
    expect(within(viewer).queryByRole('tooltip')).not.toBeInTheDocument();
    let refreshStarted!: () => void;
    let releaseRefresh!: () => void;
    const refreshRequest = new Promise<void>((resolve) => { refreshStarted = resolve; });
    const refreshResponse = new Promise<void>((resolve) => { releaseRefresh = resolve; });
    server.use(http.get('/api/v1/profiles/flamegraph', async () => {
      refreshStarted();
      await refreshResponse;
      return HttpResponse.json(flamegraphResponse);
    }));
    await userEvent.click(within(viewer).getByRole('button', { name: '刷新' }));
    await refreshRequest;
    expect(frame).toBeInTheDocument();
    await act(async () => releaseRefresh());
    await waitFor(() => expect(within(viewer).getByRole('button', { name: '刷新' })).toBeEnabled());
    expect(within(viewer).queryByRole('combobox', { name: '火焰图时间范围' })).not.toBeInTheDocument();
    expect(within(viewer).getByRole('button', { name: '火焰图' })).toHaveAttribute('aria-pressed', 'true');
    await userEvent.click(within(viewer).getByRole('button', { name: '冰柱图' }));
    expect(within(viewer).getByRole('button', { name: '冰柱图' })).toHaveAttribute('aria-pressed', 'true');
    expect(within(viewer).getByRole('group', { name: '冰柱图视图' })).toHaveClass('flex-col');
    expect(within(viewer).getByRole('button', { name: '调用图' })).toBeInTheDocument();
    await userEvent.click(within(viewer).getByRole('button', { name: '调用图' }));
    expect(within(viewer).getByRole('button', { name: '调用图' })).toHaveAttribute('aria-pressed', 'true');
    expect(within(viewer).getByText('展示最热 3 个函数，连线宽度表示累计占用')).toBeInTheDocument();
    expect(within(viewer).getByText('Self 35 samples (35.00%)')).toBeInTheDocument();
    expect(within(viewer).getAllByText('Total 100 samples (100%)')).not.toHaveLength(0);
    await userEvent.click(within(viewer).getByRole('button', { name: '全屏' }));
    expect(viewer).toHaveClass('fixed', 'inset-0');
    expect(within(viewer).getByRole('button', { name: '退出全屏' })).toBeInTheDocument();
    expect(within(viewer).queryByTitle('应用性能火焰图')).not.toBeInTheDocument();
  });
});
