import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { server } from '@/test/msw-server';
import FlowsPage from './Flows';
import McpPage from './Mcp';

const auth = vi.hoisted(() => ({ role: 'admin' }));
vi.mock('@/store/auth', () => ({
  useAuth: Object.assign(<T,>(selector: (s: typeof auth) => T) => selector(auth), { getState: () => ({ logout: () => {} }) }),
  getToken: () => null, getRefreshToken: () => null,
}));

const cases = [
  { name: 'workflow', Page: FlowsPage, url: '/api/v1/flows', title: '还没有工作流', action: '新建工作流', dialog: '新建工作流', filtered: '无匹配的工作流', item: { id: 1, name: 'sample', description: '', enabled: true, version: 1, node_count: 0, created_at: new Date().toISOString(), updated_at: new Date().toISOString() } },
  { name: 'MCP', Page: McpPage, url: '/api/v1/mcp/servers', title: '还没有注册任何 MCP 服务', action: '接入 MCP', dialog: '新建 MCP 服务', filtered: '无匹配的服务', item: { id: 1, name: 'sample', transport: 'http', endpoint: 'https://example.com/mcp', enabled: true, tools_cache: '[]' } },
];

beforeEach(() => {
  auth.role = 'admin';
  localStorage.setItem('ongrid-locale', 'zh-CN');
  server.use(http.get('/api/v1/secrets', () => HttpResponse.json({ items: [] })));
});

for (const c of cases) describe(`${c.name} empty actions`, () => {
  it('opens the existing editor from the empty card and cancels without saving', async () => {
    const save = vi.fn();
    server.use(http.get(c.url, () => HttpResponse.json({ items: [], total: 0 })), http.post(c.url, () => { save(); return HttpResponse.json({}); }));
    render(<MemoryRouter><c.Page /></MemoryRouter>);
    const title = await screen.findByText(c.title);
    await userEvent.click(within(title.parentElement!).getByRole('button', { name: c.action }));
    const dialog = await screen.findByRole('dialog', { name: c.dialog });
    await userEvent.click(within(dialog).getByRole('button', { name: '取消' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(save).not.toHaveBeenCalled();
  });
  it('hides creation for viewers', async () => {
    auth.role = 'viewer';
    server.use(http.get(c.url, () => HttpResponse.json({ items: [], total: 0 })));
    render(<MemoryRouter><c.Page /></MemoryRouter>);
    await screen.findByText(c.title);
    expect(screen.queryByRole('button', { name: c.action })).not.toBeInTheDocument();
  });
  it('clears an unmatched search and restores existing rows', async () => {
    server.use(http.get(c.url, () => HttpResponse.json({ items: [c.item], total: 1 })));
    render(<MemoryRouter><c.Page /></MemoryRouter>);
    await screen.findByText('sample');
    await userEvent.type(screen.getByRole('searchbox'), 'unmatched');
    await screen.findByText(c.filtered);
    await userEvent.click(screen.getByRole('button', { name: '清除筛选' }));
    expect(screen.getByRole('searchbox')).toHaveValue('');
    expect(screen.getByText('sample')).toBeInTheDocument();
  });
  it('does not present a load error as an empty collection', async () => {
    server.use(http.get(c.url, () => HttpResponse.json({ message: 'Unavailable' }, { status: 500 })));
    render(<MemoryRouter><c.Page /></MemoryRouter>);
    await screen.findByText(/Unavailable/);
    expect(screen.queryByText(c.title)).not.toBeInTheDocument();
  });
});
