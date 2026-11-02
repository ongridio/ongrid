import { useCallback, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type RefObject } from 'react';

type Position = { x: number; y: number; panelBelow: boolean };
const initial: Position = { x: 1, y: 1, panelBelow: false };
const storageKey = 'ongrid.assistant.position';
const gap = 12;

function clamp(value: number, min: number, max: number) {
  return Math.max(min, Math.min(value, max));
}

function readPosition(): Position {
  try {
    const p = JSON.parse(localStorage.getItem(storageKey) ?? 'null');
    if (p && Number.isFinite(p.x) && Number.isFinite(p.y) && typeof p.panelBelow === 'boolean') {
      return { x: clamp(p.x, 0, 1), y: clamp(p.y, 0, 1), panelBelow: p.panelBelow };
    }
  } catch { /* Storage is optional in private browsing. */ }
  return initial;
}

export function useAssistantPosition(launcher: RefObject<HTMLButtonElement>, narrow: boolean) {
  const bounds = useRef<HTMLDivElement>(null);
  const [panel, setPanel] = useState<HTMLDivElement | null>(null);
  const [saved] = useState(readPosition);
  const position = useRef(saved);
  const suppressClick = useRef(false);
  const drag = useRef<{
    id: number; startX: number; startY: number; left: number; top: number; original: Position; moved: boolean;
  } | null>(null);

  const layout = useCallback(() => {
    if (!bounds.current || !launcher.current) return;
    const area = bounds.current.getBoundingClientRect();
    const button = launcher.current.getBoundingClientRect();
    const p = position.current;
    const buttonLeft = p.x * (area.width - button.width);
    let buttonTop = p.y * (area.height - button.height);
    const frame = panel?.getBoundingClientRect();
    if (panel && frame) {
      // Keep hit-testing and drag coordinates in viewport pixels; scale only the contents.
      const baseWidth = Number(getComputedStyle(panel).getPropertyValue('--assistant-base-width'));
      const fullscreen = narrow && panel.hasAttribute('data-expanded');
      if (baseWidth > 0) panel.style.setProperty('--assistant-scale', String(fullscreen ? 1 : frame.width / baseWidth));
    }
    if (panel && frame && !narrow) {
      const row = button.height + gap;
      // Keep the launcher next to the window even when neither side of its saved position has room.
      const top = p.panelBelow
        ? clamp(buttonTop + row, row, area.height - frame.height)
        : clamp(buttonTop - frame.height - gap, 0, area.height - frame.height - row);
      const left = p.x * (area.width - frame.width);
      panel.style.inset = `${area.top + top}px auto auto ${area.left + left}px`;
      buttonTop = p.panelBelow ? top - row : top + frame.height + gap;
    } else if (panel) {
      panel.style.removeProperty('inset');
    }
    launcher.current.style.inset = `${buttonTop}px auto auto ${buttonLeft}px`;
  }, [launcher, narrow, panel]);

  useLayoutEffect(() => {
    layout();
    const observer = new ResizeObserver(layout);
    for (const element of [bounds.current, launcher.current, panel]) {
      if (element) observer.observe(element);
    }
    return () => observer.disconnect();
  }, [layout, launcher, panel]);

  function move(left: number, top: number) {
    if (!bounds.current || !launcher.current) return;
    const area = bounds.current.getBoundingClientRect();
    const button = launcher.current.getBoundingClientRect();
    const frame = panel && !narrow ? panel.getBoundingClientRect() : null;
    const x = clamp((left - area.left) / Math.max(1, area.width - (frame ?? button).width), 0, 1);
    let buttonTop = top - area.top;
    if (frame) {
      const row = button.height + gap;
      const below = position.current.panelBelow;
      const panelTop = clamp(buttonTop, below ? row : 0, area.height - frame.height - (below ? 0 : row));
      buttonTop = below ? panelTop - row : panelTop + frame.height + gap;
    }
    const y = clamp(buttonTop / Math.max(1, area.height - button.height), 0, 1);
    position.current = { x, y, panelBelow: frame ? position.current.panelBelow : y < 0.5 };
    layout();
  }

  function save() {
    try { localStorage.setItem(storageKey, JSON.stringify(position.current)); }
    catch { /* The position still applies in the current tab. */ }
  }
  function cancel() {
    if (drag.current?.moved) {
      position.current = drag.current.original;
      layout();
    }
    drag.current = null;
  }
  function handleTarget(target: EventTarget | null) {
    if (!(target instanceof Element)) return false;
    const control = target.closest('button');
    return !control || control === launcher.current;
  }
  function activeRect() {
    return (panel && !narrow ? panel : launcher.current)?.getBoundingClientRect();
  }

  const handlers = {
    onPointerDown(event: PointerEvent<HTMLElement>) {
      if (!event.isPrimary || event.button !== 0 || !handleTarget(event.target)) return;
      const rect = activeRect();
      if (!rect) return;
      suppressClick.current = false;
      drag.current = { id: event.pointerId, startX: event.clientX, startY: event.clientY, left: rect.left, top: rect.top, original: position.current, moved: false };
      event.currentTarget.setPointerCapture(event.pointerId);
    },
    onPointerMove(event: PointerEvent<HTMLElement>) {
      const start = drag.current;
      if (!start || start.id !== event.pointerId) return;
      const dx = event.clientX - start.startX;
      const dy = event.clientY - start.startY;
      if (!start.moved && Math.hypot(dx, dy) < 5) return;
      start.moved = true;
      move(start.left + dx, start.top + dy);
    },
    onPointerUp(event: PointerEvent<HTMLElement>) {
      if (drag.current?.id !== event.pointerId) return;
      if (drag.current.moved) {
        suppressClick.current = true;
        save();
      }
      drag.current = null;
    },
    onLostPointerCapture(event: PointerEvent<HTMLElement>) {
      if (drag.current?.id === event.pointerId) cancel();
    },
    onPointerCancel(event: PointerEvent<HTMLElement>) {
      if (drag.current?.id === event.pointerId) cancel();
    },
    onKeyDown(event: KeyboardEvent<HTMLElement>) {
      if (!handleTarget(event.target) || event.altKey || event.ctrlKey || event.metaKey) return;
      const rect = activeRect();
      if (!rect) return;
      switch (event.key) {
        case 'ArrowLeft': move(rect.left - 32, rect.top); break;
        case 'ArrowRight': move(rect.left + 32, rect.top); break;
        case 'ArrowUp': move(rect.left, rect.top - 32); break;
        case 'ArrowDown': move(rect.left, rect.top + 32); break;
        case 'Home': position.current = initial; layout(); break;
        default: return;
      }
      event.preventDefault();
      save();
    },
  };
  return { bounds, setPanel, suppressClick, handlers };
}
