import { useCallback, useEffect, useMemo, useState } from 'react';
import { Lock, Plus, Trash2, RefreshCw, X } from 'lucide-react';
import {
  listSecrets,
  listCredentialTypes,
  createSecret,
  deleteSecret,
  type SecretView,
  type CredType,
} from '@/api/secrets';
import { ApiError } from '@/api/client';
import { useI18n } from '@/i18n/locale';

// Settings → Secrets (HLD-017). The credential vault. A credential is a
// NAMED, TYPED, MULTI-FIELD instance (n8n model). The TYPE (tencentcloud /
// aws / github / custom) declares its fields + how they inject; picking a
// type renders the right form. "custom" = free-form key/value injected as
// same-named env vars. Values are write-only + AES-encrypted at rest.

type FieldRow = { key: string; value: string };
const CUSTOM = 'custom';

export default function SecretsPage() {
  const { tr } = useI18n();
  const [items, setItems] = useState<SecretView[]>([]);
  const [types, setTypes] = useState<CredType[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // add-form state
  const [name, setName] = useState('');
  const [desc, setDesc] = useState('');
  const [typeName, setTypeName] = useState(CUSTOM);
  const [typedVals, setTypedVals] = useState<Record<string, string>>({}); // for typed creds
  const [rows, setRows] = useState<FieldRow[]>([{ key: '', value: '' }]); // for custom

  const selectedType = useMemo(() => types.find((t) => t.name === typeName), [types, typeName]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [s, t] = await Promise.all([listSecrets(), listCredentialTypes()]);
      setItems(s.items ?? []);
      setTypes(t.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const setRow = (i: number, patch: Partial<FieldRow>) =>
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  const addRow = () => setRows((rs) => [...rs, { key: '', value: '' }]);
  const removeRow = (i: number) => setRows((rs) => (rs.length > 1 ? rs.filter((_, idx) => idx !== i) : rs));

  const resetForm = () => {
    setName('');
    setDesc('');
    setTypedVals({});
    setRows([{ key: '', value: '' }]);
  };

  const onAdd = async () => {
    const fields: Record<string, string> = {};
    if (typeName !== CUSTOM && selectedType) {
      for (const f of selectedType.fields) {
        const v = typedVals[f.key];
        if (v) fields[f.key] = v;
      }
    } else {
      for (const r of rows) {
        const k = r.key.trim();
        if (k && r.value) fields[k] = r.value;
      }
    }
    if (!name.trim() || Object.keys(fields).length === 0) return;
    setBusy(true);
    setErr(null);
    try {
      await createSecret({ name: name.trim(), type: typeName, description: desc.trim(), fields });
      resetForm();
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const onDelete = async (id: number) => {
    setBusy(true);
    try {
      await deleteSecret(id);
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const isCustom = typeName === CUSTOM;

  return (
    <div className="anim-fade space-y-5">
      <div className="flex items-center gap-2">
        <Lock size={18} className="text-zinc-400" />
        <h1 className="text-lg font-semibold text-zinc-100">{tr('凭证', 'Credentials')}</h1>
        <button
          type="button"
          onClick={() => void load()}
          className="ml-auto inline-flex items-center gap-1.5 rounded-md border border-zinc-700 px-2 py-1 text-[12px] text-zinc-300 hover:bg-zinc-800"
        >
          <RefreshCw size={13} />
          {tr('刷新', 'Refresh')}
        </button>
      </div>

      <p className="text-[13px] leading-relaxed text-zinc-500">
        {tr(
          '凭据库。选一个类型（腾讯云 / AWS / GitHub …）会自动列出该填的字段，并自带"注入到哪些环境变量"的规则；技能 / 外部 MCP 用上这份凭据时按类型规则注入。类型选"自定义"则自由填字段，按同名环境变量注入。字段值只写不读，AES 加密落库（设 ONGRID_SECRET_KEY）。',
          'Credential vault. Pick a type (Tencent Cloud / AWS / GitHub …) and it lists the right fields plus a built-in "which env vars to inject" rule; skills / external MCP that use this credential inject by that rule. Pick "Custom" to enter free-form fields injected as same-named env vars. Values are write-only and AES-encrypted at rest (set ONGRID_SECRET_KEY).'
        )}
      </p>

      {err && (
        <div className="rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-[12px] text-red-400">{err}</div>
      )}

      {/* add form */}
      <div className="space-y-3 rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
        <div className="text-[12px] font-medium text-zinc-300">{tr('新增凭据', 'Add credential')}</div>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('名称（如 tencent-prod）', 'Name (e.g. tencent-prod)')}
            className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 font-mono text-[12px] text-zinc-200 outline-none focus:border-zinc-600"
          />
          <select
            value={typeName}
            onChange={(e) => {
              setTypeName(e.target.value);
              setTypedVals({});
            }}
            className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-[12px] text-zinc-200 outline-none focus:border-zinc-600"
          >
            {types.map((t) => (
              <option key={t.name} value={t.name}>
                {t.label}
              </option>
            ))}
          </select>
          <input
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            placeholder={tr('备注（可选）', 'Description (optional)')}
            className="rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-[12px] text-zinc-200 outline-none focus:border-zinc-600"
          />
        </div>

        {/* typed fields */}
        {!isCustom && selectedType && (
          <div className="space-y-1.5">
            {selectedType.fields.map((f) => (
              <div key={f.key} className="flex items-center gap-2">
                <label className="w-40 shrink-0 text-[12px] text-zinc-400">{f.label}</label>
                <input
                  value={typedVals[f.key] ?? ''}
                  onChange={(e) => setTypedVals((v) => ({ ...v, [f.key]: e.target.value }))}
                  type={f.secret ? 'password' : 'text'}
                  autoComplete="new-password"
                  placeholder={f.key}
                  className="flex-1 rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-[12px] text-zinc-200 outline-none focus:border-zinc-600"
                />
              </div>
            ))}
            {selectedType.inject_env && Object.keys(selectedType.inject_env).length > 0 && (
              <div className="pt-1 text-[10px] leading-relaxed text-zinc-600">
                {tr('将注入为环境变量：', 'Injects as env vars: ')}
                <span className="font-mono text-zinc-500">{Object.keys(selectedType.inject_env).join(', ')}</span>
              </div>
            )}
          </div>
        )}

        {/* custom free-form fields */}
        {isCustom && (
          <div className="space-y-1.5">
            <div className="text-[11px] text-zinc-500">{tr('字段（键 = 环境变量名 / 值）', 'Fields (key = env var name / value)')}</div>
            {rows.map((row, i) => (
              <div key={i} className="flex items-center gap-2">
                <input
                  value={row.key}
                  onChange={(e) => setRow(i, { key: e.target.value })}
                  placeholder={tr('字段名（如 GITHUB_TOKEN）', 'field key (e.g. GITHUB_TOKEN)')}
                  className="w-48 rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 font-mono text-[12px] text-zinc-200 outline-none focus:border-zinc-600"
                />
                <input
                  value={row.value}
                  onChange={(e) => setRow(i, { value: e.target.value })}
                  type="password"
                  autoComplete="new-password"
                  placeholder={tr('值', 'value')}
                  className="flex-1 rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-[12px] text-zinc-200 outline-none focus:border-zinc-600"
                />
                <button
                  type="button"
                  onClick={() => removeRow(i)}
                  className="rounded border border-zinc-700 p-1.5 text-zinc-500 hover:text-zinc-300"
                  title={tr('删除字段', 'Remove field')}
                >
                  <X size={13} />
                </button>
              </div>
            ))}
            <button type="button" onClick={addRow} className="inline-flex items-center gap-1 text-[11px] text-indigo-400 hover:text-indigo-300">
              <Plus size={12} />
              {tr('加字段', 'Add field')}
            </button>
          </div>
        )}

        <div>
          <button
            type="button"
            onClick={() => void onAdd()}
            disabled={busy || !name.trim()}
            className="inline-flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-[12px] font-medium text-white hover:bg-indigo-500 disabled:opacity-40"
          >
            <Plus size={13} />
            {tr('保存凭据', 'Save credential')}
          </button>
        </div>
      </div>

      {/* list */}
      <div className="overflow-hidden rounded-lg border border-zinc-800">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-zinc-800 bg-zinc-900/40 text-left text-[11px] uppercase tracking-wide text-zinc-500">
              <th className="px-3 py-2 font-medium">{tr('名称', 'Name')}</th>
              <th className="px-3 py-2 font-medium">{tr('类型', 'Type')}</th>
              <th className="px-3 py-2 font-medium">{tr('字段', 'Fields')}</th>
              <th className="px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={4} className="px-3 py-6 text-center text-[12px] text-zinc-500">
                  {tr('加载中…', 'Loading…')}
                </td>
              </tr>
            ) : items.length === 0 ? (
              <tr>
                <td colSpan={4} className="px-3 py-6 text-center text-[12px] text-zinc-600">
                  {tr('还没有凭据。', 'No credentials yet.')}
                </td>
              </tr>
            ) : (
              items.map((s) => (
                <tr key={s.id} className="border-b border-zinc-800/60 last:border-0">
                  <td className="px-3 py-2 font-mono text-[12px] text-zinc-200">{s.name}</td>
                  <td className="px-3 py-2 text-[12px] text-zinc-400">
                    {types.find((t) => t.name === s.type)?.label ?? s.type ?? CUSTOM}
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex flex-wrap gap-1">
                      {s.field_keys.length === 0 ? (
                        <span className="text-[11px] text-zinc-600">—</span>
                      ) : (
                        s.field_keys.map((k) => (
                          <span key={k} className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[11px] text-zinc-400">
                            {k}
                          </span>
                        ))
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2 text-right">
                    <button
                      type="button"
                      onClick={() => void onDelete(s.id)}
                      disabled={busy}
                      className="rounded border border-zinc-700 p-1 text-zinc-500 hover:border-red-800 hover:text-red-400 disabled:opacity-40"
                      title={tr('删除', 'Delete')}
                    >
                      <Trash2 size={13} />
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
