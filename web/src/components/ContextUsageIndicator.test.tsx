import { act, cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ContextUsageIndicator } from './ContextUsageIndicator';
import { setLocale } from '@/i18n/locale';
import type { ContextUsage } from '@/api/chat';

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
beforeEach(() => setLocale('en-US'));
afterEach(() => { cleanup(); setLocale('zh-CN'); });

const selected = { provider: 'custom', model: 'example' };
const capability = { auto: 256000, total: 1000000, source: 'provider', options: [128000, 256000, 1000000] };
const usage: ContextUsage = { ...selected, known: true, estimated: 36775, window: 256000, input_budget: 243808,
  output_reserve: 8192, tools: 30648, instructions: 5495, tool_results: 0, reported: 20813, status: 'ready' };

async function openUsage() {
  const user = userEvent.setup();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Context usage' })); });
  return { user, panel: within(screen.getByRole('dialog', { name: 'Context usage' })) };
}

it('charts the same input budget for the ring, total and categories, and keeps exact counts and window selection available', async () => {
  const pick = vi.fn();
  render(<ContextUsageIndicator capability={capability} selected={selected} usage={usage} onPick={pick} />);
  expect(screen.getByRole('button', { name: 'Context usage' })).toHaveAccessibleDescription(/15\.1%/);
  const { user, panel } = await openUsage();
  expect(panel.getByText('15.1%')).toBeVisible();
  expect(panel.getByText('Est. input 36.8K / 243.8K')).toBeVisible();
  expect(panel.getByRole('meter')).toHaveAttribute('aria-valuetext', '36,775 / 243,808');
  expect(panel.getByText('12.6%')).toBeVisible();
  expect(panel.getByText('0.3%')).toBeVisible();
  const details = panel.getByText('Token details').closest('details')!;
  expect(details.open).toBe(false);
  await user.click(panel.getByText('Token details'));
  expect(details.open).toBe(true);
  expect(panel.getByText('20,813')).toBeVisible();
  await act(async () => { panel.getByRole('combobox', { name: 'Context window' }).focus(); await user.keyboard('{ArrowDown}'); });
  await act(async () => { await user.click(screen.getByRole('option', { name: '128,000' })); });
  expect(pick).toHaveBeenCalledWith({ ...selected, context_window_tokens: 128000 });
  await user.click(panel.getByRole('button', { name: 'Close' }));
  expect(screen.queryByRole('dialog')).toBeNull();
});

it('shows counts without percentages when the deployment capacity is unknown', async () => {
  render(<ContextUsageIndicator capability={{ auto: 32000 }} selected={selected} usage={{ ...usage, window: 32000, known: false }} />);
  const { panel } = await openUsage();
  expect(panel.getByText(/capacity is unknown/)).toBeVisible();
  expect(panel.getByRole('combobox')).toBeDisabled();
  expect(panel.queryByRole('meter')).toBeNull();
  expect(panel.getByText('30,648')).toBeVisible();
});

it('caps the visual meter at capacity but reports the full overage, and discards the previous model snapshot on switch', async () => {
  const { rerender } = render(<ContextUsageIndicator capability={capability} selected={selected}
    usage={{ ...usage, estimated: 300000, input_budget: 200000, status: 'over_limit' }} />);
  const { panel } = await openUsage();
  expect(panel.getByText('150.0%')).toBeVisible();
  expect(panel.getByRole('meter')).toHaveAttribute('aria-valuenow', '200000');
  rerender(<ContextUsageIndicator capability={capability} selected={{ ...selected, model: 'other' }} usage={usage} />);
  expect(panel.queryByRole('meter')).toBeNull();
  expect(panel.getByText('Send a message to see usage.')).toBeVisible();
});
