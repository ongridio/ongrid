import type { ReactNode } from 'react';
import { Eye, Play, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Chip } from '@/components/ui/Chip';
import { useI18n } from '@/i18n/locale';

import type { PoolAttribute, PoolEntry } from '../../../api/discovery';
import { DataTable, type Column } from '../../DataTable';

// The pool's table.
//
// THE COLUMNS COME FROM THE MAPPING, not from the provider's raw report: an
// operator compares a sighting with the instance it will become, and the model's
// names are what the instance page shows. A provider field the mapping does not
// mention is visible in the raw-data drawer, where it belongs.

function renderCell(value: unknown): ReactNode {
  if (value === null || value === undefined || value === '') {
    return <span className="text-text-faint">—</span>;
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="text-text-faint">—</span>;
    return <span className="block max-w-[20rem] truncate">{value.map((item) => String(item)).join(', ')}</span>;
  }
  if (typeof value === 'object') {
    return (
      <code className="block max-w-[20rem] truncate font-mono text-[11px] text-text-muted">
        {JSON.stringify(value)}
      </code>
    );
  }
  return <span className="block max-w-[20rem] truncate">{String(value)}</span>;
}

export function PoolTable({
  rows,
  attributes,
  loading,
  error,
  acceptingID,
  onRetry,
  onAccept,
  onDelete,
  onViewRaw,
}: {
  rows: PoolEntry[];
  attributes: PoolAttribute[];
  loading: boolean;
  error: string | null;
  acceptingID: number | null;
  onRetry(): void;
  onAccept(row: PoolEntry): void;
  onDelete(row: PoolEntry): void;
  onViewRaw(row: PoolEntry): void;
}) {
  const { tr } = useI18n();

  const columns: Column<PoolEntry>[] = [
    {
      key: 'unique_value',
      header: tr('唯一值', 'Unique value'),
      width: 'w-full',
      cell: (row) => (
        <span className="flex min-w-0 flex-col">
          <span className="truncate font-medium text-text">{row.unique_value || '—'}</span>
          {row.adr_name && <span className="truncate text-[11px] text-text-faint">{row.adr_name}</span>}
        </span>
      ),
    },
    ...attributes.map((attribute) => ({
      key: `attr_${attribute.id}`,
      header: attribute.alias || attribute.name,
      hideable: true,
      cell: (row: PoolEntry) => renderCell(row.instance?.[attribute.name]),
    })),
    {
      key: 'oneagent',
      header: tr('上报采集器', 'Reported by'),
      hideable: true,
      cell: (row) => {
        const label = row.oneagent_name || row.oneagent_id;
        return label ? (
          <span className="block max-w-[12rem] truncate text-xs text-text-muted">{label}</span>
        ) : (
          <span className="text-text-faint">—</span>
        );
      },
    },
    {
      key: 'updated_at',
      header: tr('更新时间', 'Updated'),
      hideable: true,
      defaultHidden: true,
      cell: (row) =>
        row.updated_at ? (
          <code className="font-mono text-[11px] text-text-muted">{row.updated_at}</code>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'status',
      header: tr('状态', 'Status'),
      cell: (row) =>
        row.is_accept ? (
          <Chip tone="success">{tr('已接受', 'Accepted')}</Chip>
        ) : (
          <Chip tone="warning">{tr('待接受', 'Pending')}</Chip>
        ),
    },
  ];

  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(row) => row.id}
      loading={loading}
      error={error}
      onRetry={onRetry}
      storageKey="discovery-pool"
      empty={tr('这个模型还没有发现记录。', 'No sightings for this model yet.')}
      actions={(row) => (
        <>
          {!row.is_accept && (
            <Button
              variant="primary"
              size="sm"
              type="button"
              onClick={() => onAccept(row)}
              disabled={acceptingID === row.id}
              aria-label={tr(`接受 ${row.unique_value}`, `Accept ${row.unique_value}`)}
              className="inline-flex items-center gap-1"
            >
              <Play size={12} aria-hidden="true" />
              {acceptingID === row.id ? tr('接受中…', 'Accepting…') : tr('接受', 'Accept')}
            </Button>
          )}
          <Button
            variant="subtle"
            size="sm"
            type="button"
            onClick={() => onViewRaw(row)}
            aria-label={tr(`查看原始数据 ${row.unique_value}`, `View raw data for ${row.unique_value}`)}
            className="inline-flex items-center gap-1"
          >
            <Eye size={12} aria-hidden="true" />
            {tr('原始数据', 'Raw data')}
          </Button>
          <Button
            variant="dangerGhost"
            size="sm"
            type="button"
            onClick={() => onDelete(row)}
            aria-label={tr(`删除 ${row.unique_value}`, `Delete ${row.unique_value}`)}
            className="inline-flex items-center gap-1"
          >
            <Trash2 size={12} aria-hidden="true" />
            {tr('删除', 'Delete')}
          </Button>
        </>
      )}
    />
  );
}
