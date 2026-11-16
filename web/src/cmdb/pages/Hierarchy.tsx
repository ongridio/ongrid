import { useEffect, useMemo, useState } from 'react';
import { RefreshCw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

import { listCITypes, listTypeAttributes, type CIType, type TypeAttribute } from '../api/citype';
import { getInstance, type SearchRow } from '../api/instance';
import { AttributeValue } from '../components/AttributeControl';
import { InstanceTree } from '../components/InstanceTree';
import { useToast } from '../components/Toast';
import { describeError } from '../api/http';
import { displayValue, isValueTypeCode } from '../lib/valuetype';

// 资源层级 — one model's instances and what sits under them.
//
// THE LEVELS ARE A LIST, NOT A DEPTH. `1,2,3` walks three levels down and returns
// the first three; the menu's "L1–L8" is exactly that. Here the cap is enforced
// by not offering a chevron past it — a tree that lets you click into a level the
// server will not return is a tree whose last click does nothing.
//
// THE RIGHT SIDE IS THE PICKED INSTANCE, read on demand. The walk returns values
// for every row it hands back, so the panel costs one request per pick rather
// than one per node.

const MAX_LEVELS = 8;

export default function HierarchyPage() {
  const { tr } = useI18n();
  const toast = useToast();

  const [models, setModels] = useState<CIType[]>([]);
  const [typeRef, setTypeRef] = useState('');
  const [maxLevel, setMaxLevel] = useState(3);
  const [attributes, setAttributes] = useState<TypeAttribute[]>([]);
  const [picked, setPicked] = useState<SearchRow | null>(null);
  const [detail, setDetail] = useState<Record<string, unknown> | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    void listCITypes()
      .then((all) => {
        setModels(all);
        if (all.length > 0) setTypeRef((current) => current || all[0].name);
      })
      .catch(() => undefined);
  }, []);

  const model = useMemo(
    () => models.find((item) => item.name === typeRef || String(item.id) === typeRef),
    [models, typeRef],
  );

  useEffect(() => {
    if (!typeRef) return;
    let live = true;
    void listTypeAttributes(typeRef)
      .then((got) => live && setAttributes(got.attributes ?? []))
      .catch(() => live && setAttributes([]));
    return () => {
      live = false;
    };
  }, [typeRef]);

  // The panel reads the instance rather than trusting the tree's row: the row is
  // a projection, and the detail is what an operator expects to be complete.
  useEffect(() => {
    if (!picked) {
      setDetail(null);
      return;
    }
    let live = true;
    setDetailLoading(true);
    void getInstance(picked._id)
      .then((got) => live && setDetail(got.values ?? {}))
      .catch((error) => {
        if (!live) return;
        setDetail(null);
        toast.error(describeError(error, tr('无法读取实例', 'Could not load the instance')));
      })
      .finally(() => live && setDetailLoading(false));
    return () => {
      live = false;
    };
  }, [picked, toast, tr]);

  const shownAttributes = attributes.slice(0, 12);

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('资源层级', 'Resource hierarchy')}
        subtitle={tr(
          '按关系一层层展开。层级只影响能展开到多深，不影响已展开的内容。',
          'Expand level by level along the relations. The cap limits how deep you can go, not what is already open.',
        )}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              type="button"
              onClick={() => setReloadKey((key) => key + 1)}
              className="inline-flex items-center gap-1.5"
            >
              <RefreshCw size={12} aria-hidden="true" />
              {tr('重新加载', 'Reload')}
            </Button>
          </div>
        }
        extra={
          <div className="flex flex-wrap items-center gap-3">
            <Select
              label={tr('顶层模型', 'Top-level model')}
              value={typeRef}
              onValueChange={setTypeRef}
              options={models.map((item) => ({ value: item.name, label: item.alias || item.name }))}
              searchable
              className="w-56"
            />
            <div className="flex items-center gap-1" role="group" aria-label={tr('展开到第几层', 'Expand to level')}>
              <span className="mr-1 text-xs text-text-muted">{tr('层级', 'Levels')}</span>
              {Array.from({ length: MAX_LEVELS }, (_, index) => index + 1).map((level) => (
                <button
                  key={level}
                  type="button"
                  onClick={() => setMaxLevel(level)}
                  aria-pressed={maxLevel === level}
                  className={
                    maxLevel === level
                      ? 'rounded bg-indigo-500/20 px-2 py-0.5 text-xs font-medium text-indigo-300'
                      : 'rounded px-2 py-0.5 text-xs text-text-muted hover:bg-card hover:text-text'
                  }
                >
                  L{level}
                </button>
              ))}
            </div>
          </div>
        }
      />

      <div className="grid min-h-0 w-full min-w-0 flex-1 grid-cols-1 items-start gap-4 overflow-y-auto p-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <Card className="min-w-0">
          {model ? (
            <InstanceTree
              // A NEW TREE PER MODEL AND CAP: the previous expansion belonged to
              // a different shape and reusing it would show stale branches.
              key={`${model.id}-${maxLevel}-${reloadKey}`}
              rootModel={model}
              maxLevel={maxLevel}
              onSelect={setPicked}
            />
          ) : (
            <p className="py-6 text-center text-xs text-text-muted">{tr('先选一个模型', 'Pick a model first')}</p>
          )}
        </Card>

        <Card className="min-w-0">
          {!picked ? (
            <p className="py-6 text-center text-xs text-text-muted">
              {tr('点一个实例查看它的属性', 'Pick an instance to see its attributes')}
            </p>
          ) : detailLoading ? (
            <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
          ) : (
            <>
              <div className="mb-2 flex items-center gap-2">
                <h2 className="min-w-0 truncate text-sm font-medium text-text">
                  {displayValue(detail?.[model?.unique_key ?? ''] ?? picked._id)}
                </h2>
                <Chip tone="info">{picked.ci_type_alias || picked.ci_type}</Chip>
              </div>
              <dl className="flex flex-col gap-2">
                {shownAttributes.map((attribute) => (
                  <div key={attribute.id} className="min-w-0">
                    <dt className="text-[11px] text-text-faint">{attribute.alias || attribute.name}</dt>
                    <dd className="break-words text-xs text-text">
                      <AttributeValue
                        type={isValueTypeCode(attribute.value_type) ? attribute.value_type : '2'}
                        value={detail?.[attribute.name]}
                      />
                    </dd>
                  </div>
                ))}
                {shownAttributes.length === 0 && (
                  <p className="text-xs text-text-muted">{tr('这个模型没有属性', 'This model has no attributes')}</p>
                )}
              </dl>
            </>
          )}
        </Card>
      </div>
    </div>
  );
}
