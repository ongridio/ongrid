import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ChatInput } from './ChatInput';

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'en-US');
  URL.createObjectURL = vi.fn(() => 'blob:test-image');
  URL.revokeObjectURL = vi.fn();
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

    expect(onSubmit).toHaveBeenCalledWith({ text: '能看到l', mentions: [], attachments: [] });
  });
});

describe('ChatInput image attachments', () => {
  it('limits the native file picker to the supported extensions', () => {
    render(
      <MemoryRouter>
        <ChatInput value="" onSubmit={vi.fn()} allowAttachments />
      </MemoryRouter>,
    );
    expect(screen.getByLabelText('Choose images')).toHaveAttribute('accept', '.png,.jpg,.jpeg,.webp');
  });

  it('previews and submits a selected image', async () => {
    const onSubmit = vi.fn();
    render(
      <MemoryRouter>
        <ChatInput value="describe" onSubmit={onSubmit} allowAttachments />
      </MemoryRouter>,
    );
    const file = new File([new Uint8Array([137, 80, 78, 71])], 'chart.png', { type: 'image/png' });
    fireEvent.change(screen.getByLabelText('Choose images'), { target: { files: [file] } });

    const thumbnail = await screen.findByAltText('chart.png');
    fireEvent.click(thumbnail);
    expect(await screen.findByAltText('Preview chart.png')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    fireEvent.click(screen.getByRole('button', { name: 'Send message' }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce());
    expect(onSubmit.mock.calls[0][0]).toMatchObject({ text: 'describe' });
    expect(onSubmit.mock.calls[0][0].attachments).toEqual([file]);
  });

  it('previews and submits an image pasted from clipboard items', async () => {
    const onSubmit = vi.fn();
    render(
      <MemoryRouter>
        <ChatInput value="" onSubmit={onSubmit} allowAttachments />
      </MemoryRouter>,
    );
    const file = new File([new Uint8Array([137, 80, 78, 71])], 'clipboard.png', { type: 'image/png' });
    const textarea = screen.getByRole('textbox', { name: /message input/i });

    fireEvent.paste(textarea, {
      clipboardData: {
        files: [],
        items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
      },
    });

    await screen.findByAltText('clipboard.png');
    fireEvent.click(screen.getByRole('button', { name: 'Send message' }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce());
    expect(onSubmit.mock.calls[0][0].attachments).toEqual([file]);
  });

  it('clears the image limit error after an attachment is removed', async () => {
    render(
      <MemoryRouter>
        <ChatInput value="" onSubmit={vi.fn()} allowAttachments />
      </MemoryRouter>,
    );
    const input = screen.getByLabelText('Choose images');
    const files = Array.from({ length: 4 }, (_, index) =>
      new File([new Uint8Array([137, 80, 78, 71])], `image-${index}.png`, { type: 'image/png' }),
    );
    fireEvent.change(input, { target: { files } });
    await screen.findByAltText('image-3.png');

    const extra = new File([new Uint8Array([137, 80, 78, 71])], 'extra.png', { type: 'image/png' });
    fireEvent.change(input, { target: { files: [extra] } });
    expect(await screen.findByRole('alert')).toHaveTextContent('Up to 4 images per message');

    fireEvent.click(screen.getByRole('button', { name: 'Remove image image-0.png' }));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});

it('toggles web search directly from the globe button', () => {
  const onToggle = vi.fn();
  const view = (enabled: boolean) => <MemoryRouter><ChatInput onSubmit={vi.fn()} webSearchEnabled={enabled} onWebSearchToggle={onToggle} /></MemoryRouter>;
  const { rerender } = render(view(true));
  const globe = screen.getByRole('switch', { name: 'Disable web search' });
  expect(globe.querySelector('svg')).toBeInTheDocument();
  expect(globe).toHaveAttribute('aria-checked', 'true');
  fireEvent.click(globe);
  expect(onToggle).toHaveBeenLastCalledWith(false);
  rerender(view(false));
  fireEvent.click(screen.getByRole('switch', { name: 'Enable web search' }));
  expect(onToggle).toHaveBeenLastCalledWith(true);
});
