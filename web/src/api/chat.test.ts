import { afterEach, describe, expect, it, vi } from 'vitest';

import { useAuth } from '@/store/auth';
import { streamMessage } from './chat';

describe('streamMessage', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    useAuth.setState({ token: null });
  });

  it('dispatches assistant_delta before the final assistant frame', async () => {
    useAuth.setState({ token: null });
    const firstChunk = new TextEncoder().encode(
      ': ok\n\nevent: assistant_delta\ndata: {"session_id":"session-1","iteration":1,"content":"故障可',
    );
    const secondChunk = new TextEncoder().encode(
      '能由磁盘压力引起"}\n\nevent: assistant\ndata: {"session_id":"session-1","iteration":1,"message_id":"message-1","content":"故障可能由磁盘压力引起","pending_tool_calls":0}\n\n',
    );
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(firstChunk);
        controller.enqueue(secondChunk);
        controller.close();
      },
    });
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, body });
    vi.stubGlobal('fetch', fetchMock);

    const deltas: string[] = [];
    const assistants: string[] = [];
    await streamMessage(
      'session-1',
      '诊断一下',
      {
        onAssistantDelta: (event) => deltas.push(event.content),
        onAssistant: (event) => assistants.push(event.content),
      },
    );

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/chat/sessions/session-1/messages/stream',
      expect.objectContaining({ method: 'POST' }),
    );
    expect(deltas).toEqual(['故障可能由磁盘压力引起']);
    expect(assistants).toEqual(['故障可能由磁盘压力引起']);
  });
});
