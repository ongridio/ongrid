import { Button } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { Check, Gauge, Globe, Monitor, Moon, Palette, Sun } from 'lucide-react';
import { ACCENT_PRESETS, useTheme } from '@/store/theme';
import { useI18n } from '@/i18n/locale';
import { useThemeMode, type ThemePreference } from '@/store/mode';
import { cn } from '@/lib/cn';

const THEME_OPTIONS: { id: ThemePreference; icon: typeof Sun }[] = [
  { id: 'system', icon: Monitor },
  { id: 'light',  icon: Sun },
  { id: 'dark',   icon: Moon },
];

export default function SettingsPreferences() {
  const accentId = useTheme((s) => s.accentId);
  const setAccent = useTheme((s) => s.setAccent);
  const { tr, locale, setLocale } = useI18n();
  const { preference: themePref, setPreference: setTheme } = useThemeMode();
  return (
    <div className="space-y-4">
      {/* Language */}
      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-5">
        <div className="mb-3 flex items-center gap-2">
          <Globe size={14} className="text-zinc-400" />
          <h2 className="text-sm font-medium text-zinc-100">{tr('语言', 'Language')}</h2>
        </div>
        <p className="mb-3 text-xs text-zinc-500">
          {tr(
            '切换界面语言。当前以高频路径覆盖为主，其余页面持续中文。',
            'Switch the UI language. High-traffic surfaces are translated first; the rest of the app stays in Chinese for now.',
          )}
        </p>
        <div className="flex gap-2">
          {(['zh-CN', 'en-US'] as const).map((l) => {
            const active = l === locale;
            return (
              <Button variant="outline" size="sm"
                key={l}
                type="button"
                onClick={() => setLocale(l)}
                className={cn(
                  'flex items-center gap-2 rounded-lg border px-3 py-1.5 text-xs transition',
                  active
                    ? 'border-zinc-600 bg-zinc-800/60 text-zinc-100'
                    : 'border-zinc-800 text-zinc-400 hover:border-zinc-700 hover:bg-zinc-800/40 hover:text-zinc-200',
                )}
              >
                <span className="font-medium">{l === 'zh-CN' ? '中文' : 'English'}</span>
                {active && <Check size={12} className="text-zinc-300" />}
              </Button>
            );
          })}
        </div>
      </section>

      {/* Theme: system / light / dark */}
      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-5">
        <div className="mb-3 flex items-center gap-2">
          <Sun size={14} className="text-zinc-400" />
          <h2 className="text-sm font-medium text-zinc-100">{tr('主题', 'Theme')}</h2>
        </div>
        <p className="mb-3 text-xs text-zinc-500">
          {tr(
            '跟随系统、浅色、深色三档。浅色支持仍在迁移中，部分页面会看到深色 token 残留。',
            'System, light, dark. Light mode is in progress; some pages still show dark tokens.',
          )}
        </p>
        <div className="flex gap-2">
          {THEME_OPTIONS.map(({ id, icon: Icon }) => {
            const active = id === themePref;
            const label =
              id === 'system'
                ? tr('跟随系统', 'System')
                : id === 'light'
                  ? tr('浅色', 'Light')
                  : tr('深色', 'Dark');
            return (
              <Button variant="outline" size="sm"
                key={id}
                type="button"
                onClick={() => setTheme(id)}
                className={cn(
                  'flex items-center gap-2 rounded-lg border px-3 py-1.5 text-xs transition',
                  active
                    ? 'border-zinc-600 bg-zinc-800/60 text-zinc-100'
                    : 'border-zinc-800 text-zinc-400 hover:border-zinc-700 hover:bg-zinc-800/40 hover:text-zinc-200',
                )}
              >
                <Icon size={12} />
                <span className="font-medium">{label}</span>
                {active && <Check size={12} className="text-zinc-300" />}
              </Button>
            );
          })}
        </div>
      </section>

      {/* Accent color */}
      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-5">
        <div className="mb-3 flex items-center gap-2">
          <Palette size={14} className="text-zinc-400" />
          <h2 className="text-sm font-medium text-zinc-100">{tr('主题色', 'Accent color')}</h2>
        </div>
        <p className="mb-3 text-xs text-zinc-500">
          {tr(
            '选择一种品牌色作为高亮 / CTA / 当前选中态。来源自 Ongrid logo 的两束渐变。',
            'Pick a brand color for highlights, primary CTAs, and active state. Drawn from the two gradients in the Ongrid logo.',
          )}
        </p>
        <div className="flex flex-wrap gap-2">
          {ACCENT_PRESETS.map((p) => {
            const active = p.id === accentId;
            return (
              <Hint key={p.id} content={`${p.label} · ${p.hex}`}><button

                type="button"
                onClick={() => setAccent(p.id)}

                className={cn(
                  'group flex items-center gap-2 rounded-lg border px-2.5 py-1.5 text-xs transition',
                  active
                    ? 'border-zinc-600 bg-zinc-800/60 text-zinc-100'
                    : 'border-zinc-800 text-zinc-400 hover:border-zinc-700 hover:bg-zinc-800/40 hover:text-zinc-200',
                )}
              >
                <span
                  aria-hidden
                  className="inline-block h-4 w-4 shrink-0 rounded-full ring-1 ring-zinc-700"
                  style={{ backgroundColor: p.hex }}
                />
                <span className="font-medium">{p.label}</span>
                {active && <Check size={12} className="text-zinc-300" />}
              </button></Hint>
            );
          })}
        </div>
        <div className="mt-4 rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
          <div className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">
            {tr('预览', 'Preview')}
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Button variant="primary" size="sm"
              type="button"
              className="px-3 py-1.5 font-medium text-accent-fg"
            >
              {tr('主要按钮', 'Primary button')}
            </Button>
            <span className="rounded-full bg-accent/15 px-2 py-0.5 text-[11px] text-accent ring-1 ring-accent/30">
              {tr('已选中', 'Selected')}
            </span>
            <span className="text-xs text-accent">{tr('链接文字', 'Link text')}</span>
            <span className="h-2 w-24 rounded-full bg-zinc-800">
              <span className="block h-full w-2/3 rounded-full bg-accent" />
            </span>
          </div>
        </div>
      </section>

      {/* Other preferences placeholder */}
      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-5">
        <div className="mb-3 flex items-center gap-2">
          <Gauge size={14} className="text-zinc-400" />
          <h2 className="text-sm font-medium text-zinc-100">{tr('其它偏好', 'Other preferences')}</h2>
        </div>
        <div className="rounded-md border border-dashed border-zinc-800 bg-zinc-950/40 px-4 py-10 text-center">
          <p className="text-xs text-zinc-400">
            {tr(
              '即将上线：默认时间窗、自动刷新、列表密度等个人化偏好。',
              'Coming soon: default time range, auto-refresh, list density, and more.',
            )}
          </p>
        </div>
      </section>
    </div>
  );
}
