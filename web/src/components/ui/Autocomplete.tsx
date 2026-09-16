import type { InputHTMLAttributes } from 'react';
import { Autocomplete as Primitive } from '@base-ui/react/autocomplete';
import { ChevronDown } from 'lucide-react';
import { Input } from './Input';
import { Button } from './Button';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

type Props = Omit<InputHTMLAttributes<HTMLInputElement>, 'value' | 'defaultValue' | 'onChange' | 'list' | 'size'> & {
  value: string;
  options: string[];
  onValueChange(value: string): void;
};

// Free text with suggestions, using the same controls and popup styles as Select.
export function Autocomplete({ value, options, onValueChange, className, disabled, ...props }: Props) {
  const { tr } = useI18n();
  return <Primitive.Root items={options} value={value} onValueChange={onValueChange} disabled={disabled} openOnInputClick>
    <Primitive.InputGroup className={cn('relative w-full', className)}>
      <Primitive.Input render={<Input />} {...props} className="pr-9" />
      <Primitive.Trigger render={<Button variant="subtle" size="icon" />} className="absolute right-0 top-0" aria-label={tr(`展开${props['aria-label'] ?? ''}选项`, `Show ${props['aria-label'] ?? ''} options`)}>
        <ChevronDown size={14} aria-hidden="true" className="text-text-faint" />
      </Primitive.Trigger>
    </Primitive.InputGroup>
    <Primitive.Portal>
      <Primitive.Positioner sideOffset={4} align="start" className="og-select-positioner">
        <Primitive.Popup className="og-select-popup">
          <Primitive.Empty><div className="px-3 py-4 text-xs text-text-muted">{tr('没有匹配项，可直接输入新值。', 'No matches. You can type a new value.')}</div></Primitive.Empty>
          <Primitive.List className="og-select-list">
            {(option: string) => <Primitive.Item key={option} value={option} className="og-select-item"><span className="min-w-0 whitespace-normal break-words">{option}</span></Primitive.Item>}
          </Primitive.List>
        </Primitive.Popup>
      </Primitive.Positioner>
    </Primitive.Portal>
  </Primitive.Root>;
}
