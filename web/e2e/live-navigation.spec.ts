import { resolve } from 'node:path';
import { test, expect } from '@playwright/test';
import { login, trackPageErrors } from './helpers';

const artifactsDir = resolve(process.cwd(), '../output/playwright');

test.describe('live frontend interaction', () => {
  test('P1 login renders the authenticated workbench', async ({ browser }) => {
    const context = await browser.newContext({
      baseURL: process.env.E2E_BASE_URL || 'https://localhost:8443',
      ignoreHTTPSErrors: true,
      storageState: { cookies: [], origins: [] },
    });
    const page = await context.newPage();
    await login(page);
    await expect(page.getByRole('link', { name: /仪表盘|Dashboard/ })).toBeVisible();
    await expect(page.getByRole('link', { name: /设备|Devices/ })).toBeVisible();
    await context.close();
  });

  const routes: Array<[string, RegExp]> = [
    ['/dashboard', /仪表盘|Dashboard/],
    ['/devices', /全部设备|All devices/],
    ['/kubernetes', /Kubernetes 集群|Kubernetes clusters/],
    ['/topology', /拓扑|Topology/],
    ['/monitor', /监控|Monitor/],
    ['/logs', /日志|Logs/],
    ['/traces', /链路|Traces/],
    ['/alerts', /告警|Alerts/],
    ['/agents', /助理|Assistants/],
    ['/workflows', /工作流|Workflows/],
    ['/skills', /技能|Skills/],
    ['/mcp', /MCP 服务|MCP servers/],
    ['/knowledge', /知识库|Knowledge base/],
    ['/knowledge/repos', /代码仓库|Code repos/],
    ['/settings/health', /设置|Settings/],
    ['/admin/users', /用户管理|Admin/],
  ];

  for (const [path, heading] of routes) {
    test(`route ${path} renders`, async ({ page }) => {
      const assertNoPageErrors = trackPageErrors(page);
      await page.goto(path);
      await expect(page.getByRole('heading', { name: heading }).first()).toBeVisible();
      assertNoPageErrors();
    });
  }

  test('P3 device row opens device detail and plugin health', async ({ page }) => {
    await page.goto('/devices');
    const rows = page.locator('tbody tr');
    await expect(rows.first()).toBeVisible();
    await rows.first().click();
    await expect(page).toHaveURL(/\/devices\/\d+/);
    await expect(page.getByRole('button', { name: /指标|Metrics/ })).toBeVisible();
    await page.getByRole('button', { name: /插件|Plugins/ }).click();
    for (const plugin of ['metrics', 'logs', 'traces', 'profiles']) {
      await expect(page.getByText(plugin, { exact: true }).first()).toBeVisible();
    }
    await expect(page.getByText(/subprocess promtail/)).toBeVisible();
  });

  test('Kubernetes cluster detail switches every resource view', async ({ page }) => {
    await page.goto('/kubernetes');
    const details = page.getByRole('link', { name: /详情|Details/ });
    await expect(details.first()).toBeVisible();
    await details.first().click();
    await expect(page).toHaveURL(/\/kubernetes\/\d+/);

    for (const tab of ['Nodes', 'Workloads', 'Pods', 'Events', 'Namespaces', 'Actions']) {
      await page.getByRole('button', { name: new RegExp(`^${tab}\\s+\\d+`) }).click();
      await expect(page).toHaveURL(new RegExp(`tab=${tab.toLowerCase()}`));
      await expect(page.getByRole('button', { name: new RegExp(`^${tab}\\s+\\d+`) })).toHaveClass(/bg-zinc-100/);
    }
  });

  test('topology view switches graph, nodes, and type management', async ({ page }) => {
    await page.goto('/topology');
    for (const tab of [/图谱|Graph/, /节点 \+ 关系|Nodes \+ relations/, /类型管理|Types/]) {
      await page.getByRole('button', { name: tab }).click();
    }
    await expect(page.getByText(/节点类型|Node types/).first()).toBeVisible();
  });

  test('P7 locale and light/dark themes update immediately', async ({ page }) => {
    const apiRequests: string[] = [];
    page.on('request', (request) => {
      if (request.url().includes('/api/v1/')) {
        apiRequests.push(request.headers()['accept-language'] || '');
      }
    });

    await page.goto('/settings/preferences');
    await page.getByRole('button', { name: 'English' }).click();
    await expect(page.getByRole('link', { name: 'Dashboard' })).toBeVisible();
    await page.goto('/devices');
    await expect.poll(() => apiRequests.includes('en-US')).toBe(true);

    await page.goto('/settings/preferences');
    await page.getByRole('button', { name: 'Light' }).click();
    await expect(page.locator('html')).toHaveClass(/light/);
    await page.screenshot({ path: resolve(artifactsDir, 'preferences-light.png'), fullPage: true });

    await page.getByRole('button', { name: 'Dark' }).click();
    await expect(page.locator('html')).not.toHaveClass(/light/);
    await page.screenshot({ path: resolve(artifactsDir, 'preferences-dark.png'), fullPage: true });
  });
});
