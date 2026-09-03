import { act, render } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { XTerminal } from './XTerminal';
import { setThemePreference } from '@/store/mode';

const terminalMock = vi.hoisted(() => ({
  instances: [] as Array<{ options: { theme?: { background?: string; foreground?: string; white?: string } } }>,
}));

vi.mock('xterm', () => ({
  Terminal: class Terminal {
    options: { theme?: { background?: string; foreground?: string; white?: string } };

    constructor(options: { theme?: { background?: string; foreground?: string; white?: string } }) {
      this.options = options;
      terminalMock.instances.push(this);
    }

    loadAddon() {}
    open() {}
    onData() { return { dispose() {} }; }
    onResize() { return { dispose() {} }; }
    attachCustomKeyEventHandler() {}
    write() {}
    writeln() {}
    clear() {}
    focus() {}
    dispose() {}
  },
}));

vi.mock('xterm-addon-fit', () => ({
  FitAddon: class FitAddon { fit() {} },
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
    localStorage.clear();
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
