import { describe, expect, it } from 'vitest';

import type { ChatMessage } from '@/api/chat';
import { mergeAssistantFinalMessage } from './chatStreamMessages';

describe('mergeAssistantFinalMessage', () => {
  it('keeps streamed reasoning when the final assistant frame replaces the delta', () => {
    const delta: ChatMessage = {
      id: 'assistant-delta-session-1-1',
      role: 'assistant',
      content: '故障可能',
      reasoning: '先检查磁盘压力，再核对 I/O 延迟。',
      pending: true,
    };

    const result = mergeAssistantFinalMessage(
      [delta],
      delta.id,
      {
        id: 'message-1',
        role: 'assistant',
        content: '故障可能由磁盘压力引起。',
        pending: false,
      },
    );

    expect(result).toEqual([
      {
        id: 'message-1',
        role: 'assistant',
        content: '故障可能由磁盘压力引起。',
        reasoning: '先检查磁盘压力，再核对 I/O 延迟。',
        pending: false,
      },
    ]);
  });

  it('preserves reasoning already present on a stable message', () => {
    const stable: ChatMessage = {
      id: 'message-1',
      role: 'assistant',
      content: '旧内容',
      reasoning: '持久化思考',
      pending: false,
    };

    const result = mergeAssistantFinalMessage(
      [stable],
      'assistant-delta-session-1-1',
      {
        id: stable.id,
        role: 'assistant',
        content: '新内容',
        pending: false,
      },
    );

    expect(result).toEqual([
      {
        ...stable,
        content: '新内容',
      },
    ]);
  });
});
