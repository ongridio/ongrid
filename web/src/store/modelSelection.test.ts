import { beforeEach, describe, expect, it } from 'vitest';
import { useModelSelection } from './modelSelection';

describe('modelSelection', () => {
  beforeEach(() => {
    useModelSelection.setState({ selected: null });
  });

  it('stores only the Home/catalog default', () => {
    useModelSelection.getState().setSelected({ provider: 'custom', model: 'qwen' });
    expect(useModelSelection.getState().selected).toEqual({ provider: 'custom', model: 'qwen' });
    expect(useModelSelection.getState()).not.toHaveProperty('sessionSelections');
  });
});
