import { selectOption } from '@/test/select-option';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import SettingsAgent from './Agent';
import { server } from '@/test/msw-server';

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'en-US');
});

describe('SettingsAgent LLM timeout', () => {
  it('loads the default and persists a validated timeout', async () => {
    let saved: Record<string, unknown> | null = null;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: [], total: 0 })),
      http.put('/api/v1/system-settings/agent/llm_timeout_seconds', async ({ request }) => {
        saved = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          category: 'agent',
          key: 'llm_timeout_seconds',
          value: '300',
          sensitive: false,
          updated_at: '2026-08-07T00:00:00Z',
        });
      }),
    );


      render(
        <MemoryRouter>
          <SettingsAgent />
        </MemoryRouter>,
      );


    const input = await screen.findByRole('spinbutton', { name: 'Timeout seconds' });
    expect(input).toHaveValue(120);

    const user = userEvent.setup();

      await user.clear(input);
      await user.type(input, '300');
      await user.click(screen.getByRole('button', { name: 'Save' }));


    await waitFor(() => expect(saved).toEqual({ value: '300', sensitive: false }));
  });

  it('rejects values outside the supported range before persistence', async () => {
    let calls = 0;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: [], total: 0 })),
      http.put('/api/v1/system-settings/agent/llm_timeout_seconds', () => {
        calls++;
        return HttpResponse.json({});
      }),
    );


      render(
        <MemoryRouter>
          <SettingsAgent />
        </MemoryRouter>,
      );


    const input = await screen.findByRole('spinbutton', { name: 'Timeout seconds' });
    const user = userEvent.setup();

      await user.clear(input);
      await user.type(input, '29');
      await user.click(screen.getByRole('button', { name: 'Save' }));


    expect(await screen.findByText(/Timeout must be a whole number/)).toBeInTheDocument();
    expect(calls).toBe(0);
  });
});

describe('SettingsAgent output language', () => {
  it('loads and persists the Agent output locale', async () => {
    let saved: Record<string, unknown> | null = null;
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({
        items: [{
          category: 'agent',
          key: 'output_locale',
          value: 'en',
          sensitive: false,
          updated_at: '2026-08-19T00:00:00Z',
        }],
        total: 1,
      })),
      http.put('/api/v1/system-settings/agent/output_locale', async ({ request }) => {
        saved = await request.json() as Record<string, unknown>;
        return HttpResponse.json({
          category: 'agent',
          key: 'output_locale',
          value: 'zh',
          sensitive: false,
          updated_at: '2026-08-19T00:00:00Z',
        });
      }),
    );


      render(
        <MemoryRouter>
          <SettingsAgent />
        </MemoryRouter>,
      );


    const select = await screen.findByRole('combobox', { name: 'Output language' });
    expect(select).toHaveTextContent('English');

    const user = userEvent.setup();

      await selectOption(select, '中文');
      await user.click(screen.getByRole('button', { name: 'Save language' }));


    await waitFor(() => expect(saved).toEqual({ value: 'zh', sensitive: false }));
  });
});

describe('SettingsAgent runtime selection', () => {
  it.each(['v2', 'v3'])('persists %s and can return to release defaults', async (version) => {
    const saved: unknown[] = [];
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: [], total: 0 })),
      http.put('/api/v1/system-settings/agent/runtime_version', async ({ request }) => {
        saved.push(await request.json());
        return HttpResponse.json({});
      }),
    );
    const user = userEvent.setup();
    render(<MemoryRouter><SettingsAgent /></MemoryRouter>);
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Runtime version' })).toBeEnabled());
    await selectOption(screen.getByRole('combobox', { name: 'Runtime version' }), `${version} · Pinned version`);
    await user.click(screen.getByRole('button', { name: 'Save runtime' }));
    await screen.findByText('Saved. Applies to new conversations.');
    expect(saved).toEqual([{ value: version, sensitive: false }]);
    await selectOption(screen.getByRole('combobox', { name: 'Runtime version' }), 'Follow release default (currently v3)');
    await user.click(screen.getByRole('button', { name: 'Save runtime' }));
    await waitFor(() => expect(saved).toEqual([{ value: version, sensitive: false }, { value: 'release', sensitive: false }]));
  });

  it('retains the selection and shows an error when saving fails', async () => {
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: [{ key: 'runtime_version', value: 'v3' }], total: 1 })),
      http.put('/api/v1/system-settings/agent/runtime_version', () => HttpResponse.json({ error: 'Runtime save failed' }, { status: 500 })),
    );
    const user = userEvent.setup();
    render(<MemoryRouter><SettingsAgent /></MemoryRouter>);
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Runtime version' })).toHaveTextContent('v3 · Pinned version'));
    await selectOption(screen.getByRole('combobox', { name: 'Runtime version' }), 'Follow release default (currently v3)');
    await user.click(screen.getByRole('button', { name: 'Save runtime' }));
    await screen.findByText('Runtime save failed');
    expect(screen.getByRole('button', { name: 'Save runtime' })).toBeEnabled();
  });
});
