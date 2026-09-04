import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { CommunicationProviderIcon, ModelIcon, ProviderIcon } from './Provider';

describe('ProviderIcon', () => {
  it.each([
    ['minimax', 'MiniMax-M2.7', '#e73562'],
    ['xiaomi', 'mimo-v2.5-pro', '#000000'],
  ])('renders the %s brand for provider and model', (provider, model, color) => {
    const providerView = render(<ProviderIcon provider={provider} />);
    expect(providerView.container.querySelector(`[fill="${color}"]`)).toBeInTheDocument();
    providerView.unmount();

    const modelView = render(<ModelIcon provider="custom" model={model} />);
    expect(modelView.container.querySelector(`[fill="${color}"]`)).toBeInTheDocument();
  });
});

describe('CommunicationProviderIcon', () => {
  it.each([
    ['feishu', '#00D6B9'],
    ['dingtalk', '#0089FF'],
    ['wecom', '#07C160'],
    ['slack', '#E01E5A'],
    ['telegram', '#229ED9'],
  ])('renders the %s brand mark', (provider, color) => {
    const view = render(<CommunicationProviderIcon provider={provider} />);
    expect(view.container.querySelector(`svg[data-brand="${provider}"] [fill="${color}"]`)).toBeInTheDocument();
  });
});
