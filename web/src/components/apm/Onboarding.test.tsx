import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { selectOption } from '@/test/select-option';
import { Onboarding } from './Onboarding';

describe('gRPC onboarding', () => {
  it.each(['zh-CN', 'en-US'])('explains the required Python plugin and Node interceptor in %s', async (locale) => {
    localStorage.setItem('ongrid-locale', locale);
    const { container } = render(<Onboarding />);
    expect(container.querySelector('pre')?.textContent).toContain('-Dotel.javaagent.extensions=');
    expect(screen.getByText(/Java Agent 2.31.1/, { selector: 'p' })).toBeInTheDocument();
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
