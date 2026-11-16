import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { selectOption } from '@/test/select-option';
import { useAuth } from '@/store/auth';
import DiscoveryPage from './Discovery';
import { ToastProvider } from '../components/Toast';

const rule = {
  id: 1,
  name: '阿里云',
  type: 'http',
  is_inner: true,
  is_plugin: false,
  unique_key: 'instance_id',
  attribute_count: 12,
  has_plugin_script: false,
};

const hostAttribute = {
  id: 10,
  name: 'hostname',
  alias: '主机名',
  value_type: '2',
  is_list: false,
  is_index: true,
  is_indexed: true,
  is_required: false,
  order: 1,
  default_show: true,
  inherited: false,
  is_password: false,
  is_reference: false,
  storage_table: 'c_value_index_texts',
};

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <DiscoveryPage />
      </ToastProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  useAuth.setState({ token: null, role: 'admin' });
  // Every tab panel may mount its own loader; only the one under test needs a
  // meaningful answer, the rest get an empty but valid list.
  server.use(
    http.get('/api/v1/cmdb/discovery/rules', () => HttpResponse.json({ items: [], total: 0 })),
    http.get('/api/v1/cmdb/discovery/target-types', () => HttpResponse.json({ items: [], total: 0 })),
    http.get('/api/v1/cmdb/discovery/providers', () => HttpResponse.json({ items: [], total: 0 })),
    http.get('/api/v1/cmdb/ci-types', () => HttpResponse.json({ items: [], total: 0 })),
  );
});

describe('the discovery page', () => {
  it('lists the rules from the server', async () => {
    server.use(
      http.get('/api/v1/cmdb/discovery/rules', () =>
        HttpResponse.json({ items: [rule], total: 1 }),
      ),
    );
    renderPage();

    expect(await screen.findByRole('heading', { name: '自动发现' })).toBeInTheDocument();
    expect(await screen.findByText('阿里云')).toBeInTheDocument();
    expect(screen.getByText('http')).toBeInTheDocument();
    expect(screen.getByText('内置')).toBeInTheDocument();
  });

  it('creates a plugin rule with its script and identifying field', async () => {
    let posted: unknown;
    server.use(
      http.get('/api/v1/cmdb/discovery/rules', () => HttpResponse.json({ items: [rule], total: 1 })),
      http.post('/api/v1/cmdb/discovery/rules', async ({ request }) => {
        posted = await request.json();
        return HttpResponse.json({ ...rule, id: 2, name: '自定义采集', is_inner: false, is_plugin: true });
      }),
    );
    renderPage();

    await screen.findByText('阿里云');
    await userEvent.click(screen.getByRole('button', { name: '新建规则' }));

    await userEvent.type(screen.getByLabelText('名称'), '自定义采集');
    await userEvent.click(screen.getByRole('checkbox', { name: '插件规则' }));
    await userEvent.type(screen.getByLabelText('标识字段'), 'hostname');
    const script = screen.getByLabelText('插件脚本');
    await userEvent.click(script);
    await userEvent.paste('def collect():\n    return []');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() =>
      expect(posted).toEqual(
        expect.objectContaining({
          name: '自定义采集',
          type: 'http',
          is_plugin: true,
          unique_key: 'hostname',
          plugin_script: 'def collect():\n    return []',
        }),
      ),
    );
  });

  it('loads a rule’s accounts and saves the whole set with the rule', async () => {
    const account = {
      id: 5,
      name: '默认账号',
      adr_id: 1,
      config: { region: 'cn-hangzhou' },
      has_secret: true,
      has_password: false,
    };
    let savedRule: unknown;
    let savedAccounts: unknown;
    server.use(
      http.get('/api/v1/cmdb/discovery/rules', () => HttpResponse.json({ items: [rule], total: 1 })),
      http.get('/api/v1/cmdb/discovery/rules/1/accounts', () =>
        HttpResponse.json({ items: [account], total: 1 }),
      ),
      http.put('/api/v1/cmdb/discovery/rules/1', async ({ request }) => {
        savedRule = await request.json();
        return HttpResponse.json(rule);
      }),
      http.put('/api/v1/cmdb/discovery/rules/1/accounts', async ({ request }) => {
        savedAccounts = await request.json();
        return HttpResponse.json({ accounts: [] });
      }),
    );
    renderPage();

    await screen.findByText('阿里云');
    await userEvent.click(screen.getByRole('button', { name: '编辑 阿里云' }));
    expect(await screen.findByLabelText('账号名称 1')).toHaveValue('默认账号');
    expect(screen.getByLabelText('账号配置 1')).toHaveValue(
      JSON.stringify({ region: 'cn-hangzhou' }, null, 2),
    );

    await userEvent.click(screen.getByRole('button', { name: '添加账号' }));
    await userEvent.type(screen.getByLabelText('账号名称 2'), '备用账号');
    const config = screen.getByLabelText('账号配置 2');
    await userEvent.clear(config);
    await userEvent.click(config);
    await userEvent.paste('{"region":"cn-beijing"}');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() =>
      expect(savedAccounts).toEqual({
        accounts: [
          { id: 5, name: '默认账号', config: { region: 'cn-hangzhou' } },
          { name: '备用账号', config: { region: 'cn-beijing' } },
        ],
      }),
    );
    expect(savedRule).toEqual(expect.objectContaining({ name: '阿里云', type: 'http' }));
  });

  it('lists a model with no targets so its first target can be created', async () => {
    server.use(
      http.get('/api/v1/cmdb/discovery/target-types', () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get('/api/v1/cmdb/ci-types', () =>
        HttpResponse.json({
          items: [{ id: 1, name: 'host', alias: '主机', enabled: true, is_attached: true, icon: '', order: 0 }],
          total: 1,
        }),
      ),
      http.get('/api/v1/cmdb/discovery/ci-types/1/targets', () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('tab', { name: '目标' }));
    await userEvent.click(await screen.findByRole('button', { name: '查看 主机 的目标' }));

    expect(await screen.findByText('这个模型还没有发现目标。')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '新建目标' })).toBeEnabled();
  });

  it('creates a target with a mapping, a mode, a schedule and auto-accept', async () => {
    let posted: unknown;
    server.use(
      http.get('/api/v1/cmdb/discovery/rules', () => HttpResponse.json({ items: [rule], total: 1 })),
      http.get('/api/v1/cmdb/discovery/target-types', () =>
        HttpResponse.json({
          items: [{ type_id: 1, name: 'host', alias: '主机', icon: '', target_count: 0 }],
          total: 1,
        }),
      ),
      http.get('/api/v1/cmdb/discovery/ci-types/1/targets', () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get('/api/v1/cmdb/ci-types/1/attributes', () =>
        HttpResponse.json({ type_id: 1, unique_id: 10, unique: 'hostname', attributes: [hostAttribute] }),
      ),
      http.get('/api/v1/cmdb/discovery/ci-types/1/discoverable-attributes', () =>
        HttpResponse.json(['hostname']),
      ),
      http.post('/api/v1/cmdb/discovery/ci-types/1/targets', async ({ request }) => {
        posted = await request.json();
        return HttpResponse.json({ id: 55 });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('tab', { name: '目标' }));
    await userEvent.click(await screen.findByRole('button', { name: '查看 主机 的目标' }));
    await userEvent.click(await screen.findByRole('button', { name: '新建目标' }));

    await selectOption(screen.getByLabelText('发现规则'), '阿里云');
    await userEvent.type(screen.getByLabelText('Agent ID'), 'agent-2');
    await userEvent.type(screen.getByLabelText('执行周期'), '0 */2 * * *');
    await userEvent.click(screen.getByRole('checkbox', { name: '自动接受' }));
    await selectOption(screen.getByLabelText('映射 hostname'), '主机名');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    await waitFor(() =>
      expect(posted).toEqual(
        expect.objectContaining({
          adr_id: 1,
          attributes: { hostname: 'hostname' },
          agent_id: 'agent-2',
          query_expr: '',
          cron: '0 */2 * * *',
          auto_accept: true,
          enabled: true,
        }),
      ),
    );
  });

  it('surfaces the backend’s refusal when a target test is requested', async () => {
    const target = {
      id: 55,
      type_id: 1,
      adr_id: 1,
      attributes: { hostname: 'hostname' },
      auto_accept: false,
      agent_id: 'agent-1',
      query_expr: '',
      cron: '',
      extra_option: {},
      has_secret: false,
      has_password: false,
      enabled: true,
      adr_name: '阿里云',
      adr_type: 'http',
      is_plugin: false,
      adr_missing: false,
    };
    server.use(
      http.get('/api/v1/cmdb/discovery/target-types', () =>
        HttpResponse.json({
          items: [{ type_id: 1, name: 'host', alias: '主机', icon: '', target_count: 1 }],
          total: 1,
        }),
      ),
      http.get('/api/v1/cmdb/discovery/ci-types/1/targets', () =>
        HttpResponse.json({ items: [target], total: 1 }),
      ),
      http.post('/api/v1/cmdb/discovery/targets/55/test', () =>
        HttpResponse.text('discovery: running a probe is not implemented', { status: 501 }),
      ),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('tab', { name: '目标' }));
    await userEvent.click(await screen.findByRole('button', { name: '查看 主机 的目标' }));
    expect(await screen.findByText('阿里云')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: '测试 阿里云' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('501');
    expect(alert).toHaveTextContent('not implemented');
  });

  it('imports a template file pasted into the panel', async () => {
    let posted: unknown;
    server.use(
      http.post('/api/v1/cmdb/discovery/templates/import', async ({ request }) => {
        posted = await request.json();
        return HttpResponse.json({ created: 1, updated: 0 });
      }),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('tab', { name: '模板' }));
    const field = screen.getByLabelText('模板 JSON');
    await userEvent.clear(field);
    await userEvent.click(field);
    await userEvent.paste('{"rules":[{"name":"自定义","type":"http"}]}');
    await userEvent.click(screen.getByRole('button', { name: '导入模板' }));

    await waitFor(() =>
      expect(posted).toEqual({ rules: [{ name: '自定义', type: 'http' }] }),
    );
    expect((await screen.findAllByText('新建 1 个，更新 0 个')).length).toBeGreaterThan(0);
  });

  it('browses a provider’s resources and their fields', async () => {
    server.use(
      http.get('/api/v1/cmdb/discovery/providers', () =>
        HttpResponse.json({
          items: [{ name: '阿里云', en: 'aliyun', type: 'http', is_inner: true, is_plugin: false }],
          total: 1,
        }),
      ),
      http.get('/api/v1/cmdb/discovery/catalog/http/aliyun/categories', () =>
        HttpResponse.json([{ category: '计算', items: ['ecs'] }]),
      ),
      http.get('/api/v1/cmdb/discovery/catalog/http/aliyun/resources', () =>
        HttpResponse.json(['ecs']),
      ),
      http.get('/api/v1/cmdb/discovery/catalog/http/aliyun/attributes', () =>
        HttpResponse.json([
          { name: 'instance_id', type: 'string', desc: '实例 ID', example: 'i-bp1' },
        ]),
      ),
    );
    renderPage();

    await userEvent.click(await screen.findByRole('tab', { name: '目录' }));
    const provider = await screen.findByText('阿里云');
    expect(provider).toBeInTheDocument();

    await userEvent.click(await screen.findByRole('button', { name: 'ecs' }));
    expect(await screen.findByText('instance_id')).toBeInTheDocument();
    expect(screen.getByText('实例 ID')).toBeInTheDocument();
  });

  it('keeps rule writes disabled for a non-admin but leaves target reads available', async () => {
    useAuth.setState({ token: null, role: 'user' });
    server.use(
      http.get('/api/v1/cmdb/discovery/rules', () => HttpResponse.json({ items: [rule], total: 1 })),
    );
    renderPage();

    await screen.findByText('阿里云');
    expect(screen.getByRole('button', { name: '新建规则' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '编辑 阿里云' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '删除 阿里云' })).toBeDisabled();

    await userEvent.click(screen.getByRole('tab', { name: '模板' }));
    expect(await screen.findByRole('button', { name: '导入模板' })).toBeDisabled();
    // Export is a read and stays available to every authenticated caller.
    expect(screen.getByRole('button', { name: '导出模板' })).toBeEnabled();
  });
});

// Keep the drawer's account editor honest about the shape it round-trips.
describe('the rule drawer account editor', () => {
  it('refuses malformed account JSON', async () => {
    const account = {
      id: 5,
      name: '默认账号',
      adr_id: 1,
      config: { region: 'cn-hangzhou' },
      has_secret: false,
      has_password: false,
    };
    let ruleWrites = 0;
    server.use(
      http.get('/api/v1/cmdb/discovery/rules', () => HttpResponse.json({ items: [rule], total: 1 })),
      http.get('/api/v1/cmdb/discovery/rules/1/accounts', () =>
        HttpResponse.json({ items: [account], total: 1 }),
      ),
      http.put('/api/v1/cmdb/discovery/rules/1', () => {
        ruleWrites += 1;
        return HttpResponse.json(rule);
      }),
    );
    renderPage();

    await screen.findByText('阿里云');
    await userEvent.click(screen.getByRole('button', { name: '编辑 阿里云' }));
    const config = await screen.findByLabelText('账号配置 1');
    await userEvent.clear(config);
    await userEvent.click(config);
    await userEvent.paste('{bad');
    await userEvent.click(screen.getByRole('button', { name: '保存' }));

    const drawer = screen.getByRole('dialog');
    expect(await within(drawer).findByRole('alert')).toHaveTextContent('JSON');
    expect(ruleWrites).toBe(0);
  });
});
