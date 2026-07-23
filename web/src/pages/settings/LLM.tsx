// LLM 模型配置页 — 每个 provider 一张子卡，含 API key / Base URL /
// 模型列表 / 默认模型。改完保存自动让 manager 失效缓存（≤1 秒生效）。
//
// 之前这页是单 provider 的旧版本；多 provider UI 一直在 /settings/
// integrations 里以 LLMCard 形式存在。这次拆出来，让 /settings/llm
// 直接进多 provider 配置（更对应它的菜单名字）。
import { useCallback, useEffect, useRef, useState } from 'react';
import { Check, Eye, EyeOff, Loader2, PlugZap, Plus, Save, Sparkles, Star, Trash2 } from 'lucide-react';
import { ApiError } from '@/api/client';
import {
  listSettings,
  revealSetting,
  saveLLMConfiguration,
  testLLMConfiguration,
  type LLMConfigurationProbeResult,
  type SystemSetting,
} from '@/api/settings';
import { Button, Card, Chip } from '@/components/ui';
import { ProviderIcon } from '@/components/icons/Provider';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

type LLMProviderID = 'openai' | 'anthropic' | 'zhipu' | 'gemini' | 'deepseek' | 'kimi' | 'custom';

type LLMProviderForm = {
  api_key: string;
  base_url: string;
  models: string[]; // 顺序敏感；index 0 = 默认（除非 default_model 覆盖）
  default_model: string;
};

type LLMProviderMeta = {
  id: LLMProviderID;
  label: string;
  labelEn?: string;
  hintZh: string;
  hintEn: string;
  baseURLPlaceholderZh: string;
  baseURLPlaceholderEn: string;
  modelPlaceholder: string;
  // custom = the generic OpenAI-compatible card: base_url is required and
  // shown prominently (not under Advanced), and it's the one place we say
  // "OpenAI-compatible" out loud.
  custom?: boolean;
  // system_settings.llm.<key> — 列在这里让 loader / saver 直接查
  keyAPIKey: string;
  keyBaseURL: string;
  keyModels: string;
  keyDefaultModel: string;
};

type LLMProbeState =
  | { kind: 'idle' }
  | { kind: 'testing'; signature: string }
  | { kind: 'ok'; signature: string; result: LLMConfigurationProbeResult }
  | { kind: 'error'; signature: string; result: LLMConfigurationProbeResult };

const LLM_PROVIDERS: LLMProviderMeta[] = [
  {
    id: 'openai',
    label: 'OpenAI',
    hintZh: 'OpenAI 官方 API。在 platform.openai.com 获取 API key。',
    hintEn: 'OpenAI official API. Get an API key at platform.openai.com.',
    baseURLPlaceholderZh: 'https://api.openai.com/v1（默认）',
    baseURLPlaceholderEn: 'https://api.openai.com/v1 (default)',
    modelPlaceholder: 'gpt-5.4',
    keyAPIKey: 'openai_api_key',
    keyBaseURL: 'openai_base_url',
    keyModels: 'openai_models',
    keyDefaultModel: 'openai_default_model',
  },
  {
    id: 'anthropic',
    label: 'Anthropic',
    hintZh: 'Anthropic Claude。在 console.anthropic.com 获取 API key。',
    hintEn: 'Anthropic Claude. Get an API key at console.anthropic.com.',
    baseURLPlaceholderZh: 'https://api.anthropic.com/v1',
    baseURLPlaceholderEn: 'https://api.anthropic.com/v1',
    modelPlaceholder: 'claude-sonnet-4-6',
    keyAPIKey: 'anthropic_api_key',
    keyBaseURL: 'anthropic_base_url',
    keyModels: 'anthropic_models',
    keyDefaultModel: 'anthropic_default_model',
  },
  {
    id: 'gemini',
    label: 'Gemini',
    hintZh: 'Google Gemini。在 aistudio.google.com 获取 API key。',
    hintEn: 'Google Gemini. Get an API key at aistudio.google.com.',
    baseURLPlaceholderZh: 'https://generativelanguage.googleapis.com/v1beta/openai',
    baseURLPlaceholderEn: 'https://generativelanguage.googleapis.com/v1beta/openai',
    modelPlaceholder: 'gemini-2.5-pro',
    keyAPIKey: 'gemini_api_key',
    keyBaseURL: 'gemini_base_url',
    keyModels: 'gemini_models',
    keyDefaultModel: 'gemini_default_model',
  },
  {
    id: 'zhipu',
    label: '智谱 GLM',
    labelEn: 'Zhipu GLM',
    hintZh: '智谱 GLM（中国）。在 open.bigmodel.cn 获取 API key。',
    hintEn: 'Zhipu GLM (China-based). Get an API key at open.bigmodel.cn.',
    baseURLPlaceholderZh: 'https://open.bigmodel.cn/api/paas/v4',
    baseURLPlaceholderEn: 'https://open.bigmodel.cn/api/paas/v4',
    modelPlaceholder: 'glm-4.7',
    keyAPIKey: 'zhipu_api_key',
    keyBaseURL: 'zhipu_base_url',
    keyModels: 'zhipu_models',
    keyDefaultModel: 'zhipu_default_model',
  },
  {
    id: 'deepseek',
    label: 'DeepSeek',
    hintZh: 'DeepSeek（中国）。在 platform.deepseek.com 获取 API key。',
    hintEn: 'DeepSeek (China-based). Get an API key at platform.deepseek.com.',
    baseURLPlaceholderZh: 'https://api.deepseek.com/v1',
    baseURLPlaceholderEn: 'https://api.deepseek.com/v1',
    modelPlaceholder: 'deepseek-v4-flash',
    keyAPIKey: 'deepseek_api_key',
    keyBaseURL: 'deepseek_base_url',
    keyModels: 'deepseek_models',
    keyDefaultModel: 'deepseek_default_model',
  },
  {
    id: 'kimi',
    label: 'Kimi',
    labelEn: 'Kimi (Moonshot)',
    hintZh: 'Kimi / Moonshot（中国）。在 platform.moonshot.cn 获取 API key。',
    hintEn: 'Kimi / Moonshot (China-based). Get an API key at platform.moonshot.cn.',
    baseURLPlaceholderZh: 'https://api.moonshot.cn/v1',
    baseURLPlaceholderEn: 'https://api.moonshot.cn/v1',
    modelPlaceholder: 'kimi-k2.6',
    keyAPIKey: 'kimi_api_key',
    keyBaseURL: 'kimi_base_url',
    keyModels: 'kimi_models',
    keyDefaultModel: 'kimi_default_model',
  },
  {
    id: 'custom',
    custom: true,
    label: '自定义（OpenAI 兼容）',
    labelEn: 'Custom (OpenAI-compatible)',
    hintZh: '任意 OpenAI 兼容服务：Ollama / vLLM / OpenRouter / LM Studio / Together / Groq 等。填 Base URL + key + 模型名即可。无需鉴权的本地服务（如 Ollama）随便填个占位 key。',
    hintEn: 'Any OpenAI-compatible service: Ollama / vLLM / OpenRouter / LM Studio / Together / Groq, etc. Enter Base URL + key + model name. For keyless local servers (e.g. Ollama) just put any placeholder key.',
    baseURLPlaceholderZh: '例 http://localhost:11434/v1（Ollama）· https://openrouter.ai/api/v1',
    baseURLPlaceholderEn: 'e.g. http://localhost:11434/v1 (Ollama) · https://openrouter.ai/api/v1',
    modelPlaceholder: 'llama3.1 / qwen2.5-coder / ...',
    keyAPIKey: 'custom_api_key',
    keyBaseURL: 'custom_base_url',
    keyModels: 'custom_models',
    keyDefaultModel: 'custom_default_model',
  },
];

// Locale-aware provider order. Operators reading the page in zh-CN see
// the China-based providers (Zhipu / DeepSeek / Kimi) at the top because
// they're the ones a local deployment most likely has API keys for and
// can reach over the public internet without VPN; en-US flips it so
// OpenAI / Anthropic / Gemini head the list. Falls back to LLM_PROVIDERS
// order for an unknown locale.
const PROVIDER_ORDER_ZH = ['zhipu', 'deepseek', 'kimi', 'openai', 'anthropic', 'gemini', 'custom'] as const;
const PROVIDER_ORDER_EN = ['openai', 'anthropic', 'gemini', 'zhipu', 'deepseek', 'kimi', 'custom'] as const;

function orderedProviders(locale: string): LLMProviderMeta[] {
  const order = locale === 'zh-CN' ? PROVIDER_ORDER_ZH : PROVIDER_ORDER_EN;
  const byId = new Map(LLM_PROVIDERS.map((p) => [p.id, p]));
  const out: LLMProviderMeta[] = [];
  for (const id of order) {
    const p = byId.get(id);
    if (p) {
      out.push(p);
      byId.delete(id);
    }
  }
  // Any provider added later that isn't in the order map still renders
  // at the end — keeps the function forward-compatible.
  for (const p of byId.values()) out.push(p);
  return out;
}

const emptyLLMForm: LLMProviderForm = {
  api_key: '',
  base_url: '',
  models: [],
  default_model: '',
};

export default function SettingsLLM() {
  const { tr, locale } = useI18n();
  // Order providers locale-aware: zh-CN puts China-based providers
  // first (Zhipu / DeepSeek / Kimi), en-US puts US-based providers
  // first (OpenAI / Anthropic / Gemini). Keeps the page in the order
  // operators are most likely to fill in.
  const providers = orderedProviders(locale);
  return (
    // One Card per provider (Integrations-style), instead of nesting
    // all 6 inside a single big card. Lets each provider have its own
    // breathing room + per-provider hint copy on the card.
    <div className="space-y-4">
      <div className="rounded-lg border border-zinc-800/60 bg-zinc-900/30 px-4 py-3 text-[12px] text-zinc-400">
        <div className="mb-1 flex items-center gap-2 text-zinc-200">
          <Sparkles size={14} className="text-zinc-400" />
          <span className="font-medium">{tr('LLM 模型', 'LLM models')}</span>
        </div>
        {tr('每个提供商可以配多个 model，聊天页的下拉就读这里。改 API key / 模型列表后 ', 'Each provider can host multiple models; the chat-page dropdown reads from here. Changes to API key / model list take effect ')}
        <b>{tr('~60 秒内自动生效', 'within ~60 seconds')}</b>
        {tr('，保存时也会立刻让 manager 失效缓存（通常 1 秒内）。留空 API key = 该提供商不出现在聊天页下拉里。', ', and saving also instantly invalidates the manager cache (usually within 1 s). Leaving the API key blank hides the provider from the chat dropdown.')}
        <div className="mt-1 text-zinc-500">
          {tr('测试配置会由 Manager 对列表中的每个模型发起一次最小请求，可能消耗少量 token；非空配置保存前必须全部验证通过。', 'The Manager tests every listed model with one minimal request, which may consume a few tokens; all models in a non-empty configuration must pass before saving.')}
        </div>
      </div>
      {providers.map((meta) => (
        <LLMProviderCard key={meta.id} meta={meta} />
      ))}
    </div>
  );
}

function LLMProviderCard({ meta }: { meta: LLMProviderMeta }) {
  const { tr } = useI18n();
  const [server, setServer] = useState<LLMProviderForm>(emptyLLMForm);
  const [draft, setDraft] = useState<LLMProviderForm>(emptyLLMForm);
  const [revealed, setRevealed] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [savedOk, setSavedOk] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [newModel, setNewModel] = useState('');
  const [probe, setProbe] = useState<LLMProbeState>({ kind: 'idle' });
  const probeVersion = useRef(0);

  const refresh = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await listSettings('llm');
      const next: LLMProviderForm = { ...emptyLLMForm, models: [] };
      for (const it of r.items as SystemSetting[]) {
        if (it.key === meta.keyBaseURL) next.base_url = it.value ?? '';
        if (it.key === meta.keyDefaultModel) next.default_model = it.value ?? '';
        if (it.key === meta.keyModels && it.value) {
          try {
            const parsed = JSON.parse(it.value);
            if (Array.isArray(parsed)) {
              next.models = parsed.filter((s) => typeof s === 'string');
            }
          } catch {
            /* keep [] */
          }
        }
      }
      const apiRow = (r.items as SystemSetting[]).find((it) => it.key === meta.keyAPIKey);
      if (apiRow && (apiRow.value ?? '') !== '') {
        try {
          const real = await revealSetting('llm', meta.keyAPIKey);
          next.api_key = real.value ?? '';
        } catch {
          /* leave empty so user can paste a fresh key */
        }
      }
      setServer(next);
      setDraft(next);
      setRevealed(false);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [meta]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const dirty =
    draft.api_key !== server.api_key ||
    draft.base_url !== server.base_url ||
    draft.default_model !== server.default_model ||
    JSON.stringify(draft.models) !== JSON.stringify(server.models);

  const update = <K extends keyof LLMProviderForm>(k: K, v: LLMProviderForm[K]) => {
    setSavedOk(false);
    probeVersion.current += 1;
    setProbe({ kind: 'idle' });
    setDraft((cur) => ({ ...cur, [k]: v }));
  };

  const addModel = () => {
    const m = newModel.trim();
    if (!m) return;
    if (draft.models.includes(m)) {
      setNewModel('');
      return;
    }
    const nextModels = [...draft.models, m];
    setDraft((cur) => ({
      ...cur,
      models: nextModels,
      default_model: cur.default_model || m,
    }));
    setSavedOk(false);
    probeVersion.current += 1;
    setProbe({ kind: 'idle' });
    setNewModel('');
  };

  const removeModel = (m: string) => {
    setSavedOk(false);
    probeVersion.current += 1;
    setProbe({ kind: 'idle' });
    setDraft((cur) => {
      const next = cur.models.filter((x) => x !== m);
      let defM = cur.default_model;
      if (defM === m) defM = next[0] ?? '';
      return { ...cur, models: next, default_model: defM };
    });
  };

  const setDefault = (m: string) => {
    setSavedOk(false);
    probeVersion.current += 1;
    setProbe({ kind: 'idle' });
    setDraft((cur) => ({ ...cur, default_model: m }));
  };

  const signatureFor = (form: LLMProviderForm) => JSON.stringify([
    meta.id,
    form.api_key,
    form.base_url.trim(),
    form.default_model.trim(),
    form.models.map((model) => model.trim()),
  ]);

  const testDraft = async (): Promise<LLMConfigurationProbeResult | null> => {
    const signature = signatureFor(draft);
    const requestVersion = probeVersion.current + 1;
    probeVersion.current = requestVersion;
    setSavedOk(false);
    setErr(null);
    setProbe({ kind: 'testing', signature });
    try {
      const result = await testLLMConfiguration({
        provider: meta.id,
        api_key: draft.api_key,
        base_url: draft.base_url.trim(),
        default_model: draft.default_model.trim(),
        models: draft.models,
      });
      if (probeVersion.current !== requestVersion) return null;
      setProbe(result.valid
        ? { kind: 'ok', signature, result }
        : { kind: 'error', signature, result });
      return result;
    } catch (e) {
      if (probeVersion.current !== requestVersion) return null;
      const detail = e instanceof ApiError ? e.message : (e as Error).message;
      const result: LLMConfigurationProbeResult = {
        valid: false,
        code: 'probe-request-failed',
        provider: meta.id,
        model: draft.default_model.trim(),
        detail,
        latency_ms: 0,
        saved: false,
        disabled: false,
      };
      setProbe({ kind: 'error', signature, result });
      return null;
    }
  };

  const submit = async () => {
    const snapshot: LLMProviderForm = { ...draft, models: [...draft.models] };
    const signature = signatureFor(snapshot);
    const requestVersion = probeVersion.current + 1;
    probeVersion.current = requestVersion;
    setSaving(true);
    setSavedOk(false);
    setErr(null);
    setProbe(snapshot.api_key.trim() === '' ? { kind: 'idle' } : { kind: 'testing', signature });
    try {
      const result = await saveLLMConfiguration({
        provider: meta.id,
        api_key: snapshot.api_key,
        base_url: snapshot.base_url.trim(),
        default_model: snapshot.default_model.trim(),
        models: snapshot.models,
      });
      if (probeVersion.current !== requestVersion) return;
      if (!result.valid || !result.saved) {
        setProbe({ kind: 'error', signature, result });
        return;
      }
      await refresh();
      setProbe(result.disabled ? { kind: 'idle' } : { kind: 'ok', signature, result });
      setSavedOk(true);
    } catch (e) {
      const detail = e instanceof ApiError ? e.message : (e as Error).message;
      if (probeVersion.current === requestVersion) {
        setProbe({
          kind: 'error',
          signature,
          result: {
            valid: false,
            code: 'probe-request-failed',
            provider: meta.id,
            model: snapshot.default_model.trim(),
            detail,
            latency_ms: 0,
            saved: false,
            disabled: false,
          },
        });
      }
    } finally {
      setSaving(false);
    }
  };

  // A custom provider also needs a base URL to be reachable; the named
  // providers have a working default endpoint, so a key alone suffices.
  const configured = server.api_key.trim() !== '' && (!meta.custom || server.base_url.trim() !== '');

  return (
    <Card className="p-5" data-testid={`llm-provider-${meta.id}`}>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <ProviderIcon provider={meta.id} size={16} />
        <h2 className="text-sm font-medium text-zinc-100">{tr(meta.label, meta.labelEn ?? meta.label)}</h2>
        {configured ? <Chip tone="success">{tr('已配置', 'Configured')}</Chip> : <Chip>{tr('未配置', 'Not configured')}</Chip>}
      </div>
      <p className="mb-4 text-[11px] text-zinc-500">{tr(meta.hintZh, meta.hintEn)}</p>

      {loading ? (
        <div className="flex h-20 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
        </div>
      ) : (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            <FieldRow
              label="API Key"
              hint={
                meta.custom
                  ? tr('无需鉴权的本地服务填任意占位值', 'For keyless local servers, any placeholder works')
                  : tr('留空 = 该提供商不出现在聊天页下拉里', 'Leave empty to hide this provider from the chat dropdown')
              }
              sensitive
              revealed={revealed}
              onToggleReveal={() => setRevealed((v) => !v)}
              value={draft.api_key}
              onChange={(v) => update('api_key', v)}
              placeholder="sk-... / tvly-... / glsa-..."
            />
            {meta.custom && (
              <FieldRow
                label={tr('Base URL（必填）', 'Base URL (required)')}
                hint={tr('你的 OpenAI 兼容端点', 'Your OpenAI-compatible endpoint')}
                value={draft.base_url}
                onChange={(v) => update('base_url', v)}
                placeholder={tr(meta.baseURLPlaceholderZh, meta.baseURLPlaceholderEn)}
              />
            )}
          </div>

          {!meta.custom && (
            // Named providers ship a working default endpoint, so Base URL is
            // an advanced override (proxy / gateway / Azure) — tuck it away so
            // first-time users just paste a key and go.
            <details className="rounded-md border border-zinc-800/60 bg-zinc-950/30 px-3 py-2">
              <summary className="cursor-pointer select-none text-[11px] text-zinc-500 hover:text-zinc-300">
                {tr('高级 · Base URL', 'Advanced · Base URL')}
              </summary>
              <div className="mt-2">
                <FieldRow
                  label="Base URL"
                  hint={tr('留空 = 用厂商官方端点；仅在走代理 / 网关时填', 'Leave empty for the vendor endpoint; set only when routing through a proxy / gateway')}
                  value={draft.base_url}
                  onChange={(v) => update('base_url', v)}
                  placeholder={tr(meta.baseURLPlaceholderZh, meta.baseURLPlaceholderEn)}
                />
              </div>
            </details>
          )}

          <div>
            <span className="mb-1 block text-xs text-zinc-400">{tr('模型列表', 'Models')}</span>
            {draft.models.length === 0 ? (
              <p className="rounded border border-dashed border-zinc-800 bg-zinc-950/40 px-3 py-2 text-[11px] text-zinc-600">
                {tr(`还没添加模型 — 在下面输入框里加一个，例 ${meta.modelPlaceholder}`, `No models yet — add one in the input below, e.g. ${meta.modelPlaceholder}`)}
              </p>
            ) : (
              <ul className="space-y-1">
                {draft.models.map((m) => {
                  const isDefault = draft.default_model === m;
                  return (
                    <li
                      key={m}
                      className="flex items-center gap-2 rounded border border-zinc-800 bg-zinc-950/40 px-2.5 py-1.5 text-[12px]"
                    >
                      <code className="font-mono text-zinc-100">{m}</code>
                      {isDefault && (
                        <span className="inline-flex items-center gap-0.5 rounded border border-emerald-700/60 bg-emerald-900/20 px-1 text-[10px] text-emerald-300">
                          <Star size={9} /> {tr('默认', 'Default')}
                        </span>
                      )}
                      <span className="ml-auto flex items-center gap-1">
                        {!isDefault && (
                          <button
                            type="button"
                            onClick={() => setDefault(m)}
                            className="rounded px-1.5 py-0.5 text-[10px] text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200"
                          >
                            {tr('设为默认', 'Set default')}
                          </button>
                        )}
                        <button
                          type="button"
                          onClick={() => removeModel(m)}
                          aria-label={tr(`移除 ${m}`, `Remove ${m}`)}
                          className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-red-300"
                        >
                          <Trash2 size={11} />
                        </button>
                      </span>
                    </li>
                  );
                })}
              </ul>
            )}
            <div className="mt-2 flex items-center gap-2">
              <input
                type="text"
                value={newModel}
                onChange={(e) => setNewModel(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    addModel();
                  }
                }}
                placeholder={tr(`新增模型，例 ${meta.modelPlaceholder}`, `Add a model, e.g. ${meta.modelPlaceholder}`)}
                className="flex-1 rounded-md border border-zinc-800 bg-zinc-950/40 px-2.5 py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none"
              />
              <button
                type="button"
                onClick={addModel}
                disabled={newModel.trim() === ''}
                className="inline-flex items-center gap-1 rounded-md border border-zinc-700 px-2.5 py-1.5 text-xs text-zinc-200 hover:border-zinc-500 hover:bg-zinc-800 disabled:cursor-not-allowed disabled:opacity-50"
              >
                <Plus size={12} />
                {tr('添加', 'Add')}
              </button>
            </div>
          </div>
        </div>
      )}

      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button
          onClick={() => void testDraft()}
          disabled={saving || probe.kind === 'testing' || draft.api_key.trim() === ''}
        >
          {probe.kind === 'testing' ? <Loader2 size={14} className="animate-spin" /> : <PlugZap size={14} />}
          <span>{probe.kind === 'testing' ? tr('检测中…', 'Testing…') : tr('测试配置', 'Test configuration')}</span>
        </Button>
        <Button onClick={submit} disabled={!dirty || saving || probe.kind === 'testing'} variant="subtle">
          {savedOk && !dirty ? <Check size={14} /> : <Save size={14} />}
          <span>{saving
            ? probe.kind === 'testing' ? tr('校验中…', 'Validating…') : tr('保存中…', 'Saving…')
            : savedOk && !dirty ? tr('已保存', 'Saved') : tr('保存', 'Save')}</span>
        </Button>
        <span className="text-xs text-zinc-500">
          {dirty
            ? tr('有未保存修改', 'Unsaved changes')
            : configured
              ? tr(`当前默认模型: ${draft.default_model || '(未设)'}`, `Current default model: ${draft.default_model || '(unset)'}`)
              : ''}
        </span>
        {err && <span className="break-all text-xs text-red-400">{err}</span>}
      </div>
      <LLMProbeLine probe={probe} />
    </Card>
  );
}

function LLMProbeLine({ probe }: { probe: LLMProbeState }) {
  const { tr } = useI18n();
  if (probe.kind === 'idle' || probe.kind === 'testing') return null;
  if (probe.kind === 'ok') {
    return (
      <p className="mt-3 text-xs text-emerald-500">
        {tr('✓ 配置可用', '✓ Configuration works')}
        {probe.result.model ? ` · ${probe.result.model}` : ''}
        {probe.result.latency_ms > 0 ? ` · ${probe.result.latency_ms} ms` : ''}
      </p>
    );
  }

  const copy = llmProbeFailureCopy(probe.result.code);
  return (
    <div className="mt-3 space-y-1 text-xs">
      <p className="text-red-500">
        ✗ {tr(copy.titleZh, copy.titleEn)}
        {probe.result.detail ? <span className="break-all text-red-400"> · {probe.result.detail}</span> : null}
      </p>
      <p className="text-zinc-500">{tr(copy.hintZh, copy.hintEn)}</p>
    </div>
  );
}

function llmProbeFailureCopy(code: string): { titleZh: string; titleEn: string; hintZh: string; hintEn: string } {
  const copies: Record<string, { titleZh: string; titleEn: string; hintZh: string; hintEn: string }> = {
    'unsupported-provider': { titleZh: '不支持的提供商', titleEn: 'Unsupported provider', hintZh: '刷新页面后重新选择受支持的提供商。', hintEn: 'Refresh the page and select a supported provider.' },
    'missing-api-key': { titleZh: '缺少 API Key', titleEn: 'API key is missing', hintZh: '填写 Key；如需禁用此提供商，可直接保存空 Key。', hintEn: 'Enter a key, or save an empty key to disable this provider.' },
    'missing-model': { titleZh: '缺少默认模型', titleEn: 'Default model is missing', hintZh: '先添加模型并将其中一个设为默认。', hintEn: 'Add a model and set one as the default.' },
    'missing-base-url': { titleZh: '缺少 Base URL', titleEn: 'Base URL is missing', hintZh: '自定义提供商必须填写 OpenAI 兼容端点。', hintEn: 'Custom providers require an OpenAI-compatible endpoint.' },
    'invalid-base-url': { titleZh: 'Base URL 格式错误', titleEn: 'Invalid Base URL', hintZh: '使用包含主机名的 http:// 或 https:// 地址，不要附带账号、查询参数或片段。', hintEn: 'Use an http:// or https:// URL with a host and no userinfo, query, or fragment.' },
    'authentication-failed': { titleZh: 'API Key 无效或已撤销', titleEn: 'API key was rejected', hintZh: '检查 Key 是否完整、属于当前厂商，并确认尚未过期或被撤销。', hintEn: 'Check that the key is complete, belongs to this provider, and has not expired or been revoked.' },
    'permission-denied': { titleZh: '账号没有模型访问权限', titleEn: 'Account lacks model access', hintZh: '检查项目、组织、区域或模型授权范围。', hintEn: 'Check project, organization, region, and model permissions.' },
    'model-not-found': { titleZh: '模型不存在或名称错误', titleEn: 'Model was not found', hintZh: '核对厂商控制台中的模型 ID，并确认账号已开通该模型。', hintEn: 'Verify the model ID in the provider console and confirm access is enabled.' },
    'quota-exceeded': { titleZh: '额度或余额不足', titleEn: 'Quota or balance exhausted', hintZh: '检查账单、余额和项目额度后重试。', hintEn: 'Check billing, balance, and project quota, then retry.' },
    'rate-limited': { titleZh: '请求被限流', titleEn: 'Request was rate limited', hintZh: '稍后重试，或检查厂商的 RPM/TPM 限制。', hintEn: 'Retry later or check the provider RPM/TPM limits.' },
    timeout: { titleZh: '连接或模型响应超时', titleEn: 'Connection or model response timed out', hintZh: '检查网络、代理和模型负载；探测最长等待 20 秒。', hintEn: 'Check network, proxy, and model load; the probe waits up to 20 seconds.' },
    'request-canceled': { titleZh: '检测已取消', titleEn: 'Probe was canceled', hintZh: '保持页面打开并重新测试。', hintEn: 'Keep the page open and test again.' },
    'dns-failed': { titleZh: '域名解析失败', titleEn: 'DNS lookup failed', hintZh: '检查 Base URL 主机名和 Manager 所在环境的 DNS。', hintEn: 'Check the Base URL hostname and DNS available to the Manager.' },
    'connection-failed': { titleZh: '无法建立连接', titleEn: 'Could not connect', hintZh: '检查地址、端口、防火墙、代理以及服务是否启动。', hintEn: 'Check address, port, firewall, proxy, and whether the service is running.' },
    'tls-failed': { titleZh: 'TLS 证书校验失败', titleEn: 'TLS certificate validation failed', hintZh: '检查证书链、域名和有效期；Manager 不会跳过证书校验。', hintEn: 'Check certificate chain, hostname, and expiry; the Manager does not skip certificate verification.' },
    'endpoint-not-found': { titleZh: 'Chat Completions 端点不存在', titleEn: 'Chat Completions endpoint not found', hintZh: '检查 Base URL 是否指向 OpenAI 兼容 API 根路径（通常以 /v1 结尾）。', hintEn: 'Check that Base URL points to an OpenAI-compatible API root, usually ending in /v1.' },
    'provider-unavailable': { titleZh: '模型服务暂时不可用', titleEn: 'Provider is temporarily unavailable', hintZh: '稍后重试，并检查厂商状态页或自建服务日志。', hintEn: 'Retry later and check the provider status page or self-hosted service logs.' },
    'invalid-request': { titleZh: '提供商拒绝了请求参数', titleEn: 'Provider rejected the request', hintZh: '检查模型是否支持 Chat Completions，以及 Base URL 是否匹配该厂商。', hintEn: 'Check that the model supports Chat Completions and the Base URL matches the provider.' },
    'invalid-response': { titleZh: '返回格式不是兼容的模型响应', titleEn: 'Provider returned an incompatible response', hintZh: '确认端点实现 OpenAI 兼容的 Chat Completions 响应格式。', hintEn: 'Confirm the endpoint implements the OpenAI-compatible Chat Completions response shape.' },
    'probe-request-failed': { titleZh: 'Manager 检测请求失败', titleEn: 'Manager probe request failed', hintZh: '检查 Manager 状态和当前登录权限后重试。', hintEn: 'Check Manager health and your current permissions, then retry.' },
    'upstream-error': { titleZh: '模型服务返回未知错误', titleEn: 'Provider returned an unknown error', hintZh: '根据上游详情检查厂商状态或自建服务日志。', hintEn: 'Use the upstream detail to check provider status or self-hosted service logs.' },
  };
  return copies[code] ?? copies['upstream-error'];
}

// FieldRow — 局部 input 包装（PromField 的微缩版，专给本页用，避免
// 跨文件 import 把 Integrations.tsx 当依赖）。
function FieldRow({
  label,
  hint,
  value,
  onChange,
  placeholder,
  sensitive,
  revealed,
  onToggleReveal,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange(v: string): void;
  placeholder?: string;
  sensitive?: boolean;
  revealed?: boolean;
  onToggleReveal?: () => void;
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
          className={cn(
            'w-full rounded-lg border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-sm text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none',
            sensitive && 'pr-9',
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
