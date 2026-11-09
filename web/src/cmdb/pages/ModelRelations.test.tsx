import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { useAuth } from '@/store/auth';
import ModelRelationsPage from './ModelRelations';
import { ToastProvider } from '../components/Toast';

const host = { id: 1, name: 'host', alias: '主机', enabled: true, is_attached: true, icon: '', order: 0, unique_key: 'hostname', parent_ids: [] };
const disk = { id: 2, name: 'disk', alias: '磁盘', enabled: true, is_attached: true, icon: '', order: 1, unique_key: 'sn', parent_ids: [] };
const rack = { id: 3, name: 'rack', alias: '机柜', enabled: true, is_attached: true, icon: '', order: 2, unique_key: 'name', parent_ids: [] };

const related = (type: typeof host, constraint: string) => ({
  ctr_id: 1,
  type,
  attributes: [],
  relation_type: { id: 1, name: '包含', color: '#6366f1' },
  constraint,
  parent_attr_ids: [],
  child_attr_ids: [],
});

const relations = [
  { id: 11, parent: related(host, '0'), child: related(disk, '0') },
  { id: 12, parent: related(rack, '0'), child: related(host, '0') },
];

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <ModelRelationsPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the flat relation list', () => {
  it('draws every parent → child pair as one row', async () => {
    server.use(http.get('/api/v1/cmdb/ci-type-relations', () => HttpResponse.json({ items: relations, total: relations.length })));
    renderPage();

    // `主机` APPEARS TWICE — once as a parent and once as a child — which is
    // exactly what a flat pair list is for.
    expect(await screen.findAllByRole('link', { name: '主机' })).toHaveLength(2);
    expect(screen.getByRole('link', { name: '磁盘' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '机柜' })).toBeInTheDocument();
    // TWO EDGES, so the parent and the child are columns rather than a tree: a
    // tree would hide that `host` is a child of one model and a parent of
    // another.
    expect(screen.getAllByText('包含')).toHaveLength(2);
    // The API stores cantor's codes; the console labels them.
    expect(screen.getAllByText('1-N')).toHaveLength(2);
  });

  it('filters by either side of the pair', async () => {
    const user = userEvent.setup();
    server.use(http.get('/api/v1/cmdb/ci-type-relations', () => HttpResponse.json({ items: relations, total: relations.length })));
    renderPage();

    await screen.findAllByRole('link', { name: '主机' });
    await user.type(screen.getByLabelText('搜索模型关系'), '机柜');
    // `机柜` IS ONLY EVER A PARENT, so filtering on it keeps the edge where it is
    // the parent — a filter that only looked at children would return nothing.
    expect(screen.getByRole('link', { name: '机柜' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: '磁盘' })).not.toBeInTheDocument();
  });

  it('disables relation writes for a non-administrator', async () => {
    useAuth.setState({ token: null, role: 'user' });
    server.use(http.get('/api/v1/cmdb/ci-type-relations', () => HttpResponse.json({ items: relations, total: relations.length })));
    renderPage();

    expect(await screen.findByRole('button', { name: '新建关系' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '删除 host → disk' })).toBeDisabled();
  });

  it('creates a parent → child relation', async () => {
    let posted: unknown;
    let postedPath = '';
    server.use(
      http.get('/api/v1/cmdb/ci-type-relations', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/cmdb/relation-types', () =>
        HttpResponse.json({ items: [{ id: 5, name: '连接', color: '#6366f1' }], total: 1 }),
      ),
      http.get('/api/v1/cmdb/ci-types', () =>
        HttpResponse.json({ items: [host, disk], total: 2 }),
      ),
      http.post(
        '/api/v1/cmdb/ci-types/:parent/relation-children/:child',
        async ({ request, params }) => {
          postedPath = `/api/v1/cmdb/ci-types/${params.parent}/relation-children/${params.child}`;
          posted = await request.json();
          return HttpResponse.json(relations[0]);
        },
      ),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '新建关系' }));
    await selectOption(screen.getByLabelText('父模型'), '主机');
    await selectOption(screen.getByLabelText('子模型'), '磁盘');
    await selectOption(screen.getByLabelText('关系类型'), '连接');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(posted).toEqual({ relation_type_id: 5, constraint: '0' }));
    expect(postedPath).toBe('/api/v1/cmdb/ci-types/1/relation-children/2');
  });

  it('asks before deleting and does nothing on cancel', async () => {
    const user = userEvent.setup();
    let deleted = 0;
    server.use(
      http.get('/api/v1/cmdb/ci-type-relations', () => HttpResponse.json({ items: relations, total: relations.length })),
      http.delete('/api/v1/cmdb/ci-type-relations/:id', () => {
        deleted += 1;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '删除 host → disk' }));
    const dialog = await screen.findByRole('dialog');
    // THE CONFIRMATION DISTINGUISHES THE SCHEMA FROM THE DATA: deleting the model
    // relation does not delete the instance relations that already exist.
    expect(within(dialog).getByText(/实例关系不会被删除/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: '取消' }));
    expect(deleted).toBe(0);

    await user.click(screen.getByRole('button', { name: '删除 host → disk' }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: '确认' }));
    await waitFor(() => expect(deleted).toBe(1));
  });
});
