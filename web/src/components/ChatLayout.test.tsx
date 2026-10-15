import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Link, MemoryRouter, Route, Routes } from 'react-router-dom';
import { delay, http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';
import { ChatLayout } from './ChatLayout';
import { useChatSessions } from '@/store/chatSessions';
import { useUi } from '@/store/ui';
import { setLocale } from '@/i18n/locale';
import { server } from '@/test/msw-server';

function renderChatLayout(path = '/') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<ChatLayout />}>
          <Route path="/" element={<main>Home content<Link to="/devices">Devices</Link></main>} />
          <Route path="/chat/:sessionId" element={<main>Chat content<textarea aria-label="Draft" /><Link to="/">Home</Link></main>} />
        </Route>
        <Route path="/devices" element={<main>Device content<Link to="/">Home</Link></main>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('Chat workspace sessions', () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem('ongrid-locale', 'zh-CN');
    useChatSessions.setState({ sessions: [], loading: false, error: false });
    useUi.setState({ sidebarCollapsed: false, sessionsCollapsed: false });
    server.use(http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [], total: 0 })));
  });

  it('shows all sessions in the dedicated list', async () => {
    const sessions = Array.from({ length: 12 }, (_, index) => ({
      id: String(index + 1),
      user_id: 1,
      title: `测试会话 ${index + 1}`,
    }));
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: sessions, total: sessions.length })),
    );

    renderChatLayout();

    expect(await screen.findByText('测试会话 1')).toBeInTheDocument();

    expect(screen.getByText('测试会话 12')).toBeInTheDocument();
  });

  it('distinguishes a failed initial load from an empty list and retries', async () => {
    server.use(http.get('/api/v1/chat/sessions', () => new HttpResponse(null, { status: 503 })));
    renderChatLayout();
    expect(await screen.findByRole('alert')).toHaveTextContent('会话加载失败，请重试。');
    expect(screen.queryByText('暂无会话')).not.toBeInTheDocument();
    act(() => setLocale('en-US'));
    expect(screen.getByRole('alert')).toHaveTextContent('Could not load sessions. Try again.');
    server.use(http.get('/api/v1/chat/sessions', async () => {
      await delay(50);
      return HttpResponse.json({ items: [] });
    }));
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(screen.getByRole('status')).toHaveTextContent('Loading…');
    expect(await screen.findByText('No sessions yet')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('keeps loaded sessions available when a refresh fails', async () => {
    server.use(http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [{ id: '1', user_id: 1, title: 'Saved session' }] })));
    renderChatLayout();
    await screen.findByRole('link', { name: 'Saved session' });
    server.use(http.get('/api/v1/chat/sessions', () => HttpResponse.error()));
    await act(() => useChatSessions.getState().refresh());
    expect(screen.getByRole('alert')).toHaveTextContent('会话加载失败');
    expect(screen.getByRole('link', { name: 'Saved session' })).toBeInTheDocument();
  });

  it('批量选择并删除会话', async () => {
    const user = userEvent.setup({ skipHover: true });
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

    renderChatLayout();

    await screen.findByText('测试会话 1');

    await user.click(screen.getByRole('button', { name: '批量删除会话' }));

    await user.click(screen.getByRole('checkbox', { name: '选择会话 测试会话 1' }));
    await user.click(screen.getByRole('checkbox', { name: '选择会话 测试会话 3' }));

    await user.click(screen.getByRole('button', { name: '删除 2 条' }));

    await waitFor(() => expect(deleted.sort()).toEqual(['1', '3']));
    expect(screen.queryByRole('dialog', { name: '批量删除会话' })).not.toBeInTheDocument();
  });

  it('keeps the list when opening a chat and closes its mobile disclosure', async () => {
    server.use(http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [{ id: '1', user_id: 1, title: 'First session' }] })));
    renderChatLayout();
    fireEvent.click(screen.getByRole('button', { name: '会话' }));
    expect(screen.getByRole('button', { name: '会话' })).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(await screen.findByRole('link', { name: 'First session' }));
    expect(screen.getByText('Chat content')).toBeInTheDocument();
    expect(screen.getByRole('complementary', { name: '会话' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'First session' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('button', { name: '会话' })).toHaveAttribute('aria-expanded', 'false');
  });

  it('removes the session column outside the home and chat routes', () => {
    renderChatLayout();
    fireEvent.click(screen.getByRole('link', { name: 'Devices' }));
    expect(screen.getByText('Device content')).toBeInTheDocument();
    expect(screen.queryByRole('complementary', { name: '会话' })).not.toBeInTheDocument();
  });

  it('opens the home composer when starting a new chat', async () => {
    server.use(http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [{ id: '1', user_id: 1, title: 'Existing session' }] })));
    renderChatLayout('/chat/1');
    await screen.findByRole('link', { name: 'Existing session' });

    fireEvent.click(screen.getByRole('link', { name: '新建会话' }));

    expect(screen.getByText('Home content')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Existing session' })).not.toHaveAttribute('aria-current');
  });

  it('collapses and restores the session list without resetting the open chat', () => {
    renderChatLayout('/chat/1');
    const draft = screen.getByRole('textbox', { name: 'Draft' });
    fireEvent.change(draft, { target: { value: 'Keep this draft' } });

    fireEvent.click(screen.getByRole('button', { name: '收起会话栏' }));
    expect(screen.getByRole('button', { name: '展开会话栏' })).toHaveAttribute('aria-expanded', 'false');
    expect(draft).toHaveValue('Keep this draft');
    expect(screen.getByText('Chat content')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '展开会话栏' }));
    expect(screen.getByRole('button', { name: '收起会话栏' })).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('textbox', { name: 'Draft' })).toBe(draft);
    expect(draft).toHaveValue('Keep this draft');
  });

  it('remembers desktop collapse across routes and storage hydration without changing mobile disclosure', async () => {
    renderChatLayout();
    fireEvent.click(screen.getByRole('button', { name: '收起会话栏' }));
    const saved = localStorage.getItem('ongrid.ui')!;
    expect(JSON.parse(saved).state).toEqual({ sidebarCollapsed: false, sessionsCollapsed: true });
    fireEvent.click(screen.getByRole('link', { name: 'Devices' }));
    fireEvent.click(screen.getByRole('link', { name: 'Home' }));
    expect(screen.getByRole('button', { name: '展开会话栏' })).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(screen.getByRole('button', { name: '会话' }));
    expect(screen.getByRole('button', { name: '会话' })).toHaveAttribute('aria-expanded', 'true');
    expect(useUi.getState().sessionsCollapsed).toBe(true);
    act(() => useUi.setState({ sessionsCollapsed: false }));
    localStorage.setItem('ongrid.ui', saved);
    await act(() => useUi.persist.rehydrate());
    expect(screen.getByRole('button', { name: '展开会话栏' })).toHaveAttribute('aria-expanded', 'false');
    expect(useUi.getState().sidebarCollapsed).toBe(false);
  });

  it('returns home after deleting the active session', async () => {
    let sessions = [{ id: '1', user_id: 1, title: 'Active session' }];
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: sessions })),
      http.delete('/api/v1/chat/sessions/1', () => {
        sessions = [];
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderChatLayout('/chat/1');
    fireEvent.click(await screen.findByRole('button', { name: '会话操作：Active session' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: '删除会话' }));
    fireEvent.click(screen.getByRole('button', { name: '删除' }));
    expect(await screen.findByText('Home content')).toBeInTheDocument();
    expect(await screen.findByText('暂无会话')).toBeInTheDocument();
  });

  it('keeps a failed deletion open, allows cancel, and retries without duplicate requests', async () => {
    let sessions = [{ id: '1', user_id: 1, title: 'Active session' }];
    let attempts = 0;
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: sessions })),
      http.delete('/api/v1/chat/sessions/1', async () => {
        attempts++;
        await delay(60);
        if (attempts === 1) return new HttpResponse(null, { status: 500 });
        sessions = [];
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderChatLayout('/chat/1');
    fireEvent.click(await screen.findByRole('button', { name: '会话操作：Active session' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: '删除会话' }));
    const confirm = screen.getByRole('button', { name: '删除' });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    fireEvent.click(screen.getByRole('button', { name: '关闭' }));
    expect(screen.getByRole('dialog', { name: '删除会话' })).toBeInTheDocument();
    expect(await screen.findByRole('alert')).toHaveTextContent('删除失败，请重试。');
    expect(attempts).toBe(1);
    expect(screen.getByText('Chat content')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '取消' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(attempts).toBe(1);
    fireEvent.click(screen.getByRole('button', { name: '会话操作：Active session' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: '删除会话' }));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '删除' }));
    expect(await screen.findByText('Home content')).toBeInTheDocument();
    expect(attempts).toBe(2);
  });

  it('renames a session from the new list', async () => {
    let title = 'Old title';
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [{ id: '1', user_id: 1, title }] })),
      http.patch('/api/v1/chat/sessions/1', async ({ request }) => {
        title = (await request.json() as { title: string }).title;
        return HttpResponse.json({ id: '1', user_id: 1, title });
      }),
    );
    renderChatLayout();
    fireEvent.click(await screen.findByRole('button', { name: '会话操作：Old title' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: '重命名会话' }));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'New title' } });
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter' });
    expect(await screen.findByRole('link', { name: 'New title' })).toBeInTheDocument();
  });

  it('preserves a failed rename and retries once despite Enter and blur', async () => {
    let title = 'Old title';
    let attempts = 0;
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [{ id: '1', user_id: 1, title }] })),
      http.patch('/api/v1/chat/sessions/1', async ({ request }) => {
        attempts++;
        await delay(60);
        if (attempts === 1) return new HttpResponse(null, { status: 500 });
        title = (await request.json() as { title: string }).title;
        return HttpResponse.json({ id: '1', user_id: 1, title });
      }),
    );
    renderChatLayout();
    fireEvent.click(await screen.findByRole('button', { name: '会话操作：Old title' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: '重命名会话' }));
    const input = screen.getByRole('textbox', { name: '会话名称' });
    fireEvent.change(input, { target: { value: 'New title' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    fireEvent.blur(input);
    expect(await screen.findByRole('alert')).toHaveTextContent('重命名失败');
    expect(attempts).toBe(1);
    expect(input).toHaveValue('New title');
    expect(input).toHaveAttribute('aria-invalid', 'true');
    expect(input).toHaveAccessibleDescription('重命名失败，请重试。');
    await userEvent.click(screen.getByRole('button', { name: '重试' }));
    expect(await screen.findByRole('link', { name: 'New title' })).toBeInTheDocument();
    expect(attempts).toBe(2);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('cancels a failed rename without saving again', async () => {
    let attempts = 0;
    server.use(
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [{ id: '1', user_id: 1, title: 'Old title' }] })),
      http.patch('/api/v1/chat/sessions/1', () => { attempts++; return HttpResponse.error(); }),
    );
    renderChatLayout();
    fireEvent.click(await screen.findByRole('button', { name: '会话操作：Old title' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: '重命名会话' }));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Unsaved title' } });
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter' });
    await screen.findByRole('alert');
    await userEvent.click(screen.getByRole('button', { name: '取消' }));
    expect(screen.getByRole('link', { name: 'Old title' })).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(attempts).toBe(1);
  });

  it('translates the session heading and empty state', async () => {
    renderChatLayout();
    await screen.findByText('暂无会话');
    act(() => setLocale('en-US'));
    expect(screen.getByRole('complementary', { name: 'Sessions' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'New chat' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Collapse sessions' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Collapse sessions' }));
    expect(screen.getByRole('button', { name: 'Expand sessions' })).toBeInTheDocument();
    expect(screen.getByText('No sessions yet')).toBeInTheDocument();
  });

  it('omits timestamps and only labels specialized agents', async () => {
    server.use(http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [
      { id: '1', user_id: 1, title: 'Default session', agent_id: 'default', updated_at: new Date().toISOString() },
      { id: '2', user_id: 1, title: 'Specialist session', agent_id: 'specialist-sre' },
    ] })));
    renderChatLayout();

    expect(await screen.findByRole('link', { name: 'Default session' })).toBeInTheDocument();
    expect(screen.queryByText('默认助理')).not.toBeInTheDocument();
    expect(screen.queryByText('刚刚')).not.toBeInTheDocument();
    expect(screen.getByText('SRE 专家')).toBeInTheDocument();
  });
});
