// XTerminal — thin wrapper around xterm.js with a small imperative API.
//
// Why imperative? The terminal owns a mutable buffer; React's render cycle
// is the wrong abstraction for byte-by-byte stdout. We instead:
//   - Mount xterm into a div, fit it to the container
//   - Forward keystrokes (`onData`) and resize events to the parent
//   - Expose `write()` / `clear()` / `focus()` / `dispose()` via attachRef
//
// The host page (DeviceShell) wires `onData` → ws.send(binary) and pipes
// inbound binary frames into `api.write()`.

import { useEffect, useRef } from 'react';
import { Terminal, type ITheme } from 'xterm';
import { FitAddon } from 'xterm-addon-fit';
import { WebLinksAddon } from 'xterm-addon-web-links';
import { useThemeMode, type ResolvedTheme } from '@/store/mode';
import 'xterm/css/xterm.css';

export type XTerminalApi = {
  write(data: Uint8Array | string): void;
  writeln(line: string): void;
  clear(): void;
  focus(): void;
  fit(): void;
  dispose(): void;
};

type Props = {
  // Called for every keystroke / paste. `data` is whatever xterm decoded;
  // we leave the encode-to-bytes step to the caller (TextEncoder) so
  // there's only one place that decides the binary format.
  onData?(data: string): void;
  // Fires whenever the visible grid changes (initial fit + ResizeObserver).
  onResize?(cols: number, rows: number): void;
  // Imperative handle injection. Parent stores the api on a ref to call
  // `write(bytes)` when WS data arrives.
  attachRef(api: XTerminalApi): void;
  readOnly?: boolean;
  className?: string;
  fontSize?: number;
  scrollback?: number;
  followAppTheme?: boolean;
};

// Theme tuned to the rest of the app: zinc-950 background, zinc-100 text,
// indigo accent for cursor. ANSI palette stays mostly default — terminal
// apps (vim/htop/nethogs) rely on those colors having semantic meaning.
const THEME: ITheme = {
  background: '#09090b',     // zinc-950
  foreground: '#e4e4e7',     // zinc-200
  cursor: '#a5b4fc',         // indigo-300
  cursorAccent: '#09090b',
  selectionBackground: '#3f3f46aa', // zinc-700 @ ~67%
  black: '#27272a',
  red: '#f87171',
  green: '#4ade80',
  yellow: '#fbbf24',
  blue: '#60a5fa',
  magenta: '#c084fc',
  cyan: '#22d3ee',
  white: '#e4e4e7',
  brightBlack: '#52525b',
  brightRed: '#fca5a5',
  brightGreen: '#86efac',
  brightYellow: '#fcd34d',
  brightBlue: '#93c5fd',
  brightMagenta: '#d8b4fe',
  brightCyan: '#67e8f9',
  brightWhite: '#fafafa',
};

const LIGHT_THEME: ITheme = {
  background: '#ffffff',
  foreground: '#27272a',
  cursor: '#4f46e5',
  cursorAccent: '#ffffff',
  selectionBackground: '#c7d2feaa',
  black: '#18181b',
  red: '#b91c1c',
  green: '#047857',
  yellow: '#a16207',
  blue: '#1d4ed8',
  magenta: '#7e22ce',
  cyan: '#0e7490',
  white: '#3f3f46',
  brightBlack: '#71717a',
  brightRed: '#dc2626',
  brightGreen: '#059669',
  brightYellow: '#ca8a04',
  brightBlue: '#2563eb',
  brightMagenta: '#9333ea',
  brightCyan: '#0891b2',
  brightWhite: '#18181b',
};

function terminalTheme(theme: ResolvedTheme): ITheme {
  return theme === 'light' ? LIGHT_THEME : THEME;
}

export function XTerminal({ onData, onResize, attachRef, readOnly = false, className = '', fontSize = 13, scrollback = 5000, followAppTheme = false }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const { resolved } = useThemeMode();

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const term = new Terminal({
      theme: followAppTheme ? terminalTheme(resolved) : THEME,
      // Match Apple Terminal / iTerm defaults — JetBrains Mono is the
      // closest the host stylesheet ships, fall back to ui-monospace.
      fontFamily:
        '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
      fontSize,
      lineHeight: 1.2,
      cursorBlink: !readOnly,
      cursorStyle: 'block',
      scrollback,
      allowProposedApi: false,
      convertEol: false, // SSH gives us proper CRLFs already.
      macOptionIsMeta: true,
      disableStdin: readOnly,
    });

    const fitAddon = new FitAddon();
    const linksAddon = new WebLinksAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(linksAddon);

    term.open(el);
    terminalRef.current = term;
    // Initial fit must happen after open() lays out the DOM.
    try {
      fitAddon.fit();
    } catch {
      /* container not laid out yet — ResizeObserver will catch up */
    }

    const dataDisposable = readOnly ? null : term.onData((d) => onData?.(d));
    const resizeDisposable = term.onResize(({ cols, rows }) => {
      onResize?.(cols, rows);
    });

    if (readOnly) {
      term.attachCustomKeyEventHandler((event) => {
        const key = event.key.toLowerCase();
        const copyLike = (event.metaKey || event.ctrlKey) && (key === 'c' || key === 'a');
        return copyLike;
      });
    }

    // Re-fit on container size changes (sidebar collapse, window resize).
    // We debounce nothing — fit() is cheap and the resize control frame
    // is throttled by SSH itself.
    const ro = new ResizeObserver(() => {
      try {
        fitAddon.fit();
      } catch {
        /* dom temporarily detached during route change */
      }
    });
    ro.observe(el);

    // Hand the imperative API back to the parent. The decoder is created
    // once and reused so we don't churn allocations per inbound chunk.
    const decoder = new TextDecoder('utf-8', { fatal: false });
    const api: XTerminalApi = {
      write: (data) => {
        if (typeof data === 'string') {
          term.write(data);
        } else {
          // xterm.write accepts Uint8Array directly in v5+. We decode
          // explicitly so non-UTF8 bytes are surrogated rather than
          // silently dropped.
          term.write(decoder.decode(data, { stream: true }));
        }
      },
      writeln: (line) => term.writeln(line),
      clear: () => term.clear(),
      focus: () => term.focus(),
      fit: () => {
        try {
          fitAddon.fit();
        } catch {
          /* noop */
        }
      },
      dispose: () => term.dispose(),
    };
    attachRef(api);

    // Autofocus only for interactive terminals. Read-only terminals should not
    // steal focus from forms on diagnostic pages.
    if (!readOnly) term.focus();

    return () => {
      terminalRef.current = null;
      ro.disconnect();
      dataDisposable?.dispose();
      resizeDisposable.dispose();
      term.dispose();
    };
    // attachRef / onData / onResize are expected to be stable refs from
    // the parent (wrapped in useCallback). We deliberately mount once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!followAppTheme || !terminalRef.current) return;
    terminalRef.current.options.theme = terminalTheme(resolved);
  }, [followAppTheme, resolved]);

  return (
    <div
      ref={containerRef}
      className={`h-full w-full ${followAppTheme ? 'bg-zinc-900' : 'bg-zinc-950'} ${className}`}
      // xterm injects its own focusable element; this wrapper doesn't
      // need a tabindex.
    />
  );
}
