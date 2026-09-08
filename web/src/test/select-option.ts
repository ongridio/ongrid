import { act, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

export async function selectOption(trigger: HTMLElement, label: string) {
  if (trigger.getAttribute('aria-expanded') !== 'true') await act(async () => { await userEvent.click(trigger); });
  const option = await screen.findByRole('option', { name: label });
  await act(async () => { await userEvent.click(option); });
}
