import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { selectOption } from '@/test/select-option';
import { Onboarding } from './Onboarding';

describe('gRPC onboarding', () => {
  it('renders the configuration guide inline with working repository links', () => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    const { container } = render(<Onboarding />);
    fireEvent.click(screen.getByRole('button', { name: '查看配置文件接入指南' }));
    expect(container.querySelector('#apm-configuration-guide pre')?.textContent).toContain('otel.service.name=order-api');
    expect(screen.getByRole('link', { name: '现有 .NET 示例' })).toHaveAttribute('href', 'https://github.com/ongridio/ongrid/blob/main/examples/apm-languages/dotnet/Program.cs');
    expect(container.querySelector('a[download]')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '收起配置文件接入指南' }));
    expect(container.querySelector('#apm-configuration-guide')).toBeNull();
  });
  it.each(['zh-CN', 'en-US'])('explains the required Python plugin and Node interceptor in %s', async (locale) => {
    localStorage.setItem('ongrid-locale', locale);
    const { container } = render(<Onboarding />);
    expect(container.querySelector('pre')?.textContent).toContain('-Dotel.javaagent.extensions=');
    expect(screen.getByText(/Java Agent 2.31.1/, { selector: 'p' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /查看配置文件接入指南|Read configuration file guide/ })).toHaveAttribute('aria-expanded', 'false');
    const select = screen.getByRole('combobox');
    await selectOption(select, 'Python');
    expect(container.querySelector('pre')?.textContent).toContain('OpenTelemetryPlugin');
    expect(container.querySelector('pre')?.textContent).toContain('opentelemetry-instrument python app.py');
    expect(screen.getByText(/grpcio-observability/, { selector: 'p' })).toBeInTheDocument();
    await selectOption(select, 'Node.js');
    expect(container.querySelector('pre')?.textContent).toContain('grpcMetricsInterceptor');
    expect(screen.getByText(/examples\/apm-languages\/grpc-metrics.cjs/, { selector: 'p' })).toBeInTheDocument();
  });
});
