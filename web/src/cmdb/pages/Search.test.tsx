import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import SearchPage from './Search';
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
  is_computed: false,
  storage_table: 'c_value_index_texts',
  ...extra,
});

function renderPage(entry = '/cmdb/search?type=host') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <ToastProvider>
        <SearchPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function handlers(searchBody: object = { numfound: 2, total: 2, page: 1, counter: {}, result: [] }) {
  server.use(
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
    http.get('/api/v1/cmdb/ci-types/host/attributes', () =>
      HttpResponse.json({
        type_id: 1,
        unique_id: 10,
        unique: 'hostname',
        attributes: [attribute(10, 'hostname'), attribute(11, 'cpu', { value_type: '0' }), attribute(12, 'root_password', { is_password: true })],
      }),
    ),
    http.post('/api/v1/cmdb/ci/s', () => HttpResponse.json(searchBody)),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the expression', () => {
  it('composes the model restriction with the operator filter and shows what is sent', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    handlers();
    server.use(
      http.post('/api/v1/cmdb/ci/s', async ({ request }) => {
        sent.push(await request.json());
        return HttpResponse.json({ numfound: 0, total: 0, page: 1, counter: {}, result: [] });
      }),
    );
    renderPage();

    // THE FIRST RUN IS THE MODEL RESTRICTION ALONE.
    await waitFor(() => expect(sent).toHaveLength(1));
    expect((sent[0] as { q: string }).q).toBe('_type:host');

    await user.type(screen.getByLabelText('筛选表达式'), 'cpu:>4');
    await user.click(screen.getByRole('button', { name: /搜索/ }));

    await waitFor(() => expect(sent).toHaveLength(2));
    expect((sent[1] as { q: string }).q).toBe('_type:host,cpu:>4');
    // WHAT IS SENT IS ON SCREEN, because "I picked a model" and "my filter" are
    // two different things and both have to be visible.
    expect(screen.getByText('_type:host,cpu:>4')).toBeInTheDocument();
  });

  it('needs no model restriction when the operator turns it off', async () => {
    const user = userEvent.setup();
    const sent: unknown[] = [];
    handlers();
    server.use(
      http.post('/api/v1/cmdb/ci/s', async ({ request }) => {
        sent.push(await request.json());
        return HttpResponse.json({ numfound: 0, total: 0, page: 1, counter: {}, result: [] });
      }),
    );
    renderPage();

    await user.click(await screen.findByLabelText('只搜这个模型'));
    await user.type(screen.getByLabelText('筛选表达式'), 'hostname:web*');
    await user.click(screen.getByRole('button', { name: /搜索/ }));

    await waitFor(() => expect(sent.length).toBeGreaterThan(1));
    expect((sent.at(-1) as { q: string }).q).toBe('hostname:web*');
  });

  it('does not offer a password attribute for matching', async () => {
    handlers();
    renderPage();

    await screen.findByLabelText('属性');
    // A FILTER ON A PASSWORD ATTRIBUTE WOULD BE A CREDENTIAL ORACLE: a caller who
    // cannot read secrets could still confirm one guess at a time. The backend
    // refuses it, so the builder does not offer it.
    expect(screen.queryByRole('option', { name: 'root_password' })).not.toBeInTheDocument();
  });
});

describe('the results', () => {
  it('derives its columns from the rows, because a search spans models', async () => {
    handlers({
      numfound: 2,
      total: 2,
      page: 1,
      counter: { host: 2 },
      result: [
        { _id: 1, _type: 1, ci_id: 1, ci_type: 'host', ci_type_alias: '主机', hostname: 'web-01', cpu: 8 },
        { _id: 2, _type: 1, ci_id: 2, ci_type: 'host', ci_type_alias: '主机', hostname: 'web-02', cpu: 4 },
      ],
    });
    renderPage();

    expect(await screen.findByText('web-01')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'hostname' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'cpu' })).toBeInTheDocument();
    // THE METADATA KEYS ARE NOT COLUMNS OF THEIR OWN beyond the ones the page
    // renders deliberately.
    expect(screen.queryByRole('columnheader', { name: '_type' })).not.toBeInTheDocument();
  });

  it('turns a facet into a clause', async () => {
    const user = userEvent.setup();
    handlers({
      numfound: 1,
      total: 1,
      page: 1,
      counter: {},
      result: [{ _id: 1, _type: 1, ci_id: 1, ci_type: 'host', hostname: 'web-01' }],
      facet: { hostname: [['web-01', 1, 'hostname']] },
    });
    renderPage();

    await user.click(await screen.findByRole('button', { name: '按 web-01 筛选' }));
    // CLICKING A FACET EDITS THE EXPRESSION, and the operator can see the clause
    // it added rather than wondering why the next search changed.
    expect(screen.getByLabelText('筛选表达式')).toHaveValue('hostname:web-01');
  });

  it('distinguishes a filter that matched nothing from no filter at all', async () => {
    handlers({ numfound: 0, total: 0, page: 1, counter: {}, result: [] });
    const { unmount } = renderPage('/cmdb/search');
    unmount();

    renderPage('/cmdb/search?type=host&q=hostname:nope');
    expect(await screen.findByText('没有匹配的资源')).toBeInTheDocument();
  });
});
