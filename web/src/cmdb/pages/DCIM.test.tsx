import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { useAuth } from '@/store/auth';
import DCIMPage from './DCIM';
import { ToastProvider } from '../components/Toast';

const tree = {
  result: [
    {
      _id: 1,
      _type: 101,
      ci_type: 'dcim_region',
      ci_type_alias: '区域',
      name: '华北',
      children: [
        {
          _id: 2,
          _type: 102,
          ci_type: 'dcim_idc',
          ci_type_alias: '数据中心',
          name: '亦庄',
          children: [
            {
              _id: 3,
              _type: 103,
              ci_type: 'dcim_server_room',
              ci_type_alias: '机房',
              name: 'A 机房',
              rack_count: 2,
              children: [],
            },
          ],
        },
      ],
    },
  ],
  type2name: { '101': 'name', '102': 'name', '103': 'name' },
};

const racks = {
  numfound: 2,
  total: 2,
  page: 1,
  counter: { rack: 2 },
  result: [
    { _id: 31, _type: 104, ci_id: 31, ci_type: 'dcim_rack', name: 'R-A01', u_count: 42, free_u_count: 30, device_count: 3 },
    // AN OVERSUBSCRIBED RACK: 50 units of devices in a 42U rack. The server can
    // produce this state, so the console has to show it rather than clamp.
    { _id: 32, _type: 104, ci_id: 32, ci_type: 'dcim_rack', name: 'R-A02', u_count: 42, free_u_count: -8, device_count: 4, is_overlap: true },
  ],
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/cmdb/dcim']}>
      <ToastProvider>
        <DCIMPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function handlers() {
  server.use(
    http.get('/api/v1/cmdb/dcim/tree-view', () => HttpResponse.json(tree)),
    http.get('/api/v1/cmdb/dcim/server-rooms/3/racks', () => HttpResponse.json(racks)),
    http.get('/api/v1/cmdb/dcim/history/operations', () =>
      HttpResponse.json({
        numfound: 1,
        result: [{ _id: 9, rack_id: 31, ci_id: 55, operate_type: 'add', created_at: '2026-10-17 10:00:00' }],
        id2ci: { '55': { name: 'srv-01' } },
      }),
    ),
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [], total: 0 })),
    http.get('/api/v1/cmdb/ci-relations/search', () =>
      HttpResponse.json({ numfound: 0, total: 0, page: 1, result: [] }),
    ),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the tree', () => {
  it('shows the containment and each room’s rack count', async () => {
    handlers();
    renderPage();

    expect(await screen.findByText('华北')).toBeInTheDocument();
    expect(screen.getByText('亦庄')).toBeInTheDocument();
    expect(screen.getByText('A 机房')).toBeInTheDocument();
    // A ROOM WITHOUT RACKS IS A ROOM SOMEBODY HAS TO FILL, so the count is
    // reported rather than omitted.
    expect(screen.getByText('2 个机柜')).toBeInTheDocument();
  });
});

describe('a room’s racks', () => {
  it('shows capacity, including an oversubscribed rack', async () => {
    const user = userEvent.setup();
    handlers();
    renderPage();

    await user.click(await screen.findByText('A 机房'));

    expect(await screen.findByText('R-A01')).toBeInTheDocument();
    expect(screen.getByText('30U')).toBeInTheDocument();
    // A NEGATIVE FREE COUNT IS A REAL STATE and is shown as it is: clamping it to
    // zero would hide a rack that is over capacity.
    expect(screen.getByText('-8U')).toBeInTheDocument();
    expect(screen.getByText('有')).toBeInTheDocument();
  });

  it('labels a history row by the instance it names', async () => {
    const user = userEvent.setup();
    handlers();
    renderPage();

    await user.click(await screen.findByText('A 机房'));
    // THE LOG NAMES THE INSTANCE AND CARRIES ITS LABEL, so a rename does not
    // rewrite history and a deleted device still leaves a trace.
    expect(await screen.findByText('srv-01')).toBeInTheDocument();
    expect(screen.getByText('add')).toBeInTheDocument();
  });
});

describe('mounting a device', () => {
  it('sends the start unit and omits the height when it is left blank', async () => {
    const user = userEvent.setup();
    handlers();
    server.use(
      http.get('/api/v1/cmdb/ci-types', () =>
        HttpResponse.json({
          items: [{ id: 5, name: 'server', alias: '服务器', enabled: true, is_attached: true, icon: '', order: 0, unique_key: 'name', parent_ids: [] }],
          total: 1,
        }),
      ),
      http.get('/api/v1/cmdb/ci-types/5/instances', () =>
        HttpResponse.json({ type_id: 5, items: [{ ci_id: 55, type_id: 5, values: { name: 'srv-01' } }], total: 1, numfound: 1, page: 1 }),
      ),
      http.get('/api/v1/cmdb/ci-relations/search', () =>
        HttpResponse.json({ numfound: 0, total: 0, page: 1, result: [] }),
      ),
    );
    const mounted: unknown[] = [];
    server.use(
      http.post('/api/v1/cmdb/dcim/server-rooms/3/racks/31/devices/55', async ({ request }) => {
        mounted.push(await request.json());
        return HttpResponse.json({ rack_id: 31, ci_id: 55, u_count: 2, free_u_count: 24 });
      }),
    );
    renderPage();

    // Open the rack drawer from the room's rack table.
    await user.click(await screen.findByText('A 机房'));
    await user.click(await screen.findByRole('button', { name: '打开机柜 R-A01' }));
    const drawer = await screen.findByRole('dialog');
    await user.click(within(drawer).getByRole('button', { name: /上架设备/ }));

    const modal = await screen.findAllByRole('dialog');
    const mount = modal[modal.length - 1];
    // THE DEVICE HAS TO BE PICKED, not typed: a mount names an instance, and the
    // form's submit refuses without one rather than sending an empty id.
    await selectOption(within(mount).getByLabelText('设备'), 'srv-01');
    const start = within(mount).getByLabelText('起始 U 位');
    await user.clear(start);
    await user.type(start, '5');
    await user.click(within(mount).getByRole('button', { name: '上架' }));

    await waitFor(() => expect(mounted).toHaveLength(1));
    // AN EMPTY HEIGHT IS "DO NOT MENTION": the server reads a missing height as
    // "the device's own", and zero as the default — two different answers.
    expect(mounted[0]).toEqual({ u_start: 5 });
    // AND THE RESULT REPORTS THE RACK'S REMAINING CAPACITY, which is the number
    // the operator wants after the fact.
    expect(await screen.findByText(/机柜剩余 24U/)).toBeInTheDocument();
  });
});

describe('renaming and moving', () => {
  it('renames a room through the DCIM node endpoint', async () => {
    const user = userEvent.setup();
    handlers();
    let updated: unknown;
    server.use(
      http.put('/api/v1/cmdb/dcim/server_room/3', async ({ request }) => {
        updated = await request.json();
        return HttpResponse.json({ ci_id: 3 });
      }),
    );
    renderPage();

    await user.click(await screen.findByText('A 机房'));
    await user.click(await screen.findByRole('button', { name: '重命名 A 机房' }));
    const dialog = await screen.findByRole('dialog');
    const name = within(dialog).getByLabelText('新名称');
    await user.clear(name);
    await user.type(name, 'B 机房');
    await user.click(within(dialog).getByRole('button', { name: '保存' }));

    await waitFor(() => expect(updated).toEqual({ name: 'B 机房' }));
  });

  it('moves a device within its rack', async () => {
    const user = userEvent.setup();
    handlers();
    server.use(
      http.get('/api/v1/cmdb/ci-relations/search', () =>
        HttpResponse.json({
          numfound: 1,
          total: 1,
          page: 1,
          result: [{ _id: 55, _type: 5, ci_id: 55, ci_type: 'server', ci_type_alias: '服务器', name: 'srv-01', u_start: 2 }],
        }),
      ),
    );
    let moved: unknown;
    server.use(
      http.put('/api/v1/cmdb/dcim/server-rooms/3/racks/31/devices/55', async ({ request }) => {
        moved = await request.json();
        return HttpResponse.json({ rack_id: 31, ci_id: 55, u_count: 2, free_u_count: 30 });
      }),
    );
    renderPage();

    await user.click(await screen.findByText('A 机房'));
    await user.click(await screen.findByRole('button', { name: '打开机柜 R-A01' }));
    const drawer = await screen.findByRole('dialog');
    await user.click(await within(drawer).findByRole('button', { name: '移动 srv-01' }));

    const dialogs = await screen.findAllByRole('dialog');
    const modal = dialogs[dialogs.length - 1];
    const start = within(modal).getByLabelText('目标起始 U 位');
    await user.clear(start);
    await user.type(start, '10');
    await user.click(within(modal).getByRole('button', { name: '移动' }));

    await waitFor(() => expect(moved).toEqual({ to_u_start: 10 }));
  });
});
