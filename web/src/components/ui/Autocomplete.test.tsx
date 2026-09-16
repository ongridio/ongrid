import { useState } from 'react';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { Autocomplete } from './Autocomplete';

it('selects suggestions by keyboard and preserves custom values without submitting the form', async () => {
  const submit = vi.fn();
  function Form() {
    const [value, setValue] = useState('');
    return <form onSubmit={event => { event.preventDefault(); submit(value); }}>
      <Autocomplete aria-label="Environment" value={value} onValueChange={setValue} options={['production', 'staging']} />
      <button type="submit">Save</button>
    </form>;
  }
  const user = userEvent.setup();
  render(<Form />);
  const input = screen.getByRole('combobox', { name: 'Environment' });
  await act(async () => { await user.type(input, 'prod'); });
  await screen.findByRole('option', { name: 'production' });
  await act(async () => { await user.keyboard('{ArrowDown}{Enter}'); });
  expect(input).toHaveValue('production');
  expect(submit).not.toHaveBeenCalled();
  await act(async () => { await user.clear(input); await user.type(input, 'preview-123'); await user.keyboard('{Escape}'); });
  expect(input).toHaveFocus();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save' })); });
  expect(submit).toHaveBeenCalledWith('preview-123');
});
