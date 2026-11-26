import { render, screen } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import ServiceTreePage from './ServiceTree';
import { ToastProvider } from '../components/Toast';

const model = (id: number, name: string, alias: string) => ({
  id,
  name,
  alias,
  enabled: true,
  is_attached: true,
  icon: '',
  order: 0,
  unique_key: 'name',
  parent_ids: [],
});

const models = [model(1, 'idc', '机房'), model(2, 'rack', '机柜'), model(3, 'host', '主机')];

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/cmdb/service-tree']}>
      <ToastProvider>
        <ServiceTreePage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function handlers(definitions: unknown[]) {
  server.use(
    http.get('/api/v1/cmdb/preferences/service-trees', () => HttpResponse.json({ items: definitions, total: definitions.length })),
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: models, total: models.length })),
    http.get('/api/v1/cmdb/ci-types/1/attributes', () =>
      HttpResponse.json({ type_id: 1, unique_id: 10, unique: 'name', attributes: [] }),
    ),
    http.get('/api/v1/cmdb/ci-types/3/attributes', () =>
      HttpResponse.json({ type_id: 3, unique_id: 30, unique: 'name', attributes: [] }),
    ),
    http.get('/api/v1/cmdb/ci-types/1/instances', () =>
      HttpResponse.json({
        type_id: 1,
        items: [{ ci_id: 101, type_id: 1, values: { name: 'dc-north' } }],
        total: 1,
        numfound: 1,
        page: 1,
      }),
    ),
    http.get('/api/v1/cmdb/ci-types/3/instances', () =>
      HttpResponse.json({ type_id: 3, items: [{ ci_id: 301, type_id: 3, values: { name: 'web-01' } }], total: 1, numfound: 1, page: 1 }),
    ),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the definition selects the shape', () => {
  it('starts from the model that is never a child, and counts the chain', async () => {
    handlers([
      {
        id: 5,
        name: '机房树',
        is_public: true,
        // 机房 → 机柜 → 主机. The ROOT is 机房 because it never appears as a child
        // — that is what makes the pairs a tree rather than a bag of edges.
        cr_ids: [
          { parent_id: 1, child_id: 2 },
          { parent_id: 2, child_id: 3 },
        ],
      },
    ]);
    renderPage();

    expect(await screen.findByText(/顶层模型/)).toBeInTheDocument();
    expect(screen.getByText('机房')).toBeInTheDocument();
    expect(screen.getByText('可展开 2 层')).toBeInTheDocument();
    // The roots are the top model's instances, not the leaf model's.
    expect(await screen.findByText('dc-north')).toBeInTheDocument();
    expect(screen.queryByText('web-01')).not.toBeInTheDocument();
  });

  it('uses the long chain’s length rather than the pair count', async () => {
    handlers([
      {
        id: 6,
        name: '两棵树',
        is_public: false,
        // TWO BRANCHES: 机房 → 机柜 and 机房 → 主机. The depth is one, although
        // there are two pairs — counting pairs would offer a level that does not
        // exist.
        cr_ids: [
          { parent_id: 1, child_id: 2 },
          { parent_id: 1, child_id: 3 },
        ],
      },
    ]);
    renderPage();

    expect(await screen.findByText('可展开 1 层')).toBeInTheDocument();
  });

  it('points at the definitions page when there are none', async () => {
    handlers([]);
    renderPage();

    expect(await screen.findByText(/还没有服务树定义/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /先创建一条/ })).toHaveAttribute('href', '/cmdb/service-trees');
  });

  it('shows a definition whose pairs no longer resolve instead of hiding it', async () => {
    handlers([{ id: 7, name: '旧定义', is_public: false, cr_ids: [] }]);
    renderPage();

    // THE ENTRY IS STILL IN THE PICKER AND THE PAGE SAYS WHY IT CANNOT RENDER.
    // Hiding it would leave an operator unable to see, let alone fix, the broken
    // definition.
    expect(await screen.findByText('旧定义')).toBeInTheDocument();
    expect(screen.getByText('这个定义没有可解析的模型对')).toBeInTheDocument();
  });
});
