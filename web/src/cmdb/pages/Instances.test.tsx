import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import InstancesPage from './Instances';
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

const attribute = (id: number, name: string, extra: Record<string, unknown> = {}) => ({
  id,
  name,
  alias: name,
  value_type: '2',
  is_list: false,
  is_index: true,
  is_indexed: true,
  is_required: false,
  order: id,
  default_show: true,
  inherited: false,
  is_password: false,
  is_reference: false,
  is_computed: false,
  storage_table: 'c_value_index_texts',
  ...extra,
});

const attributes = [
  attribute(10, 'hostname'),
  attribute(11, 'note'),
  attribute(12, 'root_password', { is_password: true }),
];

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/cmdb/instances?type=host']}>
      <ToastProvider>
        <InstancesPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function handlers(instances: unknown[] = []) {
  server.use(
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [model], total: 1 })),
    http.get('/api/v1/cmdb/ci-types/host/attributes', () =>
      HttpResponse.json({ type_id: 1, unique_id: 10, unique: 'hostname', attributes }),
    ),
    http.get('/api/v1/cmdb/ci-types/host/attribute-groups', () => HttpResponse.json([])),
    http.get('/api/v1/cmdb/preferences/ci-types/host/fields', () =>
      HttpResponse.json({
        type_id: 1,
        is_subscribed: true,
        // THE SUBSCRIPTION DECIDES THE COLUMNS: `note` is subscribed, and the
        // password is not.
        fields: [
          { attr_id: 10, is_fixed: true },
          { attr_id: 11, is_fixed: false },
        ],
      }),
    ),
    http.get('/api/v1/cmdb/ci-types/host/instances', () =>
      HttpResponse.json({ type_id: 1, items: instances, total: instances.length, numfound: instances.length, page: 1 }),
    ),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the list', () => {
  it('shows the subscribed columns, and only those', async () => {
    handlers([{ ci_id: 5, type_id: 1, values: { hostname: 'web-01', note: 'hello', root_password: 'x' } }]);
    renderPage();

    expect(await screen.findByText('web-01')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'note' })).toBeInTheDocument();
    // THE SUBSCRIPTION IS THE COLUMN SET: an attribute nobody subscribed to is
    // not a column, even though the row carries it.
    expect(screen.queryByRole('columnheader', { name: 'root_password' })).not.toBeInTheDocument();
  });

  it('says when a model cannot hold instances', async () => {
    // NO ROWS, so the empty state is what draws — and it has to explain the
    // model rather than say "no data".
    handlers([]);
    server.use(http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [{ ...model, is_attached: false }], total: 1 })));
    renderPage();

    expect(await screen.findByText(/存不了实例/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /新建实例/ })).toBeDisabled();
  });

  it('deletes after a confirmation that names the row', async () => {
    const user = userEvent.setup();
    handlers([{ ci_id: 5, type_id: 1, values: { hostname: 'web-01', note: '' } }]);
    let deleted = 0;
    server.use(
      http.delete('/api/v1/cmdb/instances/:id', () => {
        deleted += 1;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '删除实例 5' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/web-01/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: '取消' }));
    expect(deleted).toBe(0);
  });
});

describe('the form in the drawer', () => {
  it('creates with the typed values', async () => {
    const user = userEvent.setup();
    handlers([]);
    const posted: unknown[] = [];
    server.use(
      http.post('/api/v1/cmdb/instances', async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ ci_id: 6, created: true }, { status: 201 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: /新建实例/ }));
    const drawer = await screen.findByRole('dialog');
    await user.type(within(drawer).getByLabelText('hostname'), 'web-02');
    await user.type(within(drawer).getByLabelText('note'), 'rack A');
    await user.click(within(drawer).getByRole('button', { name: '保存' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toMatchObject({
      ci_type: 1,
      exist_policy: 'reject',
      values: { hostname: 'web-02', note: 'rack A' },
    });
  });

  it('does not clear a stored secret on a save that did not touch it', async () => {
    const user = userEvent.setup();
    handlers([{ ci_id: 5, type_id: 1, values: { hostname: 'web-01', note: 'hello' } }]);
    server.use(
      http.get('/api/v1/cmdb/instances/5', () =>
        HttpResponse.json({ ci_id: 5, type_id: 1, values: { hostname: 'web-01', note: 'hello' } }),
      ),
    );
    const put: unknown[] = [];
    server.use(
      http.put('/api/v1/cmdb/instances/5', async ({ request }) => {
        put.push(await request.json());
        return HttpResponse.json({ ci_id: 5, created: false });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '编辑实例 5' }));
    const drawer = await screen.findByRole('dialog');
    await user.clear(within(drawer).getByLabelText('note'));
    await user.type(within(drawer).getByLabelText('note'), 'changed');
    await user.click(within(drawer).getByRole('button', { name: '保存' }));

    await waitFor(() => expect(put).toHaveLength(1));
    const values = (put[0] as { values: Record<string, unknown> }).values;
    expect(values.note).toBe('changed');
    // THE PASSWORD BOX IS EMPTY ON EVERY EDIT — a read never returns a secret —
    // so sending that empty value would wipe a credential the operator did not
    // touch. It is OMITTED, not sent as null.
    expect('root_password' in values).toBe(false);
  });

  it('omits a computed attribute and says why it cannot be typed', async () => {
    const user = userEvent.setup();
    handlers([{ ci_id: 5, type_id: 1, values: { hostname: 'web-01', note: '' } }]);
    server.use(
      http.get('/api/v1/cmdb/ci-types/host/attributes', () =>
        HttpResponse.json({
          type_id: 1,
          unique_id: 10,
          unique: 'hostname',
          attributes: [...attributes, attribute(13, 'asset_no', { is_computed: true })],
        }),
      ),
      http.get('/api/v1/cmdb/instances/5', () =>
        HttpResponse.json({ ci_id: 5, type_id: 1, values: { hostname: 'web-01' } }),
      ),
    );
    const put: unknown[] = [];
    server.use(
      http.put('/api/v1/cmdb/instances/5', async ({ request }) => {
        put.push(await request.json());
        return HttpResponse.json({ ci_id: 5, created: false });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '编辑实例 5' }));
    const drawer = await screen.findByRole('dialog');
    expect(within(drawer).getByText(/由模型计算/)).toBeInTheDocument();
    expect(within(drawer).getByLabelText('asset_no')).toBeDisabled();

    await user.click(within(drawer).getByRole('button', { name: '保存' }));
    await waitFor(() => expect(put).toHaveLength(1));
    // THE MODEL DERIVES IT; sending a value would be ignored, and sending null
    // would be a write about a field the form cannot edit.
    expect('asset_no' in (put[0] as { values: Record<string, unknown> }).values).toBe(false);
  });
});
