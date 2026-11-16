import { useCallback, useEffect, useMemo, useState } from 'react';
import { Play, Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { EmptyState } from '@/components/ui/EmptyState';
import { Hint } from '@/components/ui/Tooltip';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import { describeError } from '../../api/http';
import { listCITypes } from '../../api/citype';
import {
  deleteTarget,
  listTargetTypes,
  listTargets,
  testTarget,
  type Target,
  type TargetTypeCount,
} from '../../api/discovery';
import { CMDBIcon } from '../CMDBIcon';
import { DataTable, type Column } from '../DataTable';
import { useToast } from '../Toast';
import { TargetDrawer } from './TargetDrawer';

// 目标 — where a rule lands.
//
// THE LEFT LIST IS MODELS, NOT RULES. An operator asks "what discovers hosts",
// not "where is the aliyun rule used", and cantor's pool tree answered the same
// way. A model with no target is absent rather than shown empty: there is
// nothing to configure under it.

export function TargetsPanel() {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [types, setTypes] = useState<TargetTypeCount[]>([]);
  const [typesLoading, setTypesLoading] = useState(true);
  const [typesErr, setTypesErr] = useState<string | null>(null);
  const [selected, setSelected] = useState<number | null>(null);

  const [targets, setTargets] = useState<Target[]>([]);
  const [loading, setLoading] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<Target | 'new' | null>(null);

  const loadTypes = useCallback(async () => {
    setTypesLoading(true);
    setTypesErr(null);
    try {
      // EVERY ATTACHED MODEL IS LISTED, not only the ones that already have a
      // target: otherwise the first target of a model could never be created
      // from this page. `target-types` supplies the counts, `ci-types` the
      // models, and a model missing from the counts simply starts at zero.
      const [counts, models] = await Promise.all([listTargetTypes(), listCITypes()]);
      const countOf = new Map(counts.map((item) => [item.type_id, item.target_count]));
      const merged: TargetTypeCount[] = models
        .filter((model) => model.is_attached)
        .map((model) => ({
          type_id: model.id,
          name: model.name,
          alias: model.alias,
          icon: model.icon,
          target_count: countOf.get(model.id) ?? 0,
        }));
      const known = new Set(merged.map((item) => item.type_id));
      for (const item of counts) {
        if (!known.has(item.type_id)) merged.push(item);
      }
      setTypes(merged);
      setSelected((current) => {
        if (current != null && merged.some((item) => item.type_id === current)) return current;
        return merged[0]?.type_id ?? null;
      });
    } catch (error) {
      setTypesErr(describeError(error, tr('无法读取可发现的模型', 'Could not load the discoverable models')));
    } finally {
      setTypesLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    void loadTypes();
  }, [loadTypes]);

  const loadTargets = useCallback(
    async (typeID: number, quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        setTargets(await listTargets(typeID));
      } catch (error) {
        setErr(describeError(error, tr('无法读取发现目标', 'Could not load the discovery targets')));
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [tr],
  );

  useEffect(() => {
    if (selected == null) {
      setTargets([]);
      return;
    }
    void loadTargets(selected);
  }, [selected, loadTargets]);

  const selectedType = useMemo(
    () => types.find((item) => item.type_id === selected) ?? null,
    [types, selected],
  );

  const labelOf = (type: TargetTypeCount) => type.alias || type.name || `#${type.type_id}`;

  const remove = async (row: Target) => {
    const name = row.adr_name || `#${row.id}`;
    const ok = await confirmAction(
      tr(`删除目标「${name}」？它不会再被采集器拉取。`, `Delete the target “${name}”? Collectors will no longer pull it.`),
    );
    if (!ok) return;
    try {
      await deleteTarget(row.id);
      toast.success(tr('已删除', 'Deleted'));
      await Promise.all([loadTargets(selected!, true), loadTypes()]);
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const runTest = async (row: Target) => {
    try {
      await testTarget(row.id);
      toast.info(tr('测试接口没有返回结果', 'The test endpoint returned nothing'));
    } catch (error) {
      // THE BACKEND ANSWERS 501. Showing its message is the point: a toast that
      // said "测试通过" would be reporting a probe that never ran.
      toast.error(describeError(error, tr('测试接口不可用', 'The test endpoint is unavailable')));
    }
  };

  const columns: Column<Target>[] = [
    {
      key: 'rule',
      header: tr('发现规则', 'Rule'),
      width: 'w-full',
      cell: (row) => (
        <span className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="truncate font-medium text-text">{row.adr_name || `#${row.adr_id}`}</span>
          {row.adr_missing && <Chip tone="warning">{tr('规则已不存在', 'Rule is gone')}</Chip>}
          {row.is_plugin && <Chip tone="info">{tr('插件', 'Plugin')}</Chip>}
        </span>
      ),
    },
    {
      key: 'mode',
      header: tr('执行方式', 'Runs on'),
      cell: (row) =>
        row.agent_id ? (
          <span className="flex items-center gap-1.5 text-xs">
            <span className="text-text-faint">Agent</span>
            <code className="max-w-[12rem] truncate font-mono text-[11px] text-text-muted">{row.agent_id}</code>
          </span>
        ) : row.query_expr ? (
          <code className="block max-w-[18rem] truncate font-mono text-[11px] text-text-muted">{row.query_expr}</code>
        ) : (
          <Chip tone="info">{tr('全部采集器', 'Every collector')}</Chip>
        ),
    },
    {
      key: 'cron',
      header: tr('周期', 'Schedule'),
      hideable: true,
      cell: (row) =>
        row.cron ? (
          <code className="font-mono text-[11px] text-text-muted">{row.cron}</code>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'auto_accept',
      header: tr('自动接受', 'Auto-accept'),
      hideable: true,
      cell: (row) =>
        row.auto_accept ? <Chip tone="success">{tr('是', 'Yes')}</Chip> : <span className="text-text-faint">—</span>,
    },
    {
      key: 'credential',
      header: tr('凭据', 'Credential'),
      hideable: true,
      cell: (row) =>
        row.has_secret || row.has_password ? (
          <Chip tone="info">{tr('已设置', 'Set')}</Chip>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'enabled',
      header: tr('状态', 'Status'),
      cell: (row) =>
        row.enabled ? (
          <Chip tone="success">{tr('启用', 'Enabled')}</Chip>
        ) : (
          <Chip>{tr('停用', 'Disabled')}</Chip>
        ),
    },
  ];

  return (
    <div className="flex min-h-0 min-w-0 flex-col gap-4 lg:flex-row">
      <Card className="shrink-0 lg:w-64">
        <div className="flex items-center justify-between gap-2 px-3 pb-2 pt-3">
          <h2 className="text-xs font-semibold text-text">{tr('模型', 'Models')}</h2>
          <Button
            variant="subtle"
            size="sm"
            type="button"
            onClick={() => void loadTypes()}
            disabled={typesLoading}
            aria-label={tr('刷新模型列表', 'Refresh the model list')}
            className="inline-flex items-center gap-1"
          >
            <RefreshCw size={11} className={cn(typesLoading && 'animate-spin')} aria-hidden="true" />
          </Button>
        </div>
        {typesErr ? (
          <p role="alert" className="mx-3 mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
            {typesErr}
          </p>
        ) : typesLoading && types.length === 0 ? (
          <p className="px-3 pb-3 text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
        ) : types.length === 0 ? (
          <p className="px-3 pb-4 text-xs text-text-faint">
            {tr('还没有可用的模型。先到「模型配置」创建模型。', 'No usable model yet. Create one under Model configuration first.')}
          </p>
        ) : (
          <ul className="flex flex-col gap-0.5 pb-2">
            {types.map((type) => {
              const active = type.type_id === selected;
              return (
                <li key={type.type_id}>
                  <button
                    type="button"
                    aria-label={tr(`查看 ${labelOf(type)} 的目标`, `View targets for ${labelOf(type)}`)}
                    aria-current={active ? 'true' : undefined}
                    onClick={() => setSelected(type.type_id)}
                    className={cn(
                      'flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-left text-xs focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500',
                      active ? 'bg-indigo-500/10 text-text' : 'text-text-muted hover:bg-card hover:text-text',
                    )}
                  >
                    <CMDBIcon value={type.icon} size={14} label={labelOf(type)} />
                    <span className="min-w-0 flex-1 truncate">{labelOf(type)}</span>
                    <span className="tabular-nums text-[11px] text-text-faint">{type.target_count}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </Card>

      <div className="min-w-0 flex-1">
        {selectedType == null ? (
          <Card>
            <EmptyState
              icon={Play}
              title={tr('选择一个模型', 'Choose a model')}
              hint={tr('左侧列出所有可挂载的模型，数字是已有的目标数。', 'The list on the left holds every attached model; the number is its target count.')}
            />
          </Card>
        ) : (
          <>
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <p className="min-w-0 text-xs text-text-muted">
                {tr(
                  `「${labelOf(selectedType)}」的目标。采集由 agent 拉取执行，这里只配置拉取什么。`,
                  `Targets of “${labelOf(selectedType)}”. Collection is an agent's pull; this page configures what it pulls.`,
                )}
              </p>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  variant="primary"
                  size="sm"
                  type="button"
                  onClick={() => setEditing('new')}
                  className="inline-flex items-center gap-1.5"
                >
                  <Plus size={12} aria-hidden="true" />
                  {tr('新建目标', 'New target')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  type="button"
                  onClick={() => void loadTargets(selectedType.type_id, true)}
                  disabled={loading || refreshing}
                  className="inline-flex items-center gap-1.5"
                >
                  <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} aria-hidden="true" />
                  {tr('刷新', 'Refresh')}
                </Button>
              </div>
            </div>

            <Card>
              <DataTable
                columns={columns}
                rows={targets}
                rowKey={(row) => row.id}
                loading={loading}
                error={err}
                onRetry={() => void loadTargets(selectedType.type_id)}
                storageKey="discovery-targets"
                empty={tr('这个模型还没有发现目标。', 'This model has no discovery target yet.')}
                actions={(row) => (
                  <>
                    <Button
                      variant="subtle"
                      size="sm"
                      type="button"
                      onClick={() => void runTest(row)}
                      aria-label={tr(`测试 ${row.adr_name || row.id}`, `Test ${row.adr_name || row.id}`)}
                      className="inline-flex items-center gap-1"
                    >
                      <Play size={12} aria-hidden="true" />
                      {tr('测试', 'Test')}
                    </Button>
                    <Button
                      variant="subtle"
                      size="sm"
                      type="button"
                      onClick={() => setEditing(row)}
                      aria-label={tr(`编辑目标 ${row.adr_name || row.id}`, `Edit target ${row.adr_name || row.id}`)}
                    >
                      {tr('编辑', 'Edit')}
                    </Button>
                    <Hint content={tr('凭据属于创建者的目标需要创建者删除。', 'A target holding a credential may only be removed by its owner.')}>
                      <Button
                        variant="dangerGhost"
                        size="sm"
                        type="button"
                        onClick={() => void remove(row)}
                        aria-label={tr(`删除目标 ${row.adr_name || row.id}`, `Delete target ${row.adr_name || row.id}`)}
                        className="inline-flex items-center gap-1"
                      >
                        <Trash2 size={12} aria-hidden="true" />
                        {tr('删除', 'Delete')}
                      </Button>
                    </Hint>
                  </>
                )}
              />
            </Card>
          </>
        )}
      </div>

      {editing && selectedType && (
        <TargetDrawer
          type={selectedType}
          target={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async (message) => {
            setEditing(null);
            toast.success(message);
            await Promise.all([loadTargets(selectedType.type_id, true), loadTypes()]);
          }}
        />
      )}
      {dialog}
    </div>
  );
}
