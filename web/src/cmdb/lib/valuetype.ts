// The EAV value-type vocabulary, mirrored from the backend.
//
// THE CODES ARE THE CONTRACT. `c_attributes.value_type` holds the single
// characters below, and every control, comparison and formatter in the CMDB is
// chosen by one of them — so this table is the console's half of a decision the
// backend already made, and the two must agree. The backend derives the physical
// value table from the same codes (see internal/cmdb/model/value), including the
// rule that INT/FLOAT/DATE/DATETIME/TIME/BOOL are always indexed, JSON is never,
// and only TEXT may be either.
//
// The labels are the operator-facing names. They are deliberately the same words
// cantor used, because an operator migrating an existing CMDB should not have to
// relearn what "布尔" means.

export type ValueTypeCode = '0' | '1' | '2' | '3' | '4' | '5' | '6' | '7';

const CODES: readonly ValueTypeCode[] = ['0', '1', '2', '3', '4', '5', '6', '7'];

export function isValueTypeCode(raw: string): raw is ValueTypeCode {
  return (CODES as readonly string[]).includes(raw);
}

/** [Chinese, English] — resolved through the caller's `tr`. */
export const VALUE_TYPE_LABELS: Record<ValueTypeCode, [string, string]> = {
  '0': ['整数', 'Integer'],
  '1': ['小数', 'Float'],
  '2': ['文本', 'Text'],
  '3': ['日期时间', 'Date & time'],
  '4': ['日期', 'Date'],
  '5': ['时间', 'Time'],
  '6': ['JSON', 'JSON'],
  '7': ['布尔', 'Boolean'],
};

/** The types a filter may compare numerically. */
export function isNumeric(type: ValueTypeCode): boolean {
  return type === '0' || type === '1';
}

/** The types stored as a point in time or a calendar day. */
export function isTemporal(type: ValueTypeCode): boolean {
  return type === '3' || type === '4' || type === '5';
}

/** JSON is not matchable: the backend refuses a filter on it. */
export function isMatchable(type: ValueTypeCode): boolean {
  return type !== '6';
}

/**
 * The native input `type` a control should use for a value type.
 *
 * IT IS THE NATIVE ONE ON PURPOSE. The project's rule is that inputs keep the
 * browser's own validation, constraints and pickers rather than growing a custom
 * date widget — see docs/design/frontend-design-language.md. `datetime-local`
 * is the browser's format ("2026-10-17T09:30"), not the storage format; the
 * conversion happens in `toWireValue`.
 */
export function inputTypeFor(type: ValueTypeCode): 'text' | 'number' | 'date' | 'datetime-local' | 'time' | 'checkbox' {
  switch (type) {
    case '0':
    case '1':
      return 'number';
    case '3':
      return 'datetime-local';
    case '4':
      return 'date';
    case '5':
      return 'time';
    case '7':
      return 'checkbox';
    default:
      return 'text';
  }
}

/**
 * Converts a control's string value into the value the API expects.
 *
 * AN EMPTY CONTROL IS `null`, NOT `''`. The distinction is the whole point of
 * the value layer: the backend stores no row for a missing value and an empty
 * string is a value it can match, so sending `''` for a cleared field would
 * create data where the operator cleared it. `null` means "no value".
 */
export function toWireValue(type: ValueTypeCode, raw: string): unknown {
  if (raw === '') return null;
  switch (type) {
    case '0': {
      const n = Number(raw);
      return Number.isFinite(n) ? Math.trunc(n) : raw;
    }
    case '1': {
      const n = Number(raw);
      return Number.isFinite(n) ? n : raw;
    }
    case '7':
      return raw === 'true' || raw === '1';
    case '3':
      // `datetime-local` gives "2026-10-17T09:30"; the backend's DATETIME rule
      // accepts a space and rejects the `T`/`Z` dressings only by normalising
      // them, so send the normalised form and let the value layer validate it.
      return normaliseDateTime(raw);
    default:
      return raw;
  }
}

/** Converts a stored value back into the string a control displays. */
export function toControlValue(type: ValueTypeCode, value: unknown): string {
  // A BOOLEAN HAS NO EMPTY STATE. A null boolean is "not set", which every
  // control shows as off — returning '' would make a switch render as
  // unchecked-by-accident and as an empty string in any other consumer.
  if (type === '7') {
    return value === true || value === 'true' || value === 1 || value === '1' ? 'true' : 'false';
  }
  if (value === null || value === undefined) return '';
  switch (type) {
    case '3': {
      const text = String(value).replace(' ', 'T');
      // `datetime-local` wants at most minutes.
      return text.length > 16 ? text.slice(0, 16) : text;
    }
    default:
      return String(value);
  }
}

function normaliseDateTime(raw: string): string {
  return raw.replace('T', ' ').replace(/Z$/, '');
}

/**
 * Renders a stored value for a table cell or a detail row.
 *
 * AN ARRAY IS JOINED, because `is_list` is many rows in one value table and the
 * console shows them as one cell — the same reading that makes an `IN` filter a
 * membership test rather than a cell comparison.
 */
export function displayValue(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (Array.isArray(value)) return value.map((item) => displayValue(item)).join(', ');
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value);
    } catch {
      return String(value);
    }
  }
  return String(value);
}

/** True when a stored value is "no value" rather than an empty one. */
export function isEmptyValue(value: unknown): boolean {
  if (value === null || value === undefined) return true;
  if (Array.isArray(value)) return value.length === 0;
  return value === '';
}
