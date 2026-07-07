import type { KeyboardEvent as ReactKeyboardEvent } from 'react';

export function isImeComposing(e: ReactKeyboardEvent<HTMLElement>): boolean {
  return e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229;
}
