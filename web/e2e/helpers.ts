import { expect, type Page } from '@playwright/test';

export function adminCredentials() {
  const email = process.env.E2E_ADMIN_EMAIL || process.env.ONGRID_ADMIN_EMAIL;
  const password = process.env.E2E_ADMIN_PASSWORD || process.env.ONGRID_ADMIN_PASSWORD;
  if (!email || !password) {
    throw new Error('E2E_ADMIN_EMAIL/E2E_ADMIN_PASSWORD or ONGRID_ADMIN_EMAIL/ONGRID_ADMIN_PASSWORD is required');
  }
  return { email, password };
}

export async function login(page: Page) {
  const { email, password } = adminCredentials();
  await page.goto('/login');
  await page.getByLabel(/邮箱|Email/).fill(email);
  await page.getByLabel(/密码|Password/).fill(password);
  await page.getByRole('button', { name: /登录|Sign in/ }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('link', { name: /Ongrid (首页|home)/ })).toBeVisible();
}

export function trackPageErrors(page: Page) {
  const errors: Error[] = [];
  page.on('pageerror', (error) => errors.push(error));
  return () => expect(errors, errors.map((error) => error.message).join('\n')).toEqual([]);
}
