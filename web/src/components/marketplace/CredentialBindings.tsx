import { Select } from '@/components/ui/Select';
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { KeyRound, Check, Loader2, Plus, X } from 'lucide-react';
import type { InstalledPack, CredentialSlotRecord } from '@/api/marketplace';
import { setPackBindings } from '@/api/marketplace';
import { listSecrets, type SecretView } from '@/api/secrets';
import { Button } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

// CredentialBindings — HLD-017 design-time credential association. The user
// OPTIONALLY associates a stored vault credential with a skill at install /
// manage time; at exec time the manager injects each associated credential's
// TYPE inject rule as env vars. This is shown for EVERY pack, not only ones
// whose manifest declares credential slots — because the user doesn't know at
// install time whether a skill declared anything, and a bare skills.sh skill
// (e.g. terrashark) may still need a credential the user knows about. So:
//   - manifest-declared slots are pre-listed as labeled rows (a hint), and
//   - the user can always ADD extra credential associations by hand.
// Everything is optional: associating nothing installs/runs the skill as-is.
//
// Persistence (bindings_json {key: credential_name}):
//   - declared slot  -> key = the slot name
//   - manual extra   -> key = "extra:" + credential_name
// The exec side ignores the key and just injects every associated credential.
const EXTRA = 'extra:';

export function CredentialBindings({
  pack,
  isAdmin,
  onSaved,
}: {
  pack: InstalledPack;
  isAdmin: boolean;
  onSaved?: () => void;
}) {
  const { tr } = useI18n();
  const slots: CredentialSlotRecord[] = pack.capabilities?.summary?.credential_slots ?? [];
  const [secrets, setSecrets] = useState<SecretView[]>([]);
  const [slotSel, setSlotSel] = useState<Record<string, string>>({});
  const [extra, setExtra] = useState<string[]>([]);
  const [touched, setTouched] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    listSecrets()
      .then((r) => {
        if (alive) setSecrets(r.items ?? []);
      })
      .catch(() => {
        /* empty / unauthorized — the "no credentials" hint covers it */
      });
    return () => {
      alive = false;
    };
  }, []);

  // (Re)hydrate the local form from the pack's persisted bindings: split the
  // flat {key: cred} map back into declared-slot rows and manual extras.
  const bindingsKey = JSON.stringify(pack.bindings);
  useEffect(() => {
    const sl: Record<string, string> = {};
    const ex: string[] = [];
    for (const [k, v] of Object.entries(pack.bindings)) {
      if (k.startsWith(EXTRA)) ex.push(v);
      else sl[k] = v;
    }
    setSlotSel(sl);
    setExtra(ex);
    setTouched(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pack.pack_id, bindingsKey]);

  const compose = (): Record<string, string> => {
    const out: Record<string, string> = {};
    for (const [k, v] of Object.entries(slotSel)) if (v) out[k] = v;
    for (const c of extra) if (c) out[EXTRA + c] = c;
    return out;
  };

  const save = async () => {
    setSaving(true);
    setErr(null);
    try {
      await setPackBindings(pack.pack_id, compose());
      setSaved(true);
      setTouched(false);
      setTimeout(() => setSaved(false), 1500);
      onSaved?.();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const credOptions = (current: string) => (
    <>
      <option value="">{tr('（未关联）', '(none)')}</option>
      {secrets.map((sec) => (
        <option key={sec.id} value={sec.name}>
          {sec.name} · {sec.type}
        </option>
      ))}
      {/* keep a stale binding visible even if the credential was deleted */}
      {current && !secrets.some((s) => s.name === current) && (
        <option value={current}>{current}（已删除？）</option>
      )}
    </>
  );

  return (
    <div className="mt-3 rounded-md border border-zinc-800/80 bg-zinc-950/40 p-3">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wide text-zinc-400">
        <KeyRound size={12} className="text-amber-400" />
        {tr('关联凭证（可选）', 'Credential association (optional)')}
      </div>
      <p className="mb-2.5 text-[11px] text-zinc-500">
        {tr(
          '为该技能关联凭证库里的凭证，执行时按凭证类型自动注入环境变量。不关联也能正常使用。',
          'Associate stored credentials with this skill; they inject as env vars (by credential type) at exec time. Optional — the skill runs fine without any.',
        )}
      </p>

      {secrets.length === 0 ? (
        <div className="text-[11px] text-zinc-500">
          {tr('凭证库还是空的，先去', 'No credentials yet — ')}
          <Link to="/settings/secrets" className="mx-1 text-blue-400 hover:underline">
            {tr('创建凭证', 'create one')}
          </Link>
          {tr('再回来关联。', 'first, then associate here.')}
        </div>
      ) : (
        <div className="space-y-2">
          {/* manifest-declared slots (a hint about what the skill expects) */}
          {slots.map((s) => (
            <div key={s.slot} className="flex flex-wrap items-center gap-2">
              <div className="min-w-[140px]">
                <div className="text-xs text-zinc-200">{s.label || s.slot}</div>
                {s.fields && s.fields.length > 0 && (
                  <div className="font-mono text-[10px] text-zinc-500">{s.fields.join(', ')}</div>
                )}
              </div>
              <Select label={s.slot}
                value={slotSel[s.slot] ?? ''}
                disabled={!isAdmin}
                onValueChange={(selectedValue) => {
                  setSlotSel((m) => ({ ...m, [s.slot]: selectedValue }));
                  setTouched(true);
                }}
                className="min-w-[200px] w-auto"
              >
                {credOptions(slotSel[s.slot] ?? '')}
              </Select>
            </div>
          ))}

          {/* manual extras — the escape hatch for skills that declared nothing */}
          {extra.map((c, i) => (
            <div key={`extra-${i}`} className="flex flex-wrap items-center gap-2">
              <div className="min-w-[140px] text-xs text-zinc-400">{tr('额外凭证', 'Extra credential')}</div>
              <Select label={tr('额外凭证', 'Extra credential')}
                value={c}
                disabled={!isAdmin}
                onValueChange={(selectedValue) => {
                  setExtra((arr) => arr.map((x, j) => (j === i ? selectedValue : x)));
                  setTouched(true);
                }}
                className="min-w-[200px] w-auto"
              >
                {credOptions(c)}
              </Select>
              {isAdmin && (
                <Button variant="dangerGhost" size="sm"
                  type="button"
                  aria-label={tr('移除', 'Remove')}
                  onClick={() => {
                    setExtra((arr) => arr.filter((_, j) => j !== i));
                    setTouched(true);
                  }}
                  className="p-1"
                >
                  <X size={13} />
                </Button>
              )}
            </div>
          ))}

          {isAdmin && (
            <Button variant="subtle" size="sm"
              type="button"
              onClick={() => {
                setExtra((arr) => [...arr, '']);
                setTouched(true);
              }}
              className="inline-flex items-center gap-1"
            >
              <Plus size={12} />
              {tr('关联凭证', 'Associate a credential')}
            </Button>
          )}

          {err && <div className="text-[11px] text-red-400">{err}</div>}
          <div className="flex items-center gap-2 pt-1">
            <Button onClick={() => void save()} disabled={!isAdmin || saving || !touched} variant="primary">
              {saving ? <Loader2 size={12} className="animate-spin" /> : saved ? <Check size={12} /> : null}
              {saved ? tr('已保存', 'Saved') : tr('保存', 'Save')}
            </Button>
            {!isAdmin && <span className="text-[11px] text-zinc-500">{tr('需要 admin 权限', 'Admin only')}</span>}
          </div>
        </div>
      )}
    </div>
  );
}
