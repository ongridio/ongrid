import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import IPAMPage from './IPAM';
import { ToastProvider } from '../components/Toast';

const tree = {
  result: [
    {
      _id: 1,
      _type: 11,
      ci_type: 'ipam_scope',
      ci_type_alias: '地址分组',
      name: '生产网',
      children: [
        {
          _id: 2,
          _type: 12,
          ci_type: 'ipam_subnet',
          ci_type_alias: '子网',
          name: '办公网段',
          cidr: '10.0.0.0/24',
          children: [],
        },
      ],
    },
  ],
  type2name: { '11': 'name', '12': 'name' },
};

const summary = {
  subnet_num: 1,
  address_num: 254,
  address_free_num: 250,
  address_assign_num: 4,
  address_unassign_num: 250,
  address_used_num: 2,
  address_used_free_num: 252,
  subnets: [
    {
      _id: 2,
      _type: 12,
      ci_type: 'ipam_subnet',
      ci_type_alias: '子网',
      name: '办公网段',
      cidr: '10.0.0.0/24',
      hosts_count: 254,
      assign_count: 4,
      used_count: 2,
      free_count: 250,
      scan_enabled: true,
      last_scan_time: '2026-10-18 03:00:00',
    },
  ],
};

const addresses = {
  numfound: 2,
  total: 2,
  page: 1,
  counter: {},
  result: [
    {
      _id: 21,
      _type: 13,
      ci_id: 21,
      ci_type: 'ipam_address',
      ci_type_alias: '地址',
      ip: '10.0.0.11',
      assign_status: 0,
      is_used: true,
      _updated_at: '2026-10-18 09:00:00',
    },
    {
      _id: 22,
      _type: 13,
      ci_id: 22,
      ci_type: 'ipam_address',
      ci_type_alias: '地址',
      ip: '10.0.0.12',
      assign_status: 1,
      is_used: false,
    },
  ],
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/cmdb/ipam']}>
      <ToastProvider>
        <IPAMPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function handlers() {
  server.use(
    http.get('/api/v1/cmdb/ipam/tree-view', () => HttpResponse.json(tree)),
    http.get('/api/v1/cmdb/ipam/stats', () => HttpResponse.json(summary)),
    http.get('/api/v1/cmdb/ipam/addresses', () => HttpResponse.json(addresses)),
    http.get('/api/v1/cmdb/ipam/subnets/hosts', () =>
      HttpResponse.json({ hosts: ['10.0.0.11', '10.0.0.12', '10.0.0.13'], truncated: false }),
    ),
    // THE RULE LIVES ON THE SUBNET'S DETAIL, not in the tree — so the scan tab
    // asks for it separately.
    http.get('/api/v1/cmdb/ipam/subnets/:id', () =>
      HttpResponse.json({
        _id: 2,
        _type: 12,
        name: '办公网段',
        cidr: '10.0.0.0/24',
        hosts_count: 254,
        assign_count: 4,
        used_count: 2,
        free_count: 250,
        scan_enabled: true,
        agent_id: '0x0000',
        cron: '0 3 * * *',
        last_scan_time: '2026-10-18 03:00:00',
      }),
    ),
    http.get('/api/v1/cmdb/ipam/history/operations', () =>
      HttpResponse.json({
        numfound: 1,
        result: [
          { _id: 9, cidr: '10.0.0.0/24', operate_type: '6', description: '10.0.0.11', created_at: '2026-10-18 09:00:00' },
        ],
      }),
    ),
    http.get('/api/v1/cmdb/ipam/history/scans', () =>
      HttpResponse.json({
        numfound: 1,
        result: [
          { _id: 5, exec_id: 'abc123', cidr: '10.0.0.0/24', status: 0, ip_num: 2, created_at: '2026-10-18 03:00:00' },
        ],
      }),
    ),
  );
}

/** Clicks a node inside the tree — the same name also appears in the summary table. */
async function treeItem(name: string): Promise<HTMLElement> {
  const region = await screen.findByRole('tree');
  return within(region).getByText(name);
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the tree', () => {
  it('shows groups and ranges, and labels a range by its CIDR', async () => {
    handlers();
    renderPage();

    const region = await screen.findByRole('tree');
    expect(within(region).getByText('生产网')).toBeInTheDocument();
    // THE SAME RANGE ALSO APPEARS IN THE SUMMARY TABLE, so the tree's node is
    // looked up inside the tree rather than anywhere on the page.
    expect(within(region).getByText('办公网段')).toBeInTheDocument();
    // THE CIDR IS THE TREE'S TRAILING CHIP: a range is identified by its
    // address block, not only by the name an operator gave it.
    expect(within(region).getByText('10.0.0.0/24')).toBeInTheDocument();
  });
});

describe('the overview', () => {
  it('reports the server’s totals and each range’s usage', async () => {
    handlers();
    renderPage();

    // THE TILES ARE THE SERVER'S NUMBERS, summed over leaf ranges — the page
    // never adds a subnet's counters up itself.
    const capacity = await screen.findByRole('group', { name: '地址容量' });
    expect(within(capacity).getByText('254')).toBeInTheDocument();
    const free = screen.getByRole('group', { name: '可用' });
    expect(within(free).getByText('250')).toBeInTheDocument();
    // AND EACH RANGE IS LISTED BESIDE THEM with its own counters.
    expect(screen.getAllByText('办公网段').length).toBeGreaterThan(0);
  });
});

describe('the addresses', () => {
  it('lists a range’s addresses with their stored state', async () => {
    const user = userEvent.setup();
    handlers();
    renderPage();

    await user.click(await treeItem('办公网段'));
    await user.click(screen.getByRole('tab', { name: /地址/ }));

    expect(await screen.findByText('10.0.0.11')).toBeInTheDocument();
    expect(screen.getByText('已分配')).toBeInTheDocument();
    // THE SECOND ADDRESS WAS RELEASED AND ITS STATE IS SHOWN AS A STATE, not
    // as a missing row: a released address is still an address.
    expect(screen.getByText('已回收')).toBeInTheDocument();
  });

  it('assigns a batch without claiming the addresses were observed in use', async () => {
    const user = userEvent.setup();
    handlers();
    const posted: unknown[] = [];
    server.use(
      http.post('/api/v1/cmdb/ipam/addresses', async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ ci_ids: [31, 32], created: 2, assign_count: 6, used_count: 2, free_count: 248 });
      }),
    );
    renderPage();

    await user.click(await treeItem('办公网段'));
    await user.click(screen.getByRole('tab', { name: /地址/ }));
    await user.click(await screen.findByRole('button', { name: /分配地址/ }));

    const modal = await screen.findByRole('dialog');
    await user.type(within(modal).getByLabelText(/每行一个/), '10.0.0.13\n10.0.0.14');
    await user.click(within(modal).getByRole('button', { name: /提交 2 个/ }));

    await waitFor(() => expect(posted).toHaveLength(1));
    // `is_used` IS ABSENT ON PURPOSE. The server reads a true value as a
    // scanner's report and clears every other address's flag, which is not what
    // "assign" means in a console form.
    expect(posted[0]).toEqual({ subnet_id: 2, ips: ['10.0.0.13', '10.0.0.14'], assign_status: 0 });
  });

  it('releases one address through the same batch endpoint', async () => {
    const user = userEvent.setup();
    handlers();
    const posted: unknown[] = [];
    server.use(
      http.post('/api/v1/cmdb/ipam/addresses', async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ ci_ids: [21], created: 0, assign_count: 3, used_count: 1, free_count: 251 });
      }),
    );
    renderPage();

    await user.click(await treeItem('办公网段'));
    await user.click(screen.getByRole('tab', { name: /地址/ }));
    await user.click(await screen.findByRole('button', { name: '回收 10.0.0.11' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({ subnet_id: 2, ips: ['10.0.0.11'], assign_status: 1 });
  });
});

describe('creating a range', () => {
  it('sends the parent, the CIDR and the name, and no scan fields when scanning is off', async () => {
    const user = userEvent.setup();
    handlers();
    const posted: unknown[] = [];
    server.use(
      http.post('/api/v1/cmdb/ipam/subnets', async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ ci_id: 40 });
      }),
    );
    renderPage();

    await user.click(await treeItem('生产网'));
    await user.click(screen.getByRole('button', { name: /在此新建/ }));

    const modal = await screen.findByRole('dialog');
    await user.type(within(modal).getByLabelText('名称'), '研发网段');
    await user.type(within(modal).getByLabelText(/网段 \(CIDR\)/), '10.0.9.0/24');
    await user.click(within(modal).getByRole('button', { name: '保存' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    // "SCANNING OFF" SENDS NO SCAN FIELDS: the server writes no rule, which is
    // a different state from a rule that exists and is disabled.
    expect(posted[0]).toEqual({ parent_id: 1, cidr: '10.0.9.0/24', name: '研发网段' });
  });
});

describe('the logs', () => {
  it('names the recorded operation rather than showing its stored code', async () => {
    const user = userEvent.setup();
    handlers();
    renderPage();

    await user.click(await screen.findByRole('tab', { name: '日志' }));
    // OPERATION TYPE 6 IS "ADDRESS ASSIGNED". The log stores a code because
    // cantor did; the console is where it becomes a sentence.
    expect(await screen.findByText('分配地址')).toBeInTheDocument();
    expect(screen.getByText('10.0.0.11')).toBeInTheDocument();
  });

  it('scopes the scan log to the selected range and shows its rule', async () => {
    const user = userEvent.setup();
    handlers();
    let scanQuery = '';
    server.use(
      http.get('/api/v1/cmdb/ipam/history/scans', ({ request }) => {
        scanQuery = new URL(request.url).search;
        return HttpResponse.json({
          numfound: 1,
          result: [
            { _id: 5, exec_id: 'abc123', cidr: '10.0.0.0/24', status: 0, ip_num: 2, created_at: '2026-10-18 03:00:00' },
          ],
        });
      }),
    );
    renderPage();

    await user.click(await treeItem('办公网段'));
    await user.click(screen.getByRole('tab', { name: '扫描' }));

    expect(await screen.findByText('扫描已开启')).toBeInTheDocument();
    expect(screen.getByText('完成')).toBeInTheDocument();
    // THE RANGE TRAVELS AS A FILTER, so the scan tab answers "what has been
    // scanned here" rather than "what has been scanned anywhere".
    await waitFor(() => expect(scanQuery).toContain('cidr='));
    expect(decodeURIComponent(scanQuery)).toContain('10.0.0.0/24');
  });
});
