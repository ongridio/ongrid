import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import SubscriptionsPage from './Subscriptions';
import { ToastProvider } from '../components/Toast';

const host = { id: 1, name: 'host', alias: '主机', enabled: true, is_attached: true, icon: '', order: 0, unique_key: 'hostname', parent_ids: [] };

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
  storage_table: 'c_value_index_texts',
  is_unique: id === 10,
  ...extra,
});

const attributes = [attribute(10, 'hostname'), attribute(11, 'cpu'), attribute(12, 'root_password', { is_password: true })];

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <SubscriptionsPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function handlers() {
  server.use(
    http.get('/api/v1/cmdb/preferences/ci-types', () =>
      HttpResponse.json({
        groups: [{ id: 1, name: '硬件', ci_types: [host] }],
        type_ids: [1],
      }),
    ),
    http.get('/api/v1/cmdb/preferences/auto-subscription', () =>
      HttpResponse.json({ base_strategy: 'all', group_ids: [], type_ids: [], enabled: false }),
    ),
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
    http.get('/api/v1/cmdb/ci-types/1/attributes', () =>
      HttpResponse.json({ type_id: 1, unique_id: 10, unique: 'hostname', attributes }),
    ),
    http.get('/api/v1/cmdb/preferences/ci-types/1/fields', () =>
      HttpResponse.json({ type_id: 1, is_subscribed: true, fields: [{ attr_id: 10, is_fixed: true }] }),
    ),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  // NO TOKEN ⇒ `useMe` makes no request, so the role comes from the login
  // response — and this page is NOT admin-gated, which the last test checks.
  useAuth.setState({ token: null, role: 'user' });
});

describe('the subscription page', () => {
  it('groups the models and shows the current order', async () => {
    handlers();
    renderPage();

    expect(await screen.findByText('硬件')).toBeInTheDocument();
    expect(screen.getAllByText('主机').length).toBeGreaterThan(0);
    expect(screen.getByLabelText('上移 host')).toBeInTheDocument();
  });

  it('pinpoints the identifying column and refuses to unpin it', async () => {
    const user = userEvent.setup();
    handlers();
    renderPage();

    await user.click(await screen.findByRole('button', { name: '选择 host 的列' }));
    const pinned = await screen.findByRole('checkbox', { name: /hostname/ });
    // A LIST WITHOUT THE IDENTIFYING COLUMN HAS ROWS NOBODY CAN TELL APART, so
    // the control is disabled rather than merely discouraged. The shared
    // `Checkbox` renders `aria-disabled` rather than the DOM `disabled`
    // attribute — it is a `span` with `role="checkbox"` — so `toBeDisabled()`
    // silently reports false for it and the attribute is what to assert on.
    expect(pinned).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByText('标识列，固定显示')).toBeInTheDocument();
    // A SECRET ATTRIBUTE MAY BE SUBSCRIBED TO, and the page says what it is.
    expect(screen.getByText('密码')).toBeInTheDocument();
  });

  it('saves the chosen columns with the fixed flag on the identifying one', async () => {
    const user = userEvent.setup();
    handlers();
    let put: unknown = null;
    server.use(
      http.put('/api/v1/cmdb/preferences/ci-types/1/fields', async ({ request }) => {
        put = await request.json();
        return HttpResponse.json({ type_id: 1, is_subscribed: true, fields: [] });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '选择 host 的列' }));
    await user.click(await screen.findByRole('checkbox', { name: /cpu/ }));
    await user.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(put).not.toBeNull());
    expect(put).toEqual({
      fields: [
        { ref: '10', is_fixed: true },
        { ref: '11', is_fixed: false },
      ],
    });
  });

  it('saves a new order when a model is moved', async () => {
    const user = userEvent.setup();
    handlers();
    server.use(
      http.get('/api/v1/cmdb/ci-types', () =>
        HttpResponse.json({
          items: [host, { ...host, id: 2, name: 'switch', alias: '交换机' }],
          total: 2,
        }),
      ),
      http.get('/api/v1/cmdb/preferences/ci-types', () =>
        HttpResponse.json({ groups: [{ id: 1, name: '硬件', ci_types: [host] }], type_ids: [1, 2] }),
      ),
    );
    let order: unknown = null;
    server.use(
      http.put('/api/v1/cmdb/preferences/ci-types/order', async ({ request }) => {
        order = await request.json();
        return HttpResponse.json({});
      }),
    );
    renderPage();

    await user.click(await screen.findByLabelText('下移 host'));
    await waitFor(() => expect(order).toEqual({ type_ids: [2, 1], is_tree: false }));
  });

  it('does not require an administrator, because a subscription is per user', async () => {
    handlers();
    renderPage();

    // THE ONLY CMDB PAGE WITH NO ADMIN GATE, and deliberately so: "which columns
    // do I want" is not a deployment-wide decision. The backend draws the same
    // line, so the controls are enabled for a plain user.
    expect(await screen.findByRole('button', { name: '选择 host 的列' })).toBeEnabled();
  });
});
