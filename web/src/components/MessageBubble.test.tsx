import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { MessageBubble, type ConfigDraftResult } from './MessageBubble';
import type { ChatMessage } from '@/api/chat';
import { executeOperationAction, getOperation } from '@/api/operations';
import { getPacketCaptureSession } from '@/api/packetCaptures';
import { getApproval } from '@/api/approvals';
import { setLocale } from '@/i18n/locale';
import * as chatApi from '@/api/chat';

vi.mock('@/api/operations', () => ({
  executeOperationAction: vi.fn(),
  getOperation: vi.fn(),
}));
vi.mock('@/api/packetCaptures', () => ({
  getPacketCaptureSession: vi.fn(),
}));
vi.mock('@/api/approvals', () => ({
  getApproval: vi.fn(),
  approveApproval: vi.fn(),
  rejectApproval: vi.fn(),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  setLocale('zh-CN');
});

describe('MessageBubble image preview', () => {
  it('opens a protected message image on click', async () => {
    URL.revokeObjectURL = vi.fn();
    vi.spyOn(chatApi, 'loadChatAttachment').mockResolvedValue('blob:protected-image');
    render(<MessageBubble sessionId="session-1" message={{
      id: 'message-1',
      role: 'user',
      content: '',
      attachments: [{ id: 'attachment-1', name: 'diagram.png', mime_type: 'image/png', size: 123 }],
    }} />);

    const thumbnail = await screen.findByAltText('diagram.png');
    fireEvent.click(thumbnail);
    expect(await screen.findByAltText('Preview diagram.png')).toBeInTheDocument();
  });
});

describe('MessageBubble shared approval card', () => {
  it.each([
    ['zh-CN', '需要你确认才能执行 capture_pcap'],
    ['en-US', 'Needs your approval to run capture_pcap'],
  ] as const)('renders a generic tool proposal in %s', async (locale, heading) => {
    setLocale(locale);
    vi.mocked(getApproval).mockResolvedValue({
      id: 'approval-1',
      kind: 'agent_tool',
      title: 'capture_pcap confirmation',
      summary: 'capture_pcap {"device_id":1,"interface":"eth0"}',
      payload: JSON.stringify({
        tool_name: 'capture_pcap',
        summary: 'capture_pcap {"device_id":1,"interface":"eth0"}',
      }),
      source: 'agent',
      session_id: 'session-1',
      status: 'pending',
      proposed_by: 1,
      created_at: new Date().toISOString(),
    });

    render(<MessageBubble message={{
      id: 'tool-card-approval',
      role: 'tool',
      kind: 'tool_card',
      tool_call: {
        id: 'call-1',
        name: 'capture_pcap',
        status: 'pending',
        result: {
          status: 'pending_approval',
          approval_id: 'approval-1',
          kind: 'agent_tool',
          tool_name: 'capture_pcap',
          command: 'capture_pcap {"device_id":1,"interface":"eth0"}',
        },
      },
    }} />);

    expect(await screen.findByText(heading)).toBeInTheDocument();
    expect(screen.getByText(/capture_pcap \{"device_id":1/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: locale === 'zh-CN' ? '批准并执行' : 'Approve & run' })).toBeInTheDocument();
  });
});

const supportedKinds = [
  'metric_threshold',
  'metric_raw',
  'metric_anomaly',
  'metric_forecast',
  'metric_burn_rate',
  'log_match',
  'log_volume',
  'trace_latency',
  'trace_error_rate',
];

function draftFor(kind: string): ConfigDraftResult {
  return {
    kind: 'config_draft',
    domain: 'alert_rule',
    action: 'create',
    summary: `Create ${kind} rule`,
    payload: {
      action: 'create',
      rule: {
        rule_key: `${kind}_natural_language`,
        kind,
        name: `${kind} from natural language`,
        severity: 'warning',
        spec: specFor(kind),
      },
    },
    preview: {
      fire_count: 2,
      samples: [{ summary: `${kind} sample` }],
    },
    warnings: [`${kind} preview warning`],
    scope: {
      type: 'host',
      label: '主机级',
      reason: '命中后会关联到具体设备。',
      change_hint: '如果要改成全局汇总，可以回复“改成全局”。',
    },
    confirmation_prompt:
      '当前告警范围：主机级。命中后会关联到具体设备。如果要改成全局汇总，可以回复“改成全局”。确认无误后可点击确认应用或回复“ok”。',
    rollback: 'Disable or edit the rule from Alerts.',
    apply_tool: 'apply_config_change',
    draft_hash: `sha256:${kind}`,
  };
}

function specFor(kind: string): Record<string, unknown> {
  switch (kind) {
    case 'metric_raw':
      return {
        expr: '(100 * max(redis_memory_used_bytes) / clamp_min(max(redis_memory_max_bytes), 1)) > 80',
      };
    case 'metric_anomaly':
      return { metric: 'cpu_pct', method: 'zscore', baseline_window: '1h', deviation: 3 };
    case 'metric_forecast':
      return { metric: 'disk_avail_bytes', predict_seconds: 21600, operator: '<=', threshold: 0 };
    case 'metric_burn_rate':
      return {
        sli: 'sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))',
        slo: 99.9,
        burns: [{ window: '1h', multiplier: 14.4 }],
      };
    case 'log_match':
      return { stream_selector: '{ongrid_source=~"journald:.+"}', line_filter: '(?i)error|panic' };
    case 'log_volume':
      return { stream_selector: '{ongrid_source=~".+"}', ratio_op: '>=', ratio_threshold: 2 };
    case 'trace_latency':
      return { service: 'checkout', quantile: 'p95', threshold_ms: 500 };
    case 'trace_error_rate':
      return { service: 'checkout', operator: '>=', threshold_pct: 1 };
    default:
      return {};
  }
}

function toolCardMessage(draft: ConfigDraftResult): ChatMessage {
  return {
    id: `tool-card-${draft.summary}`,
    role: 'tool',
    kind: 'tool_card',
    tool_call: {
      id: `call-${draft.summary}`,
      name: 'draft_config_change',
      status: 'success',
      result: draft,
    },
  };
}

describe('MessageBubble config draft card', () => {
  it('compacts persisted config confirmation user payloads', () => {
    const longConfirmation = [
      '确认应用这个配置草案。',
      'domain: alert_rule',
      'action: create',
      'draft_hash: sha256:test',
      'apply_tool: apply_config_change',
      '请调用 apply_config_change，传 confirmed=true、domain=alert_rule、action=create、上方 draft_hash 和下方原始 payload，创建这条告警规则；不要改写 payload。',
      'payload:',
      '```json',
      JSON.stringify({
        action: 'create',
        rule: {
          rule_key: 'system_disk_pressure_v2',
          kind: 'metric_raw',
        },
      }, null, 2),
      '```',
    ].join('\n');

    render(<MessageBubble message={{ id: 'user-confirmation', role: 'user', content: longConfirmation }} />);

    expect(screen.getByText('确认创建这条告警规则')).toBeInTheDocument();
    expect(screen.queryByText(/draft_hash/)).not.toBeInTheDocument();
    expect(screen.queryByText(/system_disk_pressure_v2/)).not.toBeInTheDocument();
  });

  it('keeps ordinary user messages unchanged', () => {
    render(<MessageBubble message={{ id: 'user-normal', role: 'user', content: '创建一个 CPU 告警' }} />);

    expect(screen.getByText('创建一个 CPU 告警')).toBeInTheDocument();
  });

  it.each(supportedKinds)('renders and confirms %s drafts', async (kind) => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const draft = draftFor(kind);

    render(<MessageBubble message={toolCardMessage(draft)} onConfirmConfigDraft={onConfirm} />);

    expect(screen.getByText(`Create ${kind} rule`)).toBeInTheDocument();
    expect(screen.getByText('范围：主机级')).toBeInTheDocument();
    expect(screen.getByText(/当前告警范围：主机级/)).toBeInTheDocument();
    expect(screen.getByText(
      `action: create · rule_key: ${kind}_natural_language · kind: ${kind} · name: ${kind} from natural language · severity: warning`,
    )).toBeInTheDocument();
    expect(screen.getByText('Preview fire_count=2')).toBeInTheDocument();
    expect(screen.getByText(`${kind} preview warning`)).toBeInTheDocument();
    expect(screen.getByText('Disable or edit the rule from Alerts.')).toBeInTheDocument();

    await act(async () => {
      await user.click(screen.getByRole('button', { name: /确认应用|Apply/ }));
    });

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm).toHaveBeenCalledWith(draft);
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /已确认|Confirmed/ })).toBeDisabled();
    });
  });

  it('cancels without calling confirm', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();

    render(<MessageBubble message={toolCardMessage(draftFor('metric_raw'))} onConfirmConfigDraft={onConfirm} />);
    await act(async () => {
      await user.click(screen.getByRole('button', { name: /取消|Cancel/ }));
    });

    expect(onConfirm).not.toHaveBeenCalled();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /已取消|Cancelled/ })).toBeDisabled();
    });
  });

  it('allows retry when confirm fails', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn().mockResolvedValue(false);

    render(<MessageBubble message={toolCardMessage(draftFor('metric_raw'))} onConfirmConfigDraft={onConfirm} />);
    await act(async () => {
      await user.click(screen.getByRole('button', { name: /确认应用|Apply/ }));
    });

    expect(onConfirm).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /确认应用|Apply/ })).toBeEnabled();
    });
    expect(screen.queryByRole('button', { name: /已确认|Confirmed/ })).not.toBeInTheDocument();
  });

  it('does not render a config draft card for unsupported config domains', () => {
    const draft = {
      ...draftFor('metric_raw'),
      domain: 'notification_channel',
      summary: 'Create notification channel',
    } as ConfigDraftResult;

    render(<MessageBubble message={toolCardMessage(draft)} onConfirmConfigDraft={vi.fn()} />);

    expect(screen.queryByText('Create notification channel')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /确认应用|Apply/ })).not.toBeInTheDocument();
  });

  it('renders a persisted tool result string as a draft card', () => {
    const draft = draftFor('metric_raw');
    const message: ChatMessage = {
      id: 'persisted-tool-result',
      role: 'tool',
      tool_name: 'draft_config_change',
      content: JSON.stringify(draft),
    };

    render(<MessageBubble message={message} onConfirmConfigDraft={vi.fn()} />);

    expect(screen.getByText('Create metric_raw rule')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /确认应用|Apply/ })).toBeInTheDocument();
  });

  it('renders the generic proposal envelope on a persisted draft', () => {
    const draft = {
      ...draftFor('metric_raw'),
      proposal: {
        kind: 'proposal' as const,
        type: 'config_change',
        state: 'pending_confirmation',
        title: 'Create metric_raw rule',
        actions: [
          { kind: 'confirm', label: 'Confirm', enabled: true },
          { kind: 'cancel', label: 'Cancel', enabled: true },
        ],
      },
    };

    render(<MessageBubble message={toolCardMessage(draft)} onConfirmConfigDraft={vi.fn()} />);

    expect(screen.getByText(/提案|Proposal/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /确认应用|Apply/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /取消|Cancel/ })).toBeInTheDocument();
  });
});

describe('MessageBubble operation card', () => {
  it('renders a cancellable running operation card and executes the stop action', async () => {
    vi.mocked(getOperation).mockRejectedValue(new Error('skip poll'));
    vi.mocked(executeOperationAction).mockResolvedValue({
      id: 'op-123',
      kind: 'packet_capture_session',
      state: 'cancelled',
      title: 'HTTPS capture',
      actions_json: '[]',
    });
    const user = userEvent.setup();
    const message: ChatMessage = {
      id: 'packet-capture-operation',
      role: 'tool',
      tool_name: 'capture_pcap',
      content: JSON.stringify({
        operation: {
          id: 'op-123',
          kind: 'packet_capture_session',
          title: 'HTTPS capture',
          state: 'running',
          summary: 'tcp port 443',
          links: { detail: '/artifacts/packet-sessions/pcap-session-running' },
          actions: [{ kind: 'cancel', label: 'Stop', enabled: true }],
        },
      }),
    };

    render(<MessageBubble message={message} />);

    expect(screen.getByText('HTTPS capture')).toBeInTheDocument();
    expect(screen.getByText('运行中')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '停止' }));

    expect(executeOperationAction).toHaveBeenCalledWith('op-123', 'cancel');
    await waitFor(() => expect(screen.getByText('已停止')).toBeInTheDocument());
  });

  it('polls a running operation card until it reaches a terminal state', async () => {
    vi.mocked(getOperation).mockResolvedValue({
      operation: {
        id: 'op-123',
        kind: 'packet_capture_session',
        state: 'succeeded',
        title: 'HTTPS capture',
        summary: '1/1 capture artifact(s) available',
        detail_url: '/artifacts/packet-sessions/pcap-session-running',
        actions_json: '[]',
      },
      artifacts: [],
    });
    const message: ChatMessage = {
      id: 'packet-capture-operation',
      role: 'tool',
      tool_name: 'capture_pcap',
      content: JSON.stringify({
        operation: {
          id: 'op-123',
          kind: 'packet_capture_session',
          title: 'HTTPS capture',
          state: 'running',
          summary: 'collecting',
          links: { detail: '/artifacts/packet-sessions/pcap-session-running' },
          actions: [{ kind: 'cancel', label: 'Stop', enabled: true }],
        },
      }),
    };

    render(<MessageBubble message={message} />);

    await waitFor(() => expect(screen.getByText('已完成')).toBeInTheDocument());
    expect(screen.getByText('1/1 capture artifact(s) available')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument();
  });

  it('renders a persisted packet capture result as an investigation card', () => {
    const message: ChatMessage = {
      id: 'packet-capture-result',
      role: 'tool',
      tool_name: 'capture_pcap',
      content: JSON.stringify({
        session: {
          public_id: 'pcap-session-7d5a7c7e',
          title: 'Checkout latency investigation',
          state: 'ready',
          canonical_filter: 'tcp port 443',
        },
        result: { capture: { state: 'ready' } },
      }),
    };

    render(<MessageBubble message={message} />);

    expect(screen.getByText('抓包任务')).toBeInTheDocument();
    expect(screen.getByText('Checkout latency investigation')).toBeInTheDocument();
    expect(screen.getByText('已完成')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '打开会话' })).toHaveAttribute(
      'href',
      '/artifacts/packet-sessions/pcap-session-7d5a7c7e',
    );
  });

  it('hydrates a legacy packet capture session card from the session API', async () => {
    vi.mocked(getPacketCaptureSession).mockResolvedValue({
      session: {
        id: 'pcap-session-7d5a7c7e',
        source: 'chat',
        pcap_count: 1,
        state: 'ready',
        title: 'Checkout latency investigation',
        description: '',
        canonical_filter: 'tcp port 443',
        duration_seconds: 60,
        planned_start_at: '',
        clock_quality: 'uncalibrated',
        created_at: '',
        updated_at: '',
      },
      captures: [],
    });
    const message: ChatMessage = {
      id: 'packet-capture-result',
      role: 'tool',
      tool_name: 'get_packet_capture_session',
      content: JSON.stringify({
        session: {
          public_id: 'pcap-session-7d5a7c7e',
          title: 'Checkout latency investigation',
          state: 'collecting',
          canonical_filter: 'tcp port 443',
        },
      }),
    };

    render(<MessageBubble message={message} />);

    expect(screen.getByText('运行中')).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('已完成')).toBeInTheDocument());
    expect(screen.getByText('1 个 PCAP · tcp port 443')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Stop|停止/ })).not.toBeInTheDocument();
  });
});
