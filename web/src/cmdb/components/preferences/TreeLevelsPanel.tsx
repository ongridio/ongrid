import { useCallback, useEffect, useMemo, useState } from 'react';
import { ArrowDown, ArrowUp, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Checkbox } from '@/components/ui/Checkbox';
import { Label } from '@/components/ui/Label';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

import { listCITypes, listTypeAttributes, type CIType, type TypeAttribute } from '../../api/citype';
import { describeError } from '../../api/http';
import { listTreeLevels, setTreeLevels, type TreeLevel } from '../../api/preferences';
import { useToast } from '../Toast';

// 资源层级 — which columns the hierarchy shows for one model.
//
// THE ORDER IS THE MEANING: the first level is the root grouping, the second is
// its child, and so on. The panel therefore edits an ordered list, not a set;
// checking an attribute appends it, and the arrows move it.

export function TreeLevelsPanel() {
  const { tr } = useI18n();
  const toast = useToast();
  const [views, setViews] = useState<TreeLevel[]>([]);
  const [models, setModels] = useState<CIType[]>([]);
  const [ref, setRef] = useState('');
  const [attributes, setAttributes] = useState<TypeAttribute[]>([]);
  const [levels, setLevels] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const [gotViews, gotModels] = await Promise.all([listTreeLevels(), listCITypes()]);
      setViews(gotViews);
      setModels(gotModels);
      setRef((current) => current || String(gotViews[0]?.type_id ?? gotModels[0]?.id ?? ''));
    } catch (error) {
      setErr(describeError(error, tr('无法读取树层级', 'Could not load the tree levels')));
    } finally {
      setLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!ref) {
      setAttributes([]);
      setLevels([]);
      return;
    }
    let live = true;
    void listTypeAttributes(ref)
      .then((got) => {
        if (!live) return;
        setAttributes(got.attributes ?? []);
        const saved = views.find((view) => view.type_id === Number(ref));
        setLevels((saved?.levels ?? []).map((attribute) => attribute.name));
      })
      .catch((error) => live && setErr(describeError(error, tr('无法读取模型属性', 'Could not load the model attributes'))));
    return () => {
      live = false;
    };
  }, [ref, views, tr]);

  const available = useMemo(
    () => attributes.filter((attribute) => !levels.includes(attribute.name)),
    [attributes, levels],
  );
  const byName = useMemo(() => new Map(attributes.map((attribute) => [attribute.name, attribute])), [attributes]);

  const toggle = (name: string, checked: boolean) => {
    setLevels((current) => (checked ? [...current, name] : current.filter((item) => item !== name)));
  };

  const move = (index: number, direction: -1 | 1) => {
    const target = index + direction;
    if (target < 0 || target >= levels.length) return;
    setLevels((current) => {
      const next = [...current];
      [next[index], next[target]] = [next[target], next[index]];
      return next;
    });
  };

  const save = async () => {
    if (!ref || saving) return;
    setSaving(true);
    setErr(null);
    try {
      const got = await setTreeLevels(ref, levels);
      setViews((current) => {
        const rest = current.filter((view) => view.type_id !== got.type_id);
        return levels.length === 0 ? rest : [...rest, got];
      });
      toast.success(tr('已保存树层级', 'Tree levels saved'));
    } catch (error) {
      setErr(describeError(error, tr('保存树层级失败', 'Could not save the tree levels')));
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <Card>
        <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
      </Card>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {err && (
        <div role="alert" className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}

      <Card>
        <div className="flex flex-wrap items-end gap-3">
          <div>
            <Label htmlFor="cmdb-tree-model">{tr('模型', 'Model')}</Label>
            <Select
              id="cmdb-tree-model"
              label={tr('模型', 'Model')}
              value={ref}
              onValueChange={setRef}
              options={models.map((model) => ({ value: String(model.id), label: model.alias || model.name }))}
              className="mt-1 w-56"
            />
          </div>
          <p className="max-w-xl text-xs text-text-muted">
            {tr(
              '层级顺序决定资源树的展开顺序；空列表表示这个模型不使用自定义层级。',
              'The order decides how the resource tree expands; an empty list means the model uses no custom levels.',
            )}
          </p>
        </div>
      </Card>

      {ref ? (
        <>
          <div className="grid gap-4 lg:grid-cols-2">
            <Card>
              <h2 className="mb-2 text-sm font-medium text-text">{tr('可选属性', 'Available attributes')}</h2>
              {available.length === 0 ? (
                <p className="py-4 text-center text-xs text-text-muted">
                  {tr('这个模型的属性都已加入层级', 'Every attribute is already in the levels')}
                </p>
              ) : (
                <ul className="max-h-72 divide-y divide-border overflow-y-auto">
                  {available.map((attribute) => (
                    <li key={attribute.id} className="flex items-center gap-2 py-1.5 text-xs">
                      <Checkbox
                        checked={false}
                        onCheckedChange={(next) => toggle(attribute.name, next === true)}
                        aria-label={attribute.alias || attribute.name}
                      />
                      <span className="min-w-0 flex-1 truncate">{attribute.alias || attribute.name}</span>
                      <span className="font-mono text-[11px] text-text-faint">{attribute.name}</span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>

            <Card>
              <h2 className="mb-2 text-sm font-medium text-text">{tr('已选层级', 'Chosen levels')}</h2>
              {levels.length === 0 ? (
                <p className="py-4 text-center text-xs text-text-muted">{tr('还没有层级', 'No levels yet')}</p>
              ) : (
                <ol className="divide-y divide-border">
                  {levels.map((name, index) => (
                    <li key={name} className="flex items-center gap-2 py-1.5 text-xs">
                      <span className="w-5 text-text-faint">{index + 1}.</span>
                      <span className="min-w-0 flex-1 truncate">{byName.get(name)?.alias || name}</span>
                      <Button
                        variant="subtle"
                        size="sm"
                        type="button"
                        onClick={() => move(index, -1)}
                        disabled={index === 0}
                        aria-label={tr(`上移 ${name}`, `Move ${name} up`)}
                      >
                        <ArrowUp size={12} aria-hidden="true" />
                      </Button>
                      <Button
                        variant="subtle"
                        size="sm"
                        type="button"
                        onClick={() => move(index, 1)}
                        disabled={index === levels.length - 1}
                        aria-label={tr(`下移 ${name}`, `Move ${name} down`)}
                      >
                        <ArrowDown size={12} aria-hidden="true" />
                      </Button>
                      <Button
                        variant="dangerGhost"
                        size="sm"
                        type="button"
                        onClick={() => toggle(name, false)}
                        aria-label={tr(`移除 ${name}`, `Remove ${name}`)}
                      >
                        <Trash2 size={12} aria-hidden="true" />
                      </Button>
                    </li>
                  ))}
                </ol>
              )}
              <div className="mt-3 flex justify-end">
                <Button variant="primary" type="button" onClick={() => void save()} disabled={saving}>
                  {saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
                </Button>
              </div>
            </Card>
          </div>
        </>
      ) : (
        <Card>
          <p className="py-6 text-center text-xs text-text-muted">
            {tr('还没有可配置的模型', 'No model is available yet')}
          </p>
        </Card>
      )}
    </div>
  );
}
