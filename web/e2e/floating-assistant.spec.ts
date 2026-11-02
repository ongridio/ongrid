import { expect, test, type Page } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

async function openTestPage(page: Page, width: number, zh = true) {
  await page.setViewportSize({ width, height: 820 });
  await page.addInitScript(zh => {
    localStorage.setItem('ongrid-locale', zh ? 'zh-CN' : 'en-US');
    localStorage.setItem('ongrid-theme-preference', zh ? 'light' : 'dark');
    localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'e2e-token', role: 'admin', email: 'test@example.com' }, version: 0 }));
  }, zh);
  await page.route('**/api/v1/**', route => route.fulfill({ json: { items: [], total: 0 } }));
  const longModel = 'test-provider-long-model-name-'.repeat(3) + 'pro';
  await page.route('**/api/v1/aiops/models', route => route.fulfill({ json: {
    providers: [{ id: 'test', label: 'Test', models: ['fast', longModel] }], default: { provider: 'test', model: 'fast' },
  } }));
  await page.goto('/settings/preferences');
  return longModel;
}

for (const width of [390, 768, 1280, 1920]) {
  test(`assistant launcher, prompt cards and window fit at ${width}px`, async ({ page }) => {
    const zh = width === 390 || width === 1280;
    const longModel = await openTestPage(page, width, zh);
    const launcher = page.getByRole('button', { name: zh ? 'AI 助理' : 'AI assistant', exact: true });
    await expect(launcher).toBeVisible();
    expect((await launcher.boundingBox())!.height).toBeGreaterThanOrEqual(48);
    await launcher.click();
    const dialog = page.getByRole('dialog');
    const a = dialog.getByRole('button', { name: /了解当前页|Understand this view/ });
    const b = dialog.getByRole('button', { name: /下一步排查|Next steps/ });
    const [left, right, frame] = await Promise.all([a.boundingBox(), b.boundingBox(), dialog.boundingBox()]);
    expect(left!.height).toBeGreaterThanOrEqual(106);
    expect(left!.y).toBe(right!.y);
    expect(left!.x + left!.width).toBeLessThan(right!.x);
    expect(right!.x + right!.width).toBeLessThanOrEqual(width);
    expect(frame!.x).toBeGreaterThanOrEqual(0);
    expect(frame!.y).toBeGreaterThanOrEqual(0);
    expect(frame!.width).toBe(width === 390 ? width : 420);
    await b.click();
    await expect(dialog.getByRole('textbox')).toBeFocused();
    const model = dialog.getByLabel(zh ? '选择模型' : 'Select model', { exact: true });
    await model.click();
    const search = page.getByLabel(/搜索 选择模型|Search Select model/);
    await search.fill('pro');
    const choice = page.getByRole('option', { name: longModel });
    await expect(choice).toBeInViewport();
    await choice.click();
    await expect(model).toContainText(longModel);
    const modelBox = await model.boundingBox();
    const sendBox = await dialog.getByRole('button', { name: zh ? '发送' : 'Send', exact: true }).boundingBox();
    expect(modelBox!.x + modelBox!.width).toBeLessThan(sendBox!.x);
    expect(sendBox!.x + sendBox!.width).toBeLessThanOrEqual(width);
    const expand = dialog.getByRole('button', { name: /展开窗口|Expand window/ });
    if (width === 390) {
      await expect(expand).toHaveCount(0);
      expect(frame!.height).toBe(820);
    } else {
      await expand.click();
      expect((await dialog.boundingBox())!.width).toBe(690);
      await dialog.getByRole('button', { name: /还原窗口|Restore window/ }).click();
      expect((await dialog.boundingBox())!.width).toBe(420);
    }
    await dialog.getByRole('button', { name: /会话历史|Conversation history/ }).click();
    await expect(dialog.getByText(/还没有历史对话|No conversations yet/)).toBeVisible();
    await dialog.getByRole('button', { name: /返回对话|Back to chat/ }).click();
    await expect(dialog.getByRole('textbox')).not.toHaveValue('');
    await dialog.getByRole('button', { name: /收起助理|Hide assistant/ }).click();
    await expect(launcher).toBeFocused();
  });
}

test('dragging the launcher and desktop title frees the covered area and remembers the position', async ({ page }) => {
  await openTestPage(page, 1280);
  const launcher = page.getByRole('button', { name: 'AI 助理', exact: true });
  const dialog = page.getByRole('dialog', { name: 'Ongrid AI 助理' });
  const original = (await launcher.boundingBox())!;
  const x = original.x + original.width / 2;
  const y = original.y + original.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(45, 28, { steps: 10 });
  await page.mouse.up();
  await expect(dialog).toHaveCount(0);
  const placed = (await launcher.boundingBox())!;
  expect(placed.x).toBeCloseTo(20);
  expect(placed.y).toBeCloseTo(16);
  expect(await page.evaluate(({ x, y }) => !!document.elementFromPoint(x, y)?.closest('.og-assistant-bounds'), { x, y })).toBe(false);
  await page.reload();
  await expect(launcher).toBeVisible();
  expect((await launcher.boundingBox())!.x).toBeCloseTo(placed.x);
  await launcher.click();
  const start = (await dialog.boundingBox())!;
  expect(start.x).toBeCloseTo(placed.x);
  expect(start.y).toBeGreaterThan(placed.y + placed.height);
  await dialog.getByRole('textbox').fill('keep my draft');
  const title = dialog.getByRole('heading', { name: 'Ongrid AI 助理' });
  const handle = (await title.boundingBox())!;
  await page.mouse.move(handle.x + 10, handle.y + 8);
  await page.mouse.down();
  await page.mouse.move(handle.x + 270, handle.y + 38, { steps: 6 });
  await page.mouse.up();
  const moved = (await dialog.boundingBox())!;
  expect(moved.x - start.x).toBeCloseTo(260);
  expect(moved.y - start.y).toBeCloseTo(30);
  await title.focus();
  await title.press('ArrowRight');
  expect((await dialog.boundingBox())!.x - moved.x).toBeCloseTo(32);
  await dialog.getByRole('button', { name: '会话历史' }).click();
  await dialog.getByRole('button', { name: '返回对话' }).click();
  await expect(dialog.getByRole('textbox')).toHaveValue('keep my draft');
  await dialog.getByRole('button', { name: '展开窗口' }).click();
  await page.setViewportSize({ width: 768, height: 620 });
  const resized = (await dialog.boundingBox())!;
  expect(resized.x).toBeGreaterThanOrEqual(0);
  expect(resized.y).toBeGreaterThanOrEqual(0);
  expect(resized.x + resized.width).toBeLessThanOrEqual(768);
  expect(resized.y + resized.height).toBeLessThanOrEqual(620);
  await dialog.getByRole('button', { name: '收起助理' }).click();
  await launcher.press('Home');
  await page.setViewportSize({ width: 1280, height: 820 });
  await expect.poll(async () => (await launcher.boundingBox())!.x).toBeCloseTo(original.x);
  expect((await launcher.boundingBox())!.y).toBeCloseTo(original.y);
});

test.describe('touch placement', () => {
  test.use({ hasTouch: true });
  test('drag and cancel are distinct from tapping to open the full-screen chat', async ({ page, context }) => {
    await openTestPage(page, 390);
    const launcher = page.getByRole('button', { name: 'AI 助理', exact: true });
    const dialog = page.getByRole('dialog', { name: 'Ongrid AI 助理' });
    const session = await context.newCDPSession(page);
    const original = (await launcher.boundingBox())!;
    async function swipe(cancel: boolean) {
      await session.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: original.x + 20, y: original.y + 20 }] });
      await session.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: 50, y: 120 }] });
      await session.send('Input.dispatchTouchEvent', { type: cancel ? 'touchCancel' : 'touchEnd', touchPoints: [] });
    }
    await swipe(true);
    expect((await launcher.boundingBox())!.x).toBeCloseTo(original.x);
    await swipe(false);
    expect((await launcher.boundingBox())!.x).toBeCloseTo(30);
    await expect(dialog).toHaveCount(0);
    await launcher.tap();
    await expect(dialog).toBeVisible();
    expect((await dialog.boundingBox())!.width).toBe(390);
    await dialog.getByRole('button', { name: '收起助理' }).tap();
    expect((await launcher.boundingBox())!.y).toBeCloseTo(100);
  });
});

test('history separators do not outline the second conversation', async ({ page }) => {
  await openTestPage(page, 1280);
  await page.route('**/api/v1/chat/sessions', route => route.fulfill({ json: { total: 2, items: [
    { id: 'one', title: '设备检查', user_id: 9 }, { id: 'two', title: 'hello', user_id: 9 },
  ] } }));
  await page.getByRole('button', { name: 'AI 助理', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Ongrid AI 助理' });
  await dialog.getByRole('button', { name: '会话历史' }).click();
  const first = dialog.getByRole('button', { name: /设备检查/ });
  const hello = dialog.getByRole('button', { name: /hello/ });
  await expect(hello).toBeVisible();
  await expect(hello).toHaveCSS('border-left-color', 'rgba(0, 0, 0, 0)');
  await expect(hello).toHaveCSS('border-right-color', 'rgba(0, 0, 0, 0)');
  await first.focus();
  await page.keyboard.press('Tab');
  await expect(hello).toBeFocused();
  await expect(hello).toHaveCSS('outline-style', 'solid');
  await page.keyboard.press('Enter');
  await expect(dialog.getByRole('textbox', { name: '你的问题' })).toBeVisible();
});

test('home hides the launcher and preserves the floating draft across navigation', async ({ page }) => {
  await openTestPage(page, 1280);
  const launcher = page.getByRole('button', { name: 'AI 助理', exact: true });
  const panel = page.getByRole('dialog', { name: 'Ongrid AI 助理' });
  await launcher.click();
  await panel.getByRole('textbox', { name: '你的问题' }).fill('继续检查偏好设置');
  await page.getByRole('link', { name: 'Ongrid 首页' }).click();
  const composer = page.getByRole('textbox', { name: '消息输入框' });
  await expect(composer).toBeVisible();
  await expect(launcher).toBeHidden();
  await expect(panel).toBeHidden();
  await composer.fill('另一个问题');
  await page.keyboard.press('Control+k');
  await expect(panel.getByRole('textbox', { name: '你的问题' })).toHaveValue('继续检查偏好设置');
  await page.keyboard.press('Escape');
  await expect(composer).toBeFocused();
  await expect(composer).toHaveValue('另一个问题');
  await page.goBack();
  await launcher.click();
  await expect(panel.getByRole('textbox', { name: '你的问题' })).toHaveValue('继续检查偏好设置');
  await page.goto('/?from=test#home');
  await expect(composer).toBeVisible();
  await expect(launcher).toBeHidden();
});
