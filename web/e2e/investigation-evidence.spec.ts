import { expect, test } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

for (const width of [390, 768, 1280, 1920]) for (const locale of ['zh-CN', 'en-US']) for (const theme of ['light', 'dark']) {
  test(`RCA citations and query details fit ${width}px ${locale} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await page.addInitScript(({ locale, theme }) => {
      localStorage.setItem('ongrid-locale', locale);
      localStorage.setItem('ongrid-theme-preference', theme);
      localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'e2e-token', role: 'admin' }, version: 0 }));
    }, { locale, theme });
    const source = { id: 'E1', session_id: 'root', tool_call_id: 'call', tool: 'query_logql', state: 'timeout', target: 'checkout-api-' + 'long-name-'.repeat(30), started_at: '2026-11-04T06:20:00Z' };
    let reads = 0;
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname;
      let json: unknown = { items: [], total: 0 };
      if (path === '/api/v1/alerts/incidents/42') json = { id: 42, rule_key: 'memory_high', rule_name: 'Memory high', summary: source.target, severity: 'critical', status: 'open', fired_at: '2020-01-01T00:00:00Z', labels: {} };
      if (path.endsWith('/investigation')) json = { id: 'report', incident_id: 42, status: 'ready', root_cause: 'OOM hypothesis', confidence: 0.95, evidence: [{ summary: 'Missing OOM logs', source_ids: ['E1'], status: 'unverified', issues: ['timeout:E1'] }], evidence_check: { version: 1, status: 'unverified', root_cause: { source_ids: ['E1'], status: 'unverified', issues: ['timeout:E1'] }, sources: [source] } };
      if (path.endsWith('/evidence/E1')) {
        reads++;
        json = { source, arguments: JSON.stringify({ query: 'x'.repeat(800) }), result: '{"error":"context deadline exceeded"}', truncated: false };
      }
      await route.fulfill({ json });
    });
    await page.goto('/alerts/incidents/42');
    const evidence = page.getByTestId('investigation-evidence');
    await expect(evidence).toBeVisible();
    expect(reads).toBe(0);
    await evidence.getByRole('button').click();
    await expect(evidence.getByText(/context deadline exceeded/)).toBeVisible();
    expect(reads).toBe(1);
    const overflow = await page.locator('main').evaluate(main => {
      const bounds = main.getBoundingClientRect();
      return {
        page: main.scrollWidth - main.clientWidth,
        evidence: [...main.querySelectorAll('[data-testid="investigation-evidence"], pre')].some(el => el.scrollWidth > el.clientWidth + 1),
        buttons: [...main.querySelectorAll('button')].some(el => el.getBoundingClientRect().right > bounds.right + 1),
      };
    });
    expect(overflow).toEqual({ page: 0, evidence: false, buttons: false });
  });
}
