import { act, render } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { XTerminal, type XTerminalApi } from './XTerminal';
import { setThemePreference } from '@/store/mode';

const terminalMock = vi.hoisted(() => ({
  instances: [] as Array<{ options: { theme?: { background?: string; foreground?: string; white?: string } } }>,
  dimensions: { cols: 132, rows: 42 },
}));

vi.mock('xterm', () => ({
  Terminal: class Terminal {
    cols = 80;
    rows = 24;
    resizeListener?: () => void;
    options: { theme?: { background?: string; foreground?: string; white?: string } };

    constructor(options: { theme?: { background?: string; foreground?: string; white?: string } }) {
      this.options = options;
      terminalMock.instances.push(this);
    }

    loadAddon(addon: { activate?(terminal: Terminal): void }) { addon.activate?.(this); }
    open() {}
    onData() { return { dispose() {} }; }
    onResize(listener: () => void) { this.resizeListener = listener; return { dispose() {} }; }
    resize(cols: number, rows: number) {
      if (this.cols === cols && this.rows === rows) return;
      this.cols = cols;
      this.rows = rows;
      this.resizeListener?.();
    }
    attachCustomKeyEventHandler() {}
    write() {}
    writeln() {}
    clear() {}
    focus() {}
    dispose() {}
  },
}));

vi.mock('xterm-addon-fit', () => ({
  FitAddon: class FitAddon {
    terminal?: { resize(cols: number, rows: number): void };
    activate(terminal: { resize(cols: number, rows: number): void }) { this.terminal = terminal; }
    fit() { this.terminal?.resize(terminalMock.dimensions.cols, terminalMock.dimensions.rows); }
  },
}));

vi.mock('xterm-addon-web-links', () => ({
  WebLinksAddon: class WebLinksAddon {},
}));

vi.stubGlobal('ResizeObserver', class ResizeObserver {
  observe() {}
  disconnect() {}
});

describe('XTerminal', () => {
  beforeEach(() => {
    terminalMock.instances.length = 0;
    terminalMock.dimensions = { cols: 132, rows: 42 };
    localStorage.clear();
  });

  it('reports initial fitted dimensions before any observer callback', () => {
    const onResize = vi.fn();
    render(<XTerminal attachRef={() => {}} onResize={onResize} />);
    expect(onResize.mock.calls).toEqual([[132, 42]]);
  });

  it('reports the initial default grid even without an xterm resize event', () => {
    terminalMock.dimensions = { cols: 80, rows: 24 };
    const onResize = vi.fn();
    render(<XTerminal attachRef={() => {}} onResize={onResize} />);
    expect(onResize).toHaveBeenCalledWith(80, 24);
  });

  it('explicit fit re-publishes unchanged dimensions for reconnect', () => {
    const onResize = vi.fn();
    let api!: XTerminalApi;
    render(<XTerminal attachRef={(value) => { api = value; }} onResize={onResize} />);
    onResize.mockClear();
    act(() => api.fit());
    expect(onResize.mock.calls).toEqual([[132, 42]]);
    terminalMock.dimensions = { cols: 100, rows: 30 };
    onResize.mockClear();
    act(() => api.fit());
    expect(onResize.mock.calls).toEqual([[100, 30]]);
  });

  it('只读日志启用应用主题后会响应 light 和 dark 切换', () => {
    setThemePreference('light');
    const { container } = render(<XTerminal attachRef={() => {}} readOnly followAppTheme />);

    expect(terminalMock.instances).toHaveLength(1);
    expect(terminalMock.instances[0].options.theme).toMatchObject({
      background: '#ffffff',
      foreground: '#27272a',
      white: '#3f3f46',
    });
    expect(container.firstElementChild).toHaveClass('bg-zinc-900');

    act(() => setThemePreference('dark'));

    expect(terminalMock.instances[0].options.theme).toMatchObject({
      background: '#09090b',
      foreground: '#e4e4e7',
    });
  });

  it('交互终端默认保持现有深色主题', () => {
    setThemePreference('light');
    const { container } = render(<XTerminal attachRef={() => {}} />);

    expect(terminalMock.instances[0].options.theme?.background).toBe('#09090b');
    expect(container.firstElementChild).toHaveClass('bg-zinc-950');
  });
});
