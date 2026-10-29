import { expect, test } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

for (const width of [390, 768, 1280, 1920]) for (const locale of ['zh-CN', 'en-US']) for (const theme of ['light', 'dark']) {
  test(`MCP confirmation stays readable at ${width}px ${locale} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.addInitScript(({ locale, theme }) => {
      localStorage.setItem('ongrid-locale', locale);
      localStorage.setItem('ongrid-theme-preference', theme);
      localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'e2e-token', role: 'admin', email: 'e2e@example.com' }, version: 0 }));
    }, { locale, theme });
    const approval = {
      id: 'fixture-approval', kind: 'external_mcp', status: 'pending', source: 'external_mcp', proposed_by: 1,
      title: 'MCP topology / 创建一个名称很长的临时拓扑节点 / Create a topology node with a deliberately long name',
      summary: 'Explicit user-requested change awaiting review', created_at: '2026-10-28T00:00:00Z',
      payload: JSON.stringify({ tool: 'call_api_write', arguments: { action: 'post_topology_nodes', body: { name: 'long-node-name-'.repeat(8), password: '[REDACTED]' } } }),
    };
    await page.route('**/api/v1/**', route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/api/v1/approvals/fixture-approval') return route.fulfill({ json: approval });
      if (path === '/api/v1/me') return route.fulfill({ json: { id: 1, email: 'e2e@example.com', role: 'admin', status: 'active' } });
      return route.fulfill({ json: { items: [], total: 0, pending: 1 } });
    });
    await page.goto('/approvals/fixture-approval');
    const payload = page.locator('main pre');
    await expect(payload).toContainText('post_topology_nodes');
    await expect(page.getByRole('button', { name: locale === 'zh-CN' ? '确认并执行' : 'Confirm and execute', exact: true })).toBeVisible();
    const layout = await page.locator('main').evaluate(main => {
      const bounds = main.getBoundingClientRect();
      const payload = main.querySelector('pre')!;
      const rect = payload.getBoundingClientRect();
      return {
        payloadWidth: rect.width,
        payloadOverflow: payload.scrollWidth - payload.clientWidth,
        controlsContained: [...main.querySelectorAll('button')].every(button => {
          const r = button.getBoundingClientRect();
          return r.left >= bounds.left && r.right <= bounds.right;
        }),
        pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      };
    });
    expect(layout.payloadWidth).toBeGreaterThanOrEqual(180);
    expect(layout.payloadOverflow).toBeLessThanOrEqual(1);
    expect(layout.pageOverflow).toBeLessThanOrEqual(1);
    expect(layout.controlsContained).toBe(true);
    await page.screenshot({ path: test.info().outputPath('approval.png') });
    await page.route('**/api/v1/approvals/fixture-approval', route => route.fulfill({ json: {
      ...approval, status: 'failed', reason: 'Operation cancelled by its caller',
      operation: { operation_id: 'fixture-operation', state: 'cancelled', applied: false },
    } }));
    await page.getByRole('button', { name: locale === 'zh-CN' ? '刷新状态' : 'Refresh status', exact: true }).click();
    await expect(page.getByRole('status')).toHaveText(locale === 'zh-CN' ? '已取消' : 'Cancelled');
    await expect(page.getByRole('button', { name: locale === 'zh-CN' ? '确认并执行' : 'Confirm and execute', exact: true })).toHaveCount(0);
    await page.screenshot({ path: test.info().outputPath('cancelled.png') });
  });
}

test('sign-in returns to the operation link without executing it', async ({ page }) => {
  await page.addInitScript(() => { localStorage.setItem('ongrid-locale', 'en-US'); });
  let decisions = 0;
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/approve') || path.endsWith('/reject')) decisions++;
    if (path === '/api/v1/auth/login') return route.fulfill({ json: { access_token: 'e2e-only-token', user: { role: 'admin', email: 'fixture@example.test' } } });
    if (path === '/api/v1/me') return route.fulfill({ json: { id: 1, email: 'fixture@example.test', role: 'admin', status: 'active' } });
    if (path === '/api/v1/approvals/login-target') return route.fulfill({ json: { id: 'login-target', kind: 'external_mcp', status: 'pending', source: 'external_mcp', proposed_by: 1, title: 'Login return fixture', summary: 'fixture target', payload: '{"arguments":{"name":"test-only"}}', created_at: '2026-10-29T00:00:00Z' } });
    return route.fulfill({ json: { items: [], total: 0, pending: 0 } });
  });
  await page.goto('/approvals/login-target');
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('Email').fill('fixture@example.test');
  await page.getByLabel('Password', { exact: true }).fill('test-only-password');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page).toHaveURL(/\/approvals\/login-target$/);
  await expect(page.getByRole('button', { name: 'Confirm and execute' })).toBeVisible();
  expect(decisions).toBe(0);
});
