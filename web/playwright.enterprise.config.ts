import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e',
  testMatch: 'enterprise-sandbox.spec.ts',
  workers: 1,
  timeout: 60000,
  outputDir: '../output/playwright/enterprise-results',
  reporter: [
    ['list'],
    ['json', { outputFile: '../output/playwright/enterprise-results.json' }],
  ],
  use: {
    baseURL: process.env.E2E_BASE_URL || 'http://127.0.0.1:5180',
    channel: 'chrome',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
});
