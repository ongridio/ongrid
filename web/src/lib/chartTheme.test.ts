import { describe, expect, it } from 'vitest';
import {
  chartTooltipItemStyle,
  chartTooltipLabelStyle,
  chartTooltipStyle,
} from './chartTheme';

describe('chart tooltip theme', () => {
  it('uses application theme tokens instead of fixed dark colors', () => {
    expect(chartTooltipStyle).toMatchObject({
      background: 'rgb(var(--card))',
      border: '1px solid rgb(var(--border))',
      color: 'rgb(var(--text))',
    });
    expect(chartTooltipLabelStyle.color).toBe('rgb(var(--text-muted))');
    expect(chartTooltipItemStyle.color).toBe('rgb(var(--text))');
  });
});
