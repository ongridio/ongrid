import { act, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';
import { server } from '@/test/msw-server';
import { repositoryName } from '@/api/knowledge';
import KnowledgeRepos from './KnowledgeRepos';

describe('Repository cards and SSH summary', () => {
  beforeEach(() => localStorage.setItem('ongrid-locale', 'zh-CN'));
  it('loads credential count while collapsed and shows a short repository heading', async () => {
    let release!: () => void;
    const ready = new Promise<void>((resolve) => { release = resolve; });
    const url = 'ssh://git@ongrid-demo-git:2222/demo-admin/apm-demo.git';
    server.use(
      http.get('/api/v1/knowledge/repos', () => HttpResponse.json({ items: [{ id: 1, url, branch: '1.1.0-demo', file_count: 4 }] })),
      http.get('/api/v1/knowledge/ssh-identities', async () => { await ready; return HttpResponse.json({ items: [{ id: 1, name: 'demo-readonly', hosts: ['ongrid-demo-git'] }] }); }),
    );
    render(<MemoryRouter><KnowledgeRepos /></MemoryRouter>);
    expect(screen.queryByText('未配置')).not.toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'apm-demo' })).toBeInTheDocument();
    expect(screen.getByText(url)).toBeInTheDocument();
    await act(async () => release());
    expect(await screen.findByText('已配置 1 条')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /凭证 · SSH key/ })).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(screen.getByRole('button', { name: '刷新' }));
    expect(await screen.findByText('已配置 1 条')).toBeInTheDocument();
    for (const input of ['https://example/org/apm-demo.git', 'git@example:org/apm-demo.git', 'ssh://git@example:2222/org/apm-demo.git/']) expect(repositoryName(input)).toBe('apm-demo');
  });
  it('shows a query failure instead of reporting missing credentials', async () => {
    server.use(
      http.get('/api/v1/knowledge/repos', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/knowledge/ssh-identities', () => HttpResponse.json({ message: 'failed' }, { status: 503 })),
    );
    render(<MemoryRouter><KnowledgeRepos /></MemoryRouter>);
    expect(await screen.findByText('查询失败')).toBeInTheDocument();
    expect(screen.queryByText('未配置')).not.toBeInTheDocument();
  });
});
