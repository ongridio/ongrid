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
    <label className="relative block min-w-40 flex-1">
      <Search size={14} className="pointer-events-none absolute left-3 top-2.5 text-zinc-500" />
      <input
        aria-label={label}
        placeholder={label}
        autoFocus={autoFocus}
        className="h-9 w-full rounded-md border border-zinc-800 bg-zinc-950 pl-9 pr-3 text-sm text-zinc-100"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') onChange(draft);
        }}
      />
    </label>
  );
}
