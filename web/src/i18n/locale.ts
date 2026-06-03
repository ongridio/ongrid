// locale.ts — UI language. Adopts the liaison-cloud pattern: bilingual
// strings live inline at the call site via `tr('中文', 'English')` and
// `setLocale()` dispatches a window event so `useI18n()` consumers
// re-render. Storage is localStorage; no zustand needed for this.
//
// Why inline (instead of a key-based dictionary):
//   - 0 maintenance — the strings ARE the translation pair
//   - obvious from a glance whether a string is bilingual
//   - greppable: `tr\('` for total, `tr\('[^']+', *''\)` for missing-en
//   - zero bundle cost beyond the strings themselves
//
// API mirrors liaison-cloud/web/src/i18n/index.ts so patterns transfer
// between repos.

import { useCallback, useEffect, useMemo, useState } from 'react';

export type Locale = 'zh-CN' | 'en-US';

const LOCALE_STORAGE_KEY = 'ongrid-locale';
const LOCALE_CHANGE_EVENT = 'ongrid-locale-change';

// Mainland-CN + HK/Macau timezones. The user's effective time zone is the
// primary auto-detect signal because foreign visitors with zh-* set in
// their browser (e.g. heritage speakers, students) still want the English
// UI; conversely, an in-CN user whose browser is en-US (corp default)
// still wants the Chinese UI.
const CN_TIMEZONES = new Set<string>([
  'Asia/Shanghai',
  'Asia/Chongqing',
  'Asia/Urumqi',
  'Asia/Harbin',
  'Asia/Hong_Kong',
  'Asia/Macau',
]);

function autoDetectLocale(): Locale {
  // Run only in the browser; SSR / unit-test envs fall back to zh-CN
  // (preserves the old "Chinese-default" behaviour for non-browser paths).
  if (typeof navigator === 'undefined' || typeof Intl === 'undefined') {
    return 'zh-CN';
  }
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || '';
    if (CN_TIMEZONES.has(tz)) return 'zh-CN';
    // Secondary signal: browser language. Treat as a tie-breaker for
    // timezones we don't recognise but where the user clearly speaks
    // Chinese (e.g. tz=UTC because of a misconfigured docker host).
    const lang = (navigator.language || '').toLowerCase();
    if (lang.startsWith('zh')) return 'zh-CN';
  } catch {
    /* Intl unavailable in this runtime — fall through */
  }
  return 'en-US';
}

export const getLocale = (): Locale => {
  if (typeof localStorage === 'undefined') return 'zh-CN';
  const v = localStorage.getItem(LOCALE_STORAGE_KEY);
  // User-explicit choice (set via the language switcher) ALWAYS wins.
  if (v === 'en-US' || v === 'zh-CN') return v;
  // First visit (or storage cleared): auto-detect from timezone +
  // browser language. Applies on the login page too because the React
  // SPA boots through this same hook before the route resolves.
  return autoDetectLocale();
};

export const setLocale = (locale: Locale): void => {
  localStorage.setItem(LOCALE_STORAGE_KEY, locale);
  window.dispatchEvent(new CustomEvent<Locale>(LOCALE_CHANGE_EVENT, { detail: locale }));
  // Reflect on <html lang> too for screen readers + browser hyphenation.
  document.documentElement.lang = locale;
};

// tr is the standalone translator. Use for non-React paths (e.g. module-
// level constants); components should prefer useI18n() so they re-render
// on locale change.
export const tr = (zh: string, en: string): string => (getLocale() === 'en-US' ? en : zh);

export const useI18n = () => {
  const [locale, setLocaleState] = useState<Locale>(getLocale());

  useEffect(() => {
    const listener = (e: Event) => {
      const ce = e as CustomEvent<Locale>;
      setLocaleState(ce.detail || getLocale());
    };
    window.addEventListener(LOCALE_CHANGE_EVENT, listener as EventListener);
    return () => window.removeEventListener(LOCALE_CHANGE_EVENT, listener as EventListener);
  }, []);

  const toggleLocale = useCallback(() => {
    setLocale(locale === 'zh-CN' ? 'en-US' : 'zh-CN');
  }, [locale]);

  const translator = useMemo(
    () => (zh: string, en: string) => (locale === 'en-US' ? en : zh),
    [locale],
  );

  return { locale, tr: translator, toggleLocale, setLocale };
};
