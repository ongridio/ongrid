const controllers = new Map<string, AbortController>();

const listeners = new Set<(sessionID: string) => void>();

export function subscribeChatTurns(listener: (sessionID: string) => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function getChatTurnController(sessionID: string): AbortController | null {
  return controllers.get(sessionID) ?? null;
}

export function registerChatTurnController(sessionID: string, controller: AbortController): void {
  controllers.get(sessionID)?.abort();
  controllers.set(sessionID, controller);
  listeners.forEach(listener => listener(sessionID));
}

export function unregisterChatTurnController(sessionID: string, controller: AbortController): void {
  if (controllers.get(sessionID) !== controller) return;
  controllers.delete(sessionID);
  listeners.forEach(listener => listener(sessionID));
}
