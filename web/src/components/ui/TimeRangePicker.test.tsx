import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { TimeRangePicker } from './TimeRangePicker';

const presets = [{ value: '1h', label: '最近 1 小时', durationMs: 3600000 }];

describe('TimeRangePicker', () => {
  it('keeps edits in the popup until valid Apply, and discards Cancel and Escape', async () => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<TimeRangePicker value={{ range: '1h', start: '2026-09-08T09:00:00Z', end: '2026-09-08T10:00:00Z' }} presets={presets} onChange={onChange} minDurationMs={60000} />);
    expect(screen.queryByLabelText('开始时间')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '时间范围' }));
    const initial = (screen.getByLabelText('开始时间') as HTMLInputElement).value;
    fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-09-08T01:00:00' } });
    expect(onChange).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: '取消' }));
    expect(screen.queryByLabelText('开始时间')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '时间范围' }));
    expect(screen.getByLabelText('开始时间')).toHaveValue(initial);
    await user.keyboard('{Escape}');
    expect(screen.queryByLabelText('开始时间')).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: '时间范围' }));
    fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-09-08T02:00:00' } });
    fireEvent.change(screen.getByLabelText('结束时间'), { target: { value: '2026-09-08T01:00:00' } });
    expect(screen.getByRole('button', { name: '应用' })).toBeDisabled();
    expect(screen.getByRole('alert')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('结束时间'), { target: { value: '2026-09-08T02:00:30' } });
    expect(screen.getByRole('button', { name: '应用' })).toBeDisabled();
    fireEvent.change(screen.getByLabelText('结束时间'), { target: { value: '2026-09-16T02:00:00' } });
    expect(screen.getByRole('button', { name: '应用' })).toBeDisabled();
    fireEvent.change(screen.getByLabelText('结束时间'), { target: { value: '2026-09-08T03:00:00' } });
    await user.click(screen.getByRole('button', { name: '应用' }));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith({ range: 'custom', start: new Date('2026-09-08T02:00:00').toISOString(), end: new Date('2026-09-08T03:00:00').toISOString() });
    expect(screen.queryByLabelText('开始时间')).not.toBeInTheDocument();
  });

  it('applies a relative preset immediately using the current time', async () => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    const onChange = vi.fn();
    const clock = vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2026-09-08T10:00:00Z'));
    try {
      render(<TimeRangePicker value={{ range: 'custom', start: '2026-09-07T09:00:00Z', end: '2026-09-07T10:00:00Z' }} presets={presets} onChange={onChange} />);
      fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
      fireEvent.click(screen.getByRole('button', { name: '最近 1 小时' }));
      expect(onChange).toHaveBeenCalledTimes(1);
      expect(onChange).toHaveBeenCalledWith({ range: '1h', start: '2026-09-08T09:00:00.000Z', end: '2026-09-08T10:00:00.000Z' });
    } finally { clock.mockRestore(); }
  });
});
