import { expect, test } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

for (const theme of ['light', 'dark']) {
  for (const width of [390, 768, 1280, 1920]) {
    test(`runtime selector ${theme} ${width}`, async ({ page }) => {
      const english = width === 768 || width === 1920;
      let selection = 'release';
      await page.setViewportSize({ width, height: 1000 });
      await page.addInitScript(({ theme, english }) => {
        localStorage.setItem('ongrid-theme-preference', theme);
        localStorage.setItem('ongrid-locale', english ? 'en-US' : 'zh-CN');
        localStorage.setItem('ongrid.auth', JSON.stringify({ state: {
          token: 'e2e-token', role: 'admin', email: 'e2e@example.com', refreshToken: null,
        }, version: 0 }));
      }, { theme, english });
      await page.route('**/api/v1/**', async (route) => {
        const path = new URL(route.request().url()).pathname;
        if (path === '/api/v1/system-settings/agent/runtime_version') {
          selection = route.request().postDataJSON().value;
          await route.fulfill({ json: {} });
        } else if (path === '/api/v1/system-settings') {
          await route.fulfill({ json: { items: [{ category: 'agent', key: 'runtime_version', value: selection }], total: 1 } });
        } else {
          await route.fulfill({ json: { items: [], total: 0 } });
        }
      });
      await page.goto('/settings/agent');
      const selector = page.getByRole('combobox', { name: english ? 'Runtime version' : '运行时版本', exact: true });
      await expect(selector).toBeEnabled();
      await selector.scrollIntoViewIfNeeded();
      await selector.click();
      await expect(page.getByRole('option', { name: /v2/ })).toBeEnabled();
      await expect(page.getByRole('option', { name: english ? 'v1 · No longer supported' : 'v1 · 不再支持' })).toBeDisabled();
      await page.screenshot({ path: `/tmp/ongrid-runtime-${theme}-${width}.png`, fullPage: true });
      await page.getByRole('option', { name: english ? 'v2 · Pinned version' : 'v2 · 固定版本', exact: true }).click();
      await page.getByRole('button', { name: english ? 'Save runtime' : '保存版本', exact: true }).click();
      await expect(page.getByRole('status')).toContainText(english ? 'Saved.' : '已保存');
      await page.reload();
      await expect(selector).toContainText(english ? 'v2 · Pinned version' : 'v2 · 固定版本');
      const box = await selector.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(width);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    });
  }
}
