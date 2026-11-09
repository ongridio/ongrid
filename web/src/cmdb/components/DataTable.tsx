import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { ArrowDown, ArrowUp, ChevronsUpDown, Columns3, RotateCw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Checkbox } from '@/components/ui/Checkbox';
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

// The CMDB's one table.
//
// THERE IS NO TABLE PRIMITIVE IN THE SHARED KIT, which is why this exists rather
// than each page writing its own `<table>`. Fourteen pages would otherwise have
// fourteen ideas of what a header, an action cell and an empty state look like,
// and the design language's operation-column rules would have to be re-derived
// in each one.
//
// THE LAYOUT IS AUTOMATIC, NOT FIXED. The design language prefers auto layout
// for tables whose content and action count vary — and they do here, because a
// model's columns come from its attributes. Auto layout also lets the action
// column be `w-px` and grow to its buttons instead of taking a guessed fixed
// width out of the information columns.
//
// SORTING IS THE SERVER'S WHEN IT CAN BE. A CMDB list is a page of a larger set,
// and sorting the page an operator happens to be holding would produce an order
// that is wrong past the page. When the endpoint takes a sort field the
// component reports the intent and leaves the rows alone; when it does not (the
// relation-type dictionary is the whole set) it sorts its own rows.

export type Column<T> = {
  /** Stable identity; also the column-visibility key. */
  key: string;
  header: ReactNode;
  cell: (row: T) => ReactNode;
  /** Whether the operator may sort by this column. */
  sortable?: boolean;
  /** The local sort value. Required for a sortable column when the table sorts itself. */
  sortValue?: (row: T) => string | number;
  /** A `w-*` token; omit to size to content. */
  width?: string;
  align?: 'left' | 'right';
  /** A column the operator may hide. */
  hideable?: boolean;
  /** Starts hidden; only meaningful with `hideable`. */
  defaultHidden?: boolean;
};

export type SortState = { key: string; desc: boolean };

type Props<T> = {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string | number;

  loading?: boolean;
  /** A load failure. The rows already loaded stay on screen. */
  error?: string | null;
  onRetry?: () => void;
  /** What to say when there is nothing to show. */
  empty?: ReactNode;

  sort?: SortState | null;
  /** Present ⇒ the server sorts; absent ⇒ this table sorts its own rows. */
  onSortChange?: (next: SortState | null) => void;

  /** Row actions, drawn in the sticky last column. */
  actions?: (row: T) => ReactNode;
  actionsHeader?: ReactNode;

  selection?: {
    selected: ReadonlySet<string | number>;
    onChange(next: ReadonlySet<string | number>): void;
  };

  /** Persists column visibility under this key. */
  storageKey?: string;
};

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  loading = false,
  error = null,
  onRetry,
  empty,
  sort = null,
  onSortChange,
  actions,
  actionsHeader,
  selection,
  storageKey,
}: Props<T>) {
  const { tr } = useI18n();

  // A COLUMN MAY START HIDDEN WITHOUT THE OPERATOR HAVING HIDDEN IT. The
  // defaults are a STRING so the effect below has a stable dependency: pages
  // rebuild their column array on every render, and an array dependency would
  // re-seed the state on every render. A stored choice — including an explicit
  // "show everything" — always wins over the default.
  const defaultsKey = columns
    .filter((column) => column.hideable && column.defaultHidden)
    .map((column) => column.key)
    .join(',');

  const [hidden, setHidden] = useState<string[]>(() => readHidden(storageKey, splitKeys(defaultsKey)));
  const [localSort, setLocalSort] = useState<SortState | null>(null);

  useEffect(() => {
    setHidden(readHidden(storageKey, splitKeys(defaultsKey)));
  }, [storageKey, defaultsKey]);

  // THE SERVER'S SORT WINS WHEN THERE IS ONE. Both states exist so a caller can
  // switch an endpoint between server-sorted and not without changing the page.
  const activeSort = onSortChange ? sort : localSort;

  const visible = useMemo(
    () => columns.filter((column) => !(column.hideable && hidden.includes(column.key))),
    [columns, hidden],
  );
  const hideable = columns.filter((column) => column.hideable);

  const toggleColumn = (key: string) => {
    setHidden((current) => {
      const next = current.includes(key) ? current.filter((item) => item !== key) : [...current, key];
      writeHidden(storageKey, next);
      return next;
    });
  };

  const sortedRows = useMemo(() => {
    if (onSortChange || !activeSort) return rows;
    const column = columns.find((item) => item.key === activeSort.key);
    if (!column?.sortValue) return rows;
    const direction = activeSort.desc ? -1 : 1;
    return [...rows].sort((a, b) => {
      const left = column.sortValue!(a);
      const right = column.sortValue!(b);
      if (left < right) return -direction;
      if (left > right) return direction;
      return 0;
    });
  }, [rows, columns, activeSort, onSortChange]);

  /** Ascending, then descending, then the server's natural order. */
  const nextSort = (column: Column<T>): SortState | null => {
    if (!activeSort || activeSort.key !== column.key) return { key: column.key, desc: false };
    if (!activeSort.desc) return { key: column.key, desc: true };
    return null;
  };

  const handleSort = (column: Column<T>) => {
    const next = nextSort(column);
    if (onSortChange) onSortChange(next);
    else setLocalSort(next);
  };

  const selectable = Boolean(selection);
  const pageKeys = sortedRows.map((row) => rowKey(row));
  const allSelected = selectable && pageKeys.length > 0 && pageKeys.every((key) => selection!.selected.has(key));
  const someSelected = selectable && !allSelected && pageKeys.some((key) => selection!.selected.has(key));

  const toggleAll = () => {
    if (!selection) return;
    const next = new Set(selection.selected);
    if (allSelected) pageKeys.forEach((key) => next.delete(key));
    else pageKeys.forEach((key) => next.add(key));
    selection.onChange(next);
  };

  const toggleRow = (key: string | number) => {
    if (!selection) return;
    const next = new Set(selection.selected);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    selection.onChange(next);
  };

  return (
    // THE WRAPPER CARRIES THE MODULE'S OWN CLASS so the selected-row tint can
    // beat the shared `.og-action-table` background rules without editing the
    // shared stylesheet — see ../styles.css.
    <div className="cmdb-table flex min-w-0 flex-col gap-3">
      {error && (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-3 rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300"
        >
          <span className="min-w-0 flex-1 break-words">{error}</span>
          {onRetry && (
            <Button variant="outline" size="sm" type="button" onClick={onRetry} className="inline-flex items-center gap-1.5">
              <RotateCw size={12} aria-hidden="true" />
              {tr('重试', 'Retry')}
            </Button>
          )}
        </div>
      )}

      {hideable.length > 0 && (
        <div className="flex justify-end">
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="subtle" size="sm" type="button" />}>
              <Columns3 size={12} aria-hidden="true" />
              <span className="ml-1.5">{tr('列', 'Columns')}</span>
            </DropdownMenuTrigger>
            <DropdownMenuContent className="w-52 p-1">
              {/* Plain buttons, not menu items: a visibility toggle must not
                  close the menu, and a menu item closes it by design. */}
              {hideable.map((column) => {
                const shown = !hidden.includes(column.key);
                return (
                  <button
                    key={column.key}
                    type="button"
                    onClick={() => toggleColumn(column.key)}
                    aria-pressed={shown}
                    className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs text-text-muted hover:bg-card hover:text-text focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
                  >
                    <Checkbox checked={shown} tabIndex={-1} aria-hidden="true" />
                    <span className="truncate">{column.header}</span>
                  </button>
                );
              })}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      )}

      {loading && sortedRows.length === 0 ? (
        <div className="flex h-32 items-center justify-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</div>
      ) : sortedRows.length === 0 ? (
        error ? null : <EmptyState title={tr('没有数据', 'Nothing here yet')} hint={typeof empty === 'string' ? empty : undefined} action={typeof empty === 'string' ? undefined : empty} />
      ) : (
        // THE SCROLLER IS THE TABLE'S OWN. A dense CMDB table must scroll inside
        // its container; letting it push the page wide would take the header and
        // the first columns out of reach on a narrow window.
        <div className="min-w-0 overflow-x-auto">
          <table className={cn('og-resource-table', actions && 'og-action-table')}>
            <thead>
              <tr>
                {selectable && (
                  <th scope="col" className="w-px">
                    <Checkbox
                      checked={allSelected}
                      indeterminate={someSelected}
                      onCheckedChange={toggleAll}
                      aria-label={tr('选择本页全部', 'Select every row on this page')}
                    />
                  </th>
                )}
                {visible.map((column) => {
                  const active = activeSort?.key === column.key;
                  const ariaSort = !column.sortable
                    ? undefined
                    : active
                      ? activeSort!.desc
                        ? 'descending'
                        : 'ascending'
                      : 'none';
                  return (
                    <th
                      key={column.key}
                      scope="col"
                      aria-sort={ariaSort}
                      // A WRAPPED HEADER IS AN UNREADABLE HEADER: "继承自" split
                      // over two lines stops being a word. A dense table may
                      // scroll in its own container instead.
                      className={cn('whitespace-nowrap', column.width, column.align === 'right' && 'text-right')}
                    >
                      {column.sortable ? (
                        <button
                          type="button"
                          onClick={() => handleSort(column)}
                          className="inline-flex items-center gap-1 font-medium hover:text-text focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
                        >
                          {column.header}
                          {!active ? (
                            <ChevronsUpDown size={11} className="text-text-faint" aria-hidden="true" />
                          ) : activeSort!.desc ? (
                            <ArrowDown size={11} aria-hidden="true" />
                          ) : (
                            <ArrowUp size={11} aria-hidden="true" />
                          )}
                        </button>
                      ) : (
                        column.header
                      )}
                    </th>
                  );
                })}
                {actions && (
                  // LEFT-ALIGNED AND CONTENT-SIZED: the design language fixes the
                  // operation column's alignment and forbids a guessed width.
                  <th scope="col" className="w-px whitespace-nowrap">
                    {actionsHeader ?? tr('操作', 'Actions')}
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {sortedRows.map((row) => {
                const key = rowKey(row);
                const checked = selection?.selected.has(key) ?? false;
                return (
                  <tr key={key} data-selected={checked || undefined}>
                    {selectable && (
                      <td className="w-px">
                        <Checkbox
                          checked={checked}
                          onCheckedChange={() => toggleRow(key)}
                          aria-label={tr('选择这一行', 'Select this row')}
                        />
                      </td>
                    )}
                    {visible.map((column) => (
                      <td key={column.key} className={cn('min-w-0 align-middle', column.align === 'right' && 'text-right')}>
                        {column.cell(row)}
                      </td>
                    ))}
                    {actions && (
                      <td className="sticky right-0 w-px whitespace-nowrap px-4 py-2 text-left align-middle">
                        <div className="flex items-center gap-1">{actions(row)}</div>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// --- visibility persistence ---

function storageKeyFor(storageKey?: string): string | null {
  return storageKey ? `cmdb.table.${storageKey}.hidden` : null;
}

/**
 * The columns hidden on first sight: the operator's stored choice when there is
 * one, otherwise the columns declared `defaultHidden`.
 *
 * AN EMPTY STORED LIST IS A CHOICE, NOT AN ABSENCE — an operator who switched
 * every column back on has stored `[]`, and falling back to the defaults would
 * hide them again on the next visit.
 */
function readHidden(storageKey?: string, defaults: string[] = []): string[] {
  const key = storageKeyFor(storageKey);
  if (!key) return defaults;
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return defaults;
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string') : defaults;
  } catch {
    return defaults;
  }
}

function splitKeys(joined: string): string[] {
  return joined ? joined.split(',') : [];
}

function writeHidden(storageKey: string | undefined, hidden: string[]): void {
  const key = storageKeyFor(storageKey);
  if (!key) return;
  try {
    localStorage.setItem(key, JSON.stringify(hidden));
  } catch {
    /* localStorage unavailable — the choice applies to this visit only */
  }
}
