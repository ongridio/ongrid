// Run in ego-browser against a trace detail with a model step selected.
// The Vite fixture /e2e/fixtures/agent-reasoning.html covers long model names.
import assert from 'node:assert/strict';

export async function checkAgentReasoningLayout(page, screenshots) {
  const previous = await page.evaluate(() => ({ locale: localStorage.getItem('ongrid-locale'), classes: document.documentElement.className }));
  const results = [];
  try {
    for (const width of [390, 768, 1280, 1920]) {
      await page.cdp('Emulation.setDeviceMetricsOverride', { width, height: 1000, deviceScaleFactor: 1, mobile: false });
      for (const locale of ['zh-CN', 'en-US']) for (const theme of ['light', 'dark']) {
        await page.evaluate(({ locale, theme }) => {
          localStorage.setItem('ongrid-locale', locale);
          window.dispatchEvent(new CustomEvent('ongrid-locale-change', { detail: locale }));
          document.documentElement.classList.remove('dark', 'light');
          document.documentElement.classList.add(theme);
        }, { locale, theme });
        const label = locale === 'zh-CN' ? '推理级别' : 'Reasoning effort';
        await page.waitForFunction(label => document.querySelector('.agent-step-header')?.textContent.includes(label), label);
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(null)))));
        const layout = await page.evaluate(label => {
          const header = document.querySelector('.agent-step-header');
          const bounds = el => { const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom }; };
          const captions = [...document.querySelectorAll('.agent-step-caption')].filter(el => el.textContent.includes(label));
          return {
            width: innerWidth, scroll: document.documentElement.scrollWidth, header: bounds(header),
            metadata: [...header.querySelectorAll('span')].filter(el => el.textContent.includes(label)).map(bounds),
            captions: captions.map(el => ({ ...bounds(el), parent: bounds(el.closest('.agent-step-button')) })),
          };
        }, label);
        assert(layout.scroll <= layout.width + 1, `${width}/${locale}/${theme}: page overflows`);
        assert(layout.header.left >= -1 && layout.header.right <= width + 1, 'Model header is clipped');
        assert(layout.captions.length && layout.metadata.length, 'Reasoning metadata is missing');
        for (const box of layout.metadata) assert(box.left >= layout.header.left - 1 && box.right <= layout.header.right + 1, 'Reasoning header overflows');
        for (const box of layout.captions) assert(box.left >= box.parent.left - 1 && box.right <= box.parent.right + 1 && box.bottom <= box.parent.bottom + 1, 'Reasoning caption leaves its step');
        if (screenshots && ((width === 390 && locale === 'zh-CN' && theme === 'dark') || (width === 1280 && locale === 'en-US' && theme === 'light'))) {
          await page.screenshot({ path: `${screenshots}/${width}-${locale}-${theme}.png` });
        }
        results.push({ width, locale, theme });
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
