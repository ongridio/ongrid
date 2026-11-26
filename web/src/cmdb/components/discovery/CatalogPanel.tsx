import { useCallback, useEffect, useMemo, useState } from 'react';
import { RefreshCw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import { describeError } from '../../api/http';
import {
  listComponentAttributes,
  listHTTPAttributes,
  listHTTPCategories,
  listHTTPResources,
  listProviders,
  listSNMPAttributes,
  type CatalogCategory,
  type CatalogField,
  type Provider,
} from '../../api/discovery';
import { DataTable, type Column } from '../DataTable';

// 目录 — what the shipped providers can report.
//
// IT IS REFERENCE DATA, NOT CONFIGURATION. Nothing here writes: the catalogue
// is embedded in the binary, every deployment has the same one, and the page
// exists so an operator can see which fields a mapping will be able to choose
// before they build a target.
//
// A COMPONENT WITH NO SHIPPED TEMPLATE REPORTS NOTHING, and that is shown as an
// empty table rather than an error: the catalogue is a hint, and a deployment's
// own provider may not be described by it.

function providerKey(provider: Provider): string {
  return provider.en || provider.name;
}

export function CatalogPanel() {
  const { tr } = useI18n();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [providerRef, setProviderRef] = useState('');
  const [categories, setCategories] = useState<CatalogCategory[]>([]);
  const [resources, setResources] = useState<string[]>([]);
  const [resource, setResource] = useState('');
  const [fields, setFields] = useState<CatalogField[]>([]);
  const [loadingProviders, setLoadingProviders] = useState(true);
  const [loadingResources, setLoadingResources] = useState(false);
  const [loadingFields, setLoadingFields] = useState(false);
  const [fieldsReload, setFieldsReload] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [fieldsErr, setFieldsErr] = useState<string | null>(null);

  const selectedProvider = useMemo(
    () => providers.find((provider) => providerKey(provider) === providerRef) ?? null,
    [providers, providerRef],
  );

  const loadProviders = useCallback(async () => {
    setLoadingProviders(true);
    setErr(null);
    try {
      const rows = await listProviders();
      setProviders(rows);
      setProviderRef((current) =>
        rows.some((provider) => providerKey(provider) === current)
          ? current
          : rows[0]
            ? providerKey(rows[0])
            : '',
      );
    } catch (error) {
      setErr(describeError(error, tr('无法读取提供方目录', 'Could not load the provider catalogue')));
    } finally {
      setLoadingProviders(false);
    }
  }, [tr]);

  useEffect(() => {
    void loadProviders();
  }, [loadProviders]);

  useEffect(() => {
    let live = true;
    setResource('');
    setFields([]);
    setFieldsErr(null);
    if (!selectedProvider) {
      setCategories([]);
      setResources([]);
      return;
    }
    setLoadingResources(true);
    setErr(null);
    const key = providerKey(selectedProvider);
    const load = async (): Promise<{ groups: CatalogCategory[]; items: string[] }> => {
      switch (selectedProvider.type) {
        case 'http': {
          const [gotCategories, gotResources] = await Promise.all([
            listHTTPCategories(key),
            listHTTPResources(key),
          ]);
          return { groups: gotCategories, items: gotResources };
        }
        case 'snmp':
          return { groups: [{ category: 'SNMP', items: ['snmp'] }], items: ['snmp'] };
        case 'components':
          return { groups: [{ category: tr('组件', 'Components'), items: [key] }], items: [key] };
        default:
          return { groups: [], items: [] };
      }
    };
    void load()
      .then(({ groups, items }) => {
        if (!live) return;
        setCategories(groups);
        setResources(items);
        setResource(items[0] ?? '');
      })
      .catch((error) => {
        if (live) setErr(describeError(error, tr('无法读取目录资源', 'Could not load the catalogue resources')));
      })
      .finally(() => {
        if (live) setLoadingResources(false);
      });
    return () => {
      live = false;
    };
  }, [selectedProvider, tr]);

  useEffect(() => {
    let live = true;
    if (!selectedProvider || !resource) {
      setFields([]);
      return;
    }
    setLoadingFields(true);
    setFieldsErr(null);
    const key = providerKey(selectedProvider);
    const load = async (): Promise<CatalogField[]> => {
      switch (selectedProvider.type) {
        case 'http':
          return listHTTPAttributes(key, resource);
        case 'snmp':
          return listSNMPAttributes();
        case 'components':
          return listComponentAttributes(key);
        default:
          return [];
      }
    };
    void load()
      .then((rows) => {
        if (live) setFields(rows);
      })
      .catch((error) => {
        if (live) setFieldsErr(describeError(error, tr('无法读取字段', 'Could not load the fields')));
      })
      .finally(() => {
        if (live) setLoadingFields(false);
      });
    return () => {
      live = false;
    };
  }, [selectedProvider, resource, fieldsReload, tr]);

  const columns: Column<CatalogField>[] = [
    {
      key: 'name',
      header: tr('字段', 'Field'),
      width: 'w-full',
      cell: (row) => <code className="font-mono text-[11px] text-text">{row.name}</code>,
    },
    {
      key: 'type',
      header: tr('类型', 'Type'),
      cell: (row) => <code className="font-mono text-[11px] text-text-muted">{row.type || '—'}</code>,
    },
    {
      key: 'desc',
      header: tr('说明', 'Description'),
      hideable: true,
      cell: (row) =>
        row.desc ? (
          <span className="block max-w-[28rem] truncate text-xs text-text-muted">{row.desc}</span>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'example',
      header: tr('示例', 'Example'),
      hideable: true,
      defaultHidden: true,
      cell: (row) =>
        row.example != null && row.example !== '' ? (
          <code className="block max-w-[16rem] truncate font-mono text-[11px] text-text-muted">{String(row.example)}</code>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
  ];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-text-muted">
          {tr(
            '目录是只读的参考数据：它列出内置提供方可以采集哪些字段。映射目标时从这些字段里选。',
            'The catalogue is read-only reference data: it lists the fields the built-in providers report. A target maps from these fields.',
          )}
        </p>
        <Button
          variant="outline"
          size="sm"
          type="button"
          onClick={() => void loadProviders()}
          disabled={loadingProviders}
          className="inline-flex items-center gap-1.5"
        >
          <RefreshCw size={12} className={cn(loadingProviders && 'animate-spin')} aria-hidden="true" />
          {tr('刷新', 'Refresh')}
        </Button>
      </div>

      {err && (
        <p role="alert" className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </p>
      )}

      {loadingProviders && providers.length === 0 ? (
        <Card>
          <p className="p-6 text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
        </Card>
      ) : providers.length === 0 ? (
        <Card>
          <EmptyState
            title={tr('目录为空', 'The catalogue is empty')}
            hint={tr('这个部署没有可用的内置提供方。', 'This deployment has no built-in providers.')}
          />
        </Card>
      ) : (
        <>
          <div className="flex flex-wrap items-end gap-3">
            <div className="w-full max-w-xs">
              <Select
                label={tr('提供方', 'Provider')}
                value={providerRef}
                onValueChange={setProviderRef}
                options={providers.map((provider) => ({
                  value: providerKey(provider),
                  label: provider.name,
                }))}
                searchable
                className="w-full"
              />
            </div>
            {selectedProvider && (
              <span className="pb-2 text-[11px] text-text-faint">
                {selectedProvider.type} · {resources.length} {tr('个资源', 'resources')}
              </span>
            )}
          </div>

          <div className="flex min-h-0 min-w-0 flex-col gap-4 lg:flex-row">
            <Card className="shrink-0 lg:w-64">
              <div className="px-3 pb-2 pt-3">
                <h2 className="text-xs font-semibold text-text">{tr('资源', 'Resources')}</h2>
              </div>
              {loadingResources ? (
                <p className="px-3 pb-3 text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
              ) : resources.length === 0 ? (
                <p className="px-3 pb-3 text-xs text-text-faint">
                  {tr('这个提供方没有可浏览的资源。', 'This provider has no browsable resources.')}
                </p>
              ) : (
                <div className="flex flex-col gap-2 pb-2">
                  {categories.map((group) => (
                    <div key={group.category}>
                      <p className="px-3 pb-1 text-[10px] uppercase tracking-wide text-text-faint">{group.category}</p>
                      <ul className="flex flex-col gap-0.5">
                        {group.items.map((item) => {
                          const active = item === resource;
                          return (
                            <li key={item}>
                              <button
                                type="button"
                                onClick={() => setResource(item)}
                                aria-current={active ? 'true' : undefined}
                                className={cn(
                                  'w-full rounded-md px-3 py-1.5 text-left font-mono text-[11px] focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500',
                                  active ? 'bg-indigo-500/10 text-text' : 'text-text-muted hover:bg-card hover:text-text',
                                )}
                              >
                                {item}
                              </button>
                            </li>
                          );
                        })}
                      </ul>
                    </div>
                  ))}
                </div>
              )}
            </Card>

            <div className="min-w-0 flex-1">
              <Card>
                <DataTable
                  columns={columns}
                  rows={fields}
                  rowKey={(row) => row.name}
                  loading={loadingFields}
                  error={fieldsErr}
                  onRetry={() => setFieldsReload((current) => current + 1)}
                  storageKey="discovery-catalog-fields"
                  empty={
                    resource
                      ? tr('这个资源没有字段说明。', 'This resource has no field descriptions.')
                      : tr('选择一个资源查看字段。', 'Choose a resource to see its fields.')
                  }
                />
              </Card>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
