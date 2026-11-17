import { act, cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ContextUsageIndicator } from './ContextUsageIndicator';
import type { ContextUsage, LLMProvider } from '@/api/chat';
import { setLocale } from '@/i18n/locale';

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
beforeEach(() => setLocale('en-US'));
afterEach(() => { cleanup(); setLocale('zh-CN'); });

const providers: LLMProvider[] = [{ id: 'custom', label: 'Custom', models: ['large', 'unknown'], context_budgets: {
  large: { window: 200000, output: 8192, source: 'provider' },
  unknown: { window: 64000, output: 4096, source: 'global' },
} }];
const usage: ContextUsage = { provider: 'custom', model: 'large', window: 200000, input_tokens: 80000, output_tokens: 4096, source: 'provider', trimmed: true };

it('shows the latest request estimate and hides it when the selected model changes', async () => {
  const user = userEvent.setup();
  const view = (model: string) => <ContextUsageIndicator providers={providers} selected={{ provider: 'custom', model }} usage={usage}/>;
  const { rerender } = render(view('large'));
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Context window' })); });
  expect(await screen.findByText('~40% full')).toBeInTheDocument();
  expect(screen.getByText('80,000 / 200,000 tokens')).toBeInTheDocument();
  expect(screen.getByText('4,096')).toBeInTheDocument();
  expect(screen.getByText(/Saved messages are retained/)).toBeInTheDocument();
  rerender(view('unknown'));
  expect(screen.getByText('No estimate yet')).toBeInTheDocument();
  expect(screen.getByText('64,000 tokens')).toBeInTheDocument();
  expect(screen.getByText('Global fallback')).toBeInTheDocument();
  expect(screen.queryByText('~40% full')).not.toBeInTheDocument();
});

it('does not invent a usage percentage before a request or a catalog response', async () => {
  const { rerender } = render(<ContextUsageIndicator providers={[]} selected={{ provider: 'custom', model: 'unknown' }}/>);
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
  rerender(<ContextUsageIndicator providers={providers} selected={{ provider: 'custom', model: 'unknown' }}/>);
  const user = userEvent.setup();
  await act(async () => { await user.tab(); await user.keyboard('{Enter}'); });
  expect(await screen.findByText('No estimate yet')).toBeInTheDocument();
  expect(screen.queryByText('~0% full')).not.toBeInTheDocument();
});
