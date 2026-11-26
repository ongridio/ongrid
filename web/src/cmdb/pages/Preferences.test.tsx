import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { useAuth } from '@/store/auth';
import PreferencesPage from './Preferences';
import { ToastProvider } from '../components/Toast';

const host = {
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

const switchModel = {
  ...host,
  id: 2,
  name: 'switch',
  alias: '交换机',
  unique_key: 'name',
};

const group = { id: 3, name: '硬件', ci_types: [host], is_other: false };

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

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <PreferencesPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

function baseHandlers() {
  server.use(
    http.get('/api/v1/cmdb/preferences/model-groups', () =>
      HttpResponse.json({ items: [group], total: 1 }),
    ),
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
    // The default Models tab embeds the existing subscriptions panel.
    http.get('/api/v1/cmdb/preferences/ci-types', () =>
      HttpResponse.json({ groups: [], type_ids: [] }),
    ),
    http.get('/api/v1/cmdb/preferences/auto-subscription', () =>
      HttpResponse.json({ base_strategy: 'all', group_ids: [], type_ids: [], enabled: false }),
    ),
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
});

describe('the preferences page', () => {
  it('keeps auto-subscribe reachable inside the embedded models tab', async () => {
    baseHandlers();
    renderPage();

    const button = await screen.findByRole('button', { name: /自动订阅/ });
    expect(button).toBeEnabled();
    await userEvent.click(button);
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  it('shows the model groups tab with the groups from the server', async () => {
    baseHandlers();
    renderPage();

    expect(await screen.findByRole('heading', { name: '偏好设置' })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('tab', { name: '模型分组' }));

    expect(await screen.findByText('硬件')).toBeInTheDocument();
    expect(screen.getByText('主机')).toBeInTheDocument();
  });

  it('assigns models to a group', async () => {
    let updated: unknown;
    server.use(
      http.get('/api/v1/cmdb/preferences/model-groups', () =>
        HttpResponse.json({ items: [group], total: 1 }),
      ),
      http.get('/api/v1/cmdb/ci-types', () =>
        HttpResponse.json({ items: [host, switchModel], total: 2 }),
      ),
      http.get('/api/v1/cmdb/preferences/ci-types', () =>
        HttpResponse.json({ groups: [], type_ids: [] }),
      ),
      http.get('/api/v1/cmdb/preferences/auto-subscription', () =>
        HttpResponse.json({ base_strategy: 'all', group_ids: [], type_ids: [], enabled: false }),
      ),
      http.put('/api/v1/cmdb/model-groups/3', async ({ request }) => {
        updated = await request.json();
        return HttpResponse.json({ ...group, ci_types: [host, switchModel] });
      }),
    );
    renderPage();
    await screen.findByRole('heading', { name: '偏好设置' });
    await userEvent.click(screen.getByRole('tab', { name: '模型分组' }));

    await userEvent.click(await screen.findByRole('button', { name: '分配模型 硬件' }));
    await userEvent.click(await screen.findByRole('checkbox', { name: /交换机/ }));
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(updated).toEqual({ type_ids: [1, 2] }));
  });

  it('saves ordered tree levels for a model', async () => {
    const idc = attribute(10, 'idc', '机房');
    const rack = attribute(11, 'rack', '机柜');
    let saved: unknown;
    server.use(
      http.get('/api/v1/cmdb/preferences/tree-levels', () =>
        HttpResponse.json([
          { id: 7, type_id: 1, name: 'host', alias: '主机', levels: [idc] },
        ]),
      ),
      http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
      http.get('/api/v1/cmdb/ci-types/1/attributes', () =>
        HttpResponse.json({ type_id: 1, unique_id: 10, unique: 'hostname', attributes: [idc, rack] }),
      ),
      http.put('/api/v1/cmdb/preferences/ci-types/1/tree-levels', async ({ request }) => {
        saved = await request.json();
        return HttpResponse.json({ id: 7, type_id: 1, name: 'host', alias: '主机', levels: [idc, rack] });
      }),
      http.get('/api/v1/cmdb/preferences/ci-types', () =>
        HttpResponse.json({ groups: [], type_ids: [] }),
      ),
      http.get('/api/v1/cmdb/preferences/auto-subscription', () =>
        HttpResponse.json({ base_strategy: 'all', group_ids: [], type_ids: [], enabled: false }),
      ),
    );
    renderPage();
    await screen.findByRole('heading', { name: '偏好设置' });
    await userEvent.click(screen.getByRole('tab', { name: '树层级' }));

    await userEvent.click(await screen.findByRole('checkbox', { name: /机柜/ }));
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() => expect(saved).toEqual({ levels: ['idc', 'rack'] }));
  });

  it('creates a saved search and rejects malformed JSON', async () => {
    let posted: unknown;
    let postCount = 0;
    server.use(
      http.get('/api/v1/cmdb/preferences/saved-searches', () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
      http.post('/api/v1/cmdb/preferences/saved-searches', async ({ request }) => {
        postCount += 1;
        posted = await request.json();
        return HttpResponse.json({ id: 9, name: '在线主机', option: { q: 'status:在线' } });
      }),
      http.get('/api/v1/cmdb/preferences/ci-types', () =>
        HttpResponse.json({ groups: [], type_ids: [] }),
      ),
      http.get('/api/v1/cmdb/preferences/auto-subscription', () =>
        HttpResponse.json({ base_strategy: 'all', group_ids: [], type_ids: [], enabled: false }),
      ),
    );
    renderPage();
    await screen.findByRole('heading', { name: '偏好设置' });
    await userEvent.click(screen.getByRole('tab', { name: '保存搜索' }));
    await userEvent.click(await screen.findByRole('button', { name: '新建保存搜索' }));

    await userEvent.type(screen.getByLabelText('名称'), '在线主机');
    const optionField = screen.getByLabelText('查询 JSON');
    await userEvent.clear(optionField);
    await userEvent.click(optionField);
    await userEvent.paste('{bad');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('JSON');
    expect(postCount).toBe(0);

    await userEvent.clear(optionField);
    await userEvent.click(optionField);
    await userEvent.paste('{"q":"status:在线"}');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() =>
      expect(posted).toEqual({ name: '在线主机', option: { q: 'status:在线' } }),
    );
  });

  it('disables shared-group writes for a non-admin', async () => {
    baseHandlers();
    useAuth.setState({ token: null, role: 'user' });
    renderPage();
    await screen.findByRole('heading', { name: '偏好设置' });
    await userEvent.click(screen.getByRole('tab', { name: '模型分组' }));

    expect(await screen.findByRole('button', { name: '新建分组' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '分配模型 硬件' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '删除 硬件' })).toBeDisabled();
  });

  it('creates a model group and refreshes the list', async () => {
    const groups: Array<typeof group> = [group];
    let posted: unknown;
    server.use(
      http.get('/api/v1/cmdb/preferences/model-groups', () =>
        HttpResponse.json({ items: groups, total: groups.length }),
      ),
      http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [host], total: 1 })),
      http.get('/api/v1/cmdb/preferences/ci-types', () =>
        HttpResponse.json({ groups: [], type_ids: [] }),
      ),
      http.get('/api/v1/cmdb/preferences/auto-subscription', () =>
        HttpResponse.json({ base_strategy: 'all', group_ids: [], type_ids: [], enabled: false }),
      ),
      http.post('/api/v1/cmdb/model-groups', async ({ request }) => {
        posted = await request.json();
        const created = { id: 4, name: '网络', ci_types: [], is_other: false };
        groups.push(created);
        return HttpResponse.json(created);
      }),
    );
    renderPage();
    await screen.findByRole('heading', { name: '偏好设置' });
    await userEvent.click(screen.getByRole('tab', { name: '模型分组' }));

    await userEvent.type(screen.getByLabelText('新分组名称'), '网络');
    await userEvent.click(screen.getByRole('button', { name: '新建分组' }));

    await waitFor(() => expect(posted).toEqual({ name: '网络' }));
    expect(await screen.findByText('网络')).toBeInTheDocument();
  });
});
