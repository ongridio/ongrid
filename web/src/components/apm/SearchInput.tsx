import { Label, Input } from '@/components/ui';
import { useEffect, useState } from 'react';
import { Search } from 'lucide-react';

export function SearchInput({
  value,
  onChange,
  label,
  autoFocus = false,
}: {
  value: string;
  onChange: (value: string) => void;
  label: string;
  autoFocus?: boolean;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  useEffect(() => {
    if (draft === value) return;
    const timer = setTimeout(() => onChange(draft), 300);
    return () => clearTimeout(timer);
  }, [draft, value, onChange]);
  return (
    <Label className="relative block min-w-40 flex-1">
      <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-zinc-500" />
      <Input
        aria-label={label}
        placeholder={label}
        autoFocus={autoFocus}
        className="w-full pl-9 pr-3"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') onChange(draft);
        }}
      />
    </Label>
  );
}
