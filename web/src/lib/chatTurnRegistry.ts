const controllers = new Map<string, AbortController>();

export function getChatTurnController(sessionID: string): AbortController | null {
  return controllers.get(sessionID) ?? null;
}

export function registerChatTurnController(sessionID: string, controller: AbortController): void {
  controllers.get(sessionID)?.abort();
  controllers.set(sessionID, controller);
}

export function unregisterChatTurnController(sessionID: string, controller: AbortController): void {
  if (controllers.get(sessionID) === controller) controllers.delete(sessionID);
}
