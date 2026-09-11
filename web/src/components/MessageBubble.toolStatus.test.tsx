import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { MessageBubble } from './MessageBubble';
import type { ChatMessage } from '@/api/chat';
import { setLocale } from '@/i18n/locale';

afterEach(() => { cleanup(); setLocale('zh-CN'); });

const toolName = 'mcp__iomp__iomp_read_ssh_start';
function history(result: unknown): ChatMessage {
  return { id: 'history', role: 'tool', tool_name: toolName,
    content: typeof result === 'string' ? result : JSON.stringify(result) };
}
function icon() {
  return screen.getByRole('button', { name: '工具调用 ' + toolName }).querySelector('svg');
}

describe('工具调用状态在实时结果和历史加载间保持一致', () => {
  it('批次部分失败在重新加载后仍为红色', () => {
    const result = { ok: false, data: { status: 'failed', done: true,
      commands: [{ status: 'succeeded', exit_status: 0 }, { status: 'failed', exit_status: 127 }] } };
    const view = render(<MessageBubble message={{ id: 'live', role: 'tool', kind: 'tool_card',
      tool_call: { name: toolName, status: 'error', result } }} />);
    expect(icon()).toHaveClass('text-red-400');
    view.rerender(<MessageBubble message={history(result)} />);
    expect(icon()).toHaveClass('text-red-400');
    expect(screen.getByText('失败')).toBeInTheDocument();
  });
  it('调用成功返回业务失败也显示红色', () => {
    render(<MessageBubble message={{ id: 'live', role: 'tool', kind: 'tool_card',
      tool_call: { name: toolName, status: 'success', result: { ok: false } } }} />);
    expect(icon()).toHaveClass('text-red-400');
  });
  it.each([{ error: 'connection refused' }, { isError: true }, { status: 'failed' },
    { status: 'timeout' }, { exit_code: 1 }, { exit_status: 127 }])('历史失败结果 %j', (result) => {
    render(<MessageBubble message={history(result)} />);
    expect(icon()).toHaveClass('text-red-400');
  });
  it('成功结果中的业务告警不会被递归误判', () => {
    render(<MessageBubble message={history({ ok: true, data: { status: 'failed', error: 'alarm' } })} />);
    expect(icon()).toHaveClass('text-emerald-400');
  });
  it.each(['plain output', '{invalid', {}, null])('状态不明的旧结果不显示绿勾 %j', (result) => {
    render(<MessageBubble message={history(result)} />);
    expect(icon()).not.toHaveClass('text-emerald-400');
    expect(screen.getByText('状态未知')).toBeInTheDocument();
  });
  it('未完成的实时调用继续显示运行中', () => {
    render(<MessageBubble message={{ id: 'live', role: 'tool', kind: 'tool_card',
      tool_call: { name: toolName, status: 'pending' } }} />);
    expect(screen.getByText('运行中')).toBeInTheDocument();
  });
  it('显式错误优先于成功结果', () => {
    render(<MessageBubble message={{ id: 'live', role: 'tool', kind: 'tool_card',
      tool_call: { name: toolName, status: 'success', error: 'transport failed', result: { ok: true } } }} />);
    expect(icon()).toHaveClass('text-red-400');
  });
});
