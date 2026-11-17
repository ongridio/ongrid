import { afterEach, expect, it, vi } from 'vitest';
import { modelSelectionForCatalog, createSession, postMessage, streamMessage, updateSessionModel } from './chat';
import { request } from './client';

vi.mock('./client', () => ({ ApiError: class extends Error {}, request: vi.fn() }));
vi.mock('@/store/auth', () => ({ getToken: () => null }));
afterEach(() => { vi.clearAllMocks(); vi.unstubAllGlobals(); });

it.each([
  { effort: 'none', expected: '' },
  { effort: '', expected: '' },
  { effort: 'high', expected: 'high' },
  { effort: undefined, expected: undefined },
])('sends $effort as $expected consistently across chat requests', async ({ effort, expected }) => {
  const selection = { provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: effort };
  await createSession({ title: 'test', ...selection });
  expect(vi.mocked(request).mock.lastCall?.[2]).toMatchObject({ reasoning_effort: expected });

  await updateSessionModel('test', selection.provider, selection.model, effort);
  expect(vi.mocked(request).mock.lastCall?.[2]).toMatchObject({ reasoning_effort: expected ?? '' });

  await postMessage('test', 'hello', selection);
  expect((vi.mocked(request).mock.lastCall?.[2] as Record<string, unknown>).reasoning_effort).toBe(expected);

  const fetchMock = vi.fn().mockResolvedValue(new Response(''));
  vi.stubGlobal('fetch', fetchMock);
  await streamMessage('test', 'hello', {}, selection);
  expect(JSON.parse(fetchMock.mock.lastCall?.[1].body).reasoning_effort).toBe(expected);
});

it('drops stale effort for models whose capability no longer supports it', () => {
  const selection = { provider: 'openai', model: 'gpt-4o', reasoning_effort: 'high' };
  const provider = { id: 'openai', label: 'OpenAI', models: ['gpt-4o'], reasoning_capabilities: { 'gpt-4o': { levels: [], mode: 'effort', source: 'builtin' as const } } };
  expect(modelSelectionForCatalog(selection, [provider])).toEqual({ ...selection, reasoning_effort: '' });
  expect(modelSelectionForCatalog(selection, [])).toEqual(selection);
});


it('dispatches per-request context usage separately from accumulated billing usage', async () => {
  const usage = { provider: 'custom', model: 'alias', window: 32768, input_tokens: 15000, output_tokens: 4096, source: 'provider', trimmed: true };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('event: context_usage\ndata: ' + JSON.stringify(usage) + '\n\nevent: done\ndata: {}\n\n')));
  const onContextUsage = vi.fn();
  const onDone = vi.fn();
  await streamMessage('session', 'question', { onContextUsage, onDone });
  expect(onContextUsage).toHaveBeenCalledWith(usage);
  expect(onDone).toHaveBeenCalledTimes(1);
});
