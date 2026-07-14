import { existsSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, devices } from '@playwright/test';

const webDir = dirname(fileURLToPath(import.meta.url));
const repoDir = resolve(webDir, '..');
const outputDir = resolve(repoDir, 'output/playwright');
const authFile = resolve(outputDir, '.auth/admin.json');

loadLocalEnv(resolve(repoDir, 'deploy/.env'));

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  outputDir: resolve(outputDir, 'results'),
  reporter: [
    ['list'],
    ['html', { outputFolder: resolve(outputDir, 'report'), open: 'never' }],
    ['json', { outputFile: resolve(outputDir, 'results.json') }],
  ],
  use: {
    baseURL: process.env.E2E_BASE_URL || 'https://localhost:8443',
    channel: process.env.E2E_BROWSER_CHANNEL || 'chrome',
    ignoreHTTPSErrors: true,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    {
      name: 'setup',
      testMatch: /auth\.setup\.ts/,
    },
    {
      name: 'chromium',
      testIgnore: /auth\.setup\.ts/,
      dependencies: ['setup'],
      use: {
        ...devices['Desktop Chrome'],
        storageState: authFile,
      },
    },
  ],
});

function loadLocalEnv(path: string) {
  if (!existsSync(path)) return;
  for (const rawLine of readFileSync(path, 'utf8').split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;
    const index = line.indexOf('=');
    if (index <= 0) continue;
    const key = line.slice(0, index).trim();
    if (process.env[key]) continue;
    let value = line.slice(index + 1).trim();
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1);
    }
    process.env[key] = value;
  }
}
