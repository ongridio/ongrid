import { Input } from '@/components/ui';
import { Switch } from '@/components/ui/Switch';
import { Textarea } from '@/components/ui/Textarea';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';
import {
  displayValue,
  inputTypeFor,
  toControlValue,
  toWireValue,
  type ValueTypeCode,
} from '../lib/valuetype';

// One attribute's control, chosen by its stored value type.
//
// THIS IS THE EAV ENGINE'S OTHER HALF IN THE UI. A model's attributes are data,
// so the form that edits an instance cannot be written by hand — it is generated
// from the attribute list, and every control here corresponds to one of the eight
// value types the backend accepts. A page that hand-built a form for one model
// would be a page that breaks the moment an operator adds a field.
//
// THE NATIVE INPUT TYPE IS KEPT. Date, time and number controls use the
// browser's own picker and constraints rather than a custom widget; the design
// language asks for exactly that. The conversion between the browser's format and
// the stored one lives in lib/valuetype.ts, in one place, because it is the one
// thing a hand-written page would get wrong for every field independently.

type Props = {
  /** The field's DOM id; the `Label` on the page points at it. */
  id: string;
  type: ValueTypeCode;
  value: unknown;
  onChange(next: unknown): void;

  /** `is_list` — the stored value is several rows, edited as several inputs. */
  isList?: boolean;
  disabled?: boolean;
  required?: boolean;
  placeholder?: string;
  className?: string;
  /** An accessible name, for a control whose page shows no visible `Label`. */
  'aria-label'?: string;
  'aria-describedby'?: string;
  'aria-invalid'?: boolean;
};

export function AttributeControl({
  id,
  type,
  value,
  onChange,
  isList = false,
  disabled = false,
  required = false,
  placeholder,
  className,
  ...aria
}: Props) {
  const { tr } = useI18n();

  if (isList) {
    return (
      <ListControl
        id={id}
        type={type}
        value={value}
        onChange={onChange}
        disabled={disabled}
        placeholder={placeholder}
        className={className}
        {...aria}
      />
    );
  }

  if (type === '7') {
    const checked = value === true || value === 'true' || value === 1 || value === '1';
    return (
      <div className={cn('flex h-9 items-center', className)}>
        <Switch
          checked={checked}
          disabled={disabled}
          onCheckedChange={(next) => onChange(next)}
          aria-label={aria['aria-label'] ?? placeholder ?? tr('布尔值', 'Boolean value')}
          {...aria}
        />
      </div>
    );
  }

  if (type === '6') {
    return (
      <Textarea
        id={id}
        rows={4}
        className={cn('font-mono', className)}
        value={toControlValue(type, value)}
        disabled={disabled}
        placeholder={placeholder}
        onChange={(event) => onChange(toWireValue(type, event.target.value))}
        {...aria}
      />
    );
  }

  const inputType = inputTypeFor(type);
  if (inputType === 'checkbox') return null;

  return (
    <Input
      id={id}
      type={inputType}
      value={toControlValue(type, value)}
      disabled={disabled}
      required={required}
      placeholder={placeholder}
      // A NUMBER INPUT IS STILL A STRING until `toWireValue` reads it; the
      // backend validates the type and its own message is what the operator
      // needs, so no client-side coercion is invented here.
      step={type === '1' ? 'any' : undefined}
      className={className}
      onChange={(event) => onChange(toWireValue(type, event.target.value))}
      {...aria}
    />
  );
}

// --- the multi-value editor ---
//
// `is_list` IS MANY ROWS IN ONE VALUE TABLE, not a JSON array, and the form has to
// let an operator add and remove them one at a time. A comma-separated text field
// would be smaller, but it makes a value containing a comma unrepresentable —
// which is exactly the value an operator cannot then enter.

function ListControl({
  id,
  type,
  value,
  onChange,
  disabled,
  placeholder,
  className,
  ...aria
}: Props) {
  const { tr } = useI18n();
  const items = Array.isArray(value) ? value : value === null || value === undefined || value === '' ? [] : [value];

  const replace = (index: number, next: unknown) => {
    const copy = [...items];
    copy[index] = next;
    onChange(copy);
  };
  const remove = (index: number) => {
    onChange(items.filter((_, position) => position !== index));
  };
  const add = () => {
    onChange([...items, null]);
  };

  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      {items.map((item, index) => (
        <div key={index} className="flex items-center gap-1.5">
          <AttributeControl
            id={index === 0 ? id : `${id}-${index}`}
            type={type}
            value={item}
            onChange={(next) => replace(index, next)}
            disabled={disabled}
            placeholder={placeholder}
            className="flex-1"
            // EACH INPUT NEEDS ITS OWN NAME: a `Label htmlFor` can only point at
            // one of them, so the rest are numbered for a screen reader.
            aria-label={aria['aria-label'] ? `${aria['aria-label']} ${index + 1}` : undefined}
            aria-describedby={index === 0 ? aria['aria-describedby'] : undefined}
            aria-invalid={index === 0 ? aria['aria-invalid'] : undefined}
          />
          <button
            type="button"
            onClick={() => remove(index)}
            disabled={disabled}
            aria-label={tr('删除这个值', 'Remove this value')}
            className="shrink-0 rounded px-1.5 py-1 text-xs text-text-faint hover:text-red-400 disabled:cursor-not-allowed disabled:opacity-50"
          >
            ×
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={add}
        disabled={disabled}
        className="self-start rounded px-1 py-0.5 text-xs text-indigo-400 hover:text-indigo-300 disabled:cursor-not-allowed disabled:opacity-50"
      >
        + {tr('添加一个值', 'Add a value')}
      </button>
      {items.length > 1 && (
        <span className="text-[10px] text-text-faint">
          {tr(`共 ${items.length} 个值`, `${items.length} values`)}
        </span>
      )}
    </div>
  );
}

// --- read-only rendering ---
//
// The same vocabulary on the display side, so a list cell and a form field cannot
// disagree about how a value looks. Used by every table column that shows an
// attribute.

export function AttributeValue({ type, value }: { type: ValueTypeCode; value: unknown }) {
  const { tr } = useI18n();
  if (value === null || value === undefined || value === '') {
    return <span className="text-text-faint">—</span>;
  }
  if (type === '7') {
    const on = value === true || value === 'true' || value === 1 || value === '1';
    return <span className={on ? 'text-emerald-400' : 'text-text-muted'}>{on ? tr('是', 'Yes') : tr('否', 'No')}</span>;
  }
  return <span className="min-w-0 break-words">{displayValue(value)}</span>;
}
