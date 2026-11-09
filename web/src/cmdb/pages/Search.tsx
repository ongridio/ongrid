import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { RefreshCw, Search as SearchIcon } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { PageHeader } from '@/components/ui/PageHeader';
import { PaginationFooter } from '@/components/ui/PaginationFooter';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

import { describeError } from '../api/http';
import { listCITypes, listTypeAttributes, type CIType } from '../api/citype';
import { searchInstances, type FacetBucket, type SearchPage } from '../api/instance';
import { DataTable, type Column } from '../components/DataTable';
import { QueryBar, type QueryAttribute } from '../components/QueryBar';
import { displayValue, isValueTypeCode } from '../lib/valuetype';

// 资源搜索 — the query language with a builder in front of it.
//
// THE EXPRESSION IS THE STATE. Everything on this page derives from one string:
// the builder's rows, the raw editor, what the request carries, and what the
// "effective expression" line shows. That is why the page cannot drift into the
// state where the box says one thing and the server searched another.
//
// THE MODEL SELECT COMPOSES RATHER THAN REPLACES. Restricting to a model
// prepends `_type:<name>` to the operator's own expression and the effective
// line shows the result — so "I picked a model" and "my filter" are both
// visible, instead of the model silently narrowing something the operator
// cannot see.

const PAGE_SIZE = 20;

export default function SearchPage() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();

  const [models, setModels] = useState<CIType[]>([]);
  const [typeRef, setTypeRef] = useState(params.get('type') ?? '');
  const [limitToType, setLimitToType] = useState(Boolean(params.get('type')));
  const [query, setQuery] = useState(params.get('q') ?? '');
  const [attributes, setAttributes] = useState<QueryAttribute[]>([]);
  const [result, setResult] = useState<SearchPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void listCITypes()
      .then((all) => {
        setModels(all);
        if (!typeRef && all.length > 0) setTypeRef(all[0].name);
      })
      .catch(() => undefined);
  }, [typeRef]);

  // The builder's vocabulary is the chosen model's attributes. It is one model
  // rather than the union over every model: a union would be hundreds of rows
  // and half of them would not apply to what is being searched.
  useEffect(() => {
    if (!typeRef) return;
    let live = true;
    void listTypeAttributes(typeRef)
      .then((got) => {
        if (!live) return;
        setAttributes(
          (got.attributes ?? []).map((attribute) => ({
            ref: attribute.name,
            label: attribute.alias || attribute.name,
            type: isValueTypeCode(attribute.value_type) ? attribute.value_type : '2',
            // A JSON attribute is not matchable — the backend refuses it — and a
            // password attribute would turn the search box into a credential
            // oracle.
            matchable: !attribute.is_password && attribute.value_type !== '6',
          })),
        );
      })
      .catch(() => live && setAttributes([]));
    return () => {
      live = false;
    };
  }, [typeRef]);

  // WHAT IS ACTUALLY SENT, shown to the operator.
  const effective = useMemo(() => {
    const own = query.trim();
    if (!limitToType || !typeRef) return own;
    const typeTerm = `_type:${typeRef}`;
    return own ? `${typeTerm},${own}` : typeTerm;
  }, [query, limitToType, typeRef]);

  const run = useCallback(
    async (targetPage: number) => {
      setLoading(true);
      setErr(null);
      try {
        const got = await searchInstances({
          q: effective,
          page: targetPage,
          count: PAGE_SIZE,
          facet: ['_type'],
        });
        setResult(got);
        const next = new URLSearchParams();
        if (limitToType && typeRef) next.set('type', typeRef);
        if (query.trim()) next.set('q', query.trim());
        setParams(next, { replace: true });
      } catch (error) {
        setErr(describeError(error, tr('搜索失败', 'The search failed')));
      } finally {
        setLoading(false);
      }
    },
    [effective, limitToType, typeRef, query, tr, setParams],
  );

  // The first run happens on mount so a deep link with `?q=` works, and again
  // only when the operator asks — a request per keystroke would be a request per
  // keystroke.
  useEffect(() => {
    void run(1);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const rows = useMemo(() => result?.result ?? [], [result]);

  // THE COLUMNS ARE THE RESULT'S OWN. A search spans models, so the column set
  // is derived from the rows: the metadata keys first, then the attribute names
  // the rows carry, in the order the first row has them.
  const columns = useMemo<Column<(typeof rows)[number]>[]>(() => {
    const names: string[] = [];
    for (const row of rows) {
      for (const key of Object.keys(row)) {
        if (key.startsWith('_') || key === 'ci_id' || key === 'ci_type' || key === 'ci_type_alias' || key === 'unique' || key === 'unique_alias') continue;
        if (!names.includes(key)) names.push(key);
      }
      if (names.length >= 8) break;
    }
    return [
      {
        key: '_id',
        header: tr('编号', 'ID'),
        width: 'w-px',
        cell: (row) => <code className="font-mono text-[11px] text-text-muted">{row._id}</code>,
      },
      {
        key: 'ci_type',
        header: tr('模型', 'Model'),
        cell: (row) => <Chip tone="info">{row.ci_type_alias || row.ci_type}</Chip>,
      },
      ...names.map((name) => ({
        key: name,
        header: name,
        width: names.length === 1 ? 'w-full' : undefined,
        cell: (row: (typeof rows)[number]) => (
          <span className="block min-w-0 truncate" title={displayValue(row[name])}>
            {displayValue(row[name])}
          </span>
        ),
      })),
      {
        key: '_updated_at',
        header: tr('最近修改', 'Last changed'),
        hideable: true,
        cell: (row) => <span className="whitespace-nowrap text-[11px] text-text-muted">{row._updated_at || '—'}</span>,
      },
    ];
  }, [rows, tr]);

  const facets = result?.facet ?? {};

  const appendFacet = (field: string, bucket: FacetBucket) => {
    const clause = `${field}:${displayValue(bucket[0])}`;
    setQuery((current) => (current.trim() ? `${current.trim()},${clause}` : clause));
  };

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('资源搜索', 'Resource search')}
        subtitle={tr(
          '用查询语言在所有资源里搜索。逗号是“并且”，减号是“或者”，波浪号是“不是”。',
          'Search every resource with the query language. Comma is AND, minus is OR, tilde is NOT.',
        )}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="primary"
              size="sm"
              type="button"
              onClick={() => void run(1)}
              disabled={loading}
              className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
            >
              {loading ? <RefreshCw size={12} className="animate-spin" aria-hidden="true" /> : <SearchIcon size={12} aria-hidden="true" />}
              {tr('搜索', 'Search')}
            </Button>
          </div>
        }
      />

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-6">
        <Card className="mb-4">
          <div className="mb-3 flex flex-wrap items-center gap-3">
            <Select
              label={tr('模型', 'Model')}
              value={typeRef}
              onValueChange={setTypeRef}
              options={models.map((model) => ({ value: model.name, label: model.alias || model.name }))}
              searchable
              className="w-56"
            />
            <label className="flex items-center gap-2 text-xs text-text-muted">
              <input
                type="checkbox"
                checked={limitToType}
                onChange={(event) => setLimitToType(event.target.checked)}
                className="h-3.5 w-3.5 rounded border-border"
              />
              {tr('只搜这个模型', 'Only this model')}
            </label>
            {limitToType && typeRef && (
              <code className="rounded bg-card px-1.5 py-0.5 font-mono text-[11px] text-text-faint">_type:{typeRef}</code>
            )}
          </div>

          <QueryBar attributes={attributes} value={query} onChange={setQuery} />

          <p className="mt-2 break-words text-[11px] text-text-faint">
            {tr('实际发送：', 'Sent as: ')}
            <code className="font-mono">{effective || tr('（空表达式，匹配全部）', '(empty — matches everything)')}</code>
          </p>
        </Card>

        {err && (
          <div role="alert" className="mb-3 rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}

        {Object.keys(facets).length > 0 && (
          <Card className="mb-4">
            {Object.entries(facets).map(([field, buckets]) => (
              <div key={field} className="mb-2 last:mb-0">
                <span className="mr-2 text-[11px] text-text-faint">{field}</span>
                <span className="inline-flex flex-wrap gap-1">
                  {buckets.map((bucket, index) => (
                    <button
                      key={`${field}-${index}`}
                      type="button"
                      onClick={() => appendFacet(bucket[2] || field, bucket)}
                      className="rounded focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
                      aria-label={tr(`按 ${displayValue(bucket[0])} 筛选`, `Filter by ${displayValue(bucket[0])}`)}
                    >
                      <Chip tone="default">
                        {displayValue(bucket[0])}
                        <span className="ml-1 text-text-faint">{bucket[1]}</span>
                      </Chip>
                    </button>
                  ))}
                </span>
              </div>
            ))}
          </Card>
        )}

        <Card>
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(row) => row._id}
            loading={loading}
            storageKey="search-results"
            empty={
              query.trim() || (limitToType && typeRef)
                ? tr('没有匹配的资源', 'Nothing matches that')
                : tr('还没有搜索条件', 'No filter yet')
            }
            actions={(row) => (
              <Button
                variant="subtle"
                size="sm"
                type="button"
                onClick={() => {
                  // RESULTS ARE FOR FINDING; EDITING HAPPENS ON THE MODEL'S PAGE,
                  // where the form is generated from that model rather than
                  // guessed from a row that spans models.
                  navigate(`/cmdb/instances?type=${encodeURIComponent(row.ci_type)}`);
                }}
                aria-label={tr(`在 ${row.ci_type} 里打开`, `Open in ${row.ci_type}`)}
              >
                {tr('打开模型', 'Open model')}
              </Button>
            )}
          />
          <PaginationFooter
            page={(result?.page ?? 1) - 1}
            pageSize={PAGE_SIZE}
            shown={rows.length}
            total={result?.numfound ?? 0}
            loading={loading}
            matchLabel
            onPageChange={(next) => void run(next + 1)}
            className="mt-3"
          />
        </Card>
      </div>
    </div>
  );
}
