import { expect, test } from '@playwright/test';

// Responsive screenshots for the CMDB pages added in this branch.
//
// THE API IS MOCKED. These tests are about layout and theme, not the service:
// the real endpoints need a database and a populated estate, and a screenshot
// suite that only runs on a seeded deployment is a suite that never runs. The
// mocks carry one realistic row per page so an empty state is not mistaken for
// the layout.

test.use({ storageState: { cookies: [], origins: [] } });

const RULE = {
  id: 1, name: '阿里云', type: 'http', is_inner: true, is_plugin: false,
  unique_key: 'instance_id', attribute_count: 12, has_plugin_script: false,
};
const HOST = {
  id: 1, name: 'host', alias: '主机', enabled: true, is_attached: true,
  icon: '', order: 0, unique_key: 'hostname', parent_ids: [],
};
const USER = {
  id: 7, email: 'ops@example.test', display_name: '运维', phone: '',
  role: 'admin', status: 'active', created_at: '', updated_at: '',
};
const ATTRIBUTE_RECORD = {
  record_id: 2, uid: 7, type_id: 1, type_name: 'host', type_alias: '主机',
  origin: 'ui', created_at: '2026-10-26T11:00:00Z',
  changes: [{
    id: 20, record_id: 2, uid: 7, operate_type: '2', ci_id: 100, ci_name: 'web-01',
    attr_id: 10, attr_name: 'hostname', attr_alias: '主机名', value_type: '2',
    old: 'web-00', new: 'web-01', created_at: '2026-10-26T11:00:00Z',
  }],
};
const TOPOLOGY_VIEW = {
  id: 7, name: '机房拓扑', group_id: 1, central_node_type: 1,
  central_node_instances: 'hostname:web*', path: { '1': [2] }, order: 1,
};
const TOPOLOGY_GROUP = { id: 1, name: '网络', order: 1, views: [TOPOLOGY_VIEW] };
const POOL_ENTRY = {
  id: 1, type_id: 1, adt_id: 5, unique_value: 'i-1',
  instance: { hostname: 'i-1', cpu: 4 }, oneagent_id: 'agent-1', oneagent_name: 'agent one',
  is_accept: false, adr_name: '阿里云', adr_type: 'http', is_inner: true,
  updated_at: '2026-10-26 10:00:00',
};

type PageCase = {
  path: string;
  heading: string;
  headingEn: string;
  name: string;
};

const PAGES: PageCase[] = [
  { path: '/cmdb/discovery', heading: '自动发现', headingEn: 'Auto discovery', name: 'discovery' },
  { path: '/cmdb/adc', heading: '自动发现池', headingEn: 'Discovery pool', name: 'discovery-pool' },
  { path: '/cmdb/operation-history', heading: '操作历史', headingEn: 'Operation history', name: 'operation-history' },
  { path: '/cmdb/topology-views', heading: '拓扑视图', headingEn: 'Topology views', name: 'topology-views' },
  { path: '/cmdb/preferences', heading: '偏好设置', headingEn: 'Preferences', name: 'preferences' },
  { path: '/cmdb/tree-views', heading: '资源层级', headingEn: 'Resource hierarchy', name: 'tree-views' },
  { path: '/cmdb/relations', heading: '模型关系', headingEn: 'Model relations', name: 'relations' },
  { path: '/cmdb/batch', heading: '批量导入', headingEn: 'Batch import', name: 'batch-import' },
  { path: '/cmdb/service-tree', heading: '服务树', headingEn: 'Service tree', name: 'relation-views' },
];

async function mockAPI(page: import('@playwright/test').Page) {
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    switch (path) {
      case '/api/v1/me':
        await route.fulfill({ json: { id: 7, email: USER.email, display_name: USER.display_name, phone: '', role: 'admin', status: 'active', memberships: [] } });
        return;
      case '/api/v1/users':
        await route.fulfill({ json: { items: [USER], total: 1 } });
        return;
      case '/api/v1/cmdb/ci-types':
        await route.fulfill({ json: { items: [HOST], total: 1 } });
        return;
      case '/api/v1/cmdb/discovery/rules':
        await route.fulfill({ json: { items: [RULE], total: 1 } });
        return;
      case '/api/v1/cmdb/discovery/adc/ci-types':
        await route.fulfill({ json: { items: [{ type_id: 1, name: 'host', alias: '主机', icon: '', target_count: 1 }], total: 1 } });
        return;
      case '/api/v1/cmdb/discovery/adc/counter':
        await route.fulfill({ json: { rule_count: 1, exec_target_count: 1, instance_count: 1, accept_count: 0, this_month_count: 1, this_week_count: 1, last_month_count: 0, last_week_count: 0 } });
        return;
      case '/api/v1/cmdb/discovery/adc/ci-types/1/attributes':
        await route.fulfill({ json: { items: [{ id: 10, name: 'hostname', alias: '主机名', value_type: '2', is_list: false }], total: 1 } });
        return;
      case '/api/v1/cmdb/discovery/adc':
        await route.fulfill({ json: { items: [POOL_ENTRY], total: 1, page: 1, page_size: 20 } });
        return;
      case '/api/v1/cmdb/history/attributes':
        await route.fulfill({ json: { items: [ATTRIBUTE_RECORD], total: 1, page: 1, page_size: 20 } });
        return;
      case '/api/v1/cmdb/topology-views/groups':
        await route.fulfill({ json: { items: [TOPOLOGY_GROUP], total: 1 } });
        return;
      case '/api/v1/cmdb/topology-views':
        await route.fulfill({ json: { items: [TOPOLOGY_VIEW], total: 1 } });
        return;
      case '/api/v1/cmdb/topology-views/7/graph':
        await route.fulfill({ json: {
          nodes: [
            { id: 100, type_id: 1, name: 'web-01' },
            { id: 101, type_id: 1, name: 'web-02' },
          ],
          links: [{ from: 100, to: 101 }],
          type2meta: { '1': 'icon-host' },
        } });
        return;
      case '/api/v1/cmdb/preferences/ci-types':
        await route.fulfill({ json: { groups: [], type_ids: [] } });
        return;
      case '/api/v1/cmdb/preferences/auto-subscription':
        await route.fulfill({ json: { base_strategy: 'all', group_ids: [], type_ids: [], enabled: false } });
        return;
      case '/api/v1/cmdb/preferences/tree-levels':
        await route.fulfill({ json: [] });
        return;
      default:
        await route.fulfill({ json: { items: [], total: 0 } });
    }
  });
}

for (const pageDef of PAGES) {
  const widths = [390, 768, 1280, 1920];
  const themes = ['light', 'dark'] as const;
  const locales = ['zh-CN', 'en-US'] as const;
  for (const width of widths) {
    for (const theme of themes) {
      for (const locale of locales) {
        test(`${pageDef.name} ${width}px ${theme} ${locale}`, async ({ page }, testInfo) => {
          await page.setViewportSize({ width, height: 900 });
          await page.addInitScript(({ locale, theme }) => {
            localStorage.setItem('ongrid-locale', locale);
            localStorage.setItem('ongrid-theme-preference', theme);
            localStorage.setItem('ongrid.auth', JSON.stringify({
              state: {
                token: 'e2e-token', refreshToken: null,
                email: 'e2e@example.com', role: 'admin',
              },
              version: 0,
            }));
          }, { locale, theme });
          await mockAPI(page);

          await page.goto(pageDef.path);
          await expect(page.getByRole('heading', { name: locale === 'en-US' ? pageDef.headingEn : pageDef.heading })).toBeVisible();
          await expect(page.locator('html')).toHaveClass(theme === 'light' ? /\blight\b/ : /\bdark\b/);
          // Let a lazy route and a mocked fetch settle before measuring.
          await page.waitForTimeout(300);

          const overflow = await page.evaluate(
            () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
          );
          expect(overflow).toBeLessThanOrEqual(1);

          await page.screenshot({
            path: testInfo.outputPath(`${pageDef.name}-${width}-${theme}-${locale}.png`),
            fullPage: false,
          });
        });
      }
    }
  }
}
