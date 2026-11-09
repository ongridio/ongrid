import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { beforeEach, describe, expect, it } from 'vitest';

import { QueryBar, type QueryAttribute } from './QueryBar';

const attributes: QueryAttribute[] = [
  { ref: 'hostname', label: '主机名', type: '2' },
  { ref: 'cpu', label: '核数', type: '0' },
  { ref: 'secret', label: '密码', type: '2', matchable: false },
];

// A controlled wrapper, because the expression is the component's value — the
// page owns it.
function Harness({ initial = '' }: { initial?: string }) {
  const [value, setValue] = useState(initial);
  return <QueryBar attributes={attributes} value={value} onChange={setValue} />;
}

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

describe('the builder', () => {
  it('does not offer an attribute the backend refuses to match', () => {
    render(<Harness />);
    // `secret` (a password attribute) is filtered out of the picker: the backend
    // rejects a filter on it, and offering it would turn the search box into a
    // credential oracle.
    const select = screen.getByLabelText('属性');
    expect(select).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: '密码' })).not.toBeInTheDocument();
  });

  it('adds a clause to the expression', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(screen.getByLabelText('值'), 'web*');
    await user.click(screen.getByRole('button', { name: /添加条件/ }));

    // The default attribute is the first offered one and the default operator is
    // equality, so the typed value becomes the clause's value — a trailing `*` is
    // the language's own wildcard and is emitted as typed.
    expect(screen.getByLabelText('筛选表达式')).toHaveValue('hostname:web*');
    // THE ECHO READS THE ROW BACK FROM THE EXPRESSION, so it says "starts with"
    // — a trailing `*` is the language's prefix wildcard, not a literal. The
    // builder shows what the server will do, not what was typed.
    expect(screen.getAllByText(/主机名 开头是 web/).length).toBeGreaterThan(0);
  });

  it('reopens a saved expression as rows', () => {
    render(<Harness initial="hostname:web*,-cpu:4,~hostname:old" />);
    const codes = screen.getAllByText(/主机名|核数/);
    // THREE ROWS, with the connectors read back in the language's own order —
    // `-` is OR and `~` is NOT, and swapping them selects the complement.
    expect(codes.length).toBeGreaterThanOrEqual(3);
    expect(screen.getByText('或者')).toBeInTheDocument();
    expect(screen.getByText('并且不是')).toBeInTheDocument();
  });

  it('removes a row and rewrites the expression', async () => {
    const user = userEvent.setup();
    render(<Harness initial="hostname:web*,-cpu:4" />);

    await user.click(screen.getAllByRole('button', { name: '移除这条筛选' })[0]);
    expect(screen.getByLabelText('筛选表达式')).toHaveValue('-cpu:4');
  });

  it('keeps a hand-written expression rather than rewriting it', async () => {
    const user = userEvent.setup();
    render(<Harness initial="web-01" />);

    // A free-text query is not a clause, so it is shown as written and the
    // builder refuses to pretend it understands it.
    expect(screen.getByText('web-01', { selector: 'code' })).toBeInTheDocument();

    const input = screen.getByLabelText('筛选表达式');
    await user.clear(input);
    await user.type(input, 'cpu:>=4');
    expect(input).toHaveValue('cpu:>=4');
  });
});
