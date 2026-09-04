import { beforeEach, describe, expect, it } from 'vitest';
import { useModelSelection } from './modelSelection';

describe('model selection store', () => {
  beforeEach(() => {
    localStorage.clear();
    useModelSelection.setState({ selected: null, sessionSelections: {} });
  });

  it('keeps model selections isolated by session', () => {
    const store = useModelSelection.getState();

    store.setSessionSelected('session-a', { provider: 'openai', model: 'gpt-a' });
    store.setSessionSelected('session-b', { provider: 'anthropic', model: 'claude-b' });

    expect(useModelSelection.getState().sessionSelections).toEqual({
      'session-a': { provider: 'openai', model: 'gpt-a' },
      'session-b': { provider: 'anthropic', model: 'claude-b' },
    });
  });

  it('clears only the deleted session selection', () => {
    useModelSelection.setState({
      sessionSelections: {
        'session-a': { provider: 'openai', model: 'gpt-a' },
        'session-b': { provider: 'anthropic', model: 'claude-b' },
      },
    });

    useModelSelection.getState().clearSessionSelected('session-a');

    expect(useModelSelection.getState().sessionSelections).toEqual({
      'session-b': { provider: 'anthropic', model: 'claude-b' },
    });
  });
});
