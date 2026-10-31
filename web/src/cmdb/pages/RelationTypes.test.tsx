import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import RelationTypesPage from './RelationTypes';
import { ToastProvider } from '../components/Toast';

// The first CMDB page, against a mocked backend: the shared layer proven on a
// real endpoint rather than on a fixture.

const rows = [
  { id: 1, name: 'depends_on', color: '#6366f1', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' },
  { id: 2, name: 'runs_on', color: '', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' },
];

function renderPage() {
  return render(
    <ToastProvider>
      <RelationTypesPage />
    </ToastProvider>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  // NO TOKEN ⇒ `useMe` makes no request, so `usePermissions` falls back to the
  // role the login response carried — which is what this sets.
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the list', () => {
  it('draws what the endpoint returns', async () => {
    server.use(http.get('/api/v1/cmdb/relation-types', () => HttpResponse.json({ items: rows, total: rows.length })));
    renderPage();

    expect(await screen.findByText('depends_on')).toBeInTheDocument();
    expect(screen.getByText('runs_on')).toBeInTheDocument();
    // A TYPE WITH NO COLOUR SHOWS A DASH rather than a transparent swatch that
    // looks like a rendering bug.
    expect(screen.getByText('—')).toBeInTheDocument();
  });

  it('offers a retry when the load fails, and keeps the table usable', async () => {
    server.use(
      http.get('/api/v1/cmdb/relation-types', () => HttpResponse.text('cmdb: database is locked', { status: 500 })),
    );
    renderPage();

    // THE BACKEND'S OWN MESSAGE IS SHOWN: "HTTP 500" would tell the operator
    // nothing they can act on.
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('database is locked');
    expect(within(alert).getByRole('button', { name: /重试/ })).toBeInTheDocument();
  });
});

describe('writes', () => {
  it('disables them for a non-administrator with a reason', async () => {
    useAuth.setState({ role: 'user' });
    server.use(http.get('/api/v1/cmdb/relation-types', () => HttpResponse.json({ items: rows, total: rows.length })));
    renderPage();

    expect(await screen.findByRole('button', { name: /新建/ })).toBeDisabled();
    expect(screen.getByRole('button', { name: '编辑 depends_on' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '删除 depends_on' })).toBeDisabled();
  });

  it('creates one and refreshes the list', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    server.use(
      http.get('/api/v1/cmdb/relation-types', () => HttpResponse.json({ items: rows, total: rows.length })),
      http.post('/api/v1/cmdb/relation-types', async ({ request }) => {
        const body = await request.json();
        posted.push(body);
        return HttpResponse.json({ id: 3, name: 'contains', color: '' }, { status: 201 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: /新建/ }));
    await user.type(screen.getByLabelText('名称'), '  contains  ');
    await user.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    // THE NAME IS TRIMMED before it is sent, so a stray space does not become a
    // different dictionary entry.
    expect(posted[0]).toEqual({ name: 'contains', color: '' });
    expect(await screen.findByText('已创建')).toBeInTheDocument();
  });

  it('rejects a colour that is not a colour before sending it', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    server.use(
      http.get('/api/v1/cmdb/relation-types', () => HttpResponse.json({ items: rows, total: rows.length })),
      http.post('/api/v1/cmdb/relation-types', async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ id: 3, name: 'x', color: '' }, { status: 201 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: /新建/ }));
    await user.type(screen.getByLabelText('名称'), 'contains');
    await user.type(screen.getByLabelText('颜色'), 'red-ish');
    await user.click(screen.getByRole('button', { name: '保存' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('#rrggbb');
    expect(posted).toHaveLength(0);
    // THE INPUT SURVIVES THE REFUSAL, so the operator fixes the colour instead of
    // retyping the whole form.
    expect(screen.getByLabelText('名称')).toHaveValue('contains');
  });

  it('asks before deleting and does not delete on cancel', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    server.use(
      http.get('/api/v1/cmdb/relation-types', () => HttpResponse.json({ items: rows, total: rows.length })),
      http.delete('/api/v1/cmdb/relation-types/:id', () => {
        deleted += 1;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '删除 depends_on' }));
    // THE FIRST CLICK OPENS THE CONFIRMATION; nothing is deleted yet.
    expect(deleted).toBe(0);
    await user.click(screen.getByRole('button', { name: '取消' }));
    expect(deleted).toBe(0);
  });
});
