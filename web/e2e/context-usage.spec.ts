import { expect, test } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

for (const width of [390, 768, 1280, 1920]) {
  for (const theme of ['light', 'dark']) {
    const zh = width === 390 || width === 1280;
    const locale = zh ? 'zh-CN' : 'en-US';
    test(`context usage fits the composer at ${width}px in ${locale} ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 850 });
      await page.addInitScript(({ locale, theme }) => {
        localStorage.setItem('ongrid-locale', locale);
        localStorage.setItem('ongrid-theme-preference', theme);
        localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'fixture', role: 'admin' }, version: 0 }));
      }, { locale, theme });

      const model = 'observability-gateway-long-context-model';
      const selected = { provider: 'test', model };
      const usage = { ...selected, estimated: 36775, window: 256000, input_budget: 243808, known: true,
        instructions: 5495, tools: 30648, tool_results: 0, output_reserve: 8192, reported: 20813, status: 'ready' };
      const session = { ...selected, id: 'window-test', title: 'Chat', runtime_version: 'v3', context_usage: usage };
      await page.route('**/api/v1/**', route => {
        const path = new URL(route.request().url()).pathname;
        if (path.endsWith('/aiops/models')) return route.fulfill({ json: { default: selected, runtime_version: 'v3',
          providers: [{ id: selected.provider, models: [model], reasoning_efforts: { [model]: ['medium', 'high'] }, context_windows: {
            [model]: { auto: 256000, source: 'provider', options: [128000, 256000, 1050000] },
          } }] } });
        if (path.endsWith('/agents')) return route.fulfill({ json: { items: [{ name: 'specialist-network', description: '' }] } });
        if (path.endsWith('/chat/sessions') && route.request().method() === 'POST') return route.fulfill({ json: session });
        if (path.endsWith('/chat/sessions')) return route.fulfill({ json: { items: [session], total: 1 } });
        if (path.endsWith('/context')) return route.fulfill({ json: session });
        return route.fulfill({ json: { items: [], total: 0 } });
      });

      await page.goto('/chat/window-test');
      const title = zh ? '上下文用量' : 'Context usage';
      const trigger = page.getByRole('button', { name: title });
      const add = page.getByRole('button', { name: zh ? '更多操作' : 'More actions', exact: true });
      const modelPicker = page.getByRole('combobox', { name: zh ? '选择模型' : 'Select model', exact: true });
      const effort = page.getByRole('combobox', { name: zh ? '推理级别' : 'Reasoning effort' });
      const send = page.getByRole('button', { name: zh ? '发送消息' : 'Send message' });
      const toolbar = [add, trigger, modelPicker, effort, send];
      await expect(modelPicker).toHaveText(model);
      const boxes = await Promise.all(toolbar.map(async control => (await control.boundingBox())!));
      expect(await modelPicker.locator('span.truncate').evaluate(el => el.clientWidth)).toBeGreaterThan(44);
      for (const [index, box] of boxes.entries()) {
        expect(box.width).toBeGreaterThan(0);
        expect(box.x).toBeGreaterThanOrEqual(index ? boxes[index - 1].x + boxes[index - 1].width - 1 : 0);
        expect(box.x + box.width).toBeLessThanOrEqual(width);
        expect(Math.abs(box.y + box.height / 2 - boxes[0].y - boxes[0].height / 2)).toBeLessThan(2);
      }
      if (width >= 1280) expect(boxes[1].x - boxes[0].x - boxes[0].width).toBeGreaterThan(40);
      const transparent = 'rgba(0, 0, 0, 0)';
      for (const name of [zh ? '选择模型' : 'Select model', zh ? '推理级别' : 'Reasoning effort']) {
        const picker = page.getByRole('combobox', { name, exact: true });
        await picker.hover();
        await expect(picker).toHaveCSS('background-color', transparent);
        await picker.click();
        await expect(picker).toHaveAttribute('aria-expanded', 'true');
        await expect(picker).toHaveCSS('background-color', transparent);
        await page.keyboard.press('Escape');
        await expect(picker).toBeFocused();
        await expect(picker).toHaveCSS('outline-style', 'solid');
      }
      await add.click();
      await expect(add).toHaveCSS('background-color', transparent);
      const actions = page.getByRole('menu', { name: zh ? '更多操作' : 'More actions', exact: true });
      await expect(actions.getByRole('menuitem')).toHaveCount(5);
      await page.screenshot({ path: test.info().outputPath('composer-actions.png') });
      await actions.getByRole('menuitem', { name: zh ? '专家' : 'Experts' }).click();
      await page.getByRole('menuitem', { name: zh ? '网络专家' : 'Network specialist' }).click();
      const input = page.getByRole('textbox', { name: zh ? '消息输入框' : 'Message input' });
      await expect(input).toBeFocused();
      await expect(input).toHaveValue(/specialist-network/);
      await input.fill('');
      await add.click();
      await actions.getByRole('menuitem', { name: zh ? '模式' : 'Mode' }).click();
      const mode = page.getByRole('menu', { name: zh ? '模式' : 'Mode', exact: true });
      await expect(mode.getByRole('menuitemradio', { checked: true })).toHaveText(zh ? '自动联网' : 'Automatic web search');
      const modeBox = (await mode.boundingBox())!;
      expect(modeBox.x).toBeGreaterThanOrEqual(0);
      expect(modeBox.x + modeBox.width).toBeLessThanOrEqual(width);
      await mode.getByRole('menuitemradio', { name: zh ? '关闭联网搜索' : 'Web search off' }).click();
      await page.getByRole('combobox', { name: zh ? '选择模型' : 'Select model', exact: true }).hover();
      await page.screenshot({ path: test.info().outputPath('model-hover.png') });
      await trigger.click();
      await expect(trigger).toHaveCSS('background-color', transparent);
      const panel = page.getByRole('dialog', { name: title });
      await expect(panel.getByText('15.1%')).toBeVisible();
      const coverage = await panel.getByRole('meter').evaluate(el =>
        Array.from(el.children).reduce((sum, section) => sum + section.getBoundingClientRect().width, 0) / el.getBoundingClientRect().width);
      expect(coverage).toBeCloseTo(usage.estimated / usage.input_budget, 2);
      const box = (await panel.boundingBox())!;
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(box.y + box.height).toBeLessThanOrEqual((await trigger.boundingBox())!.y);
      const windowPicker = panel.getByRole('combobox');
      await expect(windowPicker).toHaveText(zh ? '自动 · 256K' : 'Auto · 256K');
      await windowPicker.click();
      await expect(page.getByRole('option', { name: '1M', exact: true })).toBeVisible();
      await page.screenshot({ path: test.info().outputPath('window-options.png') });
      await page.keyboard.press('Escape');
      await page.screenshot({ path: test.info().outputPath('composer.png') });
      await panel.getByText(zh ? '计数详情' : 'Token details').click();
      await expect(panel.locator('details dd')).toHaveText(['36.8K / 243.8K', '5.5K', '30.6K', '632', '0', '8.2K', '20.8K']);
      await page.screenshot({ path: test.info().outputPath('token-details.png') });
      await page.keyboard.press('Escape');
      await expect(trigger).toBeFocused();
      if (width === 1280 && theme === 'light') {
        await add.click();
        const chooser = page.waitForEvent('filechooser');
        await page.getByRole('menuitem', { name: '添加文件' }).click();
        await (await chooser).setFiles({ name: 'service.log', mimeType: 'text/plain', buffer: Buffer.from('服务已就绪') });
        const draft = page.getByRole('textbox', { name: '消息输入框' });
        await expect(draft).toHaveValue('service.log\n服务已就绪');
        await expect(send).toBeEnabled();
        const sent = page.waitForRequest(req => req.url().endsWith('/messages/stream') && req.method() === 'POST');
        await send.click();
        const body = (await sent).postDataJSON();
        expect(body.content).toBe('service.log\n服务已就绪');
        expect(body.web_search_enabled).toBeFalsy();

        await page.goto('/');
        await add.click();
        await actions.getByRole('menuitem', { name: '模式' }).click();
        await mode.getByRole('menuitemradio', { name: '关闭联网搜索' }).click();
        await input.fill('检查集群状态');
        const firstTurn = page.waitForRequest(req => req.url().endsWith('/messages/stream') && req.method() === 'POST');
        await send.click();
        const firstBody = (await firstTurn).postDataJSON();
        expect(firstBody.content).toBe('检查集群状态');
        expect(firstBody.web_search_enabled).toBeFalsy();
      }
    });
  }
}
