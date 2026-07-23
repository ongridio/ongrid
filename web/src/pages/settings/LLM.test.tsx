import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import SettingsLLM from './LLM';
import { server } from '@/test/msw-server';

type SettingRow = {
  category: string;
  key: string;
  value: string;
  sensitive: boolean;
  updated_at: string;
};

const now = '2026-07-23T00:00:00Z';

function baseRows(): SettingRow[] {
  return [
    { category: 'llm', key: 'openai_api_key', value: 'secr…', sensitive: true, updated_at: now },
    { category: 'llm', key: 'openai_base_url', value: '', sensitive: false, updated_at: now },
    { category: 'llm', key: 'openai_models', value: '["gpt-test"]', sensitive: false, updated_at: now },
    { category: 'llm', key: 'openai_default_model', value: 'gpt-test', sensitive: false, updated_at: now },
  ];
}

function renderPage() {
  return render(
    <MemoryRouter>
      <SettingsLLM />
    </MemoryRouter>,
  );
}

async function openAIControls() {
  const card = await screen.findByTestId('llm-provider-openai');
  const controls = within(card);
  const testButton = controls.getByRole('button', { name: '测试配置' });
  await waitFor(() => expect(screen.queryByText('加载中…')).not.toBeInTheDocument());
  await waitFor(() => expect(testButton).toBeEnabled());
  return { card, controls, testButton };
}

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

describe('SettingsLLM configuration probe', () => {
  it('shows a distinct model-not-found reason for the current draft', async () => {
    let probeBody: Record<string, unknown> | null = null;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
      http.post('/api/v1/integrations/llm/test', async ({ request }) => {
        probeBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          valid: false,
          code: 'model-not-found',
          provider: 'openai',
          model: 'gpt-test',
          detail: 'model does not exist',
          latency_ms: 18,
        });
      }),
    );

    renderPage();
    const { controls, testButton } = await openAIControls();
    await userEvent.click(testButton);

    expect(await controls.findByText(/模型不存在或名称错误/)).toBeInTheDocument();
    expect(controls.getByText(/核对厂商控制台中的模型 ID/)).toBeInTheDocument();
    expect(probeBody).toEqual({
      provider: 'openai',
      api_key: 'secret-key',
      base_url: '',
      model: 'gpt-test',
    });
  });

  it('blocks persistence when automatic pre-save validation fails', async () => {
    let putCalls = 0;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
      http.post('/api/v1/integrations/llm/test', () => HttpResponse.json({
        valid: false,
        code: 'authentication-failed',
        provider: 'openai',
        model: 'gpt-test',
        detail: 'invalid api key',
        latency_ms: 12,
      })),
      http.put('/api/v1/system-settings/:category/:key', () => {
        putCalls += 1;
        return HttpResponse.json({});
      }),
    );

    renderPage();
    const { controls } = await openAIControls();
    const keyInput = controls.getByLabelText(/API Key/) as HTMLInputElement;
    await userEvent.clear(keyInput);
    await userEvent.type(keyInput, 'wrong-key');
    await userEvent.click(controls.getByRole('button', { name: '保存' }));

    expect(await controls.findByText(/API Key 无效或已撤销/)).toBeInTheDocument();
    expect(putCalls).toBe(0);
  });

  it('saves only after a successful probe and keeps the verified result visible', async () => {
    let currentKey = 'secret-key';
    let probeCalls = 0;
    let putCalls = 0;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: currentKey })),
      http.post('/api/v1/integrations/llm/test', async ({ request }) => {
        probeCalls += 1;
        const body = await request.json() as { api_key: string; model: string };
        expect(body.api_key).toBe('new-secret-key');
        return HttpResponse.json({
          valid: true,
          code: 'ok',
          provider: 'openai',
          model: body.model,
          latency_ms: 27,
        });
      }),
      http.put('/api/v1/system-settings/:category/:key', async ({ request, params }) => {
        putCalls += 1;
        const body = await request.json() as { value: string };
        if (params.key === 'openai_api_key') currentKey = body.value;
        return HttpResponse.json({
          category: params.category,
          key: params.key,
          value: 'new-…',
          sensitive: true,
          updated_at: now,
        });
      }),
      http.post('/api/v1/integrations/llm/invalidate', () => HttpResponse.json({ status: 'ok' })),
    );

    renderPage();
    const { controls } = await openAIControls();
    const keyInput = controls.getByLabelText(/API Key/) as HTMLInputElement;
    await userEvent.clear(keyInput);
    await userEvent.type(keyInput, 'new-secret-key');
    await userEvent.click(controls.getByRole('button', { name: '保存' }));

    expect(await controls.findByText(/配置可用 · gpt-test · 27 ms/)).toBeInTheDocument();
    expect(await controls.findByRole('button', { name: '已保存' })).toBeDisabled();
    expect(probeCalls).toBe(1);
    expect(putCalls).toBe(1);
  });
});
