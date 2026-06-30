import { createPrometheusLaunch } from '@/api/prometheus';
import { listSettings } from '@/api/settings';
import { useObservability } from '@/store/observability';
import { getLocale } from '@/i18n/locale';

// openObservabilityUrl opens a Grafana Loki/Tempo Explore deep-link after
// minting the prometheus-ticket cookie that nginx auth_request gates
// /grafana/* on (auth_request → manager /api/v1/prometheus/auth, which
// validates the Cookie). This step is REQUIRED, not optional: ongrid's own
// auth is a bearer JWT carried in the Authorization header, which a browser
// popup navigating to /grafana/... cannot send — so without the ticket
// cookie the popup gets nginx 401. (Removing this call in v0.7.115 caused
// exactly that 401; restored immediately.)
//
// Two subtleties matter and explain why a plain `await + window.open`
// failed in v0.7.36:
//
//   1. Popup blockers gate window.open on user-gesture context. The
//      ~150ms launch RPC pushes the open call past that window, so
//      browsers block the popup or open it in a fresh process whose
//      cookie-jar view lags the parent. Fix: open about:blank
//      synchronously inside the click handler, then navigate the
//      handle once the cookie mints.
//   2. noopener returns null from window.open, so we can't navigate
//      after. Same-origin Grafana means noopener's mitigation isn't
//      applicable anyway.
//
// NB: minting the cookie only gets you PAST nginx — Explore itself is
// Editor/Admin-gated inside Grafana, so the embedded Grafana must run with
// anonymous org role = Editor (deploy compose + systemd drop-in) or it
// 302s /explore back to its home. See the v0.7.115 grafana role fix.
//
// Used by Logs/Traces "在 Grafana 中打开", IncidentDetail's 跳查相关日志/
// 链路 buttons, and the Integrations Loki/Tempo cards.
export async function openObservabilityUrl(url: string): Promise<void> {
  const popup = window.open('about:blank', '_blank');
  try {
    await createPrometheusLaunch({ expr: 'up' });
  } catch {
    // Cookie didn't mint — fall through. Grafana will 401 unless the
    // user already has an active ticket from a recent metric drilldown.
  }
  if (popup && !popup.closed) {
    popup.location.replace(url);
    return;
  }
  // Popup blocker killed it — navigate in the current tab.
  window.location.href = url;
}

type DrilldownInput = {
  expr: string;
  rangeInput?: string;
  stepInput?: string;
  title?: string;
  // device_id label value used for the Grafana `var-device_id` template
  // variable. This is the linked Device.ID — NOT edge.id. The two only
  // coincide for pre-split / back-filled hosts; after the entity split a
  // re-installed edge gets a fresh edge.id while keeping its device_id,
  // so passing edge.id here points the dashboard at a non-existent series
  // (#96).
  deviceId?: string | number;
};

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, '');
}

function toRelativeFrom(rangeInput?: string): string {
  const cleaned = (rangeInput ?? '1h').trim();
  if (/^\d+\s*[smhdw]$/i.test(cleaned)) {
    return `now-${cleaned.replace(/\s+/g, '').toLowerCase()}`;
  }
  return 'now-1h';
}

// Module-level cache for the Grafana root URL pulled from
// system_settings.grafana.root_url. Drilldown buttons fire from many
// pages, but the root URL changes rarely — a 60s TTL keeps us out of
// settings-API on every chart click without making a stale-after-save
// experience much worse than "click again 60s later".
//
// invalidateGrafanaRootCache() is exported so the integration card can
// drop the cache the moment admin saves a new value, making the next
// drilldown click instant + accurate.
let cachedRoot: string | null = null;
let cachedAt = 0;
const ROOT_TTL_MS = 60_000;

export function invalidateGrafanaRootCache(): void {
  cachedRoot = null;
  cachedAt = 0;
}

export async function fetchGrafanaRootURL(): Promise<string> {
  const now = Date.now();
  if (cachedRoot != null && now - cachedAt < ROOT_TTL_MS) {
    return cachedRoot;
  }
  const sameOrigin = `${window.location.origin}/grafana`;
  try {
    const r = await listSettings('grafana');
    for (const it of r.items) {
      if (it.key === 'root_url' && (it.value ?? '').trim() !== '') {
        const stored = trimTrailingSlash(it.value);
        // Embedded mode stores http://grafana:3000/grafana (docker DNS
        // name) — the browser can't reach it directly. Fall back to
        // ongrid's same-origin nginx /grafana/ proxy in that case.
        cachedRoot = isBrowserReachableURL(stored) ? stored : sameOrigin;
        cachedAt = now;
        return cachedRoot;
      }
    }
  } catch {
    // fall through — same-origin nginx /grafana/ is the safe fallback
  }
  cachedRoot = sameOrigin;
  cachedAt = now;
  return cachedRoot;
}

// isBrowserReachableURL filters docker-internal hostnames the user's
// browser can't reach. Identical heuristic to the one in
// pages/settings/Integrations.tsx — kept duplicated rather than carving
// a tiny shared util because the two places have different import roots
// and the fn is 6 lines.
function isBrowserReachableURL(rawUrl: string): boolean {
  try {
    const u = new URL(rawUrl);
    const host = u.hostname;
    if (host === 'localhost' || host === '127.0.0.1' || host === '::1') return false;
    if (!host.includes('.') && !host.includes(':')) return false;
    return true;
  } catch {
    return false;
  }
}

// grafanaLangFromLocale maps the product UI locale to Grafana's IETF
// language tag ("zh-CN" → "zh-Hans"; everything else → "en-US"). Grafana
// 11.x reads the `lang` query param when localizationForVisualizations
// is on; if it's off the param is harmlessly ignored and the user falls
// back to GF_USERS_DEFAULT_LANGUAGE (set to en-US in docker-compose).
function grafanaLangFromLocale(): string {
  try {
    return getLocale() === 'zh-CN' ? 'zh-Hans' : 'en-US';
  } catch {
    return 'en-US';
  }
}

async function buildGrafanaUrl(input: DrilldownInput): Promise<string | null> {
  const { grafanaDashboardUid, grafanaOrgId } = useObservability.getState();
  const baseUrl = await fetchGrafanaRootURL();
  const dashboardUid = grafanaDashboardUid.trim() || 'ongrid-server-detail';
  if (!dashboardUid) return null;
  const params = new URLSearchParams();
  params.set('from', toRelativeFrom(input.rangeInput));
  params.set('to', 'now');
  params.set('lang', grafanaLangFromLocale());
  if (grafanaOrgId.trim()) {
    params.set('orgId', grafanaOrgId.trim());
  }
  if (input.deviceId !== undefined && input.deviceId !== null && String(input.deviceId).trim() !== '') {
    params.set('var-device_id', String(input.deviceId));
  }
  return `${baseUrl}/d/${dashboardUid}/server-detail?${params.toString()}`;
}

export async function openMetricDrilldown(input: DrilldownInput): Promise<void> {
  const launch = await createPrometheusLaunch({
    expr: input.expr,
    range_input: input.rangeInput,
    step_input: input.stepInput,
  });

  const grafanaUrl = await buildGrafanaUrl(input);
  if (grafanaUrl) {
    window.open(grafanaUrl, '_blank', 'noopener,noreferrer');
    return;
  }
  window.open(launch.url, '_blank', 'noopener,noreferrer');
}

export function hasGrafanaDrilldownConfig(): boolean {
  const { grafanaDashboardUid } = useObservability.getState();
  return grafanaDashboardUid.trim().length > 0;
}

// buildExploreUrl assembles a Grafana 11 Explore deep-link.
//
// Grafana 11 RETIRED the old `?left={datasource,queries,range}` JSON
// param — passing it opens Explore with an empty default datasource and
// no query, which is exactly the "在 Grafana 打开后没有数据" bug the
// Logs / Traces pages hit. v11 expects:
//
//   /explore?schemaVersion=1&orgId=1&panes=<urlencoded-json>
//
// where panes is keyed by an arbitrary short pane id and each query's
// `datasource` is an OBJECT { type, uid } — the bare-string form also
// stopped resolving in v11.
//
// dsType is "loki" | "tempo" | "prometheus"; dsUid is the provisioned
// uid (ongrid-loki / ongrid-tempo / ongrid-prometheus). query carries
// the per-engine fields (expr+queryType for loki/prom, query+queryType
// for tempo) — we pass it through verbatim so callers keep control.
export function buildExploreUrl(opts: {
  base: string;
  dsType: 'loki' | 'tempo' | 'prometheus';
  dsUid: string;
  query: Record<string, unknown>;
  // Absolute ms epoch (number) OR a Grafana relative expr ("now-1h").
  fromMs: number | string;
  toMs: number | string;
  orgId?: string;
}): string {
  const base = opts.base.replace(/\/+$/, '');
  const ds = { type: opts.dsType, uid: opts.dsUid };
  const pane = {
    datasource: opts.dsUid,
    queries: [{ refId: 'A', datasource: ds, ...opts.query }],
    range: { from: String(opts.fromMs), to: String(opts.toMs) },
  };
  // Pane key is arbitrary; Grafana uses a random 3-char id. A fixed
  // key is fine — Explore only cares that panes is a non-empty object.
  const panes = JSON.stringify({ og: pane });
  const params = new URLSearchParams();
  params.set('schemaVersion', '1');
  params.set('panes', panes);
  params.set('lang', grafanaLangFromLocale());
  if (opts.orgId && opts.orgId.trim()) params.set('orgId', opts.orgId.trim());
  return `${base}/explore?${params.toString()}`;
}
