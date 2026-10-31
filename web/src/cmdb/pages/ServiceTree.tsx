import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { RefreshCw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

import { listCITypes, listTypeAttributes, type CIType, type TypeAttribute } from '../api/citype';
import { getInstance, type SearchRow } from '../api/instance';
import { listServiceTrees, type ServiceTree } from '../api/preferences';
import { AttributeValue } from '../components/AttributeControl';
import { InstanceTree } from '../components/InstanceTree';
import { describeError } from '../api/http';
import { displayValue, isValueTypeCode } from '../lib/valuetype';

// 服务树 — a tree shaped by a definition.
//
// IT IS THE SAME WALK AS 资源层级 WITH A DIFFERENT STARTING POINT. A definition
// names the model pairs (机房 → 机柜 → 主机); the top model's instances are the
// roots and the chain's length is how far the tree may go. That is the whole
// difference, which is why the tree itself is one shared component.
//
// A DEFINITION THAT NO LONGER RESOLVES STILL APPEARS. The backend refuses to save
// one that names a missing relation, but an adopted database can hold one — and
// a page that hides it leaves the operator unable to see, let alone fix, the
// entry that is broken.

const MAX_LEVELS = 8;

export default function ServiceTreePage() {
  const { tr } = useI18n();

  const [definitions, setDefinitions] = useState<ServiceTree[]>([]);
  const [models, setModels] = useState<CIType[]>([]);
  const [chosenID, setChosenID] = useState<string>('');
  const [attributes, setAttributes] = useState<TypeAttribute[]>([]);
  const [picked, setPicked] = useState<SearchRow | null>(null);
  const [detail, setDetail] = useState<Record<string, unknown> | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const [gotTrees, gotModels] = await Promise.all([listServiceTrees(), listCITypes()]);
      setDefinitions(gotTrees);
      setModels(gotModels);
      setChosenID((current) => current || String(gotTrees[0]?.id ?? ''));
    } catch (error) {
      setErr(describeError(error, tr('无法读取服务树', 'Could not load the service trees')));
    } finally {
      setLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    void load();
  }, [load]);

  const chosen = useMemo(
    () => definitions.find((definition) => String(definition.id) === chosenID),
    [definitions, chosenID],
  );

  /**
   * The model a definition starts from.
   *
   * THE ROOT IS THE ONE PAIR SIDE THAT IS NEVER A CHILD, which is what makes a
   * definition a tree rather than a set of edges. A definition whose pairs form a
   * cycle has no root; the first pair's parent is used and the tree simply shows
   * what it can.
   */
  const shape = useMemo(() => {
    const pairs = chosen?.pairs ?? [];
    if (pairs.length === 0) return null;
    const children = new Set(pairs.map((pair) => pair.child_id));
    const rootID = pairs.find((pair) => !children.has(pair.parent_id))?.parent_id ?? pairs[0].parent_id;
    const byID = new Map(models.map((model) => [model.id, model]));
    // THE DEPTH IS THE LONGEST CHAIN from the root, capped by what the walk
    // offers. Counting pairs would over-report on a definition with two branches.
    let depth = 0;
    let frontier = [rootID];
    for (let level = 0; level < MAX_LEVELS; level += 1) {
      const next = pairs.filter((pair) => frontier.includes(pair.parent_id)).map((pair) => pair.child_id);
      if (next.length === 0) break;
      depth += 1;
      frontier = [...new Set(next)];
    }
    return { root: byID.get(rootID), depth };
  }, [chosen, models]);

  useEffect(() => {
    if (!shape?.root) {
      setAttributes([]);
      return;
    }
    let live = true;
    void listTypeAttributes(shape.root.id)
      .then((got) => live && setAttributes(got.attributes ?? []))
      .catch(() => live && setAttributes([]));
    return () => {
      live = false;
    };
  }, [shape?.root]);

  useEffect(() => {
    if (!picked) {
      setDetail(null);
      return;
    }
    let live = true;
    void getInstance(picked._id)
      .then((got) => live && setDetail(got.values ?? {}))
      .catch(() => live && setDetail(null));
    return () => {
      live = false;
    };
  }, [picked]);

  const publicCount = definitions.filter((definition) => definition.is_public).length;

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('服务树', 'Service tree')}
        subtitle={tr(
          '按服务树定义展开实例：定义说“哪种资源能挂在哪种下面”，这里展开真实的数据。',
          'Expand real instances along a definition: the definition says which kinds of resource nest, this shows what actually does.',
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
              label={tr('服务树定义', 'Tree definition')}
              value={chosenID}
              onValueChange={setChosenID}
              options={definitions.map((definition) => ({
                value: String(definition.id),
                label: definition.is_public ? `${definition.name} · ${tr('公开', 'public')}` : definition.name,
              }))}
              searchable
              className="w-64"
            />
            {shape?.root && (
              <span className="flex items-center gap-2 text-[11px] text-text-faint">
                {tr('顶层模型', 'Top model')}
                <Chip tone="info">{shape.root.alias || shape.root.name}</Chip>
                <span>
                  {shape.depth > 0 ? tr(`可展开 ${shape.depth} 层`, `${shape.depth} level(s)`) : tr('没有下级', 'No levels')}
                </span>
              </span>
            )}
            <Link to="/cmdb/service-trees" className="text-[11px] text-indigo-400 hover:text-indigo-300">
              {tr('管理定义', 'Manage definitions')} →
            </Link>
          </div>
        }
      />

      <div className="grid min-h-0 w-full min-w-0 flex-1 grid-cols-1 items-start gap-4 overflow-y-auto p-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <Card className="min-w-0">
          {err && (
            <div role="alert" className="mb-3 rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
              {err}
            </div>
          )}
          {loading ? (
            <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
          ) : definitions.length === 0 ? (
            // AN EMPTY STATE THAT NAMES THE NEXT STEP rather than saying "empty".
            <div className="py-8 text-center text-xs text-text-muted">
              {tr('还没有服务树定义。', 'No tree definitions yet.')}{' '}
              <Link to="/cmdb/service-trees" className="text-indigo-400 hover:text-indigo-300">
                {tr('先创建一条', 'Create one')} →
              </Link>
            </div>
          ) : !shape?.root ? (
            <p className="py-6 text-center text-xs text-text-muted">
              {tr('这个定义没有可解析的模型对', 'This definition has no resolvable model pairs')}
            </p>
          ) : (
            <InstanceTree
              key={`${chosenID}-${reloadKey}`}
              rootModel={shape.root}
              maxLevel={Math.max(1, Math.min(shape.depth, MAX_LEVELS))}
              onSelect={setPicked}
            />
          )}
        </Card>

        <Card className="min-w-0">
          {!picked ? (
            <p className="py-6 text-center text-xs text-text-muted">
              {tr('点一个节点查看它的属性', 'Pick a node to see its attributes')}
            </p>
          ) : (
            <>
              <div className="mb-2 flex items-center gap-2">
                <h2 className="min-w-0 truncate text-sm font-medium text-text">
                  {displayValue(detail?.[shape?.root?.unique_key ?? ''] ?? picked._id)}
                </h2>
                <Chip tone="info">{picked.ci_type_alias || picked.ci_type}</Chip>
              </div>
              <dl className="flex flex-col gap-2">
                {attributes.slice(0, 12).map((attribute) => (
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
              </dl>
              <p className="mt-3 text-[11px] text-text-faint">
                {tr(
                  `共 ${publicCount} 个公开定义。公开的定义所有人可见。`,
                  `${publicCount} of these definitions are public, and public ones are visible to everyone.`,
                )}
              </p>
            </>
          )}
        </Card>
      </div>
    </div>
  );
}
