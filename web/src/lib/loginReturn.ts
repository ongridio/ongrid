// A router state value is untrusted; only permit local paths after sign-in.
export function safeLoginReturn(value: unknown): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || /[\\\s]/.test(value)) return '/';
  try {
    const target = new URL(value, 'https://ongrid.invalid');
    if (target.origin !== 'https://ongrid.invalid' || target.pathname === '/login') return '/';
    return target.pathname + target.search + target.hash;
  } catch { return '/'; }
}
