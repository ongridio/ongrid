import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as agents from '@/api/agents';
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
    expect(screen.getByLabelText('Choose files')).toHaveAttribute('accept', '.png,.jpg,.jpeg,.webp,.txt,.log,.md,.csv,.json,.yaml,.yml');
  });

  it('previews and submits a selected image', async () => {
    const onSubmit = vi.fn();
    render(
      <MemoryRouter>
        <ChatInput value="describe" onSubmit={onSubmit} allowAttachments />
      </MemoryRouter>,
    );
    const file = new File([new Uint8Array([137, 80, 78, 71])], 'chart.png', { type: 'image/png' });
    fireEvent.change(screen.getByLabelText('Choose files'), { target: { files: [file] } });

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
    const input = screen.getByLabelText('Choose files');
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

it('accepts images and text files together in the same picker', async () => {
  const onSubmit = vi.fn();
  render(<MemoryRouter><ChatInput allowAttachments onSubmit={onSubmit} /></MemoryRouter>);
  const draft = screen.getByRole('textbox');
  const picker = screen.getByLabelText('Choose files');
  fireEvent.change(draft, { target: { value: 'Check these' } });
  const photo = new File(['image'], 'chart.png', { type: 'image/png' });
  const logs = ['a.log', 'b.TXT'].map(name => {
    const file = new File(['ready'], name);
    file.arrayBuffer = async () => new TextEncoder().encode('ready').buffer;
    return file;
  });
  fireEvent.change(picker, { target: { files: [logs[0], photo, logs[1]] } });
  await screen.findByAltText('chart.png');
  const text = 'Check these\n\na.log\nready\n\nb.TXT\nready';
  expect(draft).toHaveValue(text);
  expect(onSubmit).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: 'Send message' }));
  expect(onSubmit).toHaveBeenCalledWith({ text, mentions: [], attachments: [photo] });
});

it('keeps the draft intact when a text file is too large or contains invalid UTF-8', async () => {
  render(<MemoryRouter><ChatInput value="check the logs" allowAttachments onSubmit={vi.fn()} /></MemoryRouter>);
  const picker = screen.getByLabelText('Choose files');
  const oversized = new File([new Uint8Array(128 * 1024 + 1)], 'service.log');
  fireEvent.change(picker, { target: { files: [oversized] } });
  expect(screen.getByRole('alert')).toHaveTextContent('128 KiB');

  const binary = new File([], 'invalid.txt');
  binary.arrayBuffer = async () => Uint8Array.of(0xff).buffer;
  const photo = new File(['image'], 'chart.png', { type: 'image/png' });
  fireEvent.change(picker, { target: { files: [photo, binary] } });
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('UTF-8'));
  expect(screen.getByRole('textbox')).toHaveValue('check the logs');
  expect(screen.queryByAltText('chart.png')).toBeNull();
});

it('groups composer actions and exposes the current search mode', async () => {
  const user = userEvent.setup();
  const onToggle = vi.fn();
  render(<MemoryRouter><ChatInput onSubmit={vi.fn()} allowAttachments webSearchEnabled onWebSearchToggle={onToggle} /></MemoryRouter>);
  screen.getByRole('button', { name: 'More actions' }).focus();
  await user.keyboard('{ArrowDown}');
  expect(screen.getAllByRole('menuitem').map(item => item.textContent)).toEqual(['Add files', 'Mode', 'Experts', 'Skills', 'Connectors']);
  screen.getByRole('menuitem', { name: 'Mode' }).focus();
  await user.keyboard('{ArrowRight}');
  expect(screen.getByRole('menuitemradio', { name: 'Automatic web search' })).toBeChecked();
  fireEvent.click(screen.getByRole('menuitemradio', { name: 'Web search off' }));
  expect(onToggle).toHaveBeenCalledWith(false);
});

it('loads experts when opened and adds the selected expert to the unsent draft', async () => {
  const list = vi.spyOn(agents, 'listAgents').mockResolvedValue({ items: [{ name: 'specialist-network', description: '' }], total: 1 });
  const user = userEvent.setup();
  const onSubmit = vi.fn();
  try {
    render(<MemoryRouter><ChatInput onSubmit={onSubmit} /></MemoryRouter>);
    const input = screen.getByRole('textbox');
    await user.type(input, 'Check packet loss');
    screen.getByRole('button', { name: 'More actions' }).focus();
    await user.keyboard('{ArrowDown}');
    expect(list).not.toHaveBeenCalled();
    screen.getByRole('menuitem', { name: 'Experts' }).focus();
    await user.keyboard('{ArrowRight}');
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Network specialist' }));
    expect(input).toHaveValue('Check packet loss\nPlease ask the specialist-network expert to assist. ');
    await waitFor(() => expect(input).toHaveFocus());
    expect(onSubmit).not.toHaveBeenCalled();
  } finally {
    list.mockRestore();
  }
});
