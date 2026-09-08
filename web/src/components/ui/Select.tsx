// Adapted from shadcn/ui's Base UI Select and Combobox (MIT).
// Keep Ongrid's theme tokens and Tailwind 3; see shadcn.LICENSE.
import { Children, Fragment, isValidElement, type ReactNode } from 'react';
import { Select as SelectPrimitive } from '@base-ui/react/select';
import { Combobox } from '@base-ui/react/combobox';
import { Check, ChevronDown, Search } from 'lucide-react';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

type Option = { value: string; label: string; disabled?: boolean; icon?: ReactNode };

type Props = {
  value: string | number;
  onValueChange(value: string): void;
  options?: Option[];
  children?: ReactNode;
  label?: string;
  id?: string;
  name?: string;
  required?: boolean;
  'aria-label'?: string;
  'aria-labelledby'?: string;
  disabled?: boolean;
  searchable?: boolean;
  open?: boolean;
  onOpenChange?(open: boolean): void;
  className?: string;
  variant?: 'default' | 'ghost';
};

// Existing forms can keep their dynamic <option> children while using the same
// accessible Select/Combobox implementation as options-based callers.
function optionText(children: ReactNode): string {
  return Children.toArray(children).map((child) => isValidElement<{ children?: ReactNode }>(child)
    ? optionText(child.props.children) : String(child)).join('');
}
function collectOptions(children: ReactNode): Option[] {
  return Children.toArray(children).flatMap((child): Option[] => {
    if (!isValidElement<{ children?: ReactNode; value?: string | number; disabled?: boolean }>(child)) return [];
    if (child.type === Fragment) return collectOptions(child.props.children);
    if (child.type !== 'option') return [];
    const label = optionText(child.props.children);
    return [{ value: String(child.props.value ?? label), label, disabled: child.props.disabled }];
  });
}

export function Select({ value: rawValue, onValueChange, options: suppliedOptions, children, label, disabled, searchable, className, variant = 'default', id, name, required, 'aria-label': ariaLabel, 'aria-labelledby': labelledBy, open, onOpenChange }: Props) {
  const value = String(rawValue);
  const options = suppliedOptions ?? collectOptions(children);
  const accessibleLabel = label ?? ariaLabel;
  const { tr } = useI18n();
  const selected = options.find((option) => option.value === value);
  if (searchable ?? options.length > 10) {
    return (
      <Combobox.Root
        open={open}
        onOpenChange={onOpenChange}
        id={id}
        name={name}
        required={required}
        items={options}
        value={selected ?? null}
        onValueChange={(option) => { if (option) onValueChange(option.value); }}
        isItemEqualToValue={(a, b) => a.value === b.value}
        disabled={disabled}
        autoHighlight
      >
        <Combobox.Trigger aria-label={accessibleLabel} aria-labelledby={labelledBy} data-variant={variant} className={cn('og-select-trigger', className)}>
          {selected?.icon}<span className="min-w-0 flex-1 truncate text-left">{selected?.label ?? value}</span>
          <ChevronDown size={14} className="shrink-0 text-text-faint" aria-hidden="true" />
        </Combobox.Trigger>
        <Combobox.Portal>
          <Combobox.Positioner sideOffset={4} align="start" className="og-select-positioner">
            <Combobox.Popup className="og-select-popup" aria-label={accessibleLabel ?? tr('选项', 'Options')}>
              <div className="og-select-search">
                <Search size={14} className="shrink-0 text-text-faint" aria-hidden="true" />
                <Combobox.Input aria-label={tr(`搜索 ${accessibleLabel ?? ''}`, `Search ${accessibleLabel ?? ''}`)} placeholder={tr('搜索选项…', 'Search options…')} />
              </div>
              <Combobox.Empty><div className="px-3 py-5 text-center text-xs text-text-muted">{tr('没有匹配项', 'No results found')}</div></Combobox.Empty>
              <Combobox.List className="og-select-list">
                {(option: Option) => (
                  <Combobox.Item key={option.value} value={option} disabled={option.disabled} className="og-select-item">
                    {option.icon}<span className="min-w-0 flex-1 whitespace-normal break-words">{option.label}</span>
                    <Combobox.ItemIndicator className="absolute right-2"><Check size={14} aria-hidden="true" /></Combobox.ItemIndicator>
                  </Combobox.Item>
                )}
              </Combobox.List>
            </Combobox.Popup>
          </Combobox.Positioner>
        </Combobox.Portal>
      </Combobox.Root>
    );
  }
  return (
    <SelectPrimitive.Root open={open} onOpenChange={onOpenChange} id={id} name={name} required={required} value={value} onValueChange={(next) => { if (next !== null) onValueChange(next); }} items={options} disabled={disabled}>
      <SelectPrimitive.Trigger aria-label={accessibleLabel} aria-labelledby={labelledBy} data-variant={variant} className={cn('og-select-trigger', className)}>
        {selected?.icon}<SelectPrimitive.Value className="min-w-0 flex-1 truncate text-left" />
        <SelectPrimitive.Icon><ChevronDown size={14} className="text-text-faint" aria-hidden="true" /></SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Positioner sideOffset={4} align="start" alignItemWithTrigger={false} className="og-select-positioner">
          <SelectPrimitive.Popup className="og-select-popup">
            <SelectPrimitive.List className="og-select-list" aria-label={accessibleLabel ?? tr('选项', 'Options')}>
              {options.map((option) => (
                <SelectPrimitive.Item key={option.value} value={option.value} disabled={option.disabled} className="og-select-item">
                  {option.icon}<SelectPrimitive.ItemText className="min-w-0 flex-1 whitespace-normal break-words">{option.label}</SelectPrimitive.ItemText>
                  <SelectPrimitive.ItemIndicator className="absolute right-2"><Check size={14} aria-hidden="true" /></SelectPrimitive.ItemIndicator>
                </SelectPrimitive.Item>
              ))}
            </SelectPrimitive.List>
          </SelectPrimitive.Popup>
        </SelectPrimitive.Positioner>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
}
