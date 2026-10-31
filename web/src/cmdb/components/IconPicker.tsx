import { useMemo, useState } from 'react';
import { Check, Eraser, Search } from 'lucide-react';

import { Button } from '@/components/ui';
import { Input } from '@/components/ui/Input';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/Popover';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import { CMDBIcon, preloadCMDBIcons, useCMDBIcons } from './CMDBIcon';
import { formatIconValue, isValidColor, parseIconValue } from '../lib/iconvalue';

// Choosing a model's icon.
//
// THE VALUE IS A STRING IN THE DATA, so this is a picker over a vocabulary that
// ships with the product — not a component choice. It writes the same
// `type$$color$$isCustom$$url` value the backend stores and the rest of the
// console reads, so a model configured here looks the same in cantor.
//
// THE GRID IS SEARCHABLE AND VIRTUAL-ISH: 727 glyphs render as a scrollable box
// of a bounded height rather than pushing the dialog off the screen. Filtering
// by name is what makes that usable — the names are English-ish slugs whose first
// letters are usually a prefix of what the operator remembers.

type Props = {
  /** The stored value, or '' for none. */
  value: string;
  onChange(next: string): void;
  disabled?: boolean;
  /** The accessible name for the trigger. */
  label: string;
};

export function IconPicker({ value, onChange, disabled, label }: Props) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const parsed = parseIconValue(value);
  const glyphs = useCMDBIcons();

  // The sprite is fetched on first open, so a page that never touches an icon
  // never pays for it.
  const names = useMemo(() => {
    if (!glyphs) return [];
    const q = query.trim().toLowerCase();
    const all = Object.keys(glyphs).sort();
    return q ? all.filter((name) => name.toLowerCase().includes(q)) : all;
  }, [glyphs, query]);

  const pick = (name: string) => {
    onChange(formatIconValue({ ...parsed, type: name, custom: false, url: '' }));
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={(next) => { setOpen(next); if (next) void preloadCMDBIcons(); }}>
      <PopoverTrigger
        render={
          <Button variant="outline" type="button" disabled={disabled} className="inline-flex items-center gap-2" />
        }
      >
        <CMDBIcon value={value} size={16} label={label} />
        <span className="text-xs text-text-muted">
          {parsed.type || (parsed.custom && parsed.url ? tr('自定义图片', 'Custom image') : tr('选择图标', 'Pick an icon'))}
        </span>
      </PopoverTrigger>
      <PopoverContent className="w-[min(28rem,calc(100vw-2rem))] p-3">
        <div className="mb-2 flex items-center gap-2">
          <Search size={12} className="shrink-0 text-text-faint" aria-hidden="true" />
          <Input
            type="search"
            aria-label={tr('搜索图标', 'Search icons')}
            placeholder={tr('按名称搜索', 'Search by name')}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="flex-1"
          />
          <Button
            variant="subtle"
            size="sm"
            type="button"
            onClick={() => {
              onChange('');
              setOpen(false);
            }}
            className="inline-flex items-center gap-1"
          >
            <Eraser size={12} aria-hidden="true" />
            {tr('清除', 'Clear')}
          </Button>
        </div>

        <div className="mb-2 flex items-center gap-2">
          <span className="text-[11px] text-text-faint">{tr('颜色覆盖', 'Colour override')}</span>
          <Input
            aria-label={tr('图标颜色', 'Icon colour')}
            value={parsed.color}
            placeholder="#6366f1"
            onChange={(event) => {
              const next = event.target.value.trim();
              onChange(formatIconValue({ ...parsed, color: isValidColor(next) ? next : '' }));
            }}
            className="w-28 font-mono"
          />
          {parsed.color && (
            <span
              aria-hidden="true"
              className="h-4 w-4 rounded border border-border"
              style={{ backgroundColor: parsed.color }}
            />
          )}
          <span className="ml-auto text-[11px] text-text-faint">
            {glyphs ? tr(`${names.length} 个图标`, `${names.length} icons`) : tr('加载中…', 'Loading…')}
          </span>
        </div>

        <div
          role="listbox"
          aria-label={tr('可用图标', 'Available icons')}
          className="grid max-h-64 grid-cols-[repeat(auto-fill,minmax(2.25rem,1fr))] gap-1 overflow-y-auto"
        >
          {names.map((name) => {
            const active = parsed.type === name;
            return (
              <button
                key={name}
                type="button"
                role="option"
                aria-selected={active}
                aria-label={name}
                title={name}
                onClick={() => pick(name)}
                className={cn(
                  'relative flex h-9 items-center justify-center rounded border',
                  active
                    ? 'border-indigo-500 bg-indigo-500/10'
                    : 'border-transparent hover:border-border hover:bg-card',
                )}
              >
                <CMDBIcon value={`${name}$$`} size={18} label={name} />
                {active && <Check size={10} className="absolute bottom-0.5 right-0.5 text-indigo-400" aria-hidden="true" />}
              </button>
            );
          })}
        </div>
        {glyphs && names.length === 0 && (
          <p className="py-4 text-center text-xs text-text-faint">
            {tr('没有匹配的图标', 'No icon matches that')}
          </p>
        )}
      </PopoverContent>
    </Popover>
  );
}
