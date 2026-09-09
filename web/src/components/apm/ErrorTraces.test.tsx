import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';
import { server } from '@/test/msw-server';
import { ErrorTraces } from './ErrorTraces';

const id = '0123456789abcdef0123456789abcdef';
const params = new URLSearchParams({ service_name: 'orders', service_namespace: 'trade', environment: 'prod', service_version: 'v2', instance_id: 'pod-2', protocol: 'all', start: '2026-09-09T00:00:00Z', end: '2026-09-09T01:00:00Z' });
function ChatContext() { return <pre>{JSON.stringify(useLocation().state)}</pre>; }

describe('APM error traces', () => {
  beforeEach(() => localStorage.setItem('ongrid-locale', 'zh-CN'));
  it('scopes recent errors and starts a trace/log/performance investigation only on click', async () => {
    let query: URLSearchParams | undefined;
    let sessions = 0;
 let sessionInput: Record<string, unknown> = {};
    server.use(
      http.get('/api/v1/apm/repository-binding', () => HttpResponse.json({ data: { repo_id: '3', repo_url: 'ssh://git@example/orders.git', source_directory: 'services/orders', tag_pattern: 'v{version}', synced_ref: 'old-index-tag' } })),
      http.get('/api/v1/traces/search', ({ request }) => {
        query = new URL(request.url).searchParams;
        return HttpResponse.json({ traces: [{ traceID: id, rootTraceName: 'GET /orders', durationMs: 10, startTimeUnixNano: '1788913800000000000' }] });
      }),
      http.post('/api/v1/chat/sessions', async ({ request }) => { sessions++; sessionInput = await request.json() as Record<string, unknown>; return HttpResponse.json({ id: 'analysis-1' }); }),
    );
    render(<MemoryRouter><Routes><Route path="/" element={<ErrorTraces params={params} refresh={0} />} /><Route path="/chat/:id" element={<ChatContext />} /></Routes></MemoryRouter>);
    expect(await screen.findByText('GET /orders')).toBeInTheDocument();
    for (const value of ['resource.service.name = "orders"', 'resource.service.namespace = "trade"', 'resource.deployment.environment.name = "prod"', 'resource.service.version = "v2"', 'resource.service.instance.id = "pod-2"', 'status = error', 'kind = server', 'most_recent=true']) expect(query?.get('q')).toContain(value);
    expect(query?.get('limit')).toBe('10');
    expect(query?.get('start')).toBe(params.get('start'));
    expect(sessions).toBe(0);
    expect(screen.getByRole('link', { name: `${id.slice(0, 12)}…` })).toHaveAttribute('href', expect.stringContaining(`/traces/${id}?`));
    fireEvent.click(screen.getByRole('button', { name: 'AI 分析' }));
    await waitFor(() => expect(screen.getByText(/initialPrompt/)).toBeInTheDocument());
    const prompt = screen.getByText(/initialPrompt/).textContent!;
    for (const value of [id, 'pod-2', 'v2', 'query_traceql', 'query_logql', 'query_promql', '最近 15 分钟', '只读分析', 'repository_binding', 'services/orders', 'v{version}', 'revision', 'commit_sha', '真实行号', '相同 operation', '500 总量', '无法判断']) expect(prompt).toContain(value);
    expect(prompt).not.toContain('old-index-tag');
    expect(prompt).not.toContain('synced_ref');
    expect(sessions).toBe(1);
 expect(sessionInput.apm_source).toEqual({ trace_id: id, service_name: 'orders', service_namespace: 'trade', environment: 'prod', service_version: 'v2', instance_id: 'pod-2' });
  });
  it('distinguishes query failure from absence and lets the user retry', async () => {
    server.use(http.get('/api/v1/traces/search', () => HttpResponse.json({ message: 'Tempo unavailable' }, { status: 503 })));
    render(<MemoryRouter><ErrorTraces params={params} refresh={0} /></MemoryRouter>);
    expect(await screen.findByRole('alert')).toHaveTextContent('Tempo unavailable');
    expect(screen.queryByText('当前范围未观测到错误 Trace')).not.toBeInTheDocument();
    server.use(http.get('/api/v1/traces/search', () => HttpResponse.json({ traces: null })));
    fireEvent.click(screen.getByRole('button', { name: '重试' }));
    expect(await screen.findByText('当前范围未观测到错误 Trace')).toBeInTheDocument();
  });
});
