import { expect, test } from '@playwright/test';

const identity = { service_name: 'checkout', service_namespace: 'commerce', environment: 'production' };
test.use({ storageState: { cookies: [], origins: [] } });

for (const width of [390, 768, 1280, 1920]) {
  for (const locale of ['zh-CN', 'en-US']) {
    test(`service table contains its cells and scrolls to both edges at ${width}px in ${locale}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await page.addInitScript(({ locale }) => {
        localStorage.setItem('ongrid-locale', locale);
        localStorage.setItem('ongrid-theme-preference', locale === 'zh-CN' ? 'light' : 'dark');
        localStorage.setItem('ongrid.auth', JSON.stringify({
          state: { token: 'e2e-token', refreshToken: null, email: 'e2e@example.com', role: locale === 'zh-CN' ? 'admin' : 'viewer' }, version: 0,
        }));
      }, { locale });
      await page.route('**/api/v1/**', async route => {
        const path = new URL(route.request().url()).pathname;
        if (path === '/api/v1/apm/services') {
          await route.fulfill({ json: { data: {
            items: [identity, { service_name: 'checkout-service-'.repeat(12), service_namespace: 'commerce-'.repeat(12), environment: 'production-'.repeat(12) }].map(identity => ({
              identity, languages: ['go', 'java'], rps: 12.5, error_rate: 0.01, p95_ms: 42, data_status: 'observed', protocols: [{ protocol: 'http', p95_ms: 42 }],
            })), total: 2, page: 1, page_size: 25,
          } } });
        } else if (path === '/api/v1/apm/repository-binding') {
          const name = new URL(route.request().url()).searchParams.get('service_name');
          await route.fulfill({ json: { data: name === identity.service_name ? null : { repo_id: '1', repo_url: `https://example.com/commerce/${'checkout-'.repeat(24)}.git` } } });
        } else {
          await route.fulfill({ json: { items: [], total: 0 } });
        }
      });
      await page.goto('/apm');
      await expect(page.locator('html')).toHaveClass(locale === 'zh-CN' ? /\blight\b/ : /\bdark\b/);
      const table = page.getByRole('table');
      await expect(table.getByRole('link', { name: 'checkout', exact: true })).toBeVisible();
      await expect(table.getByRole('button', { name: /^(接入配置：|Ingestion configuration: )checkout$/ })).toHaveCount(1);
      await expect(table.locator('button[title^="https://example.com/commerce/"]')).toHaveCount(1);
      const layout = await table.evaluate(table => {
        const scroller = table.parentElement!;
        const main = table.closest('main')!;
        const cells = [...table.querySelectorAll('th, td')];
        const overflowingCells = cells.filter(cell => cell.scrollWidth > cell.clientWidth + 1 && !['hidden', 'clip'].includes(getComputedStyle(cell).overflowX)).map(cell => cell.textContent);
        scroller.scrollLeft = 0;
        const first = table.querySelector('th')!.getBoundingClientRect();
        const start = scroller.getBoundingClientRect();
        const startReachable = first.left >= start.left - 1;
        scroller.scrollLeft = scroller.scrollWidth;
        const last = table.querySelector('tbody tr td:last-child button')!.getBoundingClientRect();
        const end = scroller.getBoundingClientRect();
        return {
          pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          mainOverflow: main.scrollWidth - main.clientWidth,
          overflowingCells, startReachable,
          endReachable: last.left >= end.left - 1 && last.right <= end.right + 1,
          desktopOverflow: scroller.scrollWidth - scroller.clientWidth,
        };
      });
      expect(layout.pageOverflow).toBeLessThanOrEqual(1);
      expect(layout.mainOverflow).toBeLessThanOrEqual(1);
      expect(layout.overflowingCells).toEqual([]);
      expect(layout.startReachable).toBe(true);
      expect(layout.endReachable).toBe(true);
      if (width === 1920) expect(layout.desktopOverflow).toBeLessThanOrEqual(1);
    });
  }
}
