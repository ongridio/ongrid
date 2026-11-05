// Run against /settings/llm after expanding a provider's reasoning controls.
import assert from 'node:assert/strict';

export async function checkReasoningSettingsLayout(page, screenshots) {
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
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(null)))));
        const geometry = await page.evaluate(() => ({
          width: innerWidth, scroll: document.documentElement.scrollWidth,
          controls: [...document.querySelectorAll('[data-testid^="llm-provider-"] input, [data-testid^="llm-provider-"] button')]
            .filter(el => el.getBoundingClientRect().width > 0 && el.getAttribute('type') !== 'hidden' && el.getAttribute('aria-hidden') !== 'true')
            .map(el => { const r = el.getBoundingClientRect(); const card = el.closest('[data-testid^="llm-provider-"]').getBoundingClientRect(); return { label: el.getAttribute('aria-label') || el.textContent, left: r.left, right: r.right, cardLeft: card.left, cardRight: card.right }; }),
        }));
        assert(geometry.scroll <= geometry.width + 1, `${width}/${locale}/${theme}: page overflow`);
        for (const control of geometry.controls) assert(control.left >= control.cardLeft - 1 && control.right <= control.cardRight + 1, `${width}: ${control.label} outside its card`);
        if (screenshots && ((width === 390 && locale === 'zh-CN' && theme === 'dark') || (width === 1280 && locale === 'en-US' && theme === 'light'))) {
          await page.screenshot({ path: `${screenshots}/${width}-${locale}-${theme}.png` });
        }
        results.push({ width, locale, theme, controls: geometry.controls.length });
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
