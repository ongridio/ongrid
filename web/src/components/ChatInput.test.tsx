import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ChatInput } from './ChatInput';

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'en-US');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderChatInput(onSubmit = vi.fn()) {
  render(
    <MemoryRouter>
      <ChatInput value="能看到l" onSubmit={onSubmit} />
    </MemoryRouter>,
  );
  return {
    textarea: screen.getByRole('textbox', { name: /message input/i }),
    onSubmit,
  };
}

describe('ChatInput keyboard submit', () => {
  it('does not submit while an IME composition is active', () => {
    const { textarea, onSubmit } = renderChatInput();

    fireEvent.keyDown(textarea, { key: 'Enter', isComposing: true });

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('does not submit Safari-style IME Enter events', () => {
    const { textarea, onSubmit } = renderChatInput();

    fireEvent.keyDown(textarea, { key: 'Enter', keyCode: 229 });

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('still submits on plain Enter after composition is done', () => {
    const { textarea, onSubmit } = renderChatInput();

    fireEvent.keyDown(textarea, { key: 'Enter' });

    expect(onSubmit).toHaveBeenCalledWith({ text: '能看到l', mentions: [] });
  });
});
