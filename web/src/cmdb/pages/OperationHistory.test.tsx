import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { fullDateTime } from '@/lib/format';
import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { useAuth } from '@/store/auth';
import OperationHistoryPage from './OperationHistory';
import { ToastProvider } from '../components/Toast';

const host = { id: 1, name: 'host', alias: '主机', enabled: true, is_attached: true, icon: '', order: 0, unique_key: 'hostname', parent_ids: [] };

const attributeRecord = {
  record_id: 2,
  uid: 7,
  type_id: 1,
  type_name: 'host',
  type_alias: '主机',
  origin: 'ui',
  created_at: '2026-10-26T11:00:00Z',
  changes: [
    {
      id: 20,
      record_id: 2,
      uid: 7,
      operate_type: '2',
      ci_id: 100,
      ci_name: 'web-01',
      attr_id: 10,
      attr_name: 'hostname',
      attr_alias: '主机名',
      value_type: '2',
      old: 'web-00',
      new: 'web-01',
      created_at: '2026-10-26T11:00:00Z',
    },
  ],
};

function baseHandlers() {
  server.use(
    http.get('/api/v1/users', () =>
      HttpResponse.json({
        items: [{ id: 7, email: 'ops@example.test', display_name: '运维', phone: '', role: 'admin', status: 'active', created_at: '', updated_at: '' }],
        total: 1,
      }),
    ),
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
    http.get('/api/v1/cmdb/history/attributes', () =>
      HttpResponse.json({ items: [attributeRecord], total: 1, page: 1, page_size: 20 }),
    ),
  );
}

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <OperationHistoryPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the operation history page', () => {
  it('is admin-only', async () => {
    useAuth.setState({ token: null, role: 'user' });
    renderPage();

    expect(await screen.findByText('需要管理员权限')).toBeInTheDocument();
    expect(screen.queryByRole('tab', { name: '属性变更' })).not.toBeInTheDocument();
  });

  it('lists attribute changes with the user and attribute names', async () => {
    let query = '';
    baseHandlers();
    server.use(
      http.get('/api/v1/cmdb/history/attributes', ({ request }) => {
        query = request.url;
        return HttpResponse.json({ items: [attributeRecord], total: 1, page: 1, page_size: 20 });
      }),
    );
    renderPage();

    expect(await screen.findByRole('heading', { name: '操作历史' })).toBeInTheDocument();
    expect(await screen.findByRole('tab', { name: '属性变更' })).toBeInTheDocument();
    expect(await screen.findByText('运维')).toBeInTheDocument();
    expect(screen.getByText('主机')).toBeInTheDocument();
    expect(screen.getByText('主机名')).toBeInTheDocument();
    expect(screen.getByText('web-00')).toBeInTheDocument();
    expect(screen.getByText(fullDateTime(attributeRecord.created_at))).toBeInTheDocument();
    expect(screen.getByText('web-01')).toBeInTheDocument();
    expect(query).toContain('page=1');
    expect(query).toContain('page_size=20');
  });

  it('switches to relation changes and names both ends', async () => {
    baseHandlers();
    server.use(
      http.get('/api/v1/cmdb/history/relations', () =>
        HttpResponse.json({
          items: [
            {
              record_id: 5,
              uid: 7,
              type_id: 1,
              type_name: 'host',
              type_alias: '主机',
              created_at: '2026-10-26T12:00:00Z',
              changes: [
                {
                  id: 30,
                  record_id: 5,
                  uid: 7,
                  operate_type: '0',
                  first_ci_id: 100,
                  second_ci_id: 101,
                  first_ci_name: 'web-01',
                  second_ci_name: 'sw-01',
                  relation_type_id: 3,
                  relation_id: 9,
                },
              ],
            },
          ],
          total: 1,
          page: 1,
          page_size: 20,
        }),
      ),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('tab', { name: '关系变更' }));
    expect(await screen.findByRole('columnheader', { name: '第一端' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: '第二端' })).toBeInTheDocument();
    expect(screen.getByText('web-01')).toBeInTheDocument();
    expect(screen.getByText('sw-01')).toBeInTheDocument();
  });

  it('passes the model filter to the server', async () => {
    let query = '';
    baseHandlers();
    server.use(
      http.get('/api/v1/cmdb/history/attributes', ({ request }) => {
        query = request.url;
        return HttpResponse.json({ items: [attributeRecord], total: 1, page: 1, page_size: 20 });
      }),
    );
    renderPage();

    await screen.findByRole('tab', { name: '属性变更' });
    await selectOption(screen.getByLabelText('模型'), '主机');
    await waitFor(() => expect(query).toContain('type_id=1'));
  });

  it('shows a record’s changes in a drawer', async () => {
    baseHandlers();
    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: '查看详情 2' }));
    const drawer = await screen.findByRole('dialog');
    expect(drawer).toHaveTextContent('变更明细');
    expect(drawer).toHaveTextContent('主机名');
    expect(drawer).toHaveTextContent('web-00');
    expect(drawer).toHaveTextContent('web-01');
  });
});
