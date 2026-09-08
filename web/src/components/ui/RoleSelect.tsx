import { Label } from './Label';
import { FilterField } from './FilterField';
// RoleSelect — single dropdown reused across Monitor / Edges / Logs /
// Traces for "filter by device role". The contract is intentionally
// shaped around the EdgeRole enum + two synthetic options:
//   - ''        → all roles
//   - 'unknown' → devices whose roles array is empty (未分类)
// Callers translate '' to "no filter" and 'unknown' to their own bucket
// logic; no role string overlaps with EdgeRole values, so a string-typed
// value is safe.
import { EDGE_ROLES, EDGE_ROLE_LABELS, EDGE_ROLE_LABELS_EN } from '@/api/edges';
import { useI18n } from '@/i18n/locale';
import { Select } from './Select';
import { cn } from '@/lib/cn';

export type RoleFilterValue = '' | 'unknown' | (typeof EDGE_ROLES)[number];

// `chip` (default): caption + transparent select inside one shared frame. Used in
//                   toolbars (Monitor) where horizontal density matters.
// `block`         : caption above, full-width select below. Used in
//                   form-column layouts (Logs query form) where the
//                   column itself supplies the spacing.
type Variant = 'chip' | 'block';

export function RoleSelect({
  value,
  onChange,
  className,
  showLabel = true,
  variant = 'chip',
  omitUnknown = false,
}: {
  value: RoleFilterValue;
  onChange(value: RoleFilterValue): void;
  className?: string;
  showLabel?: boolean;
  variant?: Variant;
  // Drop the 未分类 entry — appropriate for callers like Logs/Traces
  // whose downstream query path can't represent "no role" sensibly
  // (the data source itself is keyed by edge, not by role bitmap).
  omitUnknown?: boolean;
}) {
  const { tr } = useI18n();
  const BASE_OPTIONS: { value: RoleFilterValue; label: string }[] = [
    { value: '', label: tr('所有角色', 'All roles') },
    ...EDGE_ROLES.map((r) => ({ value: r as RoleFilterValue, label: tr(EDGE_ROLE_LABELS[r], EDGE_ROLE_LABELS_EN[r]) })),
  ];
  const UNKNOWN_OPTION = { value: 'unknown' as RoleFilterValue, label: tr('未分类', 'Uncategorized') };
  const options = omitUnknown ? BASE_OPTIONS : [...BASE_OPTIONS, UNKNOWN_OPTION];
  const control = <Select
    value={value}
    onValueChange={(next) => onChange(next as RoleFilterValue)}
    options={options}
    label={tr('角色', 'Role')}
    className={variant === 'chip' ? 'min-w-[112px] flex-1' : 'w-full'}
  />;
  if (variant === 'chip' && showLabel) {
    return <FilterField label={tr('角色', 'Role')} className={className}>{control}</FilterField>;
  }
  return <Label className={cn('block', className)}>
    {showLabel && <span className="mb-1 block text-[11px] text-text-faint">{tr('角色', 'Role')}</span>}
    {control}
  </Label>;
}
