import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import {
  queryAgentRuns,
  type AgentList,
  type AgentRun,
} from '@/api/agent-observability';
import AgentObservability from './AgentObservability';

vi.mock('@/api/agent-observability', () => ({
  queryAgentRuns: vi.fn(),
  getAgentTrace: vi.fn(),
  getAgentIngestion: vi.fn(),
}));

const identity = {
  service_name: 'agent',
  service_namespace: 'support',
  environment: 'test',
};
const run: AgentRun = {
  trace_id: '1'.repeat(32),
  entry_span_id: '1'.repeat(16),
  identity,
  name: 'Sample run',
  sdks: ['langsmith'],
  start: '2026-10-14T00:00:00Z',
  duration_ms: 12,
  status: 'ok',
  models: [],
  kinds: ['workflow'],
  session_id: 'thread-a',
  step_count: 1,
  error_count: 0,
  input_preview: 'question',
  output_preview: 'answer',
  content_available: true,
  usage: {
    input_tokens: null,
    output_tokens: null,
    total_tokens: null,
    cached_tokens: null,
    reported_cost: null,
    conflict: false,
    complete: false,
  },
  issues: [],
};
const data: AgentList = {
  items: [run],
  sessions: [
    {
      session_id: 'thread-a',
      identity,
      run_count: 1,
      error_count: 0,
      first_seen: run.start,
      last_seen: run.start,
      input_preview: 'question',
      output_preview: 'answer',
    },
  ],
  facets: { applications: [identity], sdks: ['langsmith'], models: [] },
  checks: [],
  metadata: {
    start: run.start,
    end: run.start,
    candidate_limit: 50,
    candidate_traces: 1,
    read_traces: 1,
    partial: false,
    reasons: [],
    failures: [],
    sampling: 'unknown',
  },
};

function LocationProbe() {
  return <output data-testid="location">{useLocation().search}</output>;
}

describe('Agent observability queries', () => {
  it('hides unset status in session turns while keeping explicit failures and failed-step counts', async () => {
    vi.mocked(queryAgentRuns).mockReset().mockResolvedValue({
      ...data,
      items: [
        { ...run, name: 'Normal turn', status: 'unset' },
        { ...run, trace_id: '2'.repeat(32), name: 'Recovered turn', status: 'unset', error_count: 2 },
        { ...run, trace_id: '3'.repeat(32), name: 'Failed turn', status: 'error', error_count: 1 },
      ],
    });
    const { container } = render(<MemoryRouter initialEntries={['/agent-observability?tab=sessions&session_id=thread-a&session_service_name=agent&session_service_namespace=support&session_environment=test']}><AgentObservability /></MemoryRouter>);
    await screen.findByRole('link', { name: 'Normal turn' });
    expect(container.querySelectorAll('.agent-session-turn')).toHaveLength(3);
    expect(screen.queryByText('未设置状态')).not.toBeInTheDocument();
    expect(screen.queryByText('成功')).not.toBeInTheDocument();
    expect(screen.getByText('2 个失败步骤')).toBeInTheDocument();
    expect(screen.getByText('失败')).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: '执行详情' })).toHaveLength(3);
  });

  it.each([false, true])('preserves the application filter through session navigation (manually filtered: %s)', async (filtered) => {
    const otherIdentity = { ...identity, service_name: 'other-agent' };
    const initial = new URLSearchParams({ tab: 'runs', start: '2026-10-14T00:00:00Z', end: '2026-10-14T01:00:00Z' });
    if (filtered) for (const [key, value] of Object.entries(identity)) initial.set(key, value);
    vi.mocked(queryAgentRuns).mockReset().mockImplementation(async (query) => ({
      ...data,
      sessions: query.has('service_name') ? data.sessions : [
        ...data.sessions,
        { ...data.sessions[0], identity: otherIdentity },
      ],
      facets: { ...data.facets, applications: [identity, otherIdentity] },
    }));
    render(<MemoryRouter initialEntries={[`/agent-observability?${initial}`]}><AgentObservability /><LocationProbe /></MemoryRouter>);
    await screen.findByRole('link', { name: 'Sample run' });
    fireEvent.click(screen.getByRole('tab', { name: /^会话/ }));
    const before = new URLSearchParams(screen.getByTestId('location').textContent!);
    expect(before.get('service_name')).toBe(filtered ? identity.service_name : null);
    expect(queryAgentRuns).toHaveBeenCalledTimes(1);
    const links = screen.getAllByRole('link', { name: 'thread-a' });
    expect(links).toHaveLength(filtered ? 1 : 2);
    fireEvent.click(links[filtered ? 0 : 1]);
    await screen.findByRole('button', { name: '全部会话' });
    const selected = filtered ? identity : otherIdentity;
    const detail = new URLSearchParams(screen.getByTestId('location').textContent!);
    expect(detail.get('service_name')).toBe(filtered ? identity.service_name : null);
    expect(detail.get('session_service_name')).toBe(selected.service_name);
    expect(screen.getByRole('combobox', { name: '应用范围' })).toHaveTextContent(filtered ? 'agent · support · test' : '全部应用');
    expect(Object.fromEntries(vi.mocked(queryAgentRuns).mock.calls.at(-1)![0])).toMatchObject({ ...selected, session_id: 'thread-a' });
    fireEvent.click(screen.getByRole('button', { name: '全部会话' }));
    await waitFor(() => expect(screen.getAllByRole('link', { name: 'thread-a' })).toHaveLength(filtered ? 1 : 2));
    const back = new URLSearchParams(screen.getByTestId('location').textContent!);
    expect(back.get('service_name')).toBe(filtered ? identity.service_name : null);
    expect([...back.keys()].some(key => key.startsWith('session_'))).toBe(false);
    expect(back.get('start')).toBe(initial.get('start'));
    expect(back.get('end')).toBe(initial.get('end'));
    fireEvent.click(screen.getByRole('tab', { name: /^运行/ }));
    expect(new URLSearchParams(screen.getByTestId('location').textContent!).get('service_name')).toBe(filtered ? identity.service_name : null);
  });

  it('shows input and output counts independently, preserving zero, missing and conflicting usage', async () => {
    vi.mocked(queryAgentRuns).mockReset().mockResolvedValue({
      ...data,
      items: [
        { ...run, name: 'Complete usage', usage: { ...run.usage, input_tokens: 1234, output_tokens: 56, total_tokens: 1290, cached_tokens: 100, complete: true } },
        { ...run, trace_id: '2'.repeat(32), name: 'Partial usage', usage: { ...run.usage, input_tokens: 0, total_tokens: 42 } },
        { ...run, trace_id: '3'.repeat(32), name: 'Conflicting usage', usage: { ...run.usage, conflict: true } },
      ],
    });
    render(<MemoryRouter><AgentObservability /></MemoryRouter>);
    await screen.findByRole('link', { name: 'Complete usage' });
    expect(screen.getByRole('columnheader', { name: '输入 Token' })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: '输出 Token' })).toBeInTheDocument();
    const cells = (name: string) => within(screen.getByRole('link', { name }).closest('tr')!).getAllByRole('cell');
    expect(cells('Complete usage')[4]).toHaveTextContent('1,234');
    expect(cells('Complete usage')[5]).toHaveTextContent('56');
    expect(cells('Complete usage')[4]).toHaveAttribute('title', '总 Token：1,290');
    expect(cells('Partial usage')[4]).toHaveTextContent(/^0$/);
    expect(cells('Partial usage')[5]).toHaveTextContent(/^—$/);
    expect(cells('Partial usage')[5]).toHaveAttribute('title', '总 Token：42');
    expect(cells('Conflicting usage')[4]).toHaveAttribute('title', '用量冲突，未汇总');
    expect(cells('Conflicting usage')[4]).toHaveTextContent(/^—$/);
    expect(screen.getByLabelText('用量待核对')).toBeInTheDocument();
  });

  it('removes the status column and filter while preserving actionable error badges', async () => {
    const normal = Array.from({ length: 20 }, (_, i) => ({
      ...run,
      trace_id: String(i).padStart(32, '0'),
      name: `Normal ${i}`,
      status: i === 0 ? 'ok' : 'unset',
    }));
    vi.mocked(queryAgentRuns).mockReset().mockResolvedValue({
      ...data,
      items: [
        ...normal,
        { ...run, trace_id: 'a'.repeat(32), name: 'Failed run', status: 'error', error_count: 1 },
        { ...run, trace_id: 'b'.repeat(32), name: 'Recovered run', status: 'ok', error_count: 2 },
        { ...run, trace_id: 'c'.repeat(32), name: 'Unset with errors', status: 'unset', error_count: 1 },
      ],
    });
    render(<MemoryRouter><AgentObservability /></MemoryRouter>);
    await screen.findByRole('link', { name: 'Normal 0' });
    expect(screen.queryByRole('columnheader', { name: '状态' })).not.toBeInTheDocument();
    expect(screen.getAllByRole('columnheader')).toHaveLength(8);
    expect(screen.queryByRole('combobox', { name: '运行状态' })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: '仅看异常' })).not.toBeInTheDocument();
    expect(screen.queryByText('未设置状态')).not.toBeInTheDocument();
    expect(screen.queryByText('成功')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(screen.getByText('失败')).toBeInTheDocument();
    expect(screen.getByText('2 个失败步骤')).toBeInTheDocument();
    expect(screen.getByText('1 个失败步骤')).toBeInTheDocument();
    for (const name of ['Failed run', 'Recovered run', 'Unset with errors']) {
      expect(screen.getByRole('link', { name })).toBeInTheDocument();
    }
    expect(screen.queryByRole('link', { name: /^Normal / })).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('searchbox', { name: '搜索运行' }), { target: { value: 'not found' } });
    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }));
    expect(screen.getByRole('link', { name: 'Normal 0' })).toBeInTheDocument();
  });

  it('uses captured turn text as the run title and explains unavailable content', async () => {
    vi.mocked(queryAgentRuns)
      .mockReset()
      .mockResolvedValue({
        ...data,
        items: [
          {
            ...run,
            name: 'invoke_agent default',
            input_preview: '我当前设备都正常么',
            output_preview: 'Saved final reply',
            chat_session_id: 'owned-chat',
          },
          {
            ...run,
            trace_id: '2'.repeat(32),
            name: 'Unavailable conversation',
            chat_session_id: 'old-chat',
            input_preview: '',
            output_preview: '',
            input_state: 'not_recorded',
            output_state: 'capture_disabled',
            content_available: false,
          },
        ],
      });
    render(
      <MemoryRouter>
        <AgentObservability />
      </MemoryRouter>,
    );
    expect(
      await screen.findByRole('link', { name: '我当前设备都正常么' }),
    ).toBeInTheDocument();
    expect(screen.getByText('未上报正文')).toBeInTheDocument();
    expect(screen.getByText('未采集正文')).toBeInTheDocument();
    expect(screen.getAllByText('Unavailable conversation')).toHaveLength(1);
    fireEvent.change(screen.getByRole('searchbox', { name: '搜索运行' }), {
      target: { value: 'Saved final reply' },
    });
    expect(
      screen.getByRole('link', { name: '我当前设备都正常么' }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole('link', { name: 'Unavailable conversation' }),
    ).not.toBeInTheDocument();
  });
  it('clears the previous scope while a selected session is loading', async () => {
    let resolve!: (result: AgentList) => void;
    vi.mocked(queryAgentRuns)
      .mockReset()
      .mockResolvedValueOnce(data)
      .mockImplementationOnce(
        () =>
          new Promise((done) => {
            resolve = done;
          }),
      );
    const { container } = render(
      <MemoryRouter initialEntries={['/agent-observability?tab=sessions']}>
        <AgentObservability />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('link', { name: 'thread-a' }));
    await waitFor(() => expect(queryAgentRuns).toHaveBeenCalledTimes(2));
    expect(container.querySelector('.agent-session-turn')).toBeNull();
    expect(screen.getByText('正在检索 Agent 链路…')).toBeInTheDocument();
    const query = vi.mocked(queryAgentRuns).mock.calls[1][0];
    expect(Object.fromEntries(query)).toMatchObject({
      ...identity,
      session_id: 'thread-a',
    });
    await act(async () => resolve(data));
    expect(
      await screen.findByRole('link', { name: 'Sample run' }),
    ).toBeInTheDocument();
    expect(container.querySelectorAll('.agent-session-turn')).toHaveLength(1);
  });

  it('shows query failures and retries without replacing them with an empty result', async () => {
    vi.mocked(queryAgentRuns)
      .mockReset()
      .mockRejectedValueOnce(new Error('Tempo unavailable'))
      .mockResolvedValueOnce(data);
    render(
      <MemoryRouter>
        <AgentObservability />
      </MemoryRouter>,
    );
    expect(await screen.findByText('Tempo unavailable')).toBeInTheDocument();
    expect(screen.queryByText('等待第一条 Agent 链路')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '重试' }));
    expect(
      await screen.findByRole('link', { name: 'Sample run' }),
    ).toBeInTheDocument();
  });
});
