import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import SettingsChannels from './Channels';
import SettingsNotifications from './Notifications';
import { server } from '@/test/msw-server';

const now = '2026-09-04T00:00:00Z';

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

describe('communication provider settings', () => {
  it('groups multiple notification channels and keeps that provider addable', async () => {
    server.use(
      http.get('/api/v1/notification-channels', () => HttpResponse.json({
        items: [
          { id: 1, name: '值班群', type: 'slack', enabled: true, endpoint: 'https://hooks.slack.test/one', created_at: now, updated_at: now },
          { id: 2, name: '研发群', type: 'slack', enabled: false, endpoint: 'https://hooks.slack.test/two', created_at: now, updated_at: now },
        ],
        total: 2,
      })),
    );

    render(<MemoryRouter><SettingsNotifications /></MemoryRouter>);
    const card = await screen.findByTestId('notification-provider-slack');
    expect(card).toHaveTextContent('值班群');
    expect(card).toHaveTextContent('研发群');
    expect(screen.queryByTestId('notification-provider-feishu')).not.toBeInTheDocument();

    const user = userEvent.setup();
    await act(async () => {
      await user.click(screen.getByText('添加通知渠道'));
      await user.click(within(screen.getByTestId('notification-provider-picker')).getByRole('button', { name: /Slack/ }));
    });

    expect(await screen.findByRole('dialog', { name: '新建 Slack 渠道' })).toHaveTextContent('类型Slack');
  });

  it('groups multiple IM bots and opens a provider-locked create form', async () => {
    server.use(
      http.get('/api/v1/im/apps', () => HttpResponse.json({
        items: [
          { id: 1, provider: 'slack', mode: 'stream', name: '值班机器人', app_id: 'T001', has_secret: true, enabled: true, idle_timeout_seconds: 120, created_at: now, updated_at: now },
          { id: 2, provider: 'slack', mode: 'stream', name: '研发机器人', app_id: 'T002', has_secret: true, enabled: true, idle_timeout_seconds: 120, created_at: now, updated_at: now },
        ],
        total: 2,
      })),
      http.post('/api/v1/im/apps/1/test', () => HttpResponse.json({
        accepted: true,
        latency_ms: 23,
      })),
    );

    render(<MemoryRouter><SettingsChannels /></MemoryRouter>);
    const card = await screen.findByTestId('im-provider-slack');
    expect(card).toHaveTextContent('值班机器人');
    expect(card).toHaveTextContent('研发机器人');
    expect(screen.queryByTestId('im-provider-feishu')).not.toBeInTheDocument();

    const user = userEvent.setup();
    await act(async () => {
      await user.click(within(card).getAllByRole('button', { name: '测试' })[0]);
    });
    expect(await within(card).findByText('凭证有效 · 23 ms')).toBeInTheDocument();

    await act(async () => {
      await user.click(screen.getByText('添加 IM 平台'));
      await user.click(within(screen.getByTestId('im-provider-picker')).getByRole('button', { name: /Slack/ }));
    });

    const dialog = await screen.findByRole('dialog', { name: '新建Slack机器人' });
    expect(dialog).toHaveTextContent('平台Slack');
    expect(within(dialog).queryByRole('combobox', { name: '平台' })).not.toBeInTheDocument();
  });
});
