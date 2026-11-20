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
            [model]: { auto: 256000, source: 'provider', options: [128000, 256000] },
          } }] } });
        if (path.endsWith('/chat/sessions')) return route.fulfill({ json: { items: [session], total: 1 } });
        if (path.endsWith('/context')) return route.fulfill({ json: session });
        return route.fulfill({ json: { items: [], total: 0 } });
      });

      await page.goto('/chat/window-test');
      const title = zh ? '上下文用量' : 'Context usage';
      const trigger = page.getByRole('button', { name: title });
      const add = page.getByRole('button', { name: zh ? '添加附件' : 'Add attachments' });
      const modelPicker = page.getByRole('combobox', { name: zh ? '选择模型' : 'Select model', exact: true });
      const effort = page.getByRole('combobox', { name: zh ? '推理级别' : 'Reasoning effort' });
      const send = page.getByRole('button', { name: zh ? '发送消息' : 'Send message' });
      const toolbar = [add, page.getByRole('switch'), trigger, modelPicker, effort, send];
      await expect(modelPicker).toHaveText(model);
      const boxes = await Promise.all(toolbar.map(async control => (await control.boundingBox())!));
      expect(await modelPicker.locator('span.truncate').evaluate(el => el.clientWidth)).toBeGreaterThan(44);
      for (const [index, box] of boxes.entries()) {
        expect(box.width).toBeGreaterThan(0);
        expect(box.x).toBeGreaterThanOrEqual(index ? boxes[index - 1].x + boxes[index - 1].width - 1 : 0);
        expect(box.x + box.width).toBeLessThanOrEqual(width);
        expect(Math.abs(box.y + box.height / 2 - boxes[0].y - boxes[0].height / 2)).toBeLessThan(2);
      }
      if (width >= 1280) expect(boxes[2].x - boxes[1].x - boxes[1].width).toBeGreaterThan(40);
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
      const search = page.getByRole('switch');
      await search.click();
      await expect(search).toHaveCSS('background-color', transparent);
      await search.click();
      await expect(search).toHaveCSS('background-color', transparent);
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
      await expect(panel.getByRole('combobox')).toBeVisible();
      await page.screenshot({ path: test.info().outputPath('composer.png') });
      await panel.getByText(zh ? '计数详情' : 'Token details').click();
      await expect(panel.getByText('20,813')).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(trigger).toBeFocused();
      if (width === 1280 && theme === 'light') {
        await add.click();
        const chooser = page.waitForEvent('filechooser');
        await page.getByRole('menuitem', { name: '插入文本文件' }).click();
        await (await chooser).setFiles({ name: 'service.log', mimeType: 'text/plain', buffer: Buffer.from('服务已就绪') });
        const draft = page.getByRole('textbox', { name: '消息输入框' });
        await expect(draft).toHaveValue('service.log\n服务已就绪');
        await expect(send).toBeEnabled();
        const sent = page.waitForRequest(req => req.url().endsWith('/messages/stream') && req.method() === 'POST');
        await send.click();
        expect((await sent).postDataJSON().content).toBe('service.log\n服务已就绪');
      }
    });
  }
}
