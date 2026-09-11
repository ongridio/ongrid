import { Hint } from '@/components/ui/Tooltip';
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/Popover';
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ChevronDown } from 'lucide-react';
import { queryApm, serviceParams, type ApmList } from '@/api/apm';
import { useI18n } from '@/i18n/locale';
import { SearchInput } from './SearchInput';

export function ServiceSwitcher({
  params,
  navigationState,
}: {
  params: URLSearchParams;
  navigationState: unknown;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [result, setResult] = useState<ApmList>();
  const [error, setError] = useState('');
  const query = params.toString();
  useEffect(() => setOpen(false), [query]);
  useEffect(() => {
    if (!open) return;
    const p = new URLSearchParams(query);
    for (const key of [
      'service_name',
      'service_version',
      'instance_id',
      'operation',
      'environment',
      'service_namespace',
      'page',
      'tab',
    ])
      p.delete(key);
    p.set('search', search);
    p.set('page_size', '25');
    p.set('sort', 'name');
    const controller = new AbortController();
    setResult(undefined);
    setError('');
    queryApm('services', p, controller.signal)
      .then((data) => {
        if (!controller.signal.aborted) setResult(data);
      })
      .catch((e: Error) => {
        if (!controller.signal.aborted) setError(e.message);
      });
    return () => controller.abort();
  }, [open, search, query]);
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger aria-label={tr('切换服务', 'Switch service')} className="flex min-w-0 max-w-full items-center gap-2 rounded text-left hover:text-indigo-500">
      <Hint content={params.get('service_name') || ''}><span className="truncate" >{params.get('service_name')}</span></Hint><ChevronDown size={15} className="shrink-0" />
    </PopoverTrigger>
    <PopoverContent aria-label={tr('选择服务', 'Choose service')} className="w-80 text-sm font-normal">
          <SearchInput
            value={search}
            onChange={setSearch}
            label={tr('搜索服务', 'Search services')}
            autoFocus
          />
          <div className="mt-2 max-h-64 overflow-auto">
            {error ? (
              <p role="alert" className="py-3 text-red-500">
                {error}
              </p>
            ) : !result ? (
              <p role="status" className="py-3 text-zinc-500">
                {tr('查询中…', 'Loading…')}
              </p>
            ) : result.items.length === 0 ? (
              <p className="py-3 text-zinc-500">{tr('未找到服务', 'No services found')}</p>
            ) : (
              result.items.map(({ identity }) => (
                <Link
                  key={JSON.stringify(identity)}
                  state={navigationState}
                  to={`/apm/service?${serviceParams(params, identity)}`}
                  className="block rounded px-2 py-2 hover:bg-zinc-900 focus:bg-zinc-900"
                >
                  <span className="block truncate font-medium">{identity.service_name}</span>
                  <span className="text-xs text-zinc-500">
                    {identity.environment || tr('未设置', 'Unset')} /{' '}
                    {identity.service_namespace || tr('未设置', 'Unset')}
                  </span>
                </Link>
              ))
            )}
          </div>
          {result && result.total > result.items.length && (
            <p className="mt-2 text-xs text-zinc-500">
              {tr(
                '显示前 25 项，输入名称缩小范围',
                'Showing the first 25; type a name to narrow results',
              )}
            </p>
          )}
    </PopoverContent>
  </Popover>;
}
