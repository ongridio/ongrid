import { expect, test } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

for (const width of [390, 768, 1280, 1920]) for (const locale of ['zh-CN', 'en-US']) for (const theme of ['light', 'dark']) {
  test(`page header keeps text readable at ${width}px ${locale} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.addInitScript(({ locale, theme }) => {
      localStorage.setItem('ongrid-locale', locale);
      localStorage.setItem('ongrid-theme-preference', theme);
      localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'e2e-token', refreshToken: null, role: 'admin', email: 'e2e@example.com' }, version: 0 }));
    }, { locale, theme });
    await page.route('**/api/v1/**', route => route.fulfill({ json: { items: [], total: 0 } }));
    for (const url of ['/mcp', '/workflows']) {
      await page.goto(url);
      const header = page.locator('main > header');
      await expect(header.locator('h1')).toBeVisible();
      const layout = await header.evaluate(header => {
        const row = header.firstElementChild!;
        const text = row.children[0].getBoundingClientRect();
        const actions = row.children[1].getBoundingClientRect();
        return { textWidth: text.width, rowWidth: row.clientWidth, textBottom: text.bottom, actionsTop: actions.top, overflow: header.scrollWidth - header.clientWidth };
      });
      expect(layout.overflow).toBeLessThanOrEqual(1);
      if (width < 640) {
        expect(layout.textWidth).toBeGreaterThanOrEqual(layout.rowWidth - 1);
        expect(layout.actionsTop).toBeGreaterThanOrEqual(layout.textBottom);
      }
      await page.screenshot({ path: test.info().outputPath(`${url.slice(1)}.png`) });
    }
  });
}
