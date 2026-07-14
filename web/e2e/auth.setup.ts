import { mkdir } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test as setup } from '@playwright/test';
import { login } from './helpers';

const webDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const authFile = resolve(webDir, '../output/playwright/.auth/admin.json');

setup('authenticate as admin', async ({ page }) => {
  await login(page);
  await mkdir(dirname(authFile), { recursive: true });
  await page.context().storageState({ path: authFile });
});
