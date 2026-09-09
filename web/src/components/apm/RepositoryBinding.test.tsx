import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';
import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { RepositoryBindingButton } from './RepositoryBinding';

const identity = { service_name: 'orders', service_namespace: 'trade', environment: 'prod' };
const url = 'ssh://git@example/apm-demo.git';

describe('Service repository binding', () => {
  beforeEach(() => localStorage.setItem('ongrid-locale', 'zh-CN'));
  it('saves the full service scope, reloads persisted values and unbinds', async () => {
    let saved: Record<string, unknown> | null = null;
    server.use(
      http.get('/api/v1/knowledge/repos', () => HttpResponse.json({ items: [{ id: 3, url, branch: 'main' }] })),
      http.get('/api/v1/apm/repository-binding', () => HttpResponse.json({ data: saved })),
      http.put('/api/v1/apm/repository-binding', async ({ request }) => {
        const scope = new URL(request.url).searchParams;
        for (const [key, value] of Object.entries(identity)) expect(scope.get(key)).toBe(value);
        saved = { ...(await request.json() as object), identity, repo_url: url };
        return HttpResponse.json({ data: saved });
      }),
      http.delete('/api/v1/apm/repository-binding', () => { saved = null; return HttpResponse.json({ data: null }); }),
    );
    render(<MemoryRouter><RepositoryBindingButton identity={identity} canEdit /></MemoryRouter>);
    fireEvent.click(screen.getByRole('button', { name: '绑定仓库' }));
    await selectOption(await screen.findByRole('combobox', { name: '代码仓库' }), 'apm-demo');
    fireEvent.change(screen.getByLabelText('源码目录'), { target: { value: 'services/orders' } });
    fireEvent.change(screen.getByLabelText('版本 Tag 规则'), { target: { value: 'v{version}' } });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(saved).toMatchObject({ repo_id: '3', source_directory: 'services/orders', tag_pattern: 'v{version}' });
    fireEvent.click(await screen.findByRole('button', { name: 'apm-demo' }));
    expect(await screen.findByDisplayValue('services/orders')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '解除绑定' }));
    await waitFor(() => expect(saved).toBeNull());
  });
  it('does not allow a failed read to overwrite an existing binding', async () => {
    server.use(
      http.get('/api/v1/knowledge/repos', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/apm/repository-binding', () => HttpResponse.json({ message: 'unavailable' }, { status: 503 })),
    );
    render(<MemoryRouter><RepositoryBindingButton identity={identity} canEdit /></MemoryRouter>);
    fireEvent.click(screen.getByRole('button', { name: '绑定仓库' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('unavailable');
    expect(screen.getByRole('button', { name: '保存' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: '解除绑定' })).not.toBeInTheDocument();
  });
});
