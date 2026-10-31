import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import ServiceTreesPage from './ServiceTrees';
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

const relation = (parent: number, child: number) => {
  const related = (type: ReturnType<typeof model>, constraint: string) => ({
    ctr_id: 1,
    type,
    attributes: [],
    relation_type: { id: 1, name: '包含', color: '' },
    constraint,
    parent_attr_ids: [],
    child_attr_ids: [],
  });
  return { id: parent * 10 + child, parent: related(models[parent - 1], '1-N'), child: related(models[child - 1], '1-N') };
};

const relations = [relation(1, 2), relation(2, 3)];

const trees = [
  {
    id: 5,
    name: '机房树',
    is_public: true,
    cr_ids: [
      { parent_id: 1, child_id: 2 },
      { parent_id: 2, child_id: 3 },
    ],
  },
];

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <ServiceTreesPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function handlers() {
  server.use(
    http.get('/api/v1/cmdb/preferences/service-trees', () => HttpResponse.json({ items: trees, total: trees.length })),
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: models, total: models.length })),
    http.get('/api/v1/cmdb/ci-type-relations', () => HttpResponse.json({ items: relations, total: relations.length })),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the definition list', () => {
  it('renders the pairs as a readable chain', async () => {
    handlers();
    renderPage();

    expect(await screen.findByText('机房树')).toBeInTheDocument();
    expect(screen.getByText('公开')).toBeInTheDocument();
    // A DEFINITION IS A SHAPE, so the row shows the chain rather than a count:
    // 机房 → 机柜 → 主机 says what the tree will look like.
    expect(screen.getByText('机房 → 机柜 → 主机')).toBeInTheDocument();
  });

  it('points at the model relations when there are none to build from', async () => {
    server.use(
      http.get('/api/v1/cmdb/preferences/service-trees', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: models, total: models.length })),
      http.get('/api/v1/cmdb/ci-type-relations', () => HttpResponse.json({ items: [], total: 0 })),
    );
    renderPage();

    // AN EMPTY STATE THAT NAMES THE NEXT STEP, not one that just says "empty".
    expect(await screen.findByText(/先去「模型关系」里挂几个/)).toBeInTheDocument();
  });

  it('disables the writes for a non-administrator', async () => {
    useAuth.setState({ token: null, role: 'user' });
    handlers();
    renderPage();

    expect(await screen.findByRole('button', { name: /新建定义/ })).toBeDisabled();
    expect(screen.getByRole('button', { name: '编辑 机房树' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '删除 机房树' })).toBeDisabled();
  });
});

describe('creating one', () => {
  it('refuses an empty definition before sending it', async () => {
    const user = userEvent.setup();
    handlers();
    let posted = 0;
    server.use(
      http.post('/api/v1/cmdb/preferences/service-trees', () => {
        posted += 1;
        return HttpResponse.json({ id: 6, name: 'x', is_public: false, cr_ids: [] }, { status: 201 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: /新建定义/ }));
    await user.type(screen.getByLabelText('名称'), '新树');
    await user.click(screen.getByRole('button', { name: '保存' }));

    // A DEFINITION WITH NO EDGES RENDERS AN EMPTY TREE, which the backend would
    // accept and nobody could use.
    expect(await screen.findByRole('alert')).toHaveTextContent('至少选一对模型关系');
    expect(posted).toBe(0);
  });

  it('posts the model pairs when a pair is chosen', async () => {
    const user = userEvent.setup();
    handlers();
    const posted: unknown[] = [];
    server.use(
      http.post('/api/v1/cmdb/preferences/service-trees', async ({ request }) => {
        posted.push(await request.json());
        return HttpResponse.json({ id: 6, name: '新树', is_public: false, cr_ids: [] }, { status: 201 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: /新建定义/ }));
    await user.type(screen.getByLabelText('名称'), '新树');
    // The options ARE the existing model relations, so a definition cannot name
    // an edge that does not exist.
    await user.click(screen.getByText('机房 → 机柜'));
    await user.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({
      name: '新树',
      is_public: false,
      cr_ids: [{ parent_id: 1, child_id: 2 }],
    });
  });
});

describe('deleting', () => {
  it('asks first', async () => {
    const user = userEvent.setup();
    handlers();
    let deleted = 0;
    server.use(
      http.delete('/api/v1/cmdb/preferences/service-trees/:id', () => {
        deleted += 1;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderPage();

    await user.click(await screen.findByRole('button', { name: '删除 机房树' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/用它的页面就打不开了/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: '取消' }));
    expect(deleted).toBe(0);
  });
});
