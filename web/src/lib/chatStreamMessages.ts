import type { ChatMessage } from '@/api/chat';

export function mergeAssistantFinalMessage(
  messages: ChatMessage[],
  deltaID: string,
  finalMessage: ChatMessage,
): ChatMessage[] {
  const deltaMessage = messages.find((message) => message.id === deltaID);
  const withoutDelta = messages.filter((message) => message.id !== deltaID);
  const stableIndex = withoutDelta.findIndex((message) => message.id === finalMessage.id);
  const stableMessage = stableIndex >= 0 ? withoutDelta[stableIndex] : undefined;
  const reasoning = deltaMessage?.reasoning
    ?? finalMessage.reasoning
    ?? stableMessage?.reasoning;

  const merged: ChatMessage = {
    ...stableMessage,
    ...finalMessage,
  };
  if (reasoning !== undefined) {
    merged.reasoning = reasoning;
  }

  if (stableIndex < 0) {
    return [...withoutDelta, merged];
  }
  const next = withoutDelta.slice();
  next[stableIndex] = merged;
  return next;
}
