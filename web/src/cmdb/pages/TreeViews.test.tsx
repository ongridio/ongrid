import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import TreeViewsPage from './TreeViews';
import { ToastProvider } from '../components/Toast';

const attribute = (id: number, name: string, alias: string) => ({
  id,
  name,
  alias,
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
});

const idc = attribute(10, 'idc', '机房');
const rack = attribute(11, 'rack', '机柜');

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <TreeViewsPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the tree views page', () => {
  it('lazy-loads facets and shows the instances at the selected path', async () => {
    server.use(
      http.get('/api/v1/cmdb/preferences/tree-levels', () =>
        HttpResponse.json([
          { id: 7, type_id: 1, name: 'host', alias: '主机', levels: [idc, rack] },
        ]),
      ),
      http.post('/api/v1/cmdb/ci/s', async ({ request }) => {
        const body = (await request.json()) as { q: string; facet: string[] };
        if (body.facet?.[0] === 'idc') {
          return HttpResponse.json({
            numfound: 2,
            total: 0,
            page: 1,
            facet: { idc: [['A', 2, 'idc']] },
            result: [],
          });
        }
        if (body.facet?.[0] === 'rack') {
          return HttpResponse.json({
            numfound: 2,
            total: 0,
            page: 1,
            facet: { rack: [['R1', 2, 'rack']] },
            result: [],
          });
        }
        expect(body.q).toContain('idc:A');
        expect(body.q).toContain('rack:R1');
        return HttpResponse.json({
          numfound: 1,
          total: 1,
          page: 1,
          result: [
            {
              _id: 11,
              _type: 1,
              ci_id: 11,
              ci_type: 'host',
              ci_type_alias: '主机',
              unique: 'web-01',
              unique_alias: 'hostname',
              hostname: 'web-01',
              idc: 'A',
              rack: 'R1',
            },
          ],
        });
      }),
    );

    renderPage();

    await userEvent.click(await screen.findByRole('button', { name: /A/ }));
    await userEvent.click(await screen.findByRole('button', { name: /R1/ }));

    await waitFor(() => expect(screen.getByText('web-01')).toBeInTheDocument());
  });

  it('links to preferences when no tree view exists', async () => {
    server.use(
      http.get('/api/v1/cmdb/preferences/tree-levels', () => HttpResponse.json([])),
    );
    renderPage();

    expect(await screen.findByText('还没有可用的层级视图')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '去偏好设置' })).toHaveAttribute('href', '/cmdb/preferences');
  });
});
