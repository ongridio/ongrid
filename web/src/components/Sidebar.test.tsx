import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { Sidebar } from './Sidebar';
import { useAuth } from '@/store/auth';
import { useUi } from '@/store/ui';
import { server } from '@/test/msw-server';

vi.mock('@/store/me', () => ({
  useMe: () => ({ me: null, loading: false, error: null, refresh: vi.fn() }),
  usePermissions: () => ({ isAdmin: false, canMutate: true, role: 'user' }),
}));

describe('Sidebar configurable sections', () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem('ongrid-locale', 'zh-CN');
    useAuth.setState({ token: null, refreshToken: null, email: 'user@example.com', role: 'user' });
    useUi.getState().setSidebarCollapsed(false);
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: [], total: 0 })),
    );
  });

  it('隐藏子菜单并从父菜单管理入口恢复', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    expect(await screen.findByRole('link', { name: '网络设备' })).toBeInTheDocument();
    await act(async () => {
      await user.click(screen.getByRole('button', { name: '从侧栏取消固定网络设备' }));
    });

    expect(screen.queryByRole('link', { name: '网络设备' })).not.toBeInTheDocument();
    expect(localStorage.getItem('sidebar.section.resources.hidden')).toContain('network-devices');

    await act(async () => {
      await user.click(screen.getByRole('button', { name: '管理基础设施菜单' }));
    });
    const checkbox = screen.getByRole('checkbox', { name: '网络设备' });
    expect(checkbox).not.toBeChecked();
    await act(async () => {
      await user.click(checkbox);
    });

    await waitFor(() => {
      expect(screen.getByRole('link', { name: '网络设备' })).toBeInTheDocument();
    });
    expect(localStorage.getItem('sidebar.section.resources.hidden')).toBe('[]');
  });

  it('所有可折叠分组都提供菜单管理入口', () => {
    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    expect(screen.getByRole('button', { name: '管理Agent菜单' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '管理知识库菜单' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '管理基础设施菜单' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '管理监控告警菜单' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '管理日常菜单' })).toBeInTheDocument();
  });

  it('可选升级菜单设置读取失败时仍保持默认导航', async () => {
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.error()),
    );

    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    expect(await screen.findByRole('link', { name: '网络设备' })).toBeInTheDocument();
  });

  it('展开后展示全部会话', async () => {
    const user = userEvent.setup();
    const sessions = Array.from({ length: 12 }, (_, index) => ({
      id: String(index + 1),
      user_id: 1,
      title: `测试会话 ${index + 1}`,
    }));
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: sessions, total: sessions.length })),
    );

    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    expect(await screen.findByText('测试会话 1')).toBeInTheDocument();
    expect(screen.queryByText('测试会话 6')).not.toBeInTheDocument();
    await act(async () => {
      await user.click(screen.getByRole('button', { name: '展开剩余 7 条' }));
    });
    expect(screen.getByText('测试会话 12')).toBeInTheDocument();
  });

  it('批量选择并删除会话', async () => {
    const user = userEvent.setup();
    const sessions = Array.from({ length: 3 }, (_, index) => ({
      id: String(index + 1),
      user_id: 1,
      title: `测试会话 ${index + 1}`,
    }));
    const deleted: string[] = [];
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: sessions, total: sessions.length })),
      http.delete('/api/v1/chat/sessions/:id', ({ params }) => {
        deleted.push(String(params.id));
        return new HttpResponse(null, { status: 204 });
      }),
    );

    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    await screen.findByText('测试会话 1');
    await act(async () => {
      await user.click(screen.getByRole('button', { name: '批量删除会话' }));
    });
    await act(async () => {
      await user.click(screen.getByRole('checkbox', { name: '选择会话 测试会话 1' }));
      await user.click(screen.getByRole('checkbox', { name: '选择会话 测试会话 3' }));
    });
    await act(async () => {
      await user.click(screen.getByRole('button', { name: '删除 2 条' }));
    });

    await waitFor(() => expect(deleted.sort()).toEqual(['1', '3']));
    expect(screen.queryByRole('dialog', { name: '批量删除会话' })).not.toBeInTheDocument();
  });
});
