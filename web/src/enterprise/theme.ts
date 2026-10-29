import { applyResolvedTheme } from '@/store/mode';
export const THEME_KEY = 'enterprise-ai.theme';
export function bootEnterpriseTheme() {
  document.documentElement.classList.add('enterprise-ui');
  let dark = false;
  try {
    dark = localStorage.getItem(THEME_KEY) === 'dark';
    if (!localStorage.getItem('ongrid-locale'))
      localStorage.setItem('ongrid-locale', 'zh-CN');
  } catch {
    /* Light is the safe default when storage is unavailable. */
  }
  applyResolvedTheme(dark ? 'dark' : 'light');
  document.documentElement.style.setProperty('--accent', '20 99 224');
}
export function toggleEnterpriseTheme() {
  const next = document.documentElement.classList.contains('dark')
    ? 'light'
    : 'dark';
  applyResolvedTheme(next);
  try {
    localStorage.setItem(THEME_KEY, next);
  } catch {
    /* The chosen theme still applies for the current session. */
  }
}
