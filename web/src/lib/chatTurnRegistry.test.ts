import { describe, expect, it } from 'vitest';
import {
  getChatTurnController,
  registerChatTurnController,
  unregisterChatTurnController,
} from './chatTurnRegistry';

describe('chat turn registry', () => {
  it('keeps each session turn available while the user switches sessions', () => {
    const first = new AbortController();
    const second = new AbortController();

    registerChatTurnController('session-a', first);
    registerChatTurnController('session-b', second);

    expect(getChatTurnController('session-a')).toBe(first);
    expect(getChatTurnController('session-b')).toBe(second);

    unregisterChatTurnController('session-a', first);
    unregisterChatTurnController('session-b', second);
  });

  it('does not let an older turn clear its replacement', () => {
    const oldTurn = new AbortController();
    const newTurn = new AbortController();

    registerChatTurnController('session-reused', oldTurn);
    registerChatTurnController('session-reused', newTurn);

    expect(oldTurn.signal.aborted).toBe(true);
    unregisterChatTurnController('session-reused', oldTurn);
    expect(getChatTurnController('session-reused')).toBe(newTurn);

    unregisterChatTurnController('session-reused', newTurn);
  });
});
