import { act, cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ContextUsageIndicator } from './ContextUsageIndicator';
import { setLocale } from '@/i18n/locale';
import type { ContextUsage } from '@/api/chat';

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
beforeEach(() => setLocale('en-US'));
afterEach(() => { cleanup(); setLocale('zh-CN'); });
const selected = {provider:'custom',model:'example'};
const usage: ContextUsage = {...selected,known:true,estimated:16000,window:256000,input_budget:240000,output_reserve:8192,tools:4000,instructions:2000,tool_results:4000,reported:15000,status:'compacted',before:210000};

it('shows current usage and submits only a supported window for the same model', async () => {
  const pick=vi.fn();
  render(<ContextUsageIndicator capability={{auto:256000,total:1000000,source:'provider',options:[128000,256000,1000000]}} selected={selected} usage={usage} onPick={pick}/>);
  const user=userEvent.setup();
  await act(async () => { await user.click(screen.getByRole('button',{name:'Context window'})); });
  expect(screen.getByText('16,000 / 240,000')).toBeInTheDocument();
  expect(screen.getByText('210,000')).toBeInTheDocument();
  await act(async () => { screen.getByRole('combobox',{name:'Context window'}).focus(); await user.keyboard('{ArrowDown}'); });
  await act(async () => { await user.click(screen.getByRole('option',{name:'128,000'})); });
  expect(pick).toHaveBeenCalledWith({...selected,context_window_tokens:128000});
});

it('does not present the fallback budget as a known model capacity',async()=>{
 render(<ContextUsageIndicator capability={{auto:32000}} selected={selected} usage={{...usage,window:32000,known:false}}/>);
 const user=userEvent.setup();
 await act(async()=>{await user.click(screen.getByRole('button',{name:'Context window'}))});
 expect(screen.getByText(/capacity is unknown/)).toBeInTheDocument();
 expect(screen.getByRole('combobox')).toBeDisabled();
 expect(screen.getByRole('button',{name:'Context window'})).not.toHaveTextContent('%');
});
