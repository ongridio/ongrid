import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { AttributeControl, AttributeValue } from './AttributeControl';

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

describe('the control chosen by value type', () => {
  it('uses the browser-native input for each type', () => {
    const { rerender } = render(
      <AttributeControl id="f" type="0" value={8} onChange={() => {}} placeholder="cpu" aria-label="cpu" />,
    );
    expect(screen.getByLabelText('cpu')).toHaveAttribute('type', 'number');

    rerender(<AttributeControl id="f" type="4" value="2026-10-17" onChange={() => {}} placeholder="day" aria-label="day" />);
    expect(screen.getByLabelText('day')).toHaveAttribute('type', 'date');

    rerender(<AttributeControl id="f" type="3" value="2026-10-17 09:30:00" onChange={() => {}} placeholder="at" aria-label="at" />);
    const at = screen.getByLabelText('at');
    expect(at).toHaveAttribute('type', 'datetime-local');
    // THE STORED FORM IS NOT THE CONTROL'S FORM: the browser wants a `T` and no
    // seconds, and the conversion happens on the way back out.
    expect(at).toHaveValue('2026-10-17T09:30');

    rerender(<AttributeControl id="f" type="6" value={null} onChange={() => {}} placeholder="json" aria-label="json" />);
    expect(screen.getByLabelText('json').tagName).toBe('TEXTAREA');
  });

  it('reports null for a cleared field, not an empty string', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<AttributeControl id="f" type="2" value="web-01" onChange={onChange} placeholder="host" aria-label="host" />);
    await user.clear(screen.getByLabelText('host'));
    expect(onChange).toHaveBeenLastCalledWith(null);
  });

  it('toggles a boolean without a text field', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<AttributeControl id="f" type="7" value={false} onChange={onChange} placeholder="enabled" aria-label="enabled" />);
    await user.click(screen.getByRole('switch', { name: 'enabled' }));
    expect(onChange).toHaveBeenLastCalledWith(true);
  });
});

describe('a multi-valued attribute', () => {
  it('edits each value as its own input and can add and remove', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <AttributeControl
        id="tags"
        type="2"
        isList
        value={['a', 'b']}
        onChange={onChange}
        placeholder="tag"
        aria-label="tag"
      />,
    );

    // A COMMA-SEPARATED FIELD WOULD BE SMALLER and would make a value containing
    // a comma unrepresentable — which is exactly the value an operator then
    // cannot enter.
    const inputs = screen.getAllByLabelText(/^tag \d+$/);
    expect(inputs).toHaveLength(2);
    expect(inputs[0]).toHaveValue('a');
    expect(inputs[1]).toHaveValue('b');

    await user.click(screen.getAllByRole('button', { name: '删除这个值' })[0]);
    expect(onChange).toHaveBeenLastCalledWith(['b']);

    await user.click(screen.getByRole('button', { name: /添加一个值/ }));
    expect(onChange).toHaveBeenLastCalledWith(['a', 'b', null]);
  });
});

describe('read-only rendering', () => {
  it('shows an em dash for no value and a word for a boolean', () => {
    const { rerender } = render(<AttributeValue type="2" value={null} />);
    expect(screen.getByText('—')).toBeInTheDocument();

    rerender(<AttributeValue type="7" value={true} />);
    expect(screen.getByText('是')).toBeInTheDocument();

    rerender(<AttributeValue type="7" value={false} />);
    expect(screen.getByText('否')).toBeInTheDocument();

    rerender(<AttributeValue type="2" value={['a', 'b']} />);
    expect(screen.getByText('a, b')).toBeInTheDocument();
  });
});
