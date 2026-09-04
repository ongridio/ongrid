import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Activity,
  Check,
  ChevronDown,
  ChevronRight,
  ExternalLink,
  Eye,
  EyeOff,
  Save,
  Database,
  Loader2,
  PlugZap,
  Cloud,
  FileText,
  GitBranch,
  Search,
  RefreshCw,
} from 'lucide-react';
import {
  openMetricDrilldown,
  openObservabilityUrl,
  invalidateGrafanaRootCache,
  buildExploreUrl,
} from '@/lib/drilldown';
import { useObservability } from '@/store/observability';
import {
  listSettings,
  setSetting,
  revealSetting,
  testGrafanaConnection,
  syncGrafana,
  syncLokiDatasource,
  testPromConnection,
  testLokiConnection,
  testTempoConnection,
  testWebSearchConnection,
  type SystemSetting,
  type GrafanaSyncResult,
} from '@/api/settings';
import {
  currentLogBackend,
  getLogBackend,
  getLogBackendConnectionCheck,
  selectLogBackend,
  selectLokiLogBackend,
  saveLogBackend,
  startLogBackendConnectionCheck,
  testLogBackendDraft,
  type LogBackend,
  type LogBackendConnectionCheck,
  type LogBackendKind,
  type SaveLogBackendInput,
} from '@/api/logs';
import { getPluginCounts } from '@/api/integrations';
import { ApiError } from '@/api/client';
import { Button, Card } from '@/components/ui';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import BuiltInStorageAdvanced from './Advanced';

// Settings → 集成. Four cards, all backend-driven, parallel naming:
//   - Prometheus 集成   → system_settings.prom; manager reads on every
//     remote_write / PromQL call (auth ~5s TTL, URL/TLS live)
//   - Grafana 集成      → system_settings.grafana; "测试" + "同步"
//   - Loki 集成 (日志)  → system_settings.loki; the URL feeds both
//     edge-side push (logs plugin) and Grafana datasource
//   - Tempo 集成 (链路) → system_settings.tempo; same pattern as Loki
//     for the trace signal
//
// Empty Loki/Tempo URL falls back to the docker-internal seed (set on
// first boot from ONGRID_LOG_URL / ONGRID_TRACE_QUERY_URL); a
// hostname with a dot or IP is treated as customer-supplied and
// edges push directly there.
export default function SettingsIntegrations() {
  const [searchParams] = useSearchParams();
  const focusLogs = searchParams.get('focus') === 'logs';
  const logsSectionRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!focusLogs) return;
    const frame = window.requestAnimationFrame(() => {
      logsSectionRef.current?.scrollIntoView({ block: 'start' });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [focusLogs]);

  return (
    <div className="space-y-5">
      <PrometheusCard />
      <GrafanaCard />
      <div ref={logsSectionRef} id="logs-integration" className="scroll-mt-5">
        <LogsIntegrationCard />
      </div>
      <TempoCard />
      <WebSearchCard />
    </div>
  );
}

// ---------- Prometheus card (backend-driven) ----------

type PromForm = {
  query_url: string;
  remote_write_url: string;
  bearer_token: string;
  basic_user: string;
  basic_password: string;
  tls_insecure: string;
};

const PROM_KEYS: (keyof PromForm)[] = [
  'query_url',
  'remote_write_url',
  'bearer_token',
  'basic_user',
  'basic_password',
  'tls_insecure',
];

const PROM_SENSITIVE: Set<keyof PromForm> = new Set(['bearer_token', 'basic_password']);

const emptyPromForm: PromForm = {
  query_url: '',
  remote_write_url: '',
  bearer_token: '',
  basic_user: '',
  basic_password: '',
  tls_insecure: '',
};

function promRemoteWriteURL(queryURL: string): string {
  const base = queryURL.trim().replace(/\/+$/, '');
  if (!base) return '';
  try {
    const url = new URL(base);
    const match = url.pathname.match(/^\/select\/([^/]+)\/prometheus\/?$/);
    if (!match) return `${base}/api/v1/write`;
    url.pathname = `/insert/${match[1]}/prometheus/api/v1/write`;
    url.search = '';
    url.hash = '';
    if (url.port === '8481') url.port = '8480';
    return url.toString();
  } catch {
    return `${base}/api/v1/write`;
  }
}

function PrometheusCard() {
  const { tr } = useI18n();
  // Both sensitive and non-sensitive fields land in draft as cleartext.
  // For sensitive rows we hit the /reveal endpoint after the masked list
  // returns, then populate draft with the real value. The input is
  // type=password by default (renders as ●●●●●●) and an eye-icon flips
  // to type=text to expose the chars. Diff/save logic is then trivial:
  // draft[k] !== server[k] regardless of sensitivity.
  const [server, setServer] = useState<PromForm>(emptyPromForm);
  const [draft, setDraft] = useState<PromForm>(emptyPromForm);
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [savedOk, setSavedOk] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const probeRequest = useRef(0);
  // Probe state — separate from form state so saving doesn't reset the
  // last known test outcome and probe failures don't make the form look
  // dirty.
  const [probe, setProbe] = useState<
    | { kind: 'idle' }
    | { kind: 'testing' }
    | { kind: 'ok' }
    | { kind: 'error'; msg: string }
  >({ kind: 'idle' });

  const refresh = useCallback(async () => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setLoading(true);
    setErr(null);
    try {
      const r = await listSettings('prom');
      const next = { ...emptyPromForm };
      for (const it of r.items as SystemSetting[]) {
        if (!(PROM_KEYS as string[]).includes(it.key)) continue;
        if (!PROM_SENSITIVE.has(it.key as keyof PromForm)) {
          (next as Record<string, string>)[it.key] = it.value ?? '';
        }
      }
      // Fetch plaintext for any sensitive row that has a stored value.
      // Run in parallel — a single failure shouldn't block other rows.
      await Promise.all(
        (r.items as SystemSetting[])
          .filter((it) => PROM_SENSITIVE.has(it.key as keyof PromForm) && (it.value ?? '') !== '')
          .map(async (it) => {
            try {
              const real = await revealSetting('prom', it.key);
              (next as Record<string, string>)[it.key] = real.value ?? '';
            } catch {
              /* leave the field empty so the user can paste a fresh value */
            }
          })
      );
      const effective = {
        ...next,
        remote_write_url: next.remote_write_url || promRemoteWriteURL(next.query_url),
      };
      setServer(effective);
      setDraft(effective);
      setRevealed({});
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Dirty iff any field differs from what we loaded.
  const dirty = PROM_KEYS.some((k) => draft[k] !== server[k]);

  const update = (k: keyof PromForm, v: string) => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setSavedOk(false);
    setDraft((cur) => ({ ...cur, [k]: v }));
  };

  const updateQueryURL = (value: string) => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setSavedOk(false);
    setDraft((current) => ({
      ...current,
      query_url: value,
      remote_write_url:
        current.remote_write_url === '' || current.remote_write_url === promRemoteWriteURL(current.query_url)
          ? promRemoteWriteURL(value)
          : current.remote_write_url,
    }));
  };

  const submit = async () => {
    if (probe.kind !== 'ok') return;
    setSaving(true);
    setErr(null);
    try {
      for (const k of PROM_KEYS) {
        if (draft[k] === server[k]) continue;
        await setSetting('prom', k, draft[k], PROM_SENSITIVE.has(k));
      }
      await refresh();
      setSavedOk(true);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const probeProm = async () => {
    const requestID = ++probeRequest.current;
    setProbe({ kind: 'testing' });
    try {
      await testPromConnection({
        query_url: draft.query_url,
        remote_write_url: draft.remote_write_url,
        bearer_token: draft.bearer_token,
        basic_user: draft.basic_user,
        basic_password: draft.basic_password,
        tls_insecure: draft.tls_insecure === 'true',
      });
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'ok' });
    } catch (e) {
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    }
  };

  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center gap-2">
        <Database size={14} className="text-zinc-400" />
        <h2 className="text-sm font-medium text-zinc-100">{tr('Prometheus 集成', 'Prometheus integration')}</h2>
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">
        {tr(
          'Manager 用这里的 URL + 凭证写入 / 查询 TSDB。支持原版 Prometheus、VictoriaMetrics、Mimir、Cortex、Thanos receive。保存后 ~5 秒内对所有新请求生效，',
          'Manager uses this URL + credentials to write / query the TSDB. Works with vanilla Prometheus, VictoriaMetrics, Mimir, Cortex, Thanos receive. New requests pick up changes within ~5 s, ',
        )}<b>{tr('无需重启', 'no restart needed')}</b>{tr('。', '.')}
        <br />
        <span className="text-zinc-600">
          {tr(
            '内建 Prometheus 在 docker 内网，',
            'The built-in Prometheus runs on the docker internal network and ',
          )}<b>{tr('无需鉴权', 'requires no auth')}</b>{tr(
            '——下面 Bearer / Basic 留空。仅在对接外部带认证的 TSDB 时填写。',
            ' — leave Bearer / Basic blank below. Only fill them when pointing at an external auth-protected TSDB.',
          )}
        </span>
        <br />
        <span className="text-zinc-600">{tr('注：切换数据源后老数据留在原 TSDB，不会自动搬过来。', 'Note: switching data sources leaves old data in the original TSDB — it does not migrate automatically.')}</span>
      </p>

      {loading ? (
        <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <PromField
            label="Query URL"
            hint={tr('PromQL 查询根，例 http://prom:9090 或 https://vm.example.com', 'PromQL query root, e.g. http://prom:9090 or https://vm.example.com')}
            value={draft.query_url}
            onChange={updateQueryURL}
            placeholder="http://prometheus:9090/prometheus"
          />
          <PromField
            label="Remote Write URL"
            hint={tr('根据 Query URL 自动匹配；VictoriaMetrics 集群会使用 /insert/.../api/v1/write', 'Auto-matched from Query URL; VictoriaMetrics clusters use /insert/.../api/v1/write')}
            value={draft.remote_write_url}
            onChange={(v) => update('remote_write_url', v)}
            placeholder="https://vm.example.com/api/v1/write"
          />
          <PromField
            label="Bearer Token"
            hint={tr('Authorization: Bearer ... 优先于 Basic', 'Authorization: Bearer ... takes precedence over Basic')}
            sensitive
            revealed={!!revealed.bearer_token}
            onToggleReveal={() => setRevealed((r) => ({ ...r, bearer_token: !r.bearer_token }))}
            value={draft.bearer_token}
            onChange={(v) => update('bearer_token', v)}
            placeholder={tr('（留空 = 不用 Bearer）', '(empty = no Bearer)')}
          />
          <PromField
            label="Basic User"
            value={draft.basic_user}
            onChange={(v) => update('basic_user', v)}
            placeholder={tr('（留空 = 不用 Basic）', '(empty = no Basic)')}
          />
          <PromField
            label="Basic Password"
            sensitive
            revealed={!!revealed.basic_password}
            onToggleReveal={() => setRevealed((r) => ({ ...r, basic_password: !r.basic_password }))}
            value={draft.basic_password}
            onChange={(v) => update('basic_password', v)}
            placeholder={tr('（留空 = 不用 Basic）', '(empty = no Basic)')}
          />
          <label className="flex items-center gap-2 text-xs text-zinc-300">
            <input
              type="checkbox"
              checked={draft.tls_insecure === 'true'}
              onChange={(e) => update('tls_insecure', e.target.checked ? 'true' : 'false')}
              className="h-3.5 w-3.5 rounded border-zinc-600 bg-zinc-900 accent-emerald-500"
            />
            {tr('跳过 TLS 校验（自签证书时勾选）', 'Skip TLS verification (check this for self-signed certs)')}
          </label>
        </div>
      )}

      <div className="mt-5 flex flex-wrap items-center gap-3">
        <Button
          onClick={probeProm}
          disabled={probe.kind === 'testing' || draft.query_url.trim() === ''}
          variant="ghost"
        >
          {probe.kind === 'testing' ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}
          <span>{tr('测试连接', 'Test connection')}</span>
        </Button>
        <Button onClick={submit} disabled={!dirty || saving || probe.kind !== 'ok'} variant="primary">
          {savedOk && !dirty ? <Check size={14} /> : <Save size={14} />}
          <span>{saving ? tr('保存中…', 'Saving…') : savedOk && !dirty ? tr('已保存', 'Saved') : tr('保存', 'Save')}</span>
        </Button>
        <span className="text-xs text-zinc-500">
          {dirty
            ? probe.kind === 'ok'
              ? tr('测试通过，可以保存', 'Test passed; ready to save')
              : tr('请先测试连接，通过后才能保存', 'Test the connection before saving')
            : tr('保存后 ~5 秒内自动生效（无需重启）', 'Takes effect within ~5 s of saving (no restart needed)')}
        </span>
        {err && <span className="text-xs text-red-400">{err}</span>}
      </div>
      <PromProbeLine probe={probe} />
      <BuiltInStorageAdvanced service="prometheus" />
    </Card>
  );
}

// PromProbeLine renders the result of the most recent test-connection
// click. Lives next to the form so a passing probe doesn't clutter the
// rest of the page.
function PromProbeLine({
  probe,
}: {
  probe: { kind: 'idle' } | { kind: 'testing' } | { kind: 'ok' } | { kind: 'error'; msg: string };
}) {
  const { tr } = useI18n();
  switch (probe.kind) {
    case 'ok':
      return <p className="mt-3 text-xs text-emerald-400">{tr('✓ PromQL 查询与 Remote Write 写入入口均可用', '✓ PromQL query and Remote Write endpoint are both reachable')}</p>;
    case 'error':
      return <p className="mt-3 break-all text-xs text-red-400">✗ {probe.msg}</p>;
    default:
      return null;
  }
}

function PromField({
  label,
  hint,
  value,
  onChange,
  placeholder,
  sensitive,
  revealed,
  onToggleReveal,
  disabled,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange(v: string): void;
  placeholder?: string;
  sensitive?: boolean;
  // When sensitive, parent owns the revealed flag (so eye state persists
  // across re-renders) and provides a toggle. Default = hidden (●●●●).
  revealed?: boolean;
  onToggleReveal?: () => void;
  disabled?: boolean;
}) {
  const inputType = sensitive ? (revealed ? 'text' : 'password') : 'text';
  return (
    <label className="block">
      <span className="mb-1 flex items-center gap-1.5 text-xs text-zinc-400">
        {label}
        {sensitive && (
          <span className="rounded border border-amber-700/50 bg-amber-900/20 px-1 text-[10px] text-amber-300">
            sensitive
          </span>
        )}
      </span>
      <div className="relative">
        <input
          type={inputType}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          disabled={disabled}
          className={cn(
            'w-full rounded-lg border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-sm text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none disabled:cursor-not-allowed disabled:opacity-45',
            sensitive && 'pr-9'
          )}
          autoComplete="off"
        />
        {sensitive && onToggleReveal && (
          <button
            type="button"
            onClick={onToggleReveal}
            tabIndex={-1}
            aria-label={revealed ? 'Hide' : 'Show'}
            className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200"
          >
            {revealed ? <EyeOff size={14} /> : <Eye size={14} />}
          </button>
        )}
      </div>
      {hint && <span className="mt-1 block text-[11px] text-zinc-500">{hint}</span>}
    </label>
  );
}

// ---------- Grafana card (backend-driven) ----------

type GrafanaForm = {
  root_url: string;
  sa_token: string;
  api_key: string;
  org_id: string;
  tls_insecure: string;
};
const GRAFANA_KEYS: (keyof GrafanaForm)[] = ['root_url', 'sa_token', 'api_key', 'org_id', 'tls_insecure'];
const GRAFANA_SENSITIVE: Set<keyof GrafanaForm> = new Set(['sa_token', 'api_key']);
const emptyGrafanaForm: GrafanaForm = { root_url: '', sa_token: '', api_key: '', org_id: '', tls_insecure: '' };

type SyncStatus =
  | { kind: 'idle' }
  | { kind: 'testing' }
  | { kind: 'tested-ok' }
  | { kind: 'syncing' }
  | { kind: 'synced'; res: GrafanaSyncResult }
  | { kind: 'error'; msg: string };

function GrafanaCard() {
  const { tr } = useI18n();
  // Same eager-reveal discipline as PrometheusCard. Sensitive rows land
  // in draft with cleartext via /reveal so the eye toggle has something
  // to expose, but the input defaults to type=password so dots show.
  const [server, setServer] = useState<GrafanaForm>(emptyGrafanaForm);
  const [draft, setDraft] = useState<GrafanaForm>(emptyGrafanaForm);
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [savedOk, setSavedOk] = useState(false);
  const [status, setStatus] = useState<SyncStatus>({ kind: 'idle' });
  const probeRequest = useRef(0);

  const refresh = useCallback(async () => {
    probeRequest.current += 1;
    setStatus({ kind: 'idle' });
    setLoading(true);
    try {
      const r = await listSettings('grafana');
      const next = { ...emptyGrafanaForm };
      for (const it of r.items as SystemSetting[]) {
        if (!(GRAFANA_KEYS as string[]).includes(it.key)) continue;
        if (!GRAFANA_SENSITIVE.has(it.key as keyof GrafanaForm)) {
          (next as Record<string, string>)[it.key] = it.value ?? '';
        }
      }
      await Promise.all(
        (r.items as SystemSetting[])
          .filter((it) => GRAFANA_SENSITIVE.has(it.key as keyof GrafanaForm) && (it.value ?? '') !== '')
          .map(async (it) => {
            try {
              const real = await revealSetting('grafana', it.key);
              (next as Record<string, string>)[it.key] = real.value ?? '';
            } catch {
              /* leave the field empty so the user can paste a fresh token */
            }
          })
      );
      setServer(next);
      setDraft(next);
      setRevealed({});
    } catch (e) {
      setStatus({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const dirty = GRAFANA_KEYS.some((k) => draft[k] !== server[k]);

  const update = (k: keyof GrafanaForm, v: string) => {
    probeRequest.current += 1;
    setStatus({ kind: 'idle' });
    setSavedOk(false);
    setDraft((cur) => ({ ...cur, [k]: v }));
  };

  const save = async () => {
    if (status.kind !== 'tested-ok') return;
    setSaving(true);
    setStatus({ kind: 'idle' });
    try {
      for (const k of GRAFANA_KEYS) {
        if (draft[k] === server[k]) continue;
        await setSetting('grafana', k, draft[k], GRAFANA_SENSITIVE.has(k));
      }
      // Invalidate the drilldown helper's root_url cache so the next
      // chart-page 「打开 Grafana」 click picks up the new URL instantly,
      // not after the 60s TTL.
      invalidateGrafanaRootCache();
      await refresh();
      setSavedOk(true);
    } catch (e) {
      setStatus({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    const requestID = ++probeRequest.current;
    setStatus({ kind: 'testing' });
    try {
      await testGrafanaConnection({
        root_url: draft.root_url,
        sa_token: draft.sa_token,
        api_key: draft.api_key,
        org_id: draft.org_id,
        tls_insecure: draft.tls_insecure === 'true',
      });
      if (requestID !== probeRequest.current) return;
      setStatus({ kind: 'tested-ok' });
    } catch (e) {
      if (requestID !== probeRequest.current) return;
      setStatus({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    }
  };

  const sync = async () => {
    setStatus({ kind: 'syncing' });
    try {
      const res = await syncGrafana();
      setStatus({ kind: 'synced', res });
    } catch (e) {
      setStatus({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    }
  };

  const testJump = () => {
    void openMetricDrilldown({ expr: 'up', rangeInput: '1h', stepInput: '30s', title: 'up' });
  };

  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center gap-2">
        <Activity size={14} className="text-zinc-400" />
        <h2 className="text-sm font-medium text-zinc-100">{tr('Grafana 集成', 'Grafana integration')}</h2>
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">
        {tr(
          '填 Grafana 根地址 + Service Account Token，「测试」验通，「同步」自动把托管数据源和默认 dashboard 推到 Grafana 的 ',
          'Fill in the Grafana root URL + Service Account Token. "Test" verifies the connection; "Sync" pushes managed datasources and default dashboards into the Grafana ',
        )}
        <code className="mx-1 font-mono text-zinc-400">ongrid</code>
        {tr(' 文件夹。跳转过去仍然由用户在 Grafana 那边登录（Ongrid 不代登录）。', ' folder. Jumping to Grafana still requires the user to sign in there (Ongrid does not impersonate).')}
      </p>

      {loading ? (
        <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <PromField
            label="Grafana Root URL"
            hint={tr('例 https://grafana.example.com（不含路径）', 'e.g. https://grafana.example.com (no path)')}
            value={draft.root_url}
            onChange={(v) => update('root_url', v)}
            placeholder="https://grafana.example.com"
          />
          <PromField
            label="Service Account Token"
            hint={
              server.sa_token !== ''
                ? tr('已有 token（首次启动 manager 会为内建 Grafana 自动生成）。点眼睛查看，要轮换直接粘新值', 'Token already set (auto-generated for the built-in Grafana on first manager boot). Click the eye to reveal; paste a new one to rotate.')
                : tr('Grafana → Administration → Service accounts 里建 admin 账号生成', 'Create an admin Service Account at Grafana → Administration → Service accounts')
            }
            sensitive
            revealed={!!revealed.sa_token}
            onToggleReveal={() => setRevealed((r) => ({ ...r, sa_token: !r.sa_token }))}
            value={draft.sa_token}
            onChange={(v) => update('sa_token', v)}
            placeholder="glsa_..."
          />
          <PromField
            label={tr('API Key（外接 Grafana 备用）', 'API Key (fallback for external Grafana)')}
            hint={tr('对接客户自有 Grafana 但不便建 SA 时填这里；与 SA Token 选其一即可，SA 优先', "Use this when you can't create a Service Account on a customer Grafana. Choose either SA Token or API Key; SA wins if both are set.")}
            sensitive
            revealed={!!revealed.api_key}
            onToggleReveal={() => setRevealed((r) => ({ ...r, api_key: !r.api_key }))}
            value={draft.api_key}
            onChange={(v) => update('api_key', v)}
            placeholder="eyJrIj..."
          />
          <PromField
            label="Org ID"
            hint={tr('多组织 Grafana 用，单组织默认 1 即可', 'For multi-org Grafana; single-org installs always use 1')}
            value={draft.org_id}
            onChange={(v) => update('org_id', v)}
            placeholder="1"
          />
          <label className="flex items-center gap-2 text-xs text-zinc-300">
            <input
              type="checkbox"
              checked={draft.tls_insecure === 'true'}
              onChange={(e) => update('tls_insecure', e.target.checked ? 'true' : 'false')}
              className="h-3.5 w-3.5 rounded border-zinc-600 bg-zinc-900 accent-emerald-500"
            />
            {tr('跳过 TLS 校验（自签证书时勾选）', 'Skip TLS verification (check this for self-signed certs)')}
          </label>
        </div>
      )}

      <div className="mt-5 flex flex-wrap items-center gap-3">
        <Button
          onClick={test}
          disabled={status.kind === 'testing' || !canTestSync(draft)}
          variant="ghost"
        >
          {status.kind === 'testing' ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}
          <span>{tr('测试连接', 'Test connection')}</span>
        </Button>
        <Button onClick={save} disabled={!dirty || saving || status.kind !== 'tested-ok'} variant="primary">
          {savedOk && !dirty ? <Check size={14} /> : <Save size={14} />}
          <span>{saving ? tr('保存中…', 'Saving…') : savedOk && !dirty ? tr('已保存', 'Saved') : tr('保存', 'Save')}</span>
        </Button>
        <button
          type="button"
          onClick={sync}
          disabled={status.kind === 'syncing' || dirty || !canTestSync(server)}
          className="inline-flex items-center gap-1.5 rounded-lg border border-emerald-700/60 bg-emerald-900/20 px-3 py-1.5 text-sm text-emerald-300 transition-colors hover:bg-emerald-900/40 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {status.kind === 'syncing' ? <Loader2 size={14} className="animate-spin" /> : <Cloud size={14} />}
          <span>{tr('同步 Grafana', 'Sync Grafana')}</span>
        </button>
        <button
          type="button"
          onClick={testJump}
          className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-200 transition-colors hover:border-zinc-500 hover:bg-zinc-800"
        >
          <ExternalLink size={14} />
          <span>{tr('测试跳转', 'Test jump')}</span>
        </button>
      </div>

      <StatusLine status={status} dirty={dirty} />

      <GrafanaDrilldownAdvanced />
    </Card>
  );
}

// GrafanaDrilldownAdvanced is the optional bit that used to live on
// Settings → 通用 (now removed). Long-tail config: "what dashboard UID
// does the chart-page 「打开 Grafana」 button deep-link into" + "which
// Grafana org id". 99% of users never touch this — collapsed by default
// and clearly marked per-browser only.
function GrafanaDrilldownAdvanced() {
  const { tr } = useI18n();
  const { grafanaDashboardUid, grafanaOrgId, setConfig } = useObservability();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState({ grafanaDashboardUid, grafanaOrgId });
  const [savedFlag, setSavedFlag] = useState(false);

  useEffect(() => {
    setDraft({ grafanaDashboardUid, grafanaOrgId });
  }, [grafanaDashboardUid, grafanaOrgId]);

  const dirty =
    draft.grafanaDashboardUid !== grafanaDashboardUid || draft.grafanaOrgId !== grafanaOrgId;

  function save() {
    setConfig({
      grafanaDashboardUid: draft.grafanaDashboardUid,
      grafanaOrgId: draft.grafanaOrgId,
    });
    setSavedFlag(true);
  }

  return (
    <div className="mt-6 border-t border-zinc-800 pt-4">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1 text-[11px] text-zinc-500 hover:text-zinc-300"
      >
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        <span>{tr('高级：图表「打开 Grafana」深链参数（仅当前浏览器）', 'Advanced: chart "Open in Grafana" deep-link params (this browser only)')}</span>
      </button>
      {open && (
        <div className="mt-3 grid grid-cols-1 gap-4 md:grid-cols-2">
          <PromField
            label={tr('设备详情 Dashboard UID', 'Device detail Dashboard UID')}
            hint={tr('安装包默认 provision 为 ongrid-server-detail；只有把面板复制到自己文件夹换了 UID 时才需要改', 'The installer provisions this as ongrid-server-detail by default; only change it if you copied the dashboard to your own folder with a new UID')}
            value={draft.grafanaDashboardUid}
            onChange={(v) => {
              setSavedFlag(false);
              setDraft((d) => ({ ...d, grafanaDashboardUid: v }));
            }}
            placeholder="ongrid-server-detail"
          />
          <PromField
            label="Grafana orgId"
            hint={tr('单 org 安装永远是 1；多 org 隔离时填对应 id', 'Single-org installs are always 1; use the matching id for multi-org isolation')}
            value={draft.grafanaOrgId}
            onChange={(v) => {
              setSavedFlag(false);
              setDraft((d) => ({ ...d, grafanaOrgId: v }));
            }}
            placeholder="1"
          />
          <div className="md:col-span-2 flex items-center gap-3">
            <button
              type="button"
              onClick={save}
              disabled={!dirty}
              className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-200 hover:bg-zinc-800 disabled:cursor-not-allowed disabled:opacity-50"
            >
              <Save size={12} />
              <span>{savedFlag && !dirty ? tr('已保存', 'Saved') : tr('保存', 'Save')}</span>
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

// canTestSync gates the Test/Sync buttons. root_url is required; the
// bearer can come from EITHER sa_token (preferred — the embedded
// bootstrap path mints it) OR api_key (operator-pasted, used for
// external Grafana where they don't have admin to mint a fresh SA).
// sa_token / api_key come back via /reveal so they're actually
// populated when the row exists (the older `!server.sa_token` check
// was always-disabled because we used to strip sensitive values).
function canTestSync(form: GrafanaForm): boolean {
  if (form.root_url.trim() === '') return false;
  return form.sa_token.trim() !== '' || form.api_key.trim() !== '';
}

function StatusLine({ status, dirty }: { status: SyncStatus; dirty: boolean }) {
  const { tr } = useI18n();
  if (dirty) {
    if (status.kind === 'tested-ok') return <p className="mt-3 text-xs text-emerald-400">{tr('✓ 测试通过，可以保存', '✓ Test passed; ready to save')}</p>;
    if (status.kind === 'error') return <p className="mt-3 break-all text-xs text-red-400">✗ {status.msg}</p>;
    return <p className="mt-3 text-xs text-zinc-500">{tr('请先测试连接，通过后才能保存', 'Test the connection before saving')}</p>;
  }
  switch (status.kind) {
    case 'tested-ok':
      return <p className="mt-3 text-xs text-emerald-400">{tr('✓ Grafana 可达，认证通过', '✓ Grafana reachable, auth passed')}</p>;
    case 'synced':
      return (
        <p className="mt-3 text-xs text-emerald-400">
          {tr('✓ 已同步到文件夹 ', '✓ Synced to folder ')}
          <code className="font-mono">{status.res.folder}</code>{tr(' · 数据源 ', ' · datasource ')}
          <code className="font-mono">{(status.res.datasources?.length ? status.res.datasources : [status.res.datasource]).join(', ')}</code>{tr(` · ${status.res.dashboards.length} 个 dashboard`, ` · ${status.res.dashboards.length} dashboard(s)`)}
          {status.res.dashboards.length > 0 && (
            <span className="text-zinc-500">{tr('：', ': ')}{status.res.dashboards.join(tr('、', ', '))}</span>
          )}
        </p>
      );
    case 'error':
      return <p className="mt-3 break-all text-xs text-red-400">✗ {status.msg}</p>;
    default:
      return null;
  }
}

// ---------- Loki / Elasticsearch / Tempo Grafana Explore links ----

// useGrafanaExploreLink builds a Grafana Explore deep-link for one of
// the built-in datasources. It mirrors lib/drilldown.ts's behaviour:
//   1. Pull root_url from system_settings.grafana
//   2. Reject docker-internal hosts (loki:3100 / grafana:3000) the
//      browser can't reach — fall back to same-origin /grafana.
//   3. Build Grafana 11's /explore?schemaVersion=1&panes=... URL.
// Returns null while the root URL is still being fetched so the button
// renders disabled rather than pointing at the wrong place.
function useGrafanaExploreLink(datasource: string, expr: string): string | null {
  const [root, setRoot] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const sameOrigin = `${window.location.origin}/grafana`;
      try {
        const r = await listSettings('grafana');
        for (const it of r.items) {
          if (it.key === 'root_url' && (it.value ?? '').trim() !== '') {
            const stored = it.value.replace(/\/+$/, '');
            if (cancelled) return;
            setRoot(isBrowserReachableURL(stored) ? stored : sameOrigin);
            return;
          }
        }
      } catch {
        /* fall through */
      }
      if (!cancelled) setRoot(sameOrigin);
    })();
    return () => {
      cancelled = true;
    };
  }, []);
  if (!root) return null;
  // datasource string is the managed uid (ongrid-loki / ongrid-tempo /
  // ongrid-prometheus / ongrid-elasticsearch); derive the engine type for the v11
  // panes schema.
  const dsType = datasource.includes('tempo')
    ? 'tempo'
    : datasource.includes('loki')
      ? 'loki'
      : datasource.includes('elasticsearch')
        ? 'elasticsearch'
        : 'prometheus';
  const query =
    dsType === 'tempo'
      ? { query: expr, queryType: 'traceql' }
      : dsType === 'elasticsearch'
        ? { query: expr, metrics: [{ id: '1', type: 'logs' }] }
        : { expr };
  return buildExploreUrl({
    base: root,
    dsType,
    dsUid: datasource,
    query,
    fromMs: 'now-1h',
    toMs: 'now',
  });
}

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

// usePluginCount fetches "已在 N 台 edge 启用 <plugin>" once per mount.
// Returns null while loading, number on success, "err" on failure so the
// card can show inline error text instead of blowing up.
function usePluginCount(name: string): number | null | 'err' {
  const [count, setCount] = useState<number | null | 'err'>(null);
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const r = await getPluginCounts();
        if (cancelled) return;
        setCount(Number(r.counts?.[name] ?? 0));
      } catch {
        if (!cancelled) setCount('err');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [name]);
  return count;
}

type ElasticsearchLogsForm = {
  name: string;
  writeEndpoints: string;
  queryEndpoint: string;
  writeAPIKey: string;
  queryAPIKey: string;
  writeCredentialRef: string;
  queryCredentialRef: string;
  tlsInsecure: boolean;
};

const defaultElasticsearchDataset = 'ongrid.system';
const defaultElasticsearchNamespace = 'default';

const emptyElasticsearchLogsForm: ElasticsearchLogsForm = {
  name: 'external-elasticsearch',
  writeEndpoints: '',
  queryEndpoint: '',
  writeAPIKey: '',
  queryAPIKey: '',
  writeCredentialRef: '',
  queryCredentialRef: '',
  tlsInsecure: false,
};

function integrationError(error: unknown): string {
  return error instanceof ApiError ? error.message : (error as Error).message;
}

function backendToForm(backend: LogBackend): ElasticsearchLogsForm {
  return {
    name: backend.name,
    writeEndpoints: backend.write_endpoints.join('\n'),
    queryEndpoint: backend.query_endpoint,
    writeAPIKey: '',
    queryAPIKey: '',
    writeCredentialRef: backend.write_credential_ref,
    queryCredentialRef: backend.query_credential_ref,
    tlsInsecure: backend.tls_insecure,
  };
}

function LogsIntegrationCard() {
  const { tr } = useI18n();
  const [backend, setBackend] = useState<LogBackend | null>(null);
  const [selected, setSelected] = useState<LogBackendKind>('loki');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const current = currentLogBackend(backend);
  const exploreUrl = useGrafanaExploreLink(
    selected === 'elasticsearch' ? 'ongrid-elasticsearch' : 'ongrid-loki',
    selected === 'elasticsearch' ? '*' : '{ongrid_source=~".+"}',
  );

  const refresh = useCallback(async (initial = false) => {
    try {
      const value = await getLogBackend();
      setBackend(value);
      if (initial) setSelected(currentLogBackend(value));
      setError(null);
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404) {
        setBackend(null);
        if (initial) setSelected('loki');
        setError(null);
      } else {
        setError(integrationError(cause));
      }
    } finally {
      if (initial) setLoading(false);
    }
  }, []);

  useEffect(() => { void refresh(true); }, [refresh]);

  const backendLabel = current === 'elasticsearch' ? 'Elasticsearch' : 'Loki';
  const options: Array<{ kind: LogBackendKind; title: string; description: string; configured: boolean }> = [
    {
      kind: 'loki',
      title: 'Loki',
      description: tr('内置或外部 Loki', 'Built-in or external Loki'),
      configured: true,
    },
    {
      kind: 'elasticsearch',
      title: 'Elasticsearch',
      description: tr('外部全文检索后端', 'External full-text backend'),
      configured: backend != null,
    },
  ];

  return (
    <Card className="overflow-hidden p-0">
      <div className="border-b border-zinc-800 px-5 py-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <FileText size={15} className="text-zinc-400" />
              <h2 className="text-sm font-medium text-zinc-100">{tr('日志集成', 'Logs integration')}</h2>
              {!loading && <span className="inline-flex items-center gap-1.5 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 text-[10px] text-emerald-300">
                <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                {tr(`当前使用 ${backendLabel}`, `Using ${backendLabel}`)}
              </span>}
            </div>
            <p className="mt-2 max-w-4xl text-[11px] leading-5 text-zinc-500">
              {tr('Loki 与 Elasticsearch 互斥运行，只区分当前选中和未选中。选择 Elasticsearch 时会先验证 Manager 连接与权限；设备同步情况通过独立检查查看，不影响后端选择。旧后端数据不会自动合并查询或迁移。', 'Loki and Elasticsearch are mutually exclusive and are either selected or unselected. Manager connectivity and privileges are validated before selecting Elasticsearch. Device convergence is checked separately and never changes the selection. Data in the previous backend is neither merged nor migrated automatically.')}
            </p>
          </div>
          {loading && <span className="inline-flex items-center gap-2 text-xs text-zinc-500"><Loader2 size={13} className="animate-spin" />{tr('读取当前后端…', 'Reading selected backend…')}</span>}
        </div>

        <div className="mt-4 grid max-w-2xl grid-cols-2 gap-1 rounded-lg border border-zinc-800 bg-zinc-950 p-1" role="tablist" aria-label={tr('日志后端', 'Log backend')}>
          {options.map((option) => {
            const active = selected === option.kind;
            const inUse = current === option.kind;
            return (
              <button
                key={option.kind}
                type="button"
                role="tab"
                aria-selected={active}
                onClick={() => setSelected(option.kind)}
                className={cn(
                  'flex min-w-0 items-center justify-between gap-3 rounded-md px-3 py-2.5 text-left transition-colors',
                  active ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200'
                )}
              >
                <span className="min-w-0">
                  <span className="block text-xs font-medium">{option.title}</span>
                  <span className="mt-0.5 block truncate text-[10px] text-zinc-500">{option.description}</span>
                </span>
                <span className={cn('shrink-0 text-[10px]', inUse ? 'text-emerald-400' : option.configured ? 'text-zinc-500' : 'text-zinc-600')}>
                  {inUse ? tr('当前使用', 'In use') : option.configured ? tr('已配置', 'Configured') : tr('未配置', 'Not configured')}
                </span>
              </button>
            );
          })}
        </div>
        {error && <p className="mt-3 text-xs text-red-400">{error}</p>}
      </div>

      <div className="p-5" role="tabpanel">
        {selected === 'elasticsearch' ? (
          <ElasticsearchLogsCard current={current === 'elasticsearch'} exploreUrl={exploreUrl} onBackendChange={setBackend} />
        ) : (
          <LokiCard current={current === 'loki'} backend={backend} exploreUrl={exploreUrl} onBackendChange={setBackend} />
        )}
      </div>
    </Card>
  );
}

function ConnectionCheckProgress({ value, error }: { value: LogBackendConnectionCheck | null; error: string | null }) {
  const { tr } = useI18n();
  if (!value && !error) return null;
  if (!value) return <p className="mt-3 break-all text-xs text-red-400">✗ {error}</p>;

  const complete = value.all_online_verified;
  const stillChecking = !complete && value.pending > 0;
  const ratio = value.online > 0 ? Math.min(1, value.verified / value.online) : 0;
  const statusLabel = complete
    ? tr('在线设备已全部验证', 'All online devices verified')
    : value.online === 0
      ? tr('暂无在线设备可验证', 'No online devices to verify')
      : value.failed > 0 && !stillChecking
        ? tr('部分设备验证失败', 'Some devices failed verification')
        : tr('正在验证在线设备', 'Verifying online devices');

  return (
    <div className="mt-3 overflow-hidden rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-2.5" role="status" aria-live="polite">
      <div className="flex items-center justify-between gap-4">
        <span className={cn('inline-flex min-w-0 items-center gap-2 text-xs', complete ? 'text-emerald-400' : value.failed > 0 ? 'text-amber-400' : 'text-zinc-400')}>
          {stillChecking ? <Loader2 size={13} className="shrink-0 animate-spin motion-reduce:animate-none" /> : <Check size={13} className="shrink-0" />}
          <span className="truncate">{statusLabel}</span>
        </span>
        <span className="shrink-0 text-xs font-semibold tabular-nums text-zinc-200">
          {tr(`在线设备验证 ${value.verified}/${value.online}`, `Online devices verified ${value.verified}/${value.online}`)}
        </span>
      </div>
      <div
        className="mt-2 h-1 overflow-hidden rounded-full bg-zinc-800"
        role="progressbar"
        aria-label={tr('在线设备验证进度', 'Online device verification progress')}
        aria-valuemin={0}
        aria-valuemax={value.online}
        aria-valuenow={value.verified}
      >
        <span
          className={cn('block h-full w-full origin-left transform rounded-full transition-transform duration-500 ease-[cubic-bezier(0.25,1,0.5,1)] motion-reduce:transition-none', complete ? 'bg-emerald-500' : value.failed > 0 ? 'bg-amber-500' : 'bg-sky-500')}
          style={{ transform: `scaleX(${ratio})` }}
        />
      </div>
      {error && <p className="mt-2 break-all text-[11px] text-red-400">✗ {error}</p>}
    </div>
  );
}

function ElasticsearchLogsCard({ current, exploreUrl, onBackendChange }: { current: boolean; exploreUrl: string | null; onBackendChange: (backend: LogBackend) => void }) {
  const { tr } = useI18n();
  const [backend, setBackend] = useState<LogBackend | null>(null);
  const [form, setForm] = useState<ElasticsearchLogsForm>(emptyElasticsearchLogsForm);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [validated, setValidated] = useState(false);
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [connectionCheck, setConnectionCheck] = useState<LogBackendConnectionCheck | null>(null);
  const [connectionCheckError, setConnectionCheckError] = useState<string | null>(null);
  const [revealedKeys, setRevealedKeys] = useState({ write: false, query: false });

  const refresh = useCallback(async (showLoading = true) => {
    if (showLoading) setLoading(true);
    try {
      try {
        const value = await getLogBackend();
        setBackend(value);
        setForm(backendToForm(value));
        setDirty(false);
        setValidated(false);
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) {
          setBackend(null);
          setForm(emptyElasticsearchLogsForm);
          setDirty(false);
          setValidated(false);
        } else {
          throw error;
        }
      }
    } catch (error) {
      setMessage({ ok: false, text: error instanceof ApiError ? error.message : (error as Error).message });
    } finally {
      if (showLoading) setLoading(false);
    }
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  useEffect(() => {
    if (backend) onBackendChange(backend);
  }, [backend, onBackendChange]);

  useEffect(() => {
    setConnectionCheck(null);
    setConnectionCheckError(null);
  }, [backend?.id, backend?.generation]);

  useEffect(() => {
    if (!backend || !connectionCheck || connectionCheck.backend_id !== backend.id || connectionCheck.pending === 0) return;
    const timer = window.setInterval(() => {
      void getLogBackendConnectionCheck(backend.id)
        .then((value) => {
          setConnectionCheck(value);
          setConnectionCheckError(null);
        })
        .catch((error) => setConnectionCheckError(integrationError(error)));
    }, 3000);
    return () => window.clearInterval(timer);
  }, [backend, connectionCheck]);

  const update = <K extends keyof ElasticsearchLogsForm>(key: K, value: ElasticsearchLogsForm[K]) => {
    setMessage(null);
    setDirty(true);
    setValidated(false);
    setForm((current) => ({ ...current, [key]: value }));
  };

  const saveInput = (): SaveLogBackendInput | null => {
    const endpoints = form.writeEndpoints.split(/[\n,]+/).map((value) => value.trim()).filter(Boolean);
    const writeAPIKey = form.writeAPIKey.trim();
    const queryAPIKey = form.queryAPIKey.trim();
    if (!writeAPIKey && !form.writeCredentialRef) {
      setMessage({ ok: false, text: tr('请直接粘贴 ES 写入 API Key。', 'Paste an ES write API key.') });
      return null;
    }
    if (!queryAPIKey && !form.queryCredentialRef) {
      setMessage({ ok: false, text: tr('请粘贴只读查询 API Key。', 'Paste a read-only query API key.') });
      return null;
    }
    if (!writeAPIKey && !queryAPIKey && form.writeCredentialRef === form.queryCredentialRef) {
      setMessage({ ok: false, text: tr('写入 API Key 和只读查询 API Key 必须是两个不同凭证。', 'Write and read-only query API keys must use different credentials.') });
      return null;
    }
    return {
      name: form.name.trim(),
      write_endpoints: endpoints,
      query_endpoint: form.queryEndpoint.trim(),
      dataset: backend?.dataset.trim() || defaultElasticsearchDataset,
      namespace: backend?.namespace.trim() || defaultElasticsearchNamespace,
      write_credential_ref: form.writeCredentialRef || undefined,
      query_credential_ref: form.queryCredentialRef || undefined,
      write_api_key: writeAPIKey || undefined,
      query_api_key: queryAPIKey || undefined,
      preserve_ca: Boolean(backend?.has_custom_ca),
      kibana_url: backend?.kibana_url || undefined,
      tls_insecure: form.tlsInsecure,
    };
  };

  const save = async () => {
    if (!validated) return;
    const input = saveInput();
    if (!input) return;
    setBusy('save');
    setMessage(null);
    try {
      const value = await saveLogBackend(input);
      setBackend(value);
      setForm(backendToForm(value));
      setDirty(false);
      setValidated(false);
      setRevealedKeys({ write: false, query: false });
      setMessage({ ok: true, text: tr('配置已保存；点击“设为当前”后切换日志链路。', 'Configuration saved. Select it to switch the log pipeline.') });
    } catch (error) {
      setMessage({ ok: false, text: integrationError(error) });
    } finally {
      setBusy(null);
    }
  };

  const selectElasticsearch = async () => {
	if (!backend || dirty || backend.status !== 'unselected') return;
	setBusy('select');
    setMessage(null);
    try {
      const value = await selectLogBackend(backend.id);
      setBackend(value);
      setConnectionCheck(null);
      setConnectionCheckError(null);
      setMessage({
        ok: true,
        text: tr(
          'Elasticsearch 已设为当前日志后端；可点击“检查设备连接”核对各 Edge 是否同步并写入当前版本。',
          'Elasticsearch is now selected. Check device connections to verify that each edge synced and wrote to the current generation.',
        ),
      });
    } catch (error) {
      setMessage({ ok: false, text: integrationError(error) });
    } finally {
      setBusy(null);
    }
  };

  const checkDeviceConnections = async () => {
    if (!backend || !current || backend.status !== 'selected' || dirty) return;
    setBusy('connections');
    setConnectionCheckError(null);
    try {
      const value = await startLogBackendConnectionCheck(backend.id);
      setConnectionCheck(value);
    } catch (error) {
      setConnectionCheckError(integrationError(error));
    } finally {
      setBusy(null);
    }
  };

  const testElasticsearch = async () => {
    const input = saveInput();
    if (!input) return;
    setBusy('test');
    setValidated(false);
    setMessage(null);
    try {
      const result = await testLogBackendDraft(input);
      const version = result.detected_version
        ? tr(`（Elasticsearch ${result.detected_version}）`, ` (Elasticsearch ${result.detected_version})`)
        : '';
      setMessage({
        ok: true,
        text: tr(
          `连接测试通过；查询/写入端点及 API Key 权限有效${version}。`,
          `Connection test passed. Query/write endpoints and API key privileges are valid${version}.`,
        ),
      });
      setValidated(true);
    } catch (error) {
      setValidated(false);
      setMessage({ ok: false, text: integrationError(error) });
    } finally {
      setBusy(null);
    }
  };

  const canEdit = true;
  const editingCurrent = current && backend?.status === 'selected';
  const canSave = canEdit && dirty && validated;
  const canTest = true;
  const canSelect = backend != null && !dirty && backend.status === 'unselected';
  const canCheckConnections = backend != null && current && backend.status === 'selected' && !dirty;

  return (
    <section aria-label={tr('Elasticsearch 日志后端配置', 'Elasticsearch log backend configuration')}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <Database size={14} className="text-sky-400" />
            <h3 className="text-sm font-medium text-zinc-100">{tr('Elasticsearch 配置', 'Elasticsearch configuration')}</h3>
            {current && <span className="text-[10px] text-emerald-400">{tr('当前选中', 'Selected')}</span>}
          </div>
          <p className="mt-2 max-w-4xl text-[11px] leading-5 text-zinc-500">
            {tr('Edge 上的 otelcol-contrib 直接写入这些 endpoint，日志正文不经过 Manager。设为当前后，日志中心只查询 Elasticsearch；Loki 数据不会同时展示。', 'otelcol-contrib on each edge writes directly to these endpoints; log bytes never pass through Manager. Once selected, Log Center queries Elasticsearch only and does not display Loki data alongside it.')}
          </p>
        </div>
      </div>

      {loading ? <div className="flex h-36 items-center justify-center text-sm text-zinc-500"><Loader2 size={14} className="mr-2 animate-spin" />{tr('加载中…', 'Loading…')}</div> : (
        <>
          <fieldset disabled={!canEdit || busy !== null} className="mt-5 grid grid-cols-1 gap-4 disabled:opacity-65 md:grid-cols-2">
            <PromField label={tr('写入 endpoints（每行一个）', 'Write endpoints (one per line)')} hint={tr('仅允许根路径；生产环境要求 HTTPS。', 'Root URLs only; HTTPS is required in production.')} value={form.writeEndpoints} onChange={(value) => update('writeEndpoints', value)} placeholder="https://es-data-1.example.com:9200" />
            <PromField label={tr('Manager 查询 endpoint', 'Manager query endpoint')} hint={tr('必须可从 Manager 网络访问。', 'Must be reachable from the Manager network.')} value={form.queryEndpoint} onChange={(value) => update('queryEndpoint', value)} placeholder="https://es-query.example.com:9200" />
            <PromField
              label={tr('ES 写入 Encoded API Key', 'ES write Encoded API Key')}
              hint={tr('在 Elasticsearch 中创建后直接粘贴 encoded 值；已保存时留空即保持原 Key。', 'Paste the encoded value created in Elasticsearch; leave blank after saving to keep the current key.')}
              value={form.writeAPIKey}
              onChange={(value) => update('writeAPIKey', value)}
              placeholder={backend ? tr('已安全保存；留空保持不变', 'Stored securely; leave blank to keep') : 'ZXlKaGJHY2lPaUpJVXpJMU5pSjkuLi4='}
              sensitive
              revealed={revealedKeys.write}
              onToggleReveal={() => setRevealedKeys((current) => ({ ...current, write: !current.write }))}
            />
            <PromField
              label={tr('ES 只读查询 Encoded API Key', 'ES read-only query Encoded API Key')}
              hint={tr('建议仅授予目标 data stream 的读取、字段与聚合权限。', 'Grant only read, field, and aggregation access to the target data stream.')}
              value={form.queryAPIKey}
              onChange={(value) => update('queryAPIKey', value)}
              placeholder={backend ? tr('已安全保存；留空保持不变', 'Stored securely; leave blank to keep') : 'ZXlKaGJHY2lPaUpJVXpJMU5pSjkuLi4='}
              sensitive
              revealed={revealedKeys.query}
              onToggleReveal={() => setRevealedKeys((current) => ({ ...current, query: !current.query }))}
            />
            <label className="flex items-center gap-2 text-xs text-zinc-300 md:col-span-2"><input type="checkbox" checked={form.tlsInsecure} onChange={(event) => update('tlsInsecure', event.target.checked)} className="accent-amber-500" />{tr('测试环境：允许 HTTP / 跳过 TLS 校验', 'Test environments: allow HTTP / skip TLS verification')}</label>
          </fieldset>

          {editingCurrent && <p className="mt-3 text-[11px] leading-5 text-zinc-500">{tr('保存不会影响当前日志链路；点击“设为当前”会在 Manager 连接与权限测试通过后切换，设备同步可另行检查。', 'Saving does not affect the current log pipeline. Selecting switches after Manager connectivity and privilege checks; device convergence can be checked separately.')}</p>}

          <div className="mt-2 text-[11px] leading-5 text-zinc-500">{tr('API Key 是只写字段：Manager 接收后立即放入加密凭证库，读取后端配置时不会回显；Edge 只通过专用密钥通道取得写 Key。', 'API keys are write-only: Manager immediately stores them in the encrypted credential vault and never echoes them when reading backend configuration; Edge receives only the write key through the dedicated secret channel.')}</div>

          <div className="mt-5 flex flex-wrap items-end gap-3 border-t border-zinc-800/70 pt-4">
            <Button onClick={() => void testElasticsearch()} disabled={!canTest || busy !== null} variant="ghost">{busy === 'test' ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}<span>{tr('测试连接', 'Test connection')}</span></Button>
            <Button onClick={() => void save()} disabled={!canSave || busy !== null} variant="primary">{busy === 'save' ? <Loader2 size={14} className="animate-spin" /> : <Save size={14} />}<span>{tr('保存', 'Save')}</span></Button>
            {dirty && <span className="text-xs text-zinc-500">{validated ? tr('测试通过，可以保存', 'Test passed; ready to save') : tr('请先测试连接，通过后才能保存', 'Test the connection before saving')}</span>}
            <Button onClick={() => void selectElasticsearch()} disabled={!canSelect || busy !== null} variant="primary">{busy === 'select' ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}<span>{tr('设为当前', 'Select')}</span></Button>
            <Button onClick={() => void checkDeviceConnections()} disabled={!canCheckConnections || busy !== null} variant="ghost">{busy === 'connections' ? <Loader2 size={14} className="animate-spin" /> : <RefreshCw size={14} />}<span>{tr('检查设备连接', 'Check device connections')}</span></Button>
            <button
              type="button"
              disabled={!current || !exploreUrl}
              onClick={() => current && exploreUrl && void openObservabilityUrl(exploreUrl)}
              className={cn(
                'inline-flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm transition-colors',
                current && exploreUrl
                  ? 'border-zinc-700 text-zinc-200 hover:border-zinc-500 hover:bg-zinc-800'
                  : 'cursor-not-allowed border-zinc-800 text-zinc-600'
              )}
            >
              <ExternalLink size={14} />
              <span>{tr('在 Grafana 中查看日志', 'Open logs in Grafana')}</span>
            </button>
          </div>

          {message && <p className={cn('mt-3 break-all text-xs', message.ok ? 'text-emerald-400' : 'text-red-400')}>{message.ok ? '✓ ' : '✗ '}{message.text}</p>}

          <ConnectionCheckProgress value={connectionCheck} error={connectionCheckError} />

        </>
      )}
    </section>
  );
}

// LokiCard mirrors PrometheusCard. Admin fills in URL + optional basic
// auth + TLS-skip; the manager seeds the URL on first boot from the
// ONGRID_LOG_URL env var (default http://loki:3100, the docker-
// internal service). A URL whose hostname has no dot is treated as
// the docker-internal default — edges fall through to manager nginx
// /loki/api/v1/push instead. Override with a customer URL (e.g.
// https://loki.customer.com) and edges push there directly.
type LokiForm = {
  url: string;
  basic_user: string;
  basic_password: string;
  tls_insecure: string; // "true" / "false" / ""
};

const LOKI_KEYS: (keyof LokiForm)[] = ['url', 'basic_user', 'basic_password', 'tls_insecure'];
const LOKI_SENSITIVE: Set<keyof LokiForm> = new Set(['basic_password']);
const emptyLokiForm: LokiForm = { url: '', basic_user: '', basic_password: '', tls_insecure: '' };

function LokiCard({ current, backend, exploreUrl, onBackendChange }: { current: boolean; backend: LogBackend | null; exploreUrl: string | null; onBackendChange: (backend: LogBackend) => void }) {
  const { tr } = useI18n();
  const [server, setServer] = useState<LokiForm>(emptyLokiForm);
  const [draft, setDraft] = useState<LokiForm>(emptyLokiForm);
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [switching, setSwitching] = useState(false);
  const [switchMessage, setSwitchMessage] = useState<string | null>(null);
  const [savedOk, setSavedOk] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [grafanaSyncWarning, setGrafanaSyncWarning] = useState<string | null>(null);
  const [checkingConnections, setCheckingConnections] = useState(false);
  const [connectionCheck, setConnectionCheck] = useState<LogBackendConnectionCheck | null>(null);
  const [connectionCheckError, setConnectionCheckError] = useState<string | null>(null);
  const [probe, setProbe] = useState<
    | { kind: 'idle' }
    | { kind: 'testing' }
    | { kind: 'ok' }
    | { kind: 'error'; msg: string }
  >({ kind: 'idle' });
  const probeRequest = useRef(0);

  const refresh = useCallback(async () => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setLoading(true);
    setErr(null);
    try {
      const r = await listSettings('loki');
      const next = { ...emptyLokiForm };
      for (const it of r.items as SystemSetting[]) {
        if (!(LOKI_KEYS as string[]).includes(it.key)) continue;
        if (!LOKI_SENSITIVE.has(it.key as keyof LokiForm)) {
          (next as Record<string, string>)[it.key] = it.value ?? '';
        }
      }
      await Promise.all(
        (r.items as SystemSetting[])
          .filter(
            (it) => LOKI_SENSITIVE.has(it.key as keyof LokiForm) && (it.value ?? '') !== ''
          )
          .map(async (it) => {
            try {
              const real = await revealSetting('loki', it.key);
              (next as Record<string, string>)[it.key] = real.value ?? '';
            } catch {
              /* leave empty so user can paste fresh */
            }
          })
      );
      setServer(next);
      setDraft(next);
      setRevealed({});
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (current) return;
    setConnectionCheck(null);
    setConnectionCheckError(null);
  }, [current]);

  useEffect(() => {
    if (!current || !connectionCheck || connectionCheck.backend !== 'loki' || connectionCheck.pending === 0) return;
    const timer = window.setInterval(() => {
      void getLogBackendConnectionCheck()
        .then((value) => {
          setConnectionCheck(value);
          setConnectionCheckError(null);
        })
        .catch((error) => setConnectionCheckError(integrationError(error)));
    }, 3000);
    return () => window.clearInterval(timer);
  }, [current, connectionCheck]);

  const dirty = LOKI_KEYS.some((k) => draft[k] !== server[k]);
  const update = (k: keyof LokiForm, v: string) => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setSavedOk(false);
    setGrafanaSyncWarning(null);
    setDraft((cur) => ({ ...cur, [k]: v }));
  };
  const submit = async () => {
    if (probe.kind !== 'ok') return;
    setSaving(true);
    setErr(null);
    setGrafanaSyncWarning(null);
    try {
      for (const k of LOKI_KEYS) {
        if (draft[k] === server[k]) continue;
        await setSetting('loki', k, draft[k], LOKI_SENSITIVE.has(k));
      }
      await refresh();
      setSavedOk(true);
      try {
        await syncLokiDatasource();
      } catch (e) {
        const msg = e instanceof ApiError ? e.message : (e as Error).message;
        setGrafanaSyncWarning(
          tr(
            `Loki 已保存，但 Grafana 数据源同步失败：${msg}`,
            `Loki was saved, but the Grafana datasource sync failed: ${msg}`
          )
        );
      }
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  const probeLoki = async () => {
    const requestID = ++probeRequest.current;
    setProbe({ kind: 'testing' });
    try {
      await testLokiConnection({
        url: draft.url,
        basic_user: draft.basic_user,
        basic_password: draft.basic_password,
        tls_insecure: draft.tls_insecure === 'true',
      });
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'ok' });
    } catch (e) {
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    }
  };

  const switchToLoki = async () => {
    if (!backend) return;
    setSwitching(true);
    setSwitchMessage(null);
    try {
      const value = await selectLokiLogBackend();
      onBackendChange(value);
      setSwitchMessage(tr('Loki 已设为当前日志后端；可点击“检查设备连接”验证在线设备。', 'Loki is now selected. Check device connections separately to verify online devices.'));
    } catch (cause) {
      setErr(integrationError(cause));
    } finally {
      setSwitching(false);
    }
  };

  const checkDeviceConnections = async () => {
    if (!current || dirty) return;
    setCheckingConnections(true);
    setConnectionCheckError(null);
    try {
      const value = await startLogBackendConnectionCheck();
      setConnectionCheck(value);
    } catch (cause) {
      setConnectionCheckError(integrationError(cause));
    } finally {
      setCheckingConnections(false);
    }
  };

  return (
    <section aria-label={tr('Loki 日志后端配置', 'Loki log backend configuration')}>
      <div className="mb-3 flex items-center gap-2">
        <FileText size={14} className="text-zinc-400" />
        <h3 className="text-sm font-medium text-zinc-100">{tr('Loki 配置', 'Loki configuration')}</h3>
        {current && <span className="text-[10px] text-emerald-400">{tr('当前选中', 'Selected')}</span>}
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">
        {tr('可使用内置 Loki，也可以填写外部 Loki / VictoriaLogs URL。设为当前后，Edge 只写入 Loki，日志中心也只查询 Loki；Elasticsearch 中的数据不会同时展示。', 'Use the built-in Loki or configure an external Loki / VictoriaLogs URL. Once selected, Edge writes only to Loki and Log Center queries only Loki; Elasticsearch data is not displayed alongside it.')}
      </p>

      {loading ? (
        <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <PromField
            label="Loki URL"
            hint={tr('例 https://loki.customer.com（外部）；http://loki:3100（内置默认）', 'e.g. https://loki.customer.com (external); http://loki:3100 (built-in default)')}
            value={draft.url}
            onChange={(v) => update('url', v)}
            placeholder="http://loki:3100"
          />
          <PromField
            label="Basic User"
            value={draft.basic_user}
            onChange={(v) => update('basic_user', v)}
            placeholder={tr('（留空 = 不用 Basic）', '(empty = no Basic)')}
          />
          <PromField
            label="Basic Password"
            sensitive
            revealed={!!revealed.basic_password}
            onToggleReveal={() =>
              setRevealed((r) => ({ ...r, basic_password: !r.basic_password }))
            }
            value={draft.basic_password}
            onChange={(v) => update('basic_password', v)}
            placeholder={tr('（留空 = 不用 Basic）', '(empty = no Basic)')}
          />
          <label className="flex items-center gap-2 text-xs text-zinc-300">
            <input
              type="checkbox"
              checked={draft.tls_insecure === 'true'}
              onChange={(e) => update('tls_insecure', e.target.checked ? 'true' : 'false')}
              className="h-3.5 w-3.5 rounded border-zinc-600 bg-zinc-900 accent-emerald-500"
            />
            {tr('跳过 TLS 校验（自签证书时勾选）', 'Skip TLS verification (check this for self-signed certs)')}
          </label>
        </div>
      )}

      <div className="mt-5 flex flex-wrap items-center gap-3">
        <Button
          onClick={probeLoki}
          disabled={probe.kind === 'testing' || draft.url.trim() === ''}
          variant="ghost"
        >
          {probe.kind === 'testing' ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}
          <span>{tr('测试连接', 'Test connection')}</span>
        </Button>
        <Button onClick={submit} disabled={!dirty || saving || probe.kind !== 'ok'} variant="primary">
          {savedOk && !dirty ? <Check size={14} /> : <Save size={14} />}
          <span>{saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}</span>
        </Button>
        <Button onClick={() => void switchToLoki()} disabled={current || !backend || switching || dirty || probe.kind === 'testing'} variant="primary">
          {switching ? <Loader2 size={14} className="animate-spin" /> : <Check size={14} />}
          <span>{tr('设为当前', 'Select')}</span>
        </Button>
        <Button onClick={() => void checkDeviceConnections()} disabled={!current || dirty || loading || saving || switching || checkingConnections || probe.kind === 'testing'} variant="ghost">
          {checkingConnections ? <Loader2 size={14} className="animate-spin motion-reduce:animate-none" /> : <RefreshCw size={14} />}
          <span>{tr('检查设备连接', 'Check device connections')}</span>
        </Button>
        <button
          type="button"
          disabled={!exploreUrl}
          onClick={() => exploreUrl && void openObservabilityUrl(exploreUrl)}
          className={cn(
            'inline-flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm transition-colors',
            exploreUrl
              ? 'border-zinc-700 text-zinc-200 hover:border-zinc-500 hover:bg-zinc-800'
              : 'cursor-not-allowed border-zinc-800 text-zinc-600'
          )}
        >
          <ExternalLink size={14} />
          <span>{tr('在 Grafana 中查看日志', 'Open logs in Grafana')}</span>
        </button>
        {err && <span className="break-all text-xs text-red-400">{err}</span>}
        {grafanaSyncWarning && <span className="break-all text-xs text-amber-400">{grafanaSyncWarning}</span>}
        {dirty && <span className="text-xs text-zinc-500">{probe.kind === 'ok' ? tr('测试通过，可以保存', 'Test passed; ready to save') : tr('请先测试连接，通过后才能保存', 'Test the connection before saving')}</span>}
      </div>
      {switchMessage && <p className="mt-3 text-xs text-emerald-400">✓ {switchMessage}</p>}
      <ConnectionCheckProgress value={connectionCheck} error={connectionCheckError} />
      <ProbeLine probe={probe} okLabel={tr('✓ Loki 可达，/ready 返回成功', '✓ Loki reachable, /ready returned success')} />
      <BuiltInStorageAdvanced service="loki" />
    </section>
  );
}

// TempoCard mirrors LokiCard — same shape, different category. The
// URL points at the OTLP HTTP push endpoint (e.g. /v1/traces). Empty
// / default = internal tempo:3200, edges push to manager OTLP write
// path.
type TempoForm = {
  url: string;
  basic_user: string;
  basic_password: string;
  tls_insecure: string;
};

const TEMPO_KEYS: (keyof TempoForm)[] = ['url', 'basic_user', 'basic_password', 'tls_insecure'];
const TEMPO_SENSITIVE: Set<keyof TempoForm> = new Set(['basic_password']);
const emptyTempoForm: TempoForm = { url: '', basic_user: '', basic_password: '', tls_insecure: '' };

function TempoCard() {
  const { tr } = useI18n();
  const count = usePluginCount('traces');
  const exploreUrl = useGrafanaExploreLink('ongrid-tempo', '{}');
  const [server, setServer] = useState<TempoForm>(emptyTempoForm);
  const [draft, setDraft] = useState<TempoForm>(emptyTempoForm);
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [savedOk, setSavedOk] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [probe, setProbe] = useState<
    | { kind: 'idle' }
    | { kind: 'testing' }
    | { kind: 'ok' }
    | { kind: 'error'; msg: string }
  >({ kind: 'idle' });
  const probeRequest = useRef(0);

  const refresh = useCallback(async () => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setLoading(true);
    setErr(null);
    try {
      const r = await listSettings('tempo');
      const next = { ...emptyTempoForm };
      for (const it of r.items as SystemSetting[]) {
        if (!(TEMPO_KEYS as string[]).includes(it.key)) continue;
        if (!TEMPO_SENSITIVE.has(it.key as keyof TempoForm)) {
          (next as Record<string, string>)[it.key] = it.value ?? '';
        }
      }
      await Promise.all(
        (r.items as SystemSetting[])
          .filter(
            (it) => TEMPO_SENSITIVE.has(it.key as keyof TempoForm) && (it.value ?? '') !== ''
          )
          .map(async (it) => {
            try {
              const real = await revealSetting('tempo', it.key);
              (next as Record<string, string>)[it.key] = real.value ?? '';
            } catch {
              /* leave empty */
            }
          })
      );
      setServer(next);
      setDraft(next);
      setRevealed({});
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const dirty = TEMPO_KEYS.some((k) => draft[k] !== server[k]);
  const update = (k: keyof TempoForm, v: string) => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setSavedOk(false);
    setDraft((cur) => ({ ...cur, [k]: v }));
  };
  const submit = async () => {
    if (probe.kind !== 'ok') return;
    setSaving(true);
    setErr(null);
    try {
      for (const k of TEMPO_KEYS) {
        if (draft[k] === server[k]) continue;
        await setSetting('tempo', k, draft[k], TEMPO_SENSITIVE.has(k));
      }
      await refresh();
      setSavedOk(true);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  const probeTempo = async () => {
    const requestID = ++probeRequest.current;
    setProbe({ kind: 'testing' });
    try {
      await testTempoConnection({
        url: draft.url,
        basic_user: draft.basic_user,
        basic_password: draft.basic_password,
        tls_insecure: draft.tls_insecure === 'true',
      });
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'ok' });
    } catch (e) {
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    }
  };

  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center gap-2">
        <GitBranch size={14} className="text-zinc-400" />
        <h2 className="text-sm font-medium text-zinc-100">{tr('Tempo 集成（链路）', 'Tempo integration (traces)')}</h2>
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">
        {tr('填外部 Tempo / VictoriaTraces 的 OTLP HTTP 端点（含 ', 'Set an external Tempo / VictoriaTraces OTLP HTTP endpoint (including the ')}<code className="font-mono text-zinc-400">/v1/traces</code>{tr(' 路径）后，边端 ', ' path), and the edge ')}<code className="font-mono text-zinc-400">traces</code>{tr(' plugin 直接推到这里，Grafana 数据源也走这里。留空 / 留默认 = 走内置 docker-compose 的 ', ' plugin pushes there directly; the Grafana datasource also points here. Empty / default = use the bundled docker-compose ')}<code className="font-mono text-zinc-400">tempo</code>{tr(' 容器。', ' container.')}
      </p>

      <div className="mb-4 space-y-2 text-[12px] text-zinc-300">
        <PluginCountLine label="traces plugin" count={count} />
      </div>

      {loading ? (
        <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <PromField
            label="Tempo URL"
            hint={tr('例 https://tempo.customer.com/v1/traces；http://tempo:3200（内置默认）', 'e.g. https://tempo.customer.com/v1/traces; http://tempo:3200 (built-in default)')}
            value={draft.url}
            onChange={(v) => update('url', v)}
            placeholder="http://tempo:3200"
          />
          <PromField
            label="Basic User"
            value={draft.basic_user}
            onChange={(v) => update('basic_user', v)}
            placeholder={tr('（留空 = 不用 Basic）', '(empty = no Basic)')}
          />
          <PromField
            label="Basic Password"
            sensitive
            revealed={!!revealed.basic_password}
            onToggleReveal={() =>
              setRevealed((r) => ({ ...r, basic_password: !r.basic_password }))
            }
            value={draft.basic_password}
            onChange={(v) => update('basic_password', v)}
            placeholder={tr('（留空 = 不用 Basic）', '(empty = no Basic)')}
          />
          <label className="flex items-center gap-2 text-xs text-zinc-300">
            <input
              type="checkbox"
              checked={draft.tls_insecure === 'true'}
              onChange={(e) => update('tls_insecure', e.target.checked ? 'true' : 'false')}
              className="h-3.5 w-3.5 rounded border-zinc-600 bg-zinc-900 accent-emerald-500"
            />
            {tr('跳过 TLS 校验（自签证书时勾选）', 'Skip TLS verification (check this for self-signed certs)')}
          </label>
        </div>
      )}

      <div className="mt-5 flex flex-wrap items-center gap-3">
        <Button
          onClick={probeTempo}
          disabled={probe.kind === 'testing' || draft.url.trim() === ''}
          variant="ghost"
        >
          {probe.kind === 'testing' ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}
          <span>{tr('测试连接', 'Test connection')}</span>
        </Button>
        <Button onClick={submit} disabled={!dirty || saving || probe.kind !== 'ok'} variant="primary">
          {savedOk && !dirty ? <Check size={14} /> : <Save size={14} />}
          <span>{saving ? tr('保存中…', 'Saving…') : savedOk && !dirty ? tr('已保存', 'Saved') : tr('保存', 'Save')}</span>
        </Button>
        <button
          type="button"
          disabled={!exploreUrl}
          onClick={() => exploreUrl && void openObservabilityUrl(exploreUrl)}
          className={cn(
            'inline-flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm transition-colors',
            exploreUrl
              ? 'border-zinc-700 text-zinc-200 hover:border-zinc-500 hover:bg-zinc-800'
              : 'cursor-not-allowed border-zinc-800 text-zinc-600'
          )}
        >
          <ExternalLink size={14} />
          <span>{tr('在 Grafana 中查看链路', 'Open traces in Grafana')}</span>
        </button>
        {dirty && <span className="text-xs text-zinc-500">{probe.kind === 'ok' ? tr('测试通过，可以保存', 'Test passed; ready to save') : tr('请先测试连接，通过后才能保存', 'Test the connection before saving')}</span>}
        {err && <span className="break-all text-xs text-red-400">{err}</span>}
      </div>
      <ProbeLine probe={probe} okLabel={tr('✓ Tempo 连接测试成功', '✓ Tempo connection test succeeded')} />
      <BuiltInStorageAdvanced service="tempo" />
    </Card>
  );
}

// ProbeLine renders a generic probe outcome below the action row.
function ProbeLine({
  probe,
  okLabel,
}: {
  probe: { kind: 'idle' } | { kind: 'testing' } | { kind: 'ok' } | { kind: 'error'; msg: string };
  okLabel: string;
}) {
  switch (probe.kind) {
    case 'ok':
      return <p className="mt-3 text-xs text-emerald-400">{okLabel}</p>;
    case 'error':
      return <p className="mt-3 break-all text-xs text-red-400">✗ {probe.msg}</p>;
    default:
      return null;
  }
}

function PluginCountLine({ label, count }: { label: string; count: number | null | 'err' }) {
  const { tr } = useI18n();
  if (count === 'err') {
    return (
      <div className="text-[11px] text-amber-300">
        {tr('无法获取 plugin 启用统计（plugin runtime 后端可能未就绪）', 'Cannot fetch plugin enablement (the plugin runtime backend may not be ready)')}
      </div>
    );
  }
  return (
    <div className="text-zinc-400">
      {tr('已在 ', 'Enabled on ')}<span className="font-mono text-zinc-200">{count ?? '…'}</span>{tr(' 台 edge 启用 ', ' edge(s) — ')}{label}
    </div>
  );
}

// ---------- WebSearch (multi-provider) card ----------------------
//
// Backs the manager-scoped `web_search` skill. The skill dispatches to
// one of three providers; the operator picks via radio:
//
//   - SearXNG (default, self-hosted, zero-key, runs in docker-compose)
//   - Tavily  (commercial, 1k/月 free tier, returns auto-answer too)
//   - Brave Search (commercial, 2k/月 free tier, links only)
//
// Per-provider fields are shown contextually so the form doesn't ask
// the operator for irrelevant credentials. The 测试连接 button issues
// a 1-result probe to the unsaved draft before the save button is enabled.

type WebSearchForm = {
  provider: string; // "searxng" | "tavily" | "brave"
  searxng_url: string;
  tavily_api_key: string;
  brave_api_key: string;
};

const WEBSEARCH_KEYS: (keyof WebSearchForm)[] = [
  'provider',
  'searxng_url',
  'tavily_api_key',
  'brave_api_key',
];
const WEBSEARCH_SENSITIVE: Set<keyof WebSearchForm> = new Set([
  'tavily_api_key',
  'brave_api_key',
]);
const emptyWebSearchForm: WebSearchForm = {
  provider: 'searxng',
  searxng_url: 'http://searxng:8080',
  tavily_api_key: '',
  brave_api_key: '',
};

function WebSearchCard() {
  const { tr } = useI18n();
  const [server, setServer] = useState<WebSearchForm>(emptyWebSearchForm);
  const [draft, setDraft] = useState<WebSearchForm>(emptyWebSearchForm);
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [savedOk, setSavedOk] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [probe, setProbe] = useState<
    | { kind: 'idle' }
    | { kind: 'testing' }
    | { kind: 'ok'; provider: string; sample: string }
    | { kind: 'error'; msg: string }
  >({ kind: 'idle' });
  const probeRequest = useRef(0);

  const refresh = useCallback(async () => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setLoading(true);
    setErr(null);
    try {
      const r = await listSettings('websearch');
      const next = { ...emptyWebSearchForm };
      for (const it of r.items as SystemSetting[]) {
        if (!(WEBSEARCH_KEYS as string[]).includes(it.key)) continue;
        if (!WEBSEARCH_SENSITIVE.has(it.key as keyof WebSearchForm)) {
          (next as Record<string, string>)[it.key] = it.value ?? '';
        }
      }
      await Promise.all(
        (r.items as SystemSetting[])
          .filter(
            (it) => WEBSEARCH_SENSITIVE.has(it.key as keyof WebSearchForm) && (it.value ?? '') !== ''
          )
          .map(async (it) => {
            try {
              const real = await revealSetting('websearch', it.key);
              (next as Record<string, string>)[it.key] = real.value ?? '';
            } catch {
              /* leave empty so the user can paste a fresh value */
            }
          })
      );
      // Normalise: a fresh DB without seeds returns provider="" — fall
      // back to the default so the radio renders something selected.
      if (!next.provider) next.provider = 'searxng';
      if (!next.searxng_url) next.searxng_url = 'http://searxng:8080';
      setServer(next);
      setDraft(next);
      setRevealed({});
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const dirty = WEBSEARCH_KEYS.some((k) => draft[k] !== server[k]);
  const update = (k: keyof WebSearchForm, v: string) => {
    probeRequest.current += 1;
    setProbe({ kind: 'idle' });
    setSavedOk(false);
    setDraft((cur) => ({ ...cur, [k]: v }));
  };

  const submit = async () => {
    if (probe.kind !== 'ok') return;
    setSaving(true);
    setErr(null);
    try {
      for (const k of WEBSEARCH_KEYS) {
        if (draft[k] === server[k]) continue;
        await setSetting('websearch', k, draft[k], WEBSEARCH_SENSITIVE.has(k));
      }
      await refresh();
      setSavedOk(true);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const probeWebSearch = async () => {
    const requestID = ++probeRequest.current;
    setProbe({ kind: 'testing' });
    try {
      const r = await testWebSearchConnection(draft);
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'ok', provider: r.provider, sample: r.sample });
    } catch (e) {
      if (requestID !== probeRequest.current) return;
      setProbe({ kind: 'error', msg: e instanceof ApiError ? e.message : (e as Error).message });
    }
  };

  // Status hint at the bottom of the card. Encodes the provider-specific
  // "ok / missing key" verdict the same way the skill itself decides.
  const statusHint = (() => {
    if (dirty) return probe.kind === 'ok' ? tr('测试通过，可以保存', 'Test passed; ready to save') : tr('请先测试连接，通过后才能保存', 'Test the connection before saving');
    switch (server.provider) {
      case 'searxng':
        return tr(`已选 SearXNG · ${server.searxng_url || 'http://searxng:8080'}`, `Using SearXNG · ${server.searxng_url || 'http://searxng:8080'}`);
      case 'tavily':
        return server.tavily_api_key
          ? tr('已选 Tavily · key 已配置', 'Using Tavily · key configured')
          : tr('已选 Tavily · 缺 key — AI 调用会返回 skipped_reason', 'Using Tavily · key missing — AI calls will return skipped_reason');
      case 'brave':
        return server.brave_api_key
          ? tr('已选 Brave · key 已配置', 'Using Brave · key configured')
          : tr('已选 Brave · 缺 key — AI 调用会返回 skipped_reason', 'Using Brave · key missing — AI calls will return skipped_reason');
      default:
        return '';
    }
  })();

  return (
    <Card className="p-5">
      <div className="mb-3 flex items-center gap-2">
        <Search size={14} className="text-zinc-400" />
        <h2 className="text-sm font-medium text-zinc-100">{tr('联网搜索（web_search skill）', 'Web search (web_search skill)')}</h2>
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">
        {tr('AI agent 的 ', 'The AI agent\'s ')}<code className="font-mono text-zinc-400">web_search</code>
        {tr(
          ' 技能可走三种 provider，默认走 SearXNG（自托管聚合搜索，零 key 零额度）。切换到 Tavily / Brave 需要在对应平台注册 API key。无论选哪个，技能调用方式一致；只有响应里 ',
          ' skill supports three providers; defaults to SearXNG (self-hosted aggregator, no key, no quota). Switching to Tavily / Brave needs an API key from the corresponding platform. The skill call shape is identical — only the response\'s ',
        )}<code className="font-mono text-zinc-400">provider</code>{tr(' 字段不同。', ' field differs.')}
      </p>

      {loading ? (
        <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : (
        <div className="space-y-4">
          <ProviderBlock
            id="searxng"
            label="SearXNG"
            badge={tr('自托管，零成本默认', 'Self-hosted, zero-cost default')}
            checked={draft.provider === 'searxng'}
            onSelect={() => update('provider', 'searxng')}
            description={tr(
              'docker-compose 里跟 Loki/Tempo/Prom 一起跑的元搜索聚合器，把查询发到 Bing/DuckDuckGo/Brave/百度/搜狗 等，再合并结果。无 API key、无额度限制。',
              'Meta-search aggregator that runs alongside Loki/Tempo/Prom in docker-compose; fans a query out to Bing/DuckDuckGo/Brave/Baidu/Sogou and merges the results. No API key, no quota.',
            )}
          >
            <PromField
              label="SearXNG URL"
              hint={tr('默认 http://searxng:8080（docker 内网）；外部部署填完整 URL', 'Default http://searxng:8080 (docker internal); use the full URL for external deployments')}
              value={draft.searxng_url}
              onChange={(v) => update('searxng_url', v)}
              placeholder="http://searxng:8080"
            />
          </ProviderBlock>

          <ProviderBlock
            id="tavily"
            label="Tavily"
            badge={tr('需 API key · 1000 次/月免费', 'API key required · 1000 calls/month free')}
            checked={draft.provider === 'tavily'}
            onSelect={() => update('provider', 'tavily')}
            description={
              <>
                {tr('注册 ', 'Register at ')}
                <a
                  href="https://tavily.com"
                  target="_blank"
                  rel="noreferrer"
                  className="text-emerald-400 hover:text-emerald-300"
                >
                  tavily.com
                </a>{' '}
                {tr('拿 key。返回标题 + 链接 + 摘要 + auto-generated answer，质量比 SearXNG 高一档。', 'to get a key. Returns title + link + snippet + auto-generated answer; higher quality than SearXNG.')}
              </>
            }
          >
            <PromField
              label="Tavily API Key"
              hint={tr('tvly-... 形式', 'Format: tvly-...')}
              sensitive
              revealed={!!revealed.tavily_api_key}
              onToggleReveal={() =>
                setRevealed((r) => ({ ...r, tavily_api_key: !r.tavily_api_key }))
              }
              value={draft.tavily_api_key}
              onChange={(v) => update('tavily_api_key', v)}
              placeholder="tvly-..."
            />
          </ProviderBlock>

          <ProviderBlock
            id="brave"
            label="Brave Search"
            badge={tr('需 API key · 2000 次/月免费', 'API key required · 2000 calls/month free')}
            checked={draft.provider === 'brave'}
            onSelect={() => update('provider', 'brave')}
            description={
              <>
                {tr('在 ', 'Apply at ')}
                <a
                  href="https://api.search.brave.com"
                  target="_blank"
                  rel="noreferrer"
                  className="text-emerald-400 hover:text-emerald-300"
                >
                  api.search.brave.com
                </a>{' '}
                {tr('申请 key。隐私更好，但只返回链接 + 描述，没有 answer 字段。', 'for a key. Better privacy; returns links + descriptions only (no answer field).')}
              </>
            }
          >
            <PromField
              label="Brave API Key"
              hint={tr('X-Subscription-Token 形式', 'X-Subscription-Token format')}
              sensitive
              revealed={!!revealed.brave_api_key}
              onToggleReveal={() =>
                setRevealed((r) => ({ ...r, brave_api_key: !r.brave_api_key }))
              }
              value={draft.brave_api_key}
              onChange={(v) => update('brave_api_key', v)}
              placeholder="BSA..."
            />
          </ProviderBlock>
        </div>
      )}

      <div className="mt-5 flex flex-wrap items-center gap-3">
        <Button
          onClick={probeWebSearch}
          disabled={probe.kind === 'testing'}
          variant="ghost"
        >
          {probe.kind === 'testing' ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}
          <span>{tr('测试连接', 'Test connection')}</span>
        </Button>
        <Button onClick={submit} disabled={!dirty || saving || probe.kind !== 'ok'} variant="primary">
          {savedOk && !dirty ? <Check size={14} /> : <Save size={14} />}
          <span>{saving ? tr('保存中…', 'Saving…') : savedOk && !dirty ? tr('已保存', 'Saved') : tr('保存', 'Save')}</span>
        </Button>
        <span className="text-xs text-zinc-500">{statusHint}</span>
        {err && <span className="break-all text-xs text-red-400">{err}</span>}
      </div>
      <WebSearchProbeLine probe={probe} />
    </Card>
  );
}

// ProviderBlock is one row in the provider radio. The header (radio +
// label + badge) is always visible; the per-provider input fields are
// only rendered when this block is the active selection — keeps the
// card compact and avoids confusing operators with two grayed-out
// API-key inputs they don't need to fill.
function ProviderBlock({
  id,
  label,
  badge,
  checked,
  onSelect,
  description,
  children,
}: {
  id: string;
  label: string;
  badge: string;
  checked: boolean;
  onSelect: () => void;
  description: ReactNode;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        'rounded-lg border p-4 transition-colors',
        checked ? 'border-emerald-700/60 bg-emerald-900/10' : 'border-zinc-800 bg-zinc-950/40'
      )}
    >
      <label className="flex cursor-pointer items-start gap-3">
        <input
          type="radio"
          name="websearch_provider"
          value={id}
          checked={checked}
          onChange={onSelect}
          className="mt-1 h-3.5 w-3.5 accent-emerald-500"
        />
        <div className="flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-zinc-100">{label}</span>
            <span className="rounded border border-zinc-700 bg-zinc-900 px-1.5 text-[10px] text-zinc-400">
              {badge}
            </span>
          </div>
          <p className="mt-1 text-[11px] text-zinc-500">{description}</p>
        </div>
      </label>
      {checked && <div className="mt-3 grid grid-cols-1 gap-4 md:grid-cols-2">{children}</div>}
    </div>
  );
}

// WebSearchProbeLine renders the result of the most recent test-connection
// click. Shows the actual upstream-confirmed provider + a sample title
// (when the probe query returned a hit) so the operator sees tangible
// proof the wiring works.
function WebSearchProbeLine({
  probe,
}: {
  probe:
    | { kind: 'idle' }
    | { kind: 'testing' }
    | { kind: 'ok'; provider: string; sample: string }
    | { kind: 'error'; msg: string };
}) {
  const { tr } = useI18n();
  switch (probe.kind) {
    case 'ok':
      return (
        <p className="mt-3 text-xs text-emerald-400">
          ✓ {probe.provider} {tr('可达', 'reachable')}
          {probe.sample ? (
            <>
              {' '}
              {tr('· 示例结果：', '· Sample result: ')}<span className="text-zinc-300">{probe.sample}</span>
            </>
          ) : (
            tr(' · 上游返回 0 结果（key 工作正常，但探针 query 没匹配到）', ' · Upstream returned 0 results (key works, but probe query found no matches)')
          )}
        </p>
      );
    case 'error':
      return <p className="mt-3 break-all text-xs text-red-400">✗ {probe.msg}</p>;
    default:
      return null;
  }
}
