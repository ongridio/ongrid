import { act, render, screen, waitFor, within } from '@testing-library/react';
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

async function renderPage() {
  let view!: ReturnType<typeof render>;
  await act(async () => {
    view = render(
      <MemoryRouter>
        <SettingsLLM />
      </MemoryRouter>,
    );
  });
  return view;
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

describe('SettingsLLM model discovery and custom TLS', () => {
  it('fetches models from the unsaved draft without saving', async () => {
    let body: Record<string, unknown> | null = null;
    let saves = 0;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
      http.post('/api/v1/integrations/llm/models', async ({ request }) => {
        body = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ models: ['a-new', 'gpt-test'], truncated: false });
      }),
      http.post('/api/v1/integrations/llm/validate-and-save', () => {
        saves += 1;
        return HttpResponse.json({});
      }),
    );

    await renderPage();
    const { controls } = await openAIControls();
    await userEvent.click(controls.getByRole('button', { name: '获取模型' }));

    expect(await controls.findByText('a-new')).toBeInTheDocument();
    expect(body).toMatchObject({ provider: 'openai', api_key: 'secret-key' });
    expect(within(controls.getByText('gpt-test').closest('li')!).getByText('默认')).toBeInTheDocument();
    expect(controls.getByRole('button', { name: '保存' })).toBeEnabled();
    expect(saves).toBe(0);
  });

  it('shows the custom TLS warning and forwards the opt-in to discovery and testing', async () => {
    const now = new Date().toISOString();
    const rows = [
      { category: 'llm', key: 'custom_api_key', value: 'masked', sensitive: true, updated_at: now },
      { category: 'llm', key: 'custom_base_url', value: 'https://local.test/v1', sensitive: false, updated_at: now },
      { category: 'llm', key: 'custom_models', value: '["old"]', sensitive: false, updated_at: now },
      { category: 'llm', key: 'custom_default_model', value: 'old', sensitive: false, updated_at: now },
    ];
    const calls: Record<string, unknown>[] = [];
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: rows, total: rows.length })),
      http.get('/api/v1/system-settings/llm/custom_api_key/reveal', () => HttpResponse.json({ value: 'test-only-key' })),
      http.post('/api/v1/integrations/llm/models', async ({ request }) => {
        calls.push(await request.json() as Record<string, unknown>);
        return HttpResponse.json({ models: ['new'], truncated: false });
      }),
      http.post('/api/v1/integrations/llm/test', async ({ request }) => {
        calls.push(await request.json() as Record<string, unknown>);
        return HttpResponse.json({ valid: true, code: 'ok' });
      }),
    );

    await renderPage();
    const controls = within(await screen.findByTestId('llm-provider-custom'));
    const checkbox = await controls.findByRole('checkbox', { name: /跳过 TLS 校验/ });
    expect(checkbox).not.toBeChecked();
    await userEvent.click(checkbox);
    expect(controls.getByText(/将不验证服务端证书/)).toBeInTheDocument();
    await userEvent.click(controls.getByRole('button', { name: '获取模型' }));
    expect(await controls.findByText('new')).toBeInTheDocument();
    await userEvent.click(controls.getByRole('button', { name: '测试配置' }));
    await waitFor(() => expect(calls).toHaveLength(2));
    calls.forEach((call) => expect(call.tls_insecure).toBe(true));
  });
});

describe('SettingsLLM configuration probe', () => {
  it('shows configured providers and adds another provider on demand', async () => {
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
    );

    await renderPage();
    await openAIControls();
    expect(screen.queryByTestId('llm-provider-anthropic')).not.toBeInTheDocument();

    const user = userEvent.setup();
    await act(async () => {
      await user.click(screen.getByText('添加模型供应商'));
      await user.click(within(screen.getByTestId('llm-provider-picker')).getByRole('button', { name: /Anthropic/ }));
    });

    const anthropicCard = await screen.findByTestId('llm-provider-anthropic');
    await waitFor(() => expect(within(anthropicCard).queryByText('加载中…')).not.toBeInTheDocument());
    expect(screen.queryByTestId('llm-provider-gemini')).not.toBeInTheDocument();
  });

  it('shows a distinct model-not-found reason for the current draft', async () => {
    let probeBody: Record<string, unknown> | null = null;
    const rows = baseRows();
    const modelsRow = rows.find((row) => row.key === 'openai_models');
    if (modelsRow) modelsRow.value = '["gpt-test","gpt-extra"]';
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: rows, total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
      http.post('/api/v1/integrations/llm/test', async ({ request }) => {
        probeBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          valid: false,
          code: 'model-not-found',
          provider: 'openai',
          model: 'gpt-extra',
          detail: 'model does not exist',
          latency_ms: 18,
          saved: false,
          disabled: false,
        });
      }),
    );

    await renderPage();
    const { controls, testButton } = await openAIControls();
    const user = userEvent.setup();
    await act(async () => {
      await user.click(testButton);
    });

    expect(await controls.findByText(/模型不存在或名称错误/)).toBeInTheDocument();
    expect(controls.getByText(/核对厂商控制台中的模型 ID/)).toBeInTheDocument();
    expect(probeBody).toEqual({
      provider: 'openai',
      api_key: 'secret-key',
      base_url: '',
      default_model: 'gpt-test',
      models: ['gpt-test', 'gpt-extra'],
    });
  });

  it('blocks persistence when automatic pre-save validation fails', async () => {
    let saveCalls = 0;
    let legacyPutCalls = 0;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
      http.post('/api/v1/integrations/llm/validate-and-save', () => {
        saveCalls += 1;
        return HttpResponse.json({
          valid: false,
          code: 'authentication-failed',
          provider: 'openai',
          model: 'gpt-test',
          detail: 'invalid api key',
          latency_ms: 12,
          saved: false,
          disabled: false,
        });
      }),
      http.put('/api/v1/system-settings/:category/:key', () => {
        legacyPutCalls += 1;
        return HttpResponse.json({});
      }),
    );

    await renderPage();
    const { controls } = await openAIControls();
    const user = userEvent.setup();
    const keyInput = controls.getByLabelText(/API Key/) as HTMLInputElement;
    await act(async () => {
      await user.clear(keyInput);
      await user.type(keyInput, 'wrong-key');
      await user.click(controls.getByRole('button', { name: '保存' }));
    });

    expect(await controls.findByText(/API Key 无效或已撤销/)).toBeInTheDocument();
    expect(saveCalls).toBe(1);
    expect(legacyPutCalls).toBe(0);
  });

  it('saves only after a successful probe and keeps the verified result visible', async () => {
    let currentKey = 'secret-key';
    let saveCalls = 0;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: currentKey })),
      http.post('/api/v1/integrations/llm/validate-and-save', async ({ request }) => {
        saveCalls += 1;
        const body = await request.json() as { api_key: string; default_model: string; models: string[] };
        expect(body.api_key).toBe('new-secret-key');
        expect(body.models).toEqual(['gpt-test']);
        currentKey = body.api_key;
        return HttpResponse.json({
          valid: true,
          code: 'ok',
          provider: 'openai',
          model: body.default_model,
          latency_ms: 27,
          saved: true,
          disabled: false,
        });
      }),
    );

    await renderPage();
    const { controls } = await openAIControls();
    const user = userEvent.setup();
    const keyInput = controls.getByLabelText(/API Key/) as HTMLInputElement;
    await act(async () => {
      await user.clear(keyInput);
      await user.type(keyInput, 'new-secret-key');
      await user.click(controls.getByRole('button', { name: '保存' }));
    });

    expect(await controls.findByText(/配置可用 · gpt-test · 27 ms/)).toBeInTheDocument();
    expect(await controls.findByRole('button', { name: '已保存' })).toBeDisabled();
    expect(saveCalls).toBe(1);
  });

  it('saves an empty key as a disable override without calling the test endpoint', async () => {
    const rows = baseRows();
    let testCalls = 0;
    let saveBody: Record<string, unknown> | null = null;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: rows, total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
      http.post('/api/v1/integrations/llm/test', () => {
        testCalls += 1;
        return HttpResponse.json({});
      }),
      http.post('/api/v1/integrations/llm/validate-and-save', async ({ request }) => {
        saveBody = await request.json() as Record<string, unknown>;
        const apiRow = rows.find((row) => row.key === 'openai_api_key');
        if (apiRow) apiRow.value = '';
        return HttpResponse.json({
          valid: true,
          code: 'disabled',
          provider: 'openai',
          model: 'gpt-test',
          latency_ms: 0,
          saved: true,
          disabled: true,
        });
      }),
    );

    await renderPage();
    const { controls } = await openAIControls();
    const user = userEvent.setup();
    const keyInput = controls.getByLabelText(/API Key/) as HTMLInputElement;
    await act(async () => {
      await user.clear(keyInput);
      await user.click(controls.getByRole('button', { name: '保存' }));
    });

    expect(await controls.findByRole('button', { name: '已保存' })).toBeDisabled();
    expect(testCalls).toBe(0);
    expect(saveBody).toMatchObject({ api_key: '', default_model: 'gpt-test', models: ['gpt-test'] });
  });

  it('enables save after removing a persisted model', async () => {
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: baseRows(), total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
    );

    await renderPage();
    const { controls } = await openAIControls();
    const user = userEvent.setup();

    expect(controls.getByRole('button', { name: '保存' })).toBeDisabled();
    await user.click(controls.getByRole('button', { name: /gpt-test/ }));

    expect(controls.getByRole('button', { name: '保存' })).toBeEnabled();
  });

  it('keeps save enabled when removing an unsaved discovered model restores the original list', async () => {
    const rows = baseRows();
    const modelsRow = rows.find((row) => row.key === 'openai_models');
    const defaultRow = rows.find((row) => row.key === 'openai_default_model');
    if (modelsRow) modelsRow.value = '[]';
    if (defaultRow) defaultRow.value = '';
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: rows, total: 4 })),
      http.get('/api/v1/system-settings/llm/openai_api_key/reveal', () => HttpResponse.json({ value: 'secret-key' })),
      http.post('/api/v1/integrations/llm/models', () => HttpResponse.json({ models: ['discovered-model'], truncated: false })),
    );

    await renderPage();
    const { controls } = await openAIControls();
    const user = userEvent.setup();

    await user.click(controls.getByRole('button', { name: '获取模型' }));
    await user.click(await controls.findByRole('button', { name: /discovered-model/ }));

    expect(controls.getByRole('button', { name: '保存' })).toBeEnabled();
  });
});
