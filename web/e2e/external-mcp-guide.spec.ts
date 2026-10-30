import { expect, test } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

for (const width of [390, 768, 1280, 1920]) for (const locale of ['zh-CN', 'en-US']) for (const theme of ['light', 'dark']) {
  test(`WorkBuddy guide fits ${width}px ${locale} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.addInitScript(({ locale, theme }) => {
      localStorage.setItem('ongrid-locale', locale);
      localStorage.setItem('ongrid-theme-preference', theme);
      localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'e2e-token', role: 'admin', email: 'fixture@example.test' }, version: 0 }));
    }, { locale, theme });
    const endpoint = `https://${'long-server-name-'.repeat(6)}.example.test/api/v1/integrations/mcp`;
    const name = 'long-client-name-'.repeat(10);
    await page.route('**/api/v1/**', route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/api/v1/iam/mcp-tokens') return route.fulfill({ json: {
        items: [{ public_id: 'fixture-id', name, scopes: ['devices:read'], created_at: '2026-10-29T00:00:00Z', expires_at: null }],
        total: 1, enabled: true, writes_enabled: true, scopes: ['devices:read'], endpoint,
      } });
      if (path === '/api/v1/me') return route.fulfill({ json: { id: 1, email: 'fixture@example.test', role: 'admin', status: 'active' } });
      return route.fulfill({ json: { items: [], total: 0, pending: 0 } });
    });
    await page.goto('/settings/external-ai');
    const title = locale === 'zh-CN' ? '接入指南' : 'Connection guide';
    const trigger = page.getByRole('button', { name: title, exact: true });
    const guide = page.getByRole('dialog', { name: title });
    await expect(trigger).toBeEnabled();
    await expect(guide).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'WorkBuddy', exact: true })).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1);
    await page.screenshot({ path: test.info().outputPath('page-collapsed.png'), fullPage: true });
    await expect(page.getByText(name, { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: locale === 'zh-CN' ? '编辑' : 'Edit', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: locale === 'zh-CN' ? '删除' : 'Delete', exact: true })).toBeVisible();
    await trigger.focus();
    await page.keyboard.press('Enter');
    await expect(guide).toBeVisible();
    await expect(guide.getByRole('heading', { name: 'WorkBuddy', exact: true })).toBeVisible();
    await expect(guide).not.toContainText(/豆包|Doubao/);
    await expect(guide.getByRole('tab')).toHaveCount(0);
    await expect(guide).toContainText(endpoint);
    await expect(guide).toContainText('Bearer <Token>');
    const layout = await guide.evaluate(element => {
      const rect = element.getBoundingClientRect();
      const content = element.parentElement!;
      return {
        guideOverflow: element.scrollWidth - element.clientWidth,
        contentOverflow: content.scrollWidth - content.clientWidth,
        documentOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        contained: [...element.querySelectorAll('button, a, pre, dl, dt, dd, li')].filter(node => node.getClientRects().length).every(node => {
          const bounds = node.getBoundingClientRect();
          return bounds.left >= rect.left && bounds.right <= rect.right + 1;
        }),
        codeOverflow: [...element.querySelectorAll('pre, dd')].filter(node => node.getClientRects().length).map(node => node.scrollWidth - node.clientWidth),
      };
    });
    expect(layout.guideOverflow).toBeLessThanOrEqual(1);
    expect(layout.contentOverflow).toBeLessThanOrEqual(1);
    expect(layout.documentOverflow).toBeLessThanOrEqual(1);
    expect(layout.contained).toBe(true);
    expect(layout.codeOverflow.every(overflow => overflow <= 1)).toBe(true);
    await guide.screenshot({ path: test.info().outputPath('workbuddy.png') });
    await page.keyboard.press('Escape');
    await expect(guide).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await trigger.click();
    await guide.getByRole('button', { name: locale === 'zh-CN' ? '关闭' : 'Close', exact: true }).click();
    await expect(guide).toHaveCount(0);
  });
}
