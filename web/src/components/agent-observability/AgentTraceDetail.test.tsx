import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { load } from 'js-yaml';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import type { AgentStep, AgentTrace } from '@/api/agent-observability';
import { AgentTraceDetail } from './AgentTraceDetail';
import { AttributeTable, ContentView, CopyButton, RunStatus } from './AgentParts';
import {
  identityKey,
  inspectionSteps,
  messageContent,
  stepRows,
  tokens,
} from './model';
import { environmentScript } from './guides';
import { serializeContent, structuredContent } from './content-format';

const identity = {
  service_name: 'agent',
  service_namespace: 'support',
  environment: 'test',
};
const usage = {
  input_tokens: null,
  output_tokens: null,
  total_tokens: null,
  cached_tokens: null,
  reported_cost: null,
  conflict: false,
  complete: false,
};
function step(
  span_id: string,
  parent_span_id = '',
  extra: Partial<AgentStep> = {},
): AgentStep {
  return {
    span_id,
    parent_span_id,
    name: span_id,
    kind: 'span',
    sdk: 'otel',
    identity,
    start: '2026-10-14T00:00:00Z',
    duration_ms: 10,
    status: 'ok',
    model: '',
    provider: '',
    session_id: '',
    input: { state: 'not_recorded' },
    output: { state: 'not_recorded' },
    usage,
    scope: { name: 'custom', version: '1' },
    attributes: {},
    resource: {},
    events: [],
    links: [],
    issues: [],
    raw: {},
    ...extra,
  };
}

describe('Agent trace inspection', () => {
  it.each([
    ['ok', 0, ''],
    ['unset', 0, ''],
    ['error', 0, '失败'],
    ['ok', 2, '2 个失败步骤'],
    ['unset', 1, '1 个失败步骤'],
  ] as const)('shows only actionable status for %s with %s failed steps', (status, errors, text) => {
    const { container } = render(<RunStatus status={status} errors={errors} />);
    expect(container.textContent).toBe(text);
    if (text) expect(container.querySelector('[data-slot="badge"]')).toHaveAttribute('data-tone', status === 'error' ? 'danger' : 'warning');
    else expect(container).toBeEmptyDOMElement();
  });

  it('separates run totals from model usage and preserves missing values and zero', () => {
    const root = step('root', '', { kind: 'agent' });
    const model = step('model', 'root', {
      kind: 'llm',
      usage: { ...usage, input_tokens: 1000, output_tokens: 0, total_tokens: 1000, cached_tokens: 200, complete: true },
    });
    const trace: AgentTrace = {
      trace_id: '1'.repeat(32), issues: [], steps: [root, model],
      runs: [{
        trace_id: '1'.repeat(32), entry_span_id: 'root', identity,
        name: 'Multiple model calls', sdks: ['eino'], start: root.start,
        duration_ms: 20, status: 'ok', models: [], kinds: ['agent', 'llm'],
        session_id: '', step_count: 3, error_count: 0, input_preview: '',
        output_preview: '', content_available: false, issues: [],
        usage: { ...usage, input_tokens: 1200, output_tokens: 30, total_tokens: 1230, complete: true },
      }],
    };
    const view = () => <MemoryRouter><AgentTraceDetail trace={trace} selectedSpanID="model" onSelectSpan={vi.fn()} onSelectRun={vi.fn()} /></MemoryRouter>;
    const { container, rerender } = render(view());
    const facts = container.querySelector('.agent-run-facts')!;
    expect(facts).toHaveTextContent('输入 Token 1,200');
    expect(facts).toHaveTextContent('输出 Token 30');
    expect(facts).toHaveTextContent('总 Token 1,230');
    const stepUsage = container.querySelector('.agent-step-usage')!;
    expect(stepUsage).toHaveTextContent('输入 Token1,000');
    expect(stepUsage).toHaveTextContent('输出 Token0');
    expect(stepUsage).toHaveTextContent('总 Token1,000');
    expect(stepUsage).toHaveTextContent('缓存命中（计入输入）200');
    trace.runs[0].usage = { ...usage, input_tokens: 0 };
    model.usage = usage;
    rerender(view());
    expect(facts).toHaveTextContent('输入 Token 0');
    expect(facts).toHaveTextContent('输出 Token —');
    expect(facts).toHaveTextContent('总 Token —');
    expect(stepUsage).toHaveTextContent('输入 Token—');
    expect(stepUsage).toHaveTextContent('输出 Token—');
  });

  it('keeps the main execution path connected while retaining errors and direct links', () => {
    const steps = [
      step('http'),
      step('agent', 'http', { kind: 'agent' }),
      step('graph', 'agent'),
      step('model', 'graph', { kind: 'llm' }),
      step('db-error', 'model', { status: 'error' }),
      step('db', 'model'),
    ];
    const main = inspectionSteps(steps, false, new Set(['agent']));
    expect(main.map((s) => [s.span_id, s.parent_span_id])).toEqual([
      ['agent', ''],
      ['model', 'agent'],
      ['db-error', 'model'],
    ]);
    expect(inspectionSteps(steps, true, new Set())).toBe(steps);
    expect(
      inspectionSteps(steps, false, new Set(['db'])).some(
        (s) => s.span_id === 'db',
      ),
    ).toBe(true);
    expect(steps[3].parent_span_id).toBe('graph');
  });
  it('collapses descendants but keeps matching failed steps reachable through search', () => {
    const steps = [
      step('root'),
      step('tool', 'root', { status: 'error' }),
      step('model', 'root', { kind: 'llm', model: 'Fixture-Model' }),
      step('detached', 'missing'),
    ];
    expect(
      stepRows(steps, new Set(['root']), '', false).map(
        ({ step }) => step.span_id,
      ),
    ).toEqual(['root', 'detached']);
    expect(
      stepRows(steps, new Set(['root']), '', true).map(
        ({ step }) => step.span_id,
      ),
    ).toEqual(['tool']);
    expect(
      stepRows(steps, new Set(['root']), 'fixture-model', false).map(
        ({ step }) => step.span_id,
      ),
    ).toEqual(['model']);
    expect(tokens(null)).toBe('—');
    expect(tokens(0)).toBe('0');
    expect(
      identityKey({ ...identity, service_name: 'a:b', service_namespace: 'c' }),
    ).not.toBe(
      identityKey({ ...identity, service_name: 'a', service_namespace: 'b:c' }),
    );
  });

  it('keeps a recovered tool failure separate from the run status and selects steps', () => {
    const root = step('root', '', { kind: 'workflow' });
    const tool = step('tool', 'root', { status: 'error' });
    const trace: AgentTrace = {
      trace_id: '1'.repeat(32),
      issues: [],
      steps: [root, tool, step('DB SELECT devices', 'tool')],
      runs: [
        {
          trace_id: '1'.repeat(32),
          entry_span_id: 'root',
          identity,
          name: 'Recovered run',
          sdks: ['openinference'],
          start: root.start,
          duration_ms: 10,
          status: 'ok',
          models: [],
          kinds: ['workflow', 'tool'],
          session_id: 'session',
          step_count: 2,
          error_count: 1,
          input_preview: '',
          output_preview: '',
          content_available: false,
          usage,
          issues: [],
          chat_session_id: 'owned-chat',
        },
      ],
    };
    const onSelectSpan = vi.fn();
    render(
      <MemoryRouter>
        <AgentTraceDetail
          trace={trace}
          onSelectSpan={onSelectSpan}
          onSelectRun={vi.fn()}
        />
      </MemoryRouter>,
    );
    expect(screen.queryByText('成功')).not.toBeInTheDocument();
    expect(screen.getByText('1 个失败步骤')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '打开原对话' })).toHaveAttribute(
      'href',
      '/chat/owned-chat',
    );
    expect(
      screen.queryByRole('button', { name: /^DB SELECT devices/ }),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '内部步骤' }));
    expect(
      screen.getByRole('button', { name: /^DB SELECT devices/ }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '内部步骤' }));
    expect(
      screen.getByTitle('入口未报告失败，但包含失败步骤'),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /tool/ }));
    expect(onSelectSpan).toHaveBeenCalledWith('tool');
    fireEvent.click(screen.getByRole('button', { name: '下一步' }));
    expect(onSelectSpan).toHaveBeenLastCalledWith('tool');
    expect(screen.getByRole('button', { name: '上一步' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: '瀑布视图' }));
    expect(screen.getByRole('button', { name: '瀑布视图' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    fireEvent.click(screen.getByRole('button', { name: '树状视图' }));
    fireEvent.click(screen.getByRole('button', { name: '收起 root' }));
    expect(
      screen.queryByRole('button', { name: /^tool/ }),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '仅失败步骤' }));
    expect(screen.getByRole('button', { name: /^tool/ })).toBeInTheDocument();
    const details = within(screen.getByRole('region', { name: '步骤详情' }));
    expect(details.getByRole('region', { name: '输入' })).toBeInTheDocument();
    expect(details.getByRole('region', { name: '输出' })).toBeInTheDocument();
    expect(details.getByRole('region', { name: '属性' })).toBeInTheDocument();
    fireEvent.click(details.getByRole('button', { name: '收起所有详情' }));
    expect(details.getByRole('button', { name: '属性' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
    const scrollIntoView = vi.fn();
    const attributes = details.getByRole('region', {
      name: '属性',
    }).parentElement!;
    attributes.scrollIntoView = scrollIntoView;
    fireEvent.click(details.getByRole('button', { name: '定位到属性' }));
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'start' });
    expect(details.getByRole('button', { name: '属性' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    expect(details.getByRole('button', { name: '输入' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
  });

  it('reads explicit message envelopes and keeps application fields separate', () => {
    const value = {
      messages: [
        [
          {
            lc: 1,
            type: 'constructor',
            kwargs: { type: 'human', content: 'Question' },
          },
        ],
      ],
      call_count: 0,
    };
    const parsed = messageContent(value);
    expect(parsed.messages?.[0]).toMatchObject({
      role: 'user',
      content: 'Question',
    });
    expect(parsed.fields).toEqual({ call_count: 0 });
    expect(
      messageContent({ question: 'Keep this application payload' }).messages,
    ).toBeNull();
    expect(
      messageContent([
        { role: 'user', content: 'first' },
        { unknown: 'second' },
      ]).messages,
    ).toBeNull();
  });

  it('switches all four formats without losing message roles, extra fields or scalar types', async () => {
    const user = userEvent.setup();
    const value = {
      messages: [
        { role: 'user', content: '**Question**' },
        {
          role: 'assistant',
          content: 'Answer',
          tool_calls: [{ id: 'tool-42' }],
        },
      ],
      call_count: 0,
      enabled: false,
      code: '001',
      missing: null,
    };
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    render(
      <ContentView
        title="输入"
        content={{
          state: 'available',
          value,
        }}
      />,
    );
    const input = within(screen.getByRole('region', { name: '输入' }));
    expect(input.getByText('Question').tagName).toBe('STRONG');
    expect(input.getByText('用户')).toBeInTheDocument();
    expect(input.getByText('助手')).toBeInTheDocument();
    expect(input.getAllByText('附加字段')).toHaveLength(2);
    const chooseFormat = async (label: string) => {
      await user.click(input.getByRole('button', { name: '输入格式' }));
      const option = await screen.findByRole('menuitem', { name: label });
      await user.click(option);
    };
    await chooseFormat('Plain');
    expect(input.getByText('**Question**').tagName).toBe('PRE');
    expect(input.getByText('用户')).toBeInTheDocument();
    await chooseFormat('JSON');
    expect(JSON.parse(input.getByText(/"call_count": 0/).textContent!)).toEqual(
      value,
    );
    expect(input.queryByText('用户')).not.toBeInTheDocument();
    await chooseFormat('YAML');
    const yaml = input.getByText(/call_count: 0/).textContent!;
    expect(load(yaml)).toEqual(value);
    await user.click(input.getByRole('button', { name: '复制输入' }));
    expect(writeText).toHaveBeenLastCalledWith(yaml);
    await chooseFormat('Markdown');
    expect(input.getByText('Question').tagName).toBe('STRONG');
    fireEvent.click(input.getByRole('button', { name: '输入 2' }));
    expect(input.queryByText('Question')).not.toBeInTheDocument();
    writeText.mockRestore();
  });

  it('serializes root strings, multiline text, empty collections and YAML-like scalars without reinterpretation', () => {
    for (const value of [
      '**text**\nwith newline',
      'true',
      '2026-10-14',
      '',
      null,
      0,
      false,
      [],
      {},
      { 'a:b': ['001', true, null, { value: '\u4e2d\u6587' }] },
    ]) {
      expect(JSON.parse(serializeContent(value, 'json'))).toEqual(value);
      expect(load(serializeContent(value, 'yaml'))).toEqual(value);
    }
  });

  it('formats JSON text reported by tools and copies the selected representation', async () => {
    const user = userEvent.setup();
    const value = { event_type: 'Warning', limit: 50, events: [{ count: 0, active: false }], code: '001' };
    const raw = JSON.stringify(value);
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    const { rerender } = render(<ContentView title="输出" content={{ state: 'available', value: raw }} />);
    const section = screen.getByRole('region', { name: '输出' });
    const view = within(section);
    const choose = async (name: string) => {
      await user.click(view.getByRole('button', { name: '输出格式' }));
      await user.click(await screen.findByRole('menuitem', { name }));
    };
    expect(section.querySelector('pre')?.textContent).toBe(JSON.stringify(value, null, 2));
    for (const format of ['JSON', 'YAML', 'Plain', 'Markdown']) {
      await choose(format);
      const text = section.querySelector('pre')!.textContent!;
      if (format === 'YAML') expect(load(text)).toEqual(value);
      else if (format === 'Plain') expect(text).toBe(raw);
      else expect(text).toBe(JSON.stringify(value, null, 2));
      await user.click(view.getByRole('button', { name: '复制输出' }));
      expect(writeText).toHaveBeenLastCalledWith(text);
    }
    rerender(<ContentView title="输出" content={{ state: 'available', value: raw, truncated: true }} />);
    await choose('JSON');
    expect(JSON.parse(section.querySelector('pre')!.textContent!)).toBe(raw);
    expect(view.getByText(/采集时已截断/)).toBeInTheDocument();
    writeText.mockRestore();
  });

  it('recognizes JSON objects and message arrays without changing text, scalars or incomplete data', () => {
    const messages = [{ role: 'user', content: '**Question**' }, { role: 'assistant', content: 'Answer' }];
    expect(messageContent(structuredContent(JSON.stringify(messages))).messages).toEqual(messages);
    for (const value of [{ count: 0 }, [], messages]) {
      expect(structuredContent(` ${JSON.stringify(value)}\n`)).toEqual(value);
      expect(structuredContent(value)).toBe(value);
    }
    for (const value of ['true', '001', 'null', '2026-10-14', '**text**', '{"cut":', '[1,', '{"id":9007199254740993}', '{"n":1e400}']) {
      expect(structuredContent(value)).toBe(value);
    }
  });

  it('keeps long metadata collapsed and preserves complete values for inspection and copy', async () => {
    const user = userEvent.setup();
    const text = 'long-field-'.repeat(60) + '\nlast line';
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    render(
      <AttributeTable values={{ prompt: text, enabled: false, count: 0 }} />,
    );
    expect(screen.getByText('false')).toBeInTheDocument();
    expect(screen.getByText('0')).toBeInTheDocument();
    const details = screen
      .getByText(
        (_, element) =>
          element?.tagName === 'PRE' && element.textContent === text,
      )
      .closest('details')!;
    expect(details).not.toHaveAttribute('open');
    await user.click(details.querySelector('summary')!);
    expect(details).toHaveAttribute('open');
    await user.click(screen.getByRole('button', { name: '复制 prompt' }));
    expect(writeText).toHaveBeenLastCalledWith(text);
    writeText.mockRestore();
  });

  it('does not load remote media or permit script links in rendered trace content', () => {
    const { container } = render(
      <ContentView
        title="输出"
        content={{
          state: 'available',
          value:
            '![private](https://example.com/track.png)\n\n[unsafe](javascript:alert(1))',
        }}
      />,
    );
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('a')).not.toHaveAttribute(
      'href',
      expect.stringContaining('javascript:'),
    );
    expect(screen.getByText('[图片: private]')).toBeInTheDocument();
  });

  it('renders payload text without HTML execution and states missing capture honestly', () => {
    const { container, rerender } = render(
      <ContentView
        title="输入"
        content={{ state: 'available', value: '<img src=x onerror=alert(1)>' }}
      />,
    );
    expect(container.querySelector('img')).toBeNull();
    expect(
      screen.getByText('<img src=x onerror=alert(1)>'),
    ).toBeInTheDocument();
    rerender(<ContentView title="输入" content={{ state: 'not_recorded' }} />);
    expect(
      screen.getByText('SDK 未提供这部分内容，无法判断是否未采集或已脱敏。'),
    ).toBeInTheDocument();
  });

  it('reports clipboard failure when the API is unavailable', async () => {
    vi.stubGlobal('navigator', { clipboard: undefined });
    try {
      render(<CopyButton value="payload" label="复制内容" />);
      fireEvent.click(screen.getByRole('button', { name: '复制内容' }));
      expect(await screen.findByText('复制失败')).toBeInTheDocument();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('distinguishes a captured preview from complete content and copies only that preview', async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, 'writeText');
    render(<ContentView title="输出" content={{ state: 'available', value: 'retained preview', source: 'langfuse.observation.output', truncated: true, original_bytes: 120000, omitted: ['system_prompt', 'earlier_messages', 'credentials'] }} />);
    expect(screen.getByText(/采集时已截断/)).toHaveTextContent('120,000 B');
    expect(screen.getByText(/采集时已处理/)).toHaveTextContent('系统提示词仅保留长度和哈希');
    expect(screen.queryByText(/完整内容见原对话/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '复制输出预览' }));
    expect(writeText).toHaveBeenLastCalledWith('retained preview');
    writeText.mockRestore();
  });

  it('quotes shell values without evaluating them', () => {
    expect(environmentScript({ OTEL_SERVICE_NAME: "it's $(test) `id`" })).toBe(
      "export OTEL_SERVICE_NAME='it'\\''s $(test) `id`'",
    );
  });
});
