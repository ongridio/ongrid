import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { useAuth } from '@/store/auth';
import ModelDetailPage from './ModelDetail';
import { ToastProvider } from '../components/Toast';

const model = {
  id: 1,
  name: 'host',
  alias: '主机',
  enabled: true,
  is_attached: true,
  icon: '',
  order: 0,
  unique_key: 'hostname',
  parent_ids: [],
};

const disk = { ...model, id: 2, name: 'disk', alias: '磁盘', unique_key: 'sn' };

const ownAttribute = {
  id: 10,
  name: 'hostname',
  alias: '主机名',
  value_type: '2',
  is_list: false,
  is_index: true,
  is_indexed: true,
  inherited: false,
  is_required: true,
  order: 0,
  default_show: true,
  storage_table: 'c_value_index_texts',
};

// AN INHERITED ATTRIBUTE IS NOT BOUND ON THIS MODEL, so it cannot be unbound
// here — the flag and its source are what the page has to show.
const inheritedAttribute = {
  ...ownAttribute,
  id: 20,
  name: 'serial',
  alias: '序列号',
  inherited: true,
  inherited_from: '资产',
  is_required: false,
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/cmdb/models/1']}>
      <Routes>
        <Route
          path="/cmdb/models/:id"
          element={
            <ToastProvider>
              <ModelDetailPage />
            </ToastProvider>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

function baseHandlers() {
  server.use(
    http.get('/api/v1/cmdb/ci-types/1', () => HttpResponse.json(model)),
    http.get('/api/v1/cmdb/ci-types/1/attributes', () =>
      HttpResponse.json({
        type_id: 1,
        unique_id: 10,
        unique: 'hostname',
        attributes: [ownAttribute, inheritedAttribute],
      }),
    ),
    http.get('/api/v1/cmdb/ci-types/1/attribute-groups', () => HttpResponse.json([])),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the attribute tab', () => {
  it('shows the effective set and where an inherited attribute comes from', async () => {
    baseHandlers();
    renderPage();

    expect(await screen.findByText('主机名')).toBeInTheDocument();
    expect(screen.getByText('序列号')).toBeInTheDocument();
    expect(screen.getByText('继承自 资产')).toBeInTheDocument();
  });

  it('refuses to unbind an inherited attribute', async () => {
    baseHandlers();
    renderPage();

    await screen.findByText('序列号');
    // THE ATTRIBUTE IS BOUND ON THE PARENT, so an unbind button here would be a
    // button that cannot do what it says.
    expect(screen.getByRole('button', { name: '解绑 serial' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '解绑 hostname' })).toBeEnabled();
  });

  it('asks before unbinding, and reports how many values went with it', async () => {
    const user = userEvent.setup();
    baseHandlers();
    let unbound: unknown = null;
    server.use(
      http.delete('/api/v1/cmdb/ci-types/1/attributes', async ({ request }) => {
        unbound = await request.json();
        return HttpResponse.json({ unbound: 1, deleted_values: 42, affected_types: [1] });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '解绑 hostname' }));
    const dialog = await screen.findByRole('dialog');
    // THE CONFIRMATION SAYS WHAT UNBINDING MEANS: the values go with it, and that
    // is not obvious from the word.
    expect(within(dialog).getByText(/实例值会一起删除/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: '确认' }));

    await waitFor(() => expect(unbound).toEqual({ attr_ids: [10] }));
    // AND THE RESULT NAMES THE DATA, not just the column.
    expect(await screen.findByText('已解绑，并删除了 42 个值')).toBeInTheDocument();
  });

  it('sends a binding change rather than a dictionary change', async () => {
    const user = userEvent.setup();
    baseHandlers();
    let patch: unknown = null;
    server.use(
      http.put('/api/v1/cmdb/ci-types/1/attributes', async ({ request }) => {
        patch = await request.json();
        return HttpResponse.json({ updated: 1 });
      }),
    );
    renderPage();

    const toggle = await screen.findByRole('checkbox', { name: 'hostname 必填' });
    await user.click(toggle);
    // `is_required` IS A PROPERTY OF THE BINDING, not of the attribute — the same
    // attribute can be required on one model and optional on another — so this
    // goes to the bindings endpoint, not the dictionary.
    await waitFor(() => expect(patch).toEqual({ attributes: [{ attr_id: 10, is_required: false }] }));
  });
});

describe('the other tabs', () => {
  it('loads relations for this model', async () => {
    const user = userEvent.setup();
    baseHandlers();
    server.use(
      http.get('/api/v1/cmdb/ci-types/1/relation-children', () =>
        HttpResponse.json({
          items: [
            {
              id: 7,
              parent: { ctr_id: 7, type: model, attributes: [], relation_type: { id: 1, name: '包含', color: '#6366f1' }, constraint: '0', parent_attr_ids: [], child_attr_ids: [] },
              child: {
                ctr_id: 7,
                type: { ...model, id: 2, name: 'disk', alias: '磁盘' },
                attributes: [],
                relation_type: { id: 1, name: '包含', color: '#6366f1' },
                constraint: '0',
                parent_attr_ids: [],
                child_attr_ids: [],
              },
            },
          ],
          total: 1,
        }),
      ),
      http.get('/api/v1/cmdb/ci-types/1/relation-parents', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [model], total: 1 })),
    );
    renderPage();

    await user.click(await screen.findByRole('tab', { name: '模型关系' }));
    expect(await screen.findByRole('link', { name: '磁盘' })).toBeInTheDocument();
    expect(screen.getByText('子模型')).toBeInTheDocument();
    expect(screen.getByText('1-N')).toBeInTheDocument();
  });

  it('creates a relation with the wire cardinality code', async () => {
    const user = userEvent.setup();
    baseHandlers();
    let posted: unknown;
    let postedPath = '';
    server.use(
      http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [model, disk], total: 2 })),
      http.get('/api/v1/cmdb/ci-types/1/relation-children', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/cmdb/ci-types/1/relation-parents', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/cmdb/relation-types', () =>
        HttpResponse.json({ items: [{ id: 5, name: '包含', color: '#6366f1' }], total: 1 }),
      ),
      http.post('/api/v1/cmdb/ci-types/:parent/relation-children/:child', async ({ request, params }) => {
        posted = await request.json();
        postedPath = `/api/v1/cmdb/ci-types/${params.parent}/relation-children/${params.child}`;
        return HttpResponse.json({});
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('tab', { name: '模型关系' }));
    await user.click(await screen.findByRole('button', { name: '加子模型' }));
    await selectOption(screen.getByLabelText('对端模型'), '磁盘');
    await selectOption(screen.getByLabelText('基数'), '1-1');
    await user.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(posted).toEqual({ relation_type_id: 5, constraint: '1' }));
    expect(postedPath).toBe('/api/v1/cmdb/ci-types/1/relation-children/2');
  });

  it('marks a uniqueness rule that names a deleted attribute', async () => {
    const user = userEvent.setup();
    baseHandlers();
    server.use(
      http.get('/api/v1/cmdb/ci-types/1/unique-constraints', () =>
        HttpResponse.json({
          items: [{ id: 3, type_id: 1, attr_ids: [10, 99], attributes: [ownAttribute] }],
          total: 1,
        }),
      ),
    );
    renderPage();

    await user.click(await screen.findByRole('tab', { name: '唯一约束' }));
    // THE RESOLVED SET IS SHORTER THAN THE STORED IDS, which is the backend's
    // signal that the rule references something gone. cantor dropped those
    // silently; here the gap is visible.
    expect(await screen.findByText('1 个属性已不存在')).toBeInTheDocument();
  });
});

describe('the auto-discovery tab', () => {
  it('links to the discovery configuration and the pool', async () => {
    baseHandlers();
    renderPage();

    await userEvent.click(await screen.findByRole('tab', { name: '自动发现' }));
    expect(await screen.findByRole('link', { name: '打开自动发现配置' })).toHaveAttribute('href', '/cmdb/discovery');
    expect(screen.getByRole('link', { name: '打开发现池' })).toHaveAttribute('href', '/cmdb/adc');
  });
});
