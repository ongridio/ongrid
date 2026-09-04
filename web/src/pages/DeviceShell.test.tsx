import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ConnectModal, DeviceShell } from './DeviceShell';
import { server } from '@/test/msw-server';

const openShellSocket = vi.hoisted(() => vi.fn());

vi.mock('@/components/XTerminal', () => ({ XTerminal: () => null }));
vi.mock('@/api/webshell', async () => ({
  ...await vi.importActual<typeof import('@/api/webshell')>('@/api/webshell'),
  openShellSocket,
}));

const edge = {
  id: 70,
  name: 'test-edge',
  status: 'online',
  roles: [],
  access_key_id: 'test',
  last_seen_at: '2026-09-04T00:00:00Z',
  device_id: 42,
};

function renderShell() {
  return render(
    <MemoryRouter initialEntries={['/devices/42/shell']}>
      <Routes>
        <Route path="/devices/:deviceId/shell" element={<DeviceShell />} />
        <Route path="/devices" element={<div>设备列表页</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('ConnectModal', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    openShellSocket.mockReset();
    server.use(
      http.get('/api/v1/edges', () => HttpResponse.json({ items: [edge], total: 1 })),
      http.get('/api/v1/edges/70', () => HttpResponse.json(edge)),
      http.get('/api/v1/devices/42/shell/credentials', () => HttpResponse.json({ items: [] })),
      http.post('/api/v1/devices/42/shell/tickets', () => HttpResponse.json({
        ticket: 'test-ticket',
        expires_at: '2026-09-04T00:00:30Z',
      })),
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('先展示已保存账户，新增账户时直接连接并延后保存', async () => {
    server.use(
      http.get('/api/v1/devices/42/shell/credentials', () =>
        HttpResponse.json({
          items: [
            { id: 1, ssh_user: 'root', ssh_port: 22, created_at: '2026-09-03T00:00:00Z' },
            { id: 2, ssh_user: 'ops', ssh_port: 2202, created_at: '2026-09-03T00:00:00Z' },
          ],
        }),
      ),
    );
    const onSubmit = vi.fn();

    render(
      <ConnectModal
        open
        deviceId="42"
        title="连接到测试设备"
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />,
    );

    const dialog = screen.getByRole('dialog', { name: '连接到测试设备' });
    await within(dialog).findByText('root');
    expect(within(dialog).getByText('127.0.0.1 · 端口 22')).toBeInTheDocument();
    expect(within(dialog).getByText('127.0.0.1 · 端口 2202')).toBeInTheDocument();
    expect(within(dialog).queryByText('登录方式')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('OS 用户')).not.toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole('button', { name: '连接' }));
    expect(onSubmit).toHaveBeenCalledWith({
      user: 'root',
      password: '',
      port: 22,
      credentialId: 1,
    });
    onSubmit.mockClear();

    fireEvent.click(within(dialog).getByRole('button', { name: '新增账户' }));
    expect(within(dialog).getByLabelText('OS 用户')).toHaveValue('root');
    fireEvent.change(within(dialog).getByLabelText('OS 用户'), { target: { value: 'tester' } });
    fireEvent.change(within(dialog).getByLabelText('SSH 端口'), { target: { value: '2222' } });
    fireEvent.change(within(dialog).getByLabelText('密码'), { target: { value: 'secret' } });
    fireEvent.click(within(dialog).getByLabelText('保存此账户，下次可直接登录'));
    expect(within(dialog).queryByText('凭据名称')).not.toBeInTheDocument();
    expect(within(dialog).getByText('连接目标固定为设备本机 127.0.0.1，可使用任意有效 SSH 端口。')).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: '连接' }));

    expect(onSubmit).toHaveBeenCalledWith({
      user: 'tester',
      password: 'secret',
      port: 2222,
      saveCredential: true,
    });
  });

  it('把保存意图放进一次性票据交给后端在认证后处理', async () => {
    let ticketBody: Record<string, unknown> | undefined;
    const socket = {
      readyState: WebSocket.OPEN,
      send: vi.fn(),
      close: vi.fn(),
      onopen: null,
      onmessage: null,
      onerror: null,
      onclose: null,
    } as unknown as WebSocket;
    openShellSocket.mockReturnValue(socket);
    server.use(
      http.post('/api/v1/devices/42/shell/tickets', async ({ request }) => {
        ticketBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ ticket: 'test-ticket', expires_at: '2026-09-04T00:00:30Z' });
      }),
    );
    renderShell();

    fireEvent.change(await screen.findByLabelText('密码'), { target: { value: 'secret' } });
    fireEvent.click(screen.getByLabelText('保存此账户，下次可直接登录'));
    fireEvent.click(screen.getByRole('button', { name: '连接' }));
    await waitFor(() => expect(openShellSocket).toHaveBeenCalledWith('42', 'test-ticket'));
    expect(ticketBody).toMatchObject({ ssh_user: 'root', ssh_pass: 'secret', ssh_port: 22, save_credential: true });

    act(() => socket.onmessage?.({ data: JSON.stringify({ type: 'ready' }) } as MessageEvent));
    expect(screen.queryByText(/保存账户失败/)).not.toBeInTheDocument();
  });

  it('SSH 失败时显示具体错误且不保存账户', async () => {
    let ticketBody: Record<string, unknown> | undefined;
    const socket = {
      readyState: WebSocket.OPEN,
      send: vi.fn(),
      close: vi.fn(),
      onopen: null,
      onmessage: null,
      onerror: null,
      onclose: null,
    } as unknown as WebSocket;
    openShellSocket.mockReturnValue(socket);
    server.use(
      http.post('/api/v1/devices/42/shell/tickets', async ({ request }) => {
        ticketBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ ticket: 'test-ticket', expires_at: '2026-09-04T00:00:30Z' });
      }),
    );
    renderShell();

    fireEvent.change(await screen.findByLabelText('密码'), { target: { value: 'wrong' } });
    fireEvent.click(screen.getByLabelText('保存此账户，下次可直接登录'));
    fireEvent.click(screen.getByRole('button', { name: '连接' }));
    await waitFor(() => expect(openShellSocket).toHaveBeenCalledWith('42', 'test-ticket'));

    act(() => socket.onmessage?.({
      data: JSON.stringify({ type: 'auth_error', message: '用户名或密码错误' }),
    } as MessageEvent));

    expect(await screen.findByText('SSH 认证失败：用户名或密码错误')).toBeInTheDocument();
    expect(ticketBody).toMatchObject({ save_credential: true });
  });

  it('最外层 SSH 会话退出后离开终端页', async () => {
    const close = vi.spyOn(window, 'close').mockImplementation(() => {});
    const socket = {
      readyState: WebSocket.OPEN,
      send: vi.fn(),
      close: vi.fn(),
      onopen: null,
      onmessage: null,
      onerror: null,
      onclose: null,
    } as unknown as WebSocket;
    openShellSocket.mockReturnValue(socket);
    renderShell();

    fireEvent.change(await screen.findByLabelText('密码'), { target: { value: 'secret' } });
    fireEvent.click(screen.getByRole('button', { name: '连接' }));
    await waitFor(() => expect(openShellSocket).toHaveBeenCalledWith('42', 'test-ticket'));

    act(() => socket.onmessage?.({
      data: JSON.stringify({ type: 'exit', exit_code: 0 }),
    } as MessageEvent));

    expect(close).toHaveBeenCalledOnce();
    expect(await screen.findByText('设备列表页')).toBeInTheDocument();
  });

  it('点击关闭直接离开终端页', () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const close = vi.spyOn(window, 'close').mockImplementation(() => {});
    renderShell();

    fireEvent.click(screen.getByRole('button', { name: '关闭终端' }));

    expect(confirm).not.toHaveBeenCalled();
    expect(close).toHaveBeenCalledOnce();
    expect(screen.getByText('设备列表页')).toBeInTheDocument();
  });
});
