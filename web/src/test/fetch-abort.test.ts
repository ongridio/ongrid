import { describe, expect, it } from 'vitest';
import { http, HttpResponse } from 'msw';
import { server } from './msw-server';

describe('JSDOM 与原生 fetch 取消兼容', () => {
  it('Request 接受全局 AbortController 的信号并传播取消原因', () => {
    const controller = new AbortController();
    const request = new Request('http://localhost/probe', { signal: controller.signal });
    expect(request.signal.aborted).toBe(false);
    controller.abort('test cancellation');
    expect(request.signal.aborted).toBe(true);
    expect(request.signal.reason).toBe('test cancellation');
  });

  it('MSW 中进行中的 fetch 仍然能够取消', async () => {
    let started!: () => void;
    let release!: () => void;
    const entered = new Promise<void>((resolve) => { started = resolve; });
    const gate = new Promise<void>((resolve) => { release = resolve; });
    server.use(http.get('http://localhost/probe', async () => {
      started();
      await gate;
      return HttpResponse.json({ ok: true });
    }));
    const controller = new AbortController();
    const pending = fetch('http://localhost/probe', { signal: controller.signal });
    const rejected = expect(pending).rejects.toMatchObject({ name: 'AbortError' });
    try {
      await entered;
      controller.abort();
      await rejected;
    } finally {
      release();
    }
  });
});
