import { RotateCcw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';
import type { User } from '@/api/users';

import type { CIType } from '../../api/citype';

// The history filter bar.
//
// THE TIME FIELDS ARE `datetime-local`, not free text: the backend accepts two
// formats, and a person should not have to remember either. The value is
// converted to the API's `YYYY-MM-DD HH:MM:SS` at the edge.

export type FilterState = {
  start: string;
  end: string;
  uid: string;
  typeID: string;
  operate: string;
};

export const EMPTY_FILTERS: FilterState = { start: '', end: '', uid: '', typeID: '', operate: '' };

export type OperateOption = { value: string; label: string };

export function HistoryFilters({
  value,
  models,
  users,
  operateOptions,
  onChange,
  onReset,
}: {
  value: FilterState;
  models: CIType[];
  users: User[];
  operateOptions: OperateOption[];
  onChange(next: FilterState): void;
  onReset(): void;
}) {
  const { tr } = useI18n();
  const update = (patch: Partial<FilterState>) => onChange({ ...value, ...patch });

  return (
    <Card>
      <div className="flex flex-wrap items-end gap-3 p-3">
        <div>
          <Label htmlFor="history-start">{tr('开始时间', 'From')}</Label>
          <Input
            id="history-start"
            type="datetime-local"
            value={value.start}
            onChange={(event) => update({ start: event.target.value })}
            className="mt-1 w-52"
          />
        </div>
        <div>
          <Label htmlFor="history-end">{tr('结束时间', 'To')}</Label>
          <Input
            id="history-end"
            type="datetime-local"
            value={value.end}
            onChange={(event) => update({ end: event.target.value })}
            className="mt-1 w-52"
          />
        </div>
        <div>
          <Select
            label={tr('用户', 'User')}
            value={value.uid}
            onValueChange={(uid) => update({ uid })}
            options={[
              { value: '', label: tr('全部用户', 'Every user') },
              ...users.map((user) => ({
                value: String(user.id),
                label: user.display_name || user.email || `#${user.id}`,
              })),
            ]}
            searchable
            className="w-48"
          />
        </div>
        <div>
          <Select
            label={tr('模型', 'Model')}
            value={value.typeID}
            onValueChange={(typeID) => update({ typeID })}
            options={[
              { value: '', label: tr('全部模型', 'Every model') },
              ...models.map((model) => ({ value: String(model.id), label: model.alias || model.name })),
            ]}
            searchable
            className="w-48"
          />
        </div>
        <div>
          <Select
            label={tr('操作', 'Action')}
            value={value.operate}
            onValueChange={(operate) => update({ operate })}
            options={[{ value: '', label: tr('全部操作', 'Every action') }, ...operateOptions]}
            className="w-44"
          />
        </div>
        <Button
          variant="outline"
          size="sm"
          type="button"
          onClick={onReset}
          className="inline-flex items-center gap-1.5"
        >
          <RotateCcw size={12} aria-hidden="true" />
          {tr('重置', 'Reset')}
        </Button>
      </div>
    </Card>
  );
}

/**
 * Converts a `datetime-local` value to an RFC 3339 instant.
 *
 * THE PICKER IS A WALL CLOCK WITH NO ZONE, and the server compares instants.
 * `new Date('2026-10-26T00:00')` reads that wall clock in the browser's zone,
 * and toISOString() states which instant that is; sending the bare wall clock
 * made a UTC+8 operator filter the UTC day instead of their own.
 */
export function toAPITime(value: string): string | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  const parsed = new Date(trimmed);
  if (Number.isNaN(parsed.getTime())) return undefined;
  return parsed.toISOString();
}
