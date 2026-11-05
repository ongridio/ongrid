// Run from an ego-browser TaskSpace against a page with the chat composer open.
// With Vite running, /e2e/fixtures/reasoning-picker.html covers long model names.
// await (await import('/absolute/repo/web/e2e/reasoning-picker.browser.mjs')).checkReasoningPickerLayout(page, '/absolute/screenshots');
import assert from 'node:assert/strict';

export async function checkReasoningPickerLayout(page, screenshots) {
  const previous = await page.evaluate(() => ({ locale: localStorage.getItem('ongrid-locale'), classes: document.documentElement.className }));
  const results = [];
  try {
    for (const width of [390, 768, 1280, 1920]) {
      await page.cdp('Emulation.setDeviceMetricsOverride', { width, height: 1000, deviceScaleFactor: 1, mobile: false });
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(null)))));
      for (const locale of ['zh-CN', 'en-US']) {
        for (const theme of ['light', 'dark']) {
          await page.evaluate(({ locale, theme }) => {
            localStorage.setItem('ongrid-locale', locale);
            window.dispatchEvent(new CustomEvent('ongrid-locale-change', { detail: locale }));
            document.documentElement.classList.remove('dark', 'light');
            document.documentElement.classList.add(theme);
          }, { locale, theme });
          const effortLabel = locale === 'zh-CN' ? '推理级别' : 'Reasoning effort';
          await page.waitForFunction(label => !!document.querySelector(`button[aria-label="${label}"]`), effortLabel);
          const boxes = await page.evaluate(() => {
            const labels = ['选择模型', 'Select model', '推理级别', 'Reasoning effort', '发送消息', 'Send message', '发送', 'Send', '添加图片', 'Add images'];
            return { width: innerWidth, scroll: document.documentElement.scrollWidth, controls: [...document.querySelectorAll('button')]
              .filter(el => labels.includes(el.getAttribute('aria-label')) && el.getBoundingClientRect().width > 0)
              .map(el => { const r = el.getBoundingClientRect(); return { label: el.getAttribute('aria-label'), left: r.left, right: r.right, top: r.top, bottom: r.bottom }; }) };
          });
          assert(boxes.scroll <= boxes.width + 1, `${width}/${locale}/${theme}: page overflows`);
          for (const box of boxes.controls) assert(box.left >= -1 && box.right <= width + 1, `${width}: ${box.label} clipped`);
          for (let i = 0; i < boxes.controls.length; i++) for (const other of boxes.controls.slice(i + 1)) {
            const box = boxes.controls[i];
            const overlap = Math.min(box.right, other.right) - Math.max(box.left, other.left) > 1 && Math.min(box.bottom, other.bottom) - Math.max(box.top, other.top) > 1;
            assert(!overlap, `${width}: ${box.label} overlaps ${other.label}`);
          }
          await page.click(`loc=css:button[aria-label="${effortLabel}"]`);
          await page.waitForFunction(() => [...document.querySelectorAll('[role="listbox"]')].some(el => el.getBoundingClientRect().width > 0));
          const popup = await page.evaluate(() => {
            const el = [...document.querySelectorAll('[role="listbox"]')].find(el => el.getBoundingClientRect().width > 0);
            const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, labels: el.innerText };
          });
          assert(popup.left >= -1 && popup.right <= width + 1 && popup.top >= -1 && popup.bottom <= 1001, `${width}: effort popup clipped`);
          assert(popup.labels.trim(), 'Effort options are empty');
          assert(!/推理|Reasoning:|\bOff\b/.test(popup.labels), 'Efforts must use English labels without a prefix or off choice');
          assert(popup.labels.includes('Default'), 'Default effort is missing');
          if (screenshots && ((width === 390 && locale === 'zh-CN' && theme === 'dark') || (width === 1280 && locale === 'en-US' && theme === 'light'))) {
            await page.screenshot({ path: `${screenshots}/${width}-${locale}-${theme}.png` });
          }
          await page.keyboard.press('Escape');
          await page.waitForFunction(label => document.querySelector(`button[aria-label="${label}"]`)?.getAttribute('aria-expanded') === 'false', effortLabel);
          results.push({ width, locale, theme });
        }
      }
    }
  } finally {
    await page.evaluate(previous => {
      if (previous.locale === null) localStorage.removeItem('ongrid-locale');
      else localStorage.setItem('ongrid-locale', previous.locale);
      window.dispatchEvent(new CustomEvent('ongrid-locale-change', { detail: previous.locale || 'zh-CN' }));
      document.documentElement.className = previous.classes;
    }, previous);
    await page.cdp('Emulation.clearDeviceMetricsOverride');
  }
  return results;
}
