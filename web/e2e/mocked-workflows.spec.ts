import { expect, test, type Page } from '@playwright/test';
import { trackPageErrors } from './helpers';

const now = '2026-07-14T00:00:00Z';

async function useEnglish(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('ongrid-locale', 'en-US');
  });
}

test.describe('deterministic frontend workflows', () => {
  test('P2/P8 alert row opens a complete incident detail and preserves the full summary', async ({ page }) => {
    await useEnglish(page);
    const assertNoPageErrors = trackPageErrors(page);
    const summary =
      'CPU saturation stayed above 95% for fifteen minutes while request latency increased across the checkout service';
    const incident = {
      id: 42,
      rule_key: 'cpu_saturation',
      rule_name: 'CPU saturation',
      severity: 'critical',
      status: 'open',
      summary,
      target_type: 'edge',
      target_id: '7',
      target_name: 'edge-alpha',
      labels: { service: 'checkout', environment: 'test' },
      event_count: 3,
      fired_at: '2026-07-13T23:00:00Z',
      last_fired_at: '2026-07-13T23:15:00Z',
      updated_at: now,
    };

    await page.route('**/api/v1/alerts/incidents**', async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === '/api/v1/alerts/incidents') {
        await route.fulfill({ json: { items: [incident], total: 1 } });
        return;
      }
      if (url.pathname === '/api/v1/alerts/incidents/42/events') {
        await route.fulfill({
          json: {
            items: [
              {
                id: 1,
                incident_id: 42,
                event_type: 'fired',
                status_after: 'open',
                severity: 'critical',
                title: 'Alert fired',
                message: summary,
                actor_type: 'system',
                occurred_at: incident.fired_at,
                created_at: incident.fired_at,
              },
            ],
            total: 1,
          },
        });
        return;
      }
      if (url.pathname === '/api/v1/alerts/incidents/42/investigation') {
        await route.fulfill({
          json: {
            id: 'investigation-42',
            incident_id: 42,
            status: 'ready',
            root_cause: 'A runaway worker exhausted the CPU quota on edge-alpha.',
            affected_window: '2026-07-13T23:00:00Z/2026-07-13T23:15:00Z',
            pinpointed_target: { device_id: 'edge-alpha', service: 'checkout' },
            evidence: [{ step: 1, tool: 'query_metrics', summary: 'CPU remained above 95%.' }],
            confidence: 0.93,
            tool_call_count: 1,
            created_at: '2026-07-13T23:16:00Z',
            ready_at: '2026-07-13T23:16:05Z',
          },
        });
        return;
      }
      if (url.pathname === '/api/v1/alerts/incidents/42') {
        await route.fulfill({ json: incident });
        return;
      }
      await route.fallback();
    });
    await page.route('**/api/v1/edges**', (route) =>
      route.fulfill({ json: { items: [{ id: 70, device_id: 7, name: 'edge-alpha' }], total: 1 } }),
    );
    await page.route('**/api/v1/chat/sessions**', (route) =>
      route.fulfill({ json: { items: [], total: 0 } }),
    );
    await page.route('**/api/v1/devices/7', (route) =>
      route.fulfill({ json: { id: 7, name: 'edge-alpha', node_id: null } }),
    );

    await page.goto('/alerts');
    const summaryCell = page.getByText(summary, { exact: true });
    await expect(summaryCell).toBeVisible();
    await expect(summaryCell).toHaveAttribute('title', summary);
    await expect(page.getByText('edge-alpha · #7')).toBeVisible();

    await summaryCell.click();
    await expect(page).toHaveURL(/\/alerts\/incidents\/42$/);
    await expect(page.getByRole('heading', { name: summary })).toBeVisible();
    await expect(page.getByText('Root cause report')).toBeVisible();
    await expect(page.getByText('A runaway worker exhausted the CPU quota on edge-alpha.')).toBeVisible();
    await expect(page.getByText(/device=edge-alpha/)).toBeVisible();
    assertNoPageErrors();
  });

  test('P4 creates an alert rule with default routing and send-policy controls', async ({ page }) => {
    await useEnglish(page);
    const assertNoPageErrors = trackPageErrors(page);
    const rules: Array<Record<string, unknown>> = [];
    let submitted: Record<string, unknown> | null = null;

    await page.route('**/api/v1/alert-rules**', async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname === '/api/v1/alert-rules' && request.method() === 'GET') {
        await route.fulfill({ json: { items: rules, total: rules.length } });
        return;
      }
      if (url.pathname === '/api/v1/alert-rules' && request.method() === 'POST') {
        submitted = request.postDataJSON() as Record<string, unknown>;
        const created = {
          ...submitted,
          id: 101,
          source_type: 'user',
          created_at: now,
          updated_at: now,
        };
        rules.push(created);
        await route.fulfill({ status: 201, json: created });
        return;
      }
      await route.fallback();
    });
    await page.route('**/api/v1/alerts/runtime-info', (route) =>
      route.fulfill({ json: { evaluator_interval_seconds: 300, notify_cooldown_seconds: 600 } }),
    );
    await page.route('**/api/v1/notification-channels', (route) =>
      route.fulfill({
        json: {
          items: [
            {
              id: 9,
              name: 'default-slack',
              type: 'slack',
              enabled: true,
              endpoint: 'https://hooks.slack.test/services/example',
              created_at: now,
              updated_at: now,
            },
          ],
          total: 1,
        },
      }),
    );

    await page.goto('/alerts/rules');
    await page.getByRole('button', { name: 'New rule' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'New alert rule' })).toBeVisible();
    await dialog.getByPlaceholder('cpu_high').fill('e2e_cpu_high');
    await dialog.getByPlaceholder('CPU under load').fill('E2E CPU high');

    const defaultRouting = dialog.getByText('Default', { exact: true }).locator('..').locator('input[type="checkbox"]');
    await expect(defaultRouting).toBeChecked();
    const windowSelect = dialog.getByText('Window', { exact: true }).locator('..').locator('select');
    await expect(windowSelect).toHaveValue('0');
    await windowSelect.selectOption('600');
    await expect(dialog.getByText(/Notify only after/)).toBeVisible();

    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.getByText('E2E CPU high')).toBeVisible();
    expect(submitted).toMatchObject({
      rule_key: 'e2e_cpu_high',
      name: 'E2E CPU high',
      notify_channel_ids: [],
      notify_window_seconds: 600,
      notify_min_fires: 1,
      enabled: true,
    });
    assertNoPageErrors();
  });

  test('P5 creates a Slack notification channel and sends a test message', async ({ page }) => {
    await useEnglish(page);
    const assertNoPageErrors = trackPageErrors(page);
    const channels: Array<Record<string, unknown>> = [];
    let createBody: Record<string, unknown> | null = null;
    let testCalls = 0;

    await page.route('**/api/v1/notification-channels**', async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname === '/api/v1/notification-channels' && request.method() === 'GET') {
        await route.fulfill({ json: { items: channels, total: channels.length } });
        return;
      }
      if (url.pathname === '/api/v1/notification-channels' && request.method() === 'POST') {
        createBody = request.postDataJSON() as Record<string, unknown>;
        const created = { ...createBody, id: 201, created_at: now, updated_at: now };
        channels.push(created);
        await route.fulfill({ status: 201, json: created });
        return;
      }
      if (url.pathname === '/api/v1/notification-channels/201/test' && request.method() === 'POST') {
        testCalls += 1;
        await route.fulfill({ json: { accepted: true, message: 'accepted' } });
        return;
      }
      await route.fallback();
    });

    await page.goto('/settings/notifications');
    const slackCard = page.getByRole('heading', { name: 'Slack' }).locator('xpath=ancestor::section');
    await slackCard.getByRole('button', { name: 'New' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByRole('heading', { name: 'New Slack channel' })).toBeVisible();
    await dialog.getByLabel('Name').fill('e2e-slack-alerts');
    await dialog.getByLabel('Endpoint URL').fill('https://hooks.slack.test/services/e2e');
    await dialog.getByRole('button', { name: 'Save' }).click();

    await expect(slackCard.getByText('e2e-slack-alerts')).toBeVisible();
    await slackCard.getByRole('button', { name: 'Test' }).click();
    await expect(page.getByText('Test message sent to e2e-slack-alerts')).toBeVisible();
    expect(createBody).toMatchObject({
      name: 'e2e-slack-alerts',
      type: 'slack',
      endpoint: 'https://hooks.slack.test/services/e2e',
      enabled: true,
    });
    expect(testCalls).toBe(1);
    assertNoPageErrors();
  });

  test('P6 creates an enabled Slack two-way channel with both tokens serialized', async ({ page }) => {
    await useEnglish(page);
    const assertNoPageErrors = trackPageErrors(page);
    const apps: Array<Record<string, unknown>> = [];
    let createBody: Record<string, unknown> | null = null;

    await page.route('**/api/v1/im/apps**', async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname === '/api/v1/im/apps' && request.method() === 'GET') {
        await route.fulfill({ json: { items: apps, total: apps.length } });
        return;
      }
      if (url.pathname === '/api/v1/im/apps' && request.method() === 'POST') {
        createBody = request.postDataJSON() as Record<string, unknown>;
        const created = {
          ...createBody,
          id: 301,
          has_secret: true,
          idle_timeout_seconds: 120,
          created_at: now,
          updated_at: now,
        };
        apps.push(created);
        await route.fulfill({ status: 201, json: created });
        return;
      }
      await route.fallback();
    });

    await page.goto('/settings/channels');
    await page.getByRole('button', { name: 'New' }).click();
    const dialog = page.getByRole('dialog');
    await dialog.getByLabel('Provider').selectOption('slack');
    await dialog.getByLabel('Name (display only)').fill('e2e-slack-bot');
    await dialog.getByLabel('app_id').fill('T0E2ETEST');
    await dialog.getByLabel('app_token').fill('xapp-e2e-placeholder');
    await dialog.getByLabel('bot_token').fill('xoxb-e2e-placeholder');
    await dialog.getByLabel('allow_from (sender allowlist)').fill('U0E2ETEST');
    await dialog.getByRole('button', { name: 'Save' }).click();

    const row = page.getByRole('row').filter({ hasText: 'e2e-slack-bot' });
    await expect(row).toContainText('Slack');
    await expect(row).toContainText('stream');
    await expect(row).toContainText('Enabled');
    const serialized = JSON.parse(String(createBody?.app_secret));
    expect(serialized).toEqual({
      app_token: 'xapp-e2e-placeholder',
      bot_token: 'xoxb-e2e-placeholder',
    });
    expect(createBody).toMatchObject({
      provider: 'slack',
      mode: 'stream',
      name: 'e2e-slack-bot',
      app_id: 'T0E2ETEST',
      allow_from: 'U0E2ETEST',
      enabled: true,
    });
    assertNoPageErrors();
  });
});
