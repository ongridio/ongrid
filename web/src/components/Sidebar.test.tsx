import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
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
    useUi.setState({ sidebarCollapsed: false, sessionsCollapsed: false, sessionsPreviewOpen: false, sessionsTriggerHovered: false });
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/system-settings', () => HttpResponse.json({ items: [], total: 0 })),
    );
  });

  it('reveals auto-hidden sessions from the menu header only on chat routes', () => {
    useUi.setState({ sessionsCollapsed: true });
    const view = render(<MemoryRouter><Sidebar /></MemoryRouter>);
    expect(screen.queryByRole('button', { name: '展开会话栏' })).not.toBeInTheDocument();
    const trigger = screen.getByLabelText('会话栏预览区域');
    fireEvent.pointerEnter(trigger.closest('[data-session-reveal]')!, { pointerType: 'mouse' });
    expect(useUi.getState().sessionsPreviewOpen).toBe(true);
    expect(useUi.getState().sessionsCollapsed).toBe(true);
    view.unmount();
    useUi.setState({ sessionsPreviewOpen: false, sessionsTriggerHovered: false });
    render(<MemoryRouter initialEntries={['/devices']}><Sidebar /></MemoryRouter>);
    expect(screen.queryByRole('button', { name: '展开会话栏' })).not.toBeInTheDocument();
    fireEvent.pointerEnter(screen.getByRole('link', { name: 'Ongrid 首页' }).closest('[data-session-reveal]')!, { pointerType: 'mouse' });
    expect(useUi.getState().sessionsPreviewOpen).toBe(false);
  });

  it('uses the icon rail on narrow screens without overwriting desktop preferences', () => {
    let matches = true;
    let notify: (() => void) | undefined;
    const media = {
      get matches() { return matches; },
      media: '(max-width: 767px)', onchange: null,
      addEventListener: vi.fn((_event: string, listener: () => void) => { notify = listener; }),
      removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn(),
    };
    vi.stubGlobal('matchMedia', vi.fn(() => media));
    try {
      const view = render(<MemoryRouter><Sidebar /></MemoryRouter>);
      fireEvent.click(screen.getAllByRole('button', { name: '展开侧边栏' })[0]);
      expect(screen.getByRole('button', { name: '折叠侧边栏' })).toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: '折叠侧边栏' }));
      expect(useUi.getState().sidebarCollapsed).toBe(false);
      act(() => { matches = false; notify?.(); });
      expect(screen.getByRole('button', { name: '折叠侧边栏' })).toBeInTheDocument();
      act(() => useUi.getState().setSidebarCollapsed(true));
      expect(screen.getAllByRole('button', { name: '展开侧边栏' })).toHaveLength(2);
      view.unmount();
      expect(media.removeEventListener).toHaveBeenCalledWith('change', expect.any(Function));
    } finally { vi.unstubAllGlobals(); }
  });

  it('groups the CMDB entries under model and resource headings', async () => {
    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole('button', { name: 'CMDB' }));
    expect(await screen.findByText('模型管理')).toBeInTheDocument();
    expect(screen.getByText('资源管理')).toBeInTheDocument();
  });

  it('隐藏子菜单并从父菜单管理入口恢复', async () => {
    const user = userEvent.setup({ skipHover: true });
    render(
      <MemoryRouter>
        <Sidebar />
      </MemoryRouter>,
    );

    expect(await screen.findByRole('link', { name: '网络设备' })).toBeInTheDocument();

      fireEvent.click(screen.getByRole('button', { name: '从侧栏取消固定网络设备' }));


    expect(screen.queryByRole('link', { name: '网络设备' })).not.toBeInTheDocument();
    expect(localStorage.getItem('sidebar.section.resources.hidden')).toContain('network-devices');


      await user.click(screen.getByRole('button', { name: '管理基础设施菜单' }));

    const checkbox = await screen.findByRole('checkbox', { name: '网络设备' });
    expect(checkbox).not.toBeChecked();

      fireEvent.click(checkbox);


    await waitFor(() => {
      expect(screen.getByRole('link', { name: '网络设备' })).toBeInTheDocument();
    });
    expect(localStorage.getItem('sidebar.section.resources.hidden')).toBe('[]');
  }, 20000);

  it.each([null, '[]', '["agent-observability"]'])('Agent 观测不再出现在侧栏及菜单管理，兼容旧设置：%s', async (saved) => {
    if (saved !== null) localStorage.setItem('sidebar.section.observability.hidden', saved);
    const user = userEvent.setup({ skipHover: true });
    render(<MemoryRouter initialEntries={['/traces']}><Sidebar /></MemoryRouter>);

    expect(screen.getByRole('link', { name: '链路' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Agent' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '管理监控告警菜单' }));
    expect(await screen.findByRole('checkbox', { name: '链路' })).toBeChecked();
    expect(screen.queryByRole('checkbox', { name: 'Agent' })).not.toBeInTheDocument();
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

  it('keeps chat sessions out of the navigation menu', () => {
    render(<MemoryRouter><Sidebar /></MemoryRouter>);
    expect(screen.queryByText('会话')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '批量删除会话' })).not.toBeInTheDocument();
  });
});
