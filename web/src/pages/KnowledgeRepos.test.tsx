import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
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
  it('distinguishes fetched history from pending counts and shows background syncing', async () => {
    server.use(
      http.get('/api/v1/knowledge/repos', () => HttpResponse.json({ items: [
        { id: 1, url: 'https://example/full.git', branch: 'main', file_count: 0, commit_count: 321, tag_count: 0, branch_count: 2, source_synced_at: '2026-09-09T08:00:00Z', history_complete: true },
        { id: 2, url: 'https://example/pending.git', branch: 'main', file_count: 0, syncing: true },
      ] })),
      http.get('/api/v1/knowledge/ssh-identities', () => HttpResponse.json({ items: [] })),
    );
    render(<MemoryRouter><KnowledgeRepos /></MemoryRouter>);
    expect(await screen.findByText('Commit 321')).toBeInTheDocument();
    expect(screen.getByText('Tag 0')).toBeInTheDocument();
    expect(screen.getByText('Commit —')).toBeInTheDocument();
    expect(screen.getByText('完整历史已同步')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '同步中…' })).toBeDisabled();
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
  it('keeps failed verification input and edits the existing repository without changing its URL', async () => {
    const repo = { id: 7, url: 'https://example/widget.git', branch: 'main', description: 'docs', file_count: 2 };
    let saved = false;
    server.use(
      http.get('/api/v1/knowledge/repos', () => HttpResponse.json({ items: [repo] })),
      http.get('/api/v1/knowledge/ssh-identities', () => HttpResponse.json({ items: [] })),
      http.patch('/api/v1/knowledge/repos/7', async ({ request }) => {
        const body = await request.json() as { branch: string; description: string };
        expect(body).not.toHaveProperty('url');
        if (body.branch === 'missing') return HttpResponse.json({ message: 'branch or tag missing not found' }, { status: 400 });
        repo.branch = body.branch;
        saved = true;
        return HttpResponse.json(repo);
      }),
    );
    render(<MemoryRouter><KnowledgeRepos /></MemoryRouter>);
    fireEvent.click(await screen.findByRole('button', { name: '编辑' }));
    expect(screen.getByLabelText('仓库 URL *')).toBeDisabled();
    const branch = screen.getByLabelText('文档索引分支或 Tag');
    fireEvent.change(branch, { target: { value: 'missing' } });
    fireEvent.click(screen.getByRole('button', { name: '验证并保存' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('branch or tag missing not found');
    expect(branch).toHaveValue('missing');
    expect(saved).toBe(false);
    fireEvent.change(branch, { target: { value: 'v2' } });
    fireEvent.click(screen.getByRole('button', { name: '验证并保存' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(saved).toBe(true);
    expect(screen.getByText('v2')).toBeInTheDocument();
  });
  it('verifies new repositories before closing the create dialog', async () => {
    server.use(
      http.get('/api/v1/knowledge/repos', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/knowledge/ssh-identities', () => HttpResponse.json({ items: [] })),
      http.post('/api/v1/knowledge/repos', () => HttpResponse.json({ message: 'repository access denied' }, { status: 400 })),
    );
    render(<MemoryRouter><KnowledgeRepos /></MemoryRouter>);
    fireEvent.click((await screen.findAllByRole('button', { name: '添加仓库' }))[0]);
    fireEvent.change(screen.getByLabelText('仓库 URL *'), { target: { value: 'https://example/private.git' } });
    fireEvent.click(screen.getByRole('button', { name: '验证并保存' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('repository access denied');
    expect(screen.getByLabelText('仓库 URL *')).toHaveValue('https://example/private.git');
    expect(screen.queryByText('保存（保存后再点同步）')).not.toBeInTheDocument();
  });

});
