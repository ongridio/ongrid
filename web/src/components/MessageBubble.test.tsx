import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { MessageBubble, type ConfigDraftResult } from './MessageBubble';
import type { ChatMessage } from '@/api/chat';

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
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
});
