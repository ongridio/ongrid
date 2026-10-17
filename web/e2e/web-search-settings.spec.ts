import { expect, test } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });
for (const theme of ['light', 'dark']) {
  for (const width of [390, 768, 1280, 1920]) {
    test(`search settings ${theme} ${width}`, async ({ page }) => {
      const en = width === 768 || width === 1920;
      let saved: Record<string, string> = {};
      let probeFails = false;
      await page.setViewportSize({ width, height: 1000 });
      await page.addInitScript(({ theme, en }) => {
        localStorage.setItem('ongrid-theme-preference', theme);
        localStorage.setItem('ongrid-locale', en ? 'en-US' : 'zh-CN');
        localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'test-token', role: 'admin', email: 'e2e@example.com', refreshToken: null }, version: 0 }));
      }, { theme, en });
      await page.route('**/api/v1/**', async (route) => {
        const url = new URL(route.request().url());
        if (url.pathname === '/api/v1/system-settings') {
          const items = url.searchParams.get('category') === 'websearch'
            ? Object.entries(saved).map(([key, value]) => ({ category: 'websearch', key, value })) : [];
          return route.fulfill({ json: { items, total: items.length } });
        }
        if (url.pathname.endsWith('/reveal')) return route.fulfill({ json: { value: 'test-only-key' } });
        if (url.pathname === '/api/v1/integrations/websearch/test') {
          const body = route.request().postDataJSON();
          expect(body.fallback_provider).toBe('brave');
          expect(body.searxng_engines).toBe('360search,sogou');
          return route.fulfill(probeFails ? { status: 502, json: { error: 'CAPTCHA: engine unavailable' } } : { json: { status: 'ok', provider: 'searxng', sample: 'Kubernetes' } });
        }
        if (url.pathname === '/api/v1/integrations/websearch/validate-and-save') {
          saved = route.request().postDataJSON();
          return route.fulfill({ json: { status: 'saved', provider: 'searxng', sample: 'Kubernetes' } });
        }
        await route.fulfill({ json: { items: [], total: 0 } });
      });
      await page.goto('/settings/integrations');
      const card = page.locator('section').filter({ has: page.getByRole('heading', { name: en ? 'Web search (web_search skill)' : '联网搜索（web_search skill）', exact: true }) });
      const fallback = card.getByRole('combobox', { name: en ? 'Backup search provider' : '备用搜索来源' });
      await fallback.click();
      await page.getByRole('option', { name: 'Brave', exact: true }).click();
      await expect(card.getByLabel('Brave API Key', { exact: false })).toBeVisible();
      await card.getByLabel('Brave API Key', { exact: false }).fill('test-only-key');
      await card.getByLabel(en ? 'SearXNG engines' : 'SearXNG 引擎', { exact: false }).fill('360search,sogou');
      const probe = card.getByRole('button', { name: en ? 'Test connection' : '测试连接', exact: true });
      const save = card.getByRole('button', { name: en ? 'Save' : '保存', exact: true });
      await expect(save).toBeDisabled();
      await probe.click();
      await expect(save).toBeEnabled();
      await save.click();
      await expect.poll(() => saved.fallback_provider).toBe('brave');
      await expect.poll(() => saved.request_timeout_seconds).toBe('20');
      await expect(card.getByRole('button', { name: en ? 'Saved' : '已保存', exact: true })).toBeVisible();
      await fallback.scrollIntoViewIfNeeded();
      await page.screenshot({ path: `/tmp/ongrid-search-${theme}-${width}-top.png` });
      await card.getByLabel('Brave API Key', { exact: false }).scrollIntoViewIfNeeded();
      await page.screenshot({ path: `/tmp/ongrid-search-${theme}-${width}-bottom.png` });
      const box = await card.boundingBox();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(width);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      probeFails = true;
      await probe.click();
      await expect(card.getByText('✗ CAPTCHA: engine unavailable')).toBeVisible();
      await expect(card.getByRole('button', { name: en ? 'Saved' : '已保存', exact: true })).toBeDisabled();
    });
  }
}
