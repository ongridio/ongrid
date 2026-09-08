import { useId, useState } from 'react';
import { ChevronDown, Clock } from 'lucide-react';
import { Button, Input, Label } from '@/components/ui';
import { Popover, PopoverContent, PopoverTitle, PopoverTrigger } from '@/components/ui/Popover';
import { localDateTime } from '@/lib/telemetryContext';
import { useI18n } from '@/i18n/locale';

export type TimeRangeSelection = { range: string; start: string; end: string };

type Props = {
  value: { range: string; start?: string; end?: string };
  presets: readonly { value: string; label: string; durationMs: number }[];
  onChange: (value: TimeRangeSelection) => void;
  minDurationMs?: number;
};

export function TimeRangePicker({ value, presets, onChange, minDurationMs = 1000 }: Props) {
  const { tr } = useI18n();
  const id = useId();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState({ start: '', end: '' });
  const start = Date.parse(draft.start);
  const end = Date.parse(draft.end);
  const valid = Number.isFinite(start) && Number.isFinite(end) && end - start >= minDurationMs && end - start <= 7 * 86400000;
  const label = value.range === 'custom'
    ? tr('自定义时间', 'Custom range')
    : presets.find((preset) => preset.value === value.range)?.label || tr('时间范围', 'Time range');
  const windowLabel = value.range === 'custom' && value.start && value.end
    ? `${localDateTime(value.start).replace('T', ' ')} — ${localDateTime(value.end).replace('T', ' ')}`
    : '';
  const changeOpen = (next: boolean) => {
    if (next) {
      const now = Date.now();
      const duration = presets.find((preset) => preset.value === value.range)?.durationMs || 3600000;
      setDraft({
        start: localDateTime(value.start || new Date(now - duration).toISOString()),
        end: localDateTime(value.end || new Date(now).toISOString()),
      });
    }
    setOpen(next);
  };
  return (
    <Popover open={open} onOpenChange={changeOpen}>
      <PopoverTrigger render={<Button aria-label={tr('时间范围', 'Time range')} title={windowLabel || label} className="w-56 max-w-full justify-between" />}>
        <Clock size={13} aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate text-left">{windowLabel ? `${localDateTime(value.start!).slice(5, 16).replace('T', ' ')} — ${localDateTime(value.end!).slice(5, 16).replace('T', ' ')}` : label}</span>
        <ChevronDown size={13} aria-hidden="true" />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[32rem] max-w-[calc(100vw-1rem)] !p-0">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
          <PopoverTitle className="text-sm font-medium text-text">{tr('时间范围', 'Time range')}</PopoverTitle>
          <span className="text-xs text-text-muted">{tr('本地时区', 'Local timezone')} · {Intl.DateTimeFormat().resolvedOptions().timeZone}</span>
        </div>
        <div className="grid sm:grid-cols-[9rem_minmax(0,1fr)]">
          <div className="border-b border-border p-3 sm:border-b-0 sm:border-r">
            <p className="mb-2 text-xs text-text-muted">{tr('快捷范围', 'Quick ranges')}</p>
            <div className="grid grid-cols-2 gap-1 sm:grid-cols-1">
              {presets.map((preset) => (
                <Button key={preset.value} variant={value.range === preset.value ? "outline" : "subtle"} size="sm" aria-pressed={value.range === preset.value} className="justify-start"
                  onClick={() => {
                    const now = Date.now();
                    onChange({ range: preset.value, start: new Date(now - preset.durationMs).toISOString(), end: new Date(now).toISOString() });
                    setOpen(false);
                  }}>
                  {preset.label}
                </Button>
              ))}
            </div>
          </div>
          <form className="min-w-0 space-y-3 p-4" onSubmit={(event) => {
            event.preventDefault();
            event.stopPropagation();
            if (!valid) return;
            onChange({ range: 'custom', start: new Date(start).toISOString(), end: new Date(end).toISOString() });
            setOpen(false);
          }}>
            <p className="text-sm font-medium text-text">{tr('自定义范围', 'Custom range')}</p>
            {(['start', 'end'] as const).map((key) => (
              <div key={key} className="space-y-1.5">
                <Label htmlFor={`${id}-${key}`}>{key === 'start' ? tr('开始时间', 'Start time') : tr('结束时间', 'End time')}</Label>
                <Input id={`${id}-${key}`} type="datetime-local" step="1" value={draft[key]} required aria-invalid={!valid} aria-describedby={!valid ? `${id}-error` : undefined}
                  onChange={(event) => setDraft((current) => ({ ...current, [key]: event.target.value }))} className="w-full min-w-0" />
              </div>
            ))}
            {!valid && <p id={`${id}-error`} role="alert" className="text-xs text-red-500">{minDurationMs >= 60000
              ? tr('请选择有效的起止时间，范围为 1 分钟至 7 天。', 'Choose a valid window between 1 minute and 7 days.')
              : tr('结束时间须晚于开始时间，范围不超过 7 天。', 'End must be after start, within a 7-day window.')}</p>}
            <div className="flex justify-end gap-2 pt-1">
              <Button onClick={() => setOpen(false)}>{tr('取消', 'Cancel')}</Button>
              <Button type="submit" variant="primary" disabled={!valid}>{tr('应用', 'Apply')}</Button>
            </div>
          </form>
        </div>
      </PopoverContent>
    </Popover>
  );
}
