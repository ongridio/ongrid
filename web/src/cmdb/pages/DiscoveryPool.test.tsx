import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { delay, http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import DiscoveryPoolPage from './DiscoveryPool';
import { ToastProvider } from '../components/Toast';

const hostAttribute = {
  id: 10,
  name: 'hostname',
  alias: '主机名',
  value_type: '2',
  is_list: false,
};

const pending = {
  id: 1,
  type_id: 1,
  adt_id: 5,
  unique_value: 'i-1',
  instance: { hostname: 'i-1', cpu: 4 },
  oneagent_id: 'agent-1',
  oneagent_name: 'agent one',
  is_accept: false,
  adr_name: '阿里云',
  adr_type: 'http',
  is_inner: true,
  updated_at: '2026-10-26 10:00:00',
};

const accepted = { ...pending, is_accept: true, ci_id: 70, accept_by: 'ops@example.test' };

const counters = {
  rule_count: 1,
  exec_target_count: 1,
  instance_count: 1,
  accept_count: 0,
  this_month_count: 1,
  this_week_count: 1,
  last_month_count: 0,
  last_week_count: 0,
};

function poolHandlers(entry = pending, counter = counters) {
  server.use(
    http.get('/api/v1/cmdb/discovery/adc/ci-types', () =>
      HttpResponse.json({
        items: [{ type_id: 1, name: 'host', alias: '主机', icon: '', target_count: 1 }],
        total: 1,
      }),
    ),
    http.get('/api/v1/cmdb/discovery/adc/ci-types/1/attributes', () =>
      HttpResponse.json({ items: [hostAttribute], total: 1 }),
    ),
    http.get('/api/v1/cmdb/discovery/adc/counter', () => HttpResponse.json(counter)),
    http.get('/api/v1/cmdb/discovery/adc', () =>
      HttpResponse.json({ items: [entry], total: 1, page: 1, page_size: 20 }),
    ),
  );
}

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <DiscoveryPoolPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the discovery pool', () => {
  it('lists a model and its sightings with the mapped columns', async () => {
    poolHandlers();
    renderPage();

    expect(await screen.findByRole('heading', { name: '自动发现池' })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: '查看 主机 的发现池' })).toBeInTheDocument();
    expect(await screen.findByRole('columnheader', { name: '主机名' })).toBeInTheDocument();
    expect(screen.getAllByText('i-1').length).toBeGreaterThan(0);
    expect(await screen.findByLabelText('待接受数量')).toHaveTextContent('1');
    expect(screen.getByLabelText('已接受数量')).toHaveTextContent('0');
    expect(screen.getByText('agent one')).toBeInTheDocument();
  });

  it('accepts a pending sighting and refreshes the counters', async () => {
    let puts = 0;
    let current = pending;
    server.use(
      http.get('/api/v1/cmdb/discovery/adc/ci-types', () =>
        HttpResponse.json({
          items: [{ type_id: 1, name: 'host', alias: '主机', icon: '', target_count: 1 }],
          total: 1,
        }),
      ),
      http.get('/api/v1/cmdb/discovery/adc/ci-types/1/attributes', () =>
        HttpResponse.json({ items: [hostAttribute], total: 1 }),
      ),
      http.get('/api/v1/cmdb/discovery/adc/counter', () =>
        HttpResponse.json(current.is_accept ? { ...counters, accept_count: 1 } : counters),
      ),
      http.get('/api/v1/cmdb/discovery/adc', () =>
        HttpResponse.json({ items: [current], total: 1, page: 1, page_size: 20 }),
      ),
      http.put('/api/v1/cmdb/discovery/adc/1/accept', () => {
        puts += 1;
        current = accepted;
        return HttpResponse.json({ adc_id: 1, item: accepted });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '接受 i-1' }));

    await waitFor(() => expect(puts).toBe(1));
    await waitFor(() => expect(screen.getByLabelText('已接受数量')).toHaveTextContent('1'));
    expect(screen.queryByRole('button', { name: '接受 i-1' })).not.toBeInTheDocument();
  });

  it('does not let a previous model’s delayed response overwrite the current one', async () => {
    const switchAttribute = {
      id: 20, name: 'name', alias: '设备名', value_type: '2', is_list: false,
    };
    const switchEntry = {
      ...pending,
      id: 2,
      type_id: 2,
      unique_value: 's-1',
      instance: { name: 's-1' },
      oneagent_name: 'agent two',
    };
    server.use(
      http.get('/api/v1/cmdb/discovery/adc/ci-types', () =>
        HttpResponse.json({
          items: [
            { type_id: 1, name: 'host', alias: '主机', icon: '', target_count: 1 },
            { type_id: 2, name: 'switch', alias: '交换机', icon: '', target_count: 1 },
          ],
          total: 2,
        }),
      ),
      http.get('/api/v1/cmdb/discovery/adc/ci-types/:typeID/attributes', async ({ params }) => {
        if (String(params.typeID) === '1') {
          await delay(400);
          return HttpResponse.json({ items: [hostAttribute], total: 1 });
        }
        return HttpResponse.json({ items: [switchAttribute], total: 1 });
      }),
      http.get('/api/v1/cmdb/discovery/adc/counter', async ({ request }) => {
        const typeID = new URL(request.url).searchParams.get('type_id');
        if (typeID === '1') {
          await delay(400);
          return HttpResponse.json(counters);
        }
        return HttpResponse.json({ ...counters, instance_count: 1 });
      }),
      http.get('/api/v1/cmdb/discovery/adc', async ({ request }) => {
        const typeID = new URL(request.url).searchParams.get('type_id');
        if (typeID === '1') {
          await delay(400);
          return HttpResponse.json({ items: [pending], total: 1, page: 1, page_size: 20 });
        }
        return HttpResponse.json({ items: [switchEntry], total: 1, page: 1, page_size: 20 });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '查看 交换机 的发现池' }));
    expect((await screen.findAllByText('s-1')).length).toBeGreaterThan(0);
    expect(await screen.findByRole('columnheader', { name: '设备名' })).toBeInTheDocument();

    // Let the previous model's requests land; none of them may be applied now.
    await delay(600);
    expect(screen.queryAllByText('i-1')).toHaveLength(0);
    expect(screen.queryByRole('columnheader', { name: '主机名' })).not.toBeInTheDocument();
  });

  it('shows the raw report in a drawer', async () => {
    poolHandlers();
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '查看原始数据 i-1' }));
    const drawer = await screen.findByRole('dialog');
    expect(drawer).toHaveTextContent('"hostname": "i-1"');
    expect(drawer).toHaveTextContent('"cpu": 4');
  });

  it('lists a model’s discovery log and clears it', async () => {
    let cleared = 0;
    let histories = [
      { id: 9, type_id: 1, stdout: 'add resource: i-1 | rule: 阿里云 | agent: agent one', created_at: '2026-10-26 10:00:00' },
    ];
    poolHandlers();
    server.use(
      http.get('/api/v1/cmdb/discovery/adc/exec-histories', () =>
        HttpResponse.json({ items: histories, total: histories.length, page: 1, page_size: 20 }),
      ),
      http.delete('/api/v1/cmdb/discovery/adc/exec-histories/1', () => {
        cleared += 1;
        histories = [];
        return HttpResponse.json({ code: 200 });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '执行日志' }));
    expect(await screen.findByText(/add resource: i-1/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: '清空日志' }));
    await userEvent.click(await screen.findByRole('button', { name: '确认' }));

    await waitFor(() => expect(cleared).toBe(1));
    expect(await screen.findByText('没有执行日志')).toBeInTheDocument();
  });

  it('shows an empty state when no model has a target', async () => {
    server.use(
      http.get('/api/v1/cmdb/discovery/adc/ci-types', () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
    );
    renderPage();

    expect(await screen.findByText('还没有可用的发现模型')).toBeInTheDocument();
  });
});
