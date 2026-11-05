import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation, useNavigate } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AgentSidePanel } from './AgentSidePanel';
import { AssistantRoute, useAssistantPage } from '@/lib/assistantPage';
import { createSession, getMessages, listModels, listSessions, streamMessage, updateSessionModel } from '@/api/chat';

vi.stubGlobal('ResizeObserver', class {
  observe() {}
  unobserve() {}
  disconnect() {}
});

vi.mock('@/store/chatSessions', () => ({ invalidateChatSessions: vi.fn() }));

vi.mock('@/api/chat', async (load) => ({
  ...await load<typeof import('@/api/chat')>(),
  createSession: vi.fn(),
  getMessages: vi.fn(),
  listSessions: vi.fn(),
  listModels: vi.fn(),
  updateSessionModel: vi.fn(),
  streamMessage: vi.fn(),
}));

function BusinessPage() {
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const incident = pathname === '/alert';
  useAssistantPage(incident ? '告警详情' : '凭据管理', incident ? 'Incident' : 'Credentials',
    incident ? { incident_id: 41, severity: 'critical' } : { credential_count: 3 });
  return <><button onClick={() => navigate('/settings/secrets')}>switch page</button>
    <input aria-label="secret" type="password" defaultValue="never-send-this" /></>;
}

function Console() {
  const location = useLocation();
  return <AssistantRoute.Provider value={location.key}><BusinessPage/><AgentSidePanel/></AssistantRoute.Provider>;
}

describe('global assistant', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listModels).mockResolvedValue({
      providers: [{ id: 'provider', label: 'Provider', models: ['fast', 'pro'] }],
      default: { provider: 'provider', model: 'fast' },
    });
    vi.mocked(streamMessage).mockImplementation(async (_sid, _question, callbacks) => {
      callbacks.onDone?.({ assistant_message: { content: 'finished' } } as Parameters<NonNullable<typeof callbacks.onDone>>[0]);
    });
  });
  it('pins the confirmed page for follow-ups and starts a new conversation with only declared facts', async () => {
    vi.mocked(createSession).mockImplementation(async input => ({
      page_context: input.page_context, title: input.title, id: input.related_incident_id ? 'incident-session' : 'settings-session', user_id: 9,
    }));
    render(<MemoryRouter initialEntries={['/alert']}><Console/></MemoryRouter>);
    const launch = screen.getByRole('button', { name: 'AI 助理' });
    fireEvent.click(launch);
    const send = async (q: string, n: number) => {
      const panel = within(await screen.findByRole('dialog', { name: 'Ongrid AI 助理' }));
      fireEvent.change(panel.getByRole('textbox'), { target: { value: q } });
      fireEvent.click(panel.getByRole('button', { name: '发送' }));
      await waitFor(() => expect(streamMessage).toHaveBeenCalledTimes(n));
    };
    expect(createSession).not.toHaveBeenCalled();
    await send('Why?', 1);
    fireEvent.click(screen.getByRole('button', { name: '收起助理' }));
    fireEvent.click(screen.getByText('switch page'));
    fireEvent.click(launch);
    await send('And the severity?', 2);
    expect(createSession).toHaveBeenCalledTimes(1);
    expect(vi.mocked(streamMessage).mock.calls.map(c => c.slice(0, 2))).toEqual([
      ['incident-session', 'Why?'], ['incident-session', 'And the severity?'],
    ]);
    fireEvent.click(screen.getByRole('button', { name: '基于当前页新聊' }));
    await send('How many?', 3);
    const request = vi.mocked(createSession).mock.lastCall?.[0];
    expect(request?.page_context).toEqual({ page: '凭据管理', fields: { credential_count: '3' } });
    expect(request?.related_incident_id).toBeUndefined();
    expect(JSON.stringify(request)).not.toContain('never-send-this');
  });

  it('fills a suggestion without posting and restores a saved conversation in place with its own context', async () => {
    const old = { id: 'saved', user_id: 9, title: '设备磁盘排查', provider: 'provider', model: 'pro', page_context: { page: '设备详情', fields: { device_id: '7' } } };
    vi.mocked(listSessions).mockResolvedValue({ items: [old], total: 1 });
    vi.mocked(getMessages).mockRejectedValueOnce(new Error('消息读取失败')).mockResolvedValue({
      items: [{ id: 'a', role: 'user', content: '磁盘还有多少空间？' }, { id: 'b', role: 'assistant', content: '剩余 17 GB。' }], total: 2,
    });
    render(<MemoryRouter initialEntries={['/alert']}><Console/></MemoryRouter>);
    fireEvent.click(screen.getByRole('button', { name: 'AI 助理' }));
    const panel = within(await screen.findByRole('dialog', { name: 'Ongrid AI 助理' }));
    fireEvent.click(panel.getByRole('button', { name: /了解当前页/ }));
    const question = '这条告警是怎么触发的？';
    expect(panel.getByRole('textbox')).toHaveValue(question);
    expect(createSession).not.toHaveBeenCalled();
    expect(streamMessage).not.toHaveBeenCalled();
    const history = () => fireEvent.click(panel.getByRole('button', { name: '会话历史' }));
    const select = async () => fireEvent.click(await panel.findByRole('button', { name: /设备磁盘排查/ }));
    history(); await select();
    fireEvent.click(await screen.findByRole('button', { name: '取消' }));
    fireEvent.click(panel.getByRole('button', { name: '返回对话' }));
    expect(panel.getByRole('textbox')).toHaveValue(question);
    expect(getMessages).not.toHaveBeenCalled();
    history(); await select();
    fireEvent.click(await screen.findByRole('button', { name: '确认' }));
    expect(await panel.findByRole('alert')).toHaveTextContent('消息读取失败');
    fireEvent.click(panel.getByRole('button', { name: '返回对话' }));
    expect(panel.getByRole('textbox')).toHaveValue(question);
    history(); await select();
    fireEvent.click(await screen.findByRole('button', { name: '确认' }));
    expect(await panel.findByText('剩余 17 GB。')).toBeVisible();
    expect(panel.getByText('磁盘还有多少空间？')).toBeVisible();
    expect(panel.getByText('设备详情')).toBeVisible();
    expect(panel.getByLabelText('选择模型')).toHaveTextContent('pro');
    expect(panel.queryByRole('button', { name: /了解当前页/ })).not.toBeInTheDocument();
    fireEvent.change(panel.getByRole('textbox'), { target: { value: '需要扩容吗？' } });
    fireEvent.click(panel.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(vi.mocked(streamMessage).mock.lastCall?.slice(0, 2)).toEqual(['saved', '需要扩容吗？']));
    expect(vi.mocked(streamMessage).mock.lastCall?.[3]).toMatchObject({ provider: 'provider', model: 'pro', webSearchEnabled: false });
    expect(createSession).not.toHaveBeenCalled();
  });

  it('pins a model on creation and only uses a new model after the session update succeeds', async () => {
    const user = userEvent.setup();
    vi.mocked(createSession).mockImplementation(async ({ title, model, provider, page_context }) => ({
      id: 's1', user_id: 9, title, model, provider, page_context,
    }));
    vi.mocked(updateSessionModel).mockRejectedValueOnce(new Error('无法保存模型')).mockResolvedValue(undefined);
    render(<MemoryRouter initialEntries={['/alert']}><Console/></MemoryRouter>);
    fireEvent.click(screen.getByRole('button', { name: 'AI 助理' }));
    const panel = within(await screen.findByRole('dialog', { name: 'Ongrid AI 助理' }));
    const model = await panel.findByLabelText('选择模型');
    async function pick(name: string) {
      await act(async () => user.click(model));
      const option = await screen.findByRole('option', { name });
      await act(async () => user.click(option));
    }
    expect(model).toHaveTextContent('fast');
    await pick('pro');
    fireEvent.click(panel.getByRole('switch', { name: '开启联网搜索' }));
    fireEvent.change(panel.getByLabelText('你的问题'), { target: { value: 'first' } });
    fireEvent.click(panel.getByRole('button', { name: '发送' }));
    expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ provider: 'provider', model: 'pro' }));
    await waitFor(() => expect(vi.mocked(streamMessage).mock.lastCall?.[3]).toMatchObject({ model: 'pro', webSearchEnabled: true }));
    fireEvent.change(panel.getByLabelText('你的问题'), { target: { value: 'follow up' } });
    await pick('fast');
    expect(await panel.findByRole('alert')).toHaveTextContent('无法保存模型');
    expect(model).toHaveTextContent('pro');
    expect(panel.getByLabelText('你的问题')).toHaveValue('follow up');
    await pick('fast');
    expect(updateSessionModel).toHaveBeenLastCalledWith('s1', 'provider', 'fast', '');
    expect(model).toHaveTextContent('fast');
    fireEvent.click(panel.getByRole('button', { name: '发送' }));
    expect(vi.mocked(streamMessage).mock.lastCall?.slice(0, 2)).toEqual(['s1', 'follow up']);
    expect(vi.mocked(streamMessage).mock.lastCall?.[3]).toMatchObject({ model: 'fast' });
    expect(createSession).toHaveBeenCalledTimes(1);
  });

  it('creates a provider-default conversation after switching back from the configured high default', async () => {
    const user = userEvent.setup();
    vi.mocked(listModels).mockResolvedValue({
      providers: [{ id: 'provider', label: 'Provider', models: ['fast', 'pro'], reasoning_efforts: { fast: ['low', 'high'] } }],
      default: { provider: 'provider', model: 'fast', reasoning_effort: 'high' },
    });
    vi.mocked(createSession).mockImplementation(async ({ title, provider, model, reasoning_effort, page_context }) => ({
      title, provider, model, reasoning_effort, page_context, id: 'default-session', user_id: 9,
    }));
    render(<MemoryRouter initialEntries={['/alert']}><Console/></MemoryRouter>);
    fireEvent.click(screen.getByRole('button', { name: 'AI 助理' }));
    const panel = within(await screen.findByRole('dialog', { name: 'Ongrid AI 助理' }));
    const model = await panel.findByLabelText('选择模型');
    expect(panel.getByLabelText('推理级别')).toHaveTextContent('High');
    for (const name of ['pro', 'fast']) {
      await act(async () => user.click(model));
      const option = await screen.findByRole('option', { name });
      await act(async () => user.click(option));
    }
    expect(panel.getByLabelText('推理级别')).toHaveTextContent('Default');
    fireEvent.change(panel.getByLabelText('你的问题'), { target: { value: 'test default' } });
    fireEvent.click(panel.getByRole('button', { name: '发送' }));
    await waitFor(() => expect(streamMessage).toHaveBeenCalled());
    expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ provider: 'provider', model: 'fast', reasoning_effort: '' }));
    expect(vi.mocked(streamMessage).mock.lastCall?.[3]).toMatchObject({ reasoning_effort: '' });
  });
});
