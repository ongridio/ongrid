import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { PageHeader } from './PageHeader';
import { Tabs, TabsList, TabsTrigger } from './Tabs';

describe('PageHeader', () => {
  it('keeps page navigation inside the shared header boundary', () => {
    render(<Tabs defaultValue="services"><PageHeader title="Services" navigation={
      <TabsList><TabsTrigger value="services">Service list</TabsTrigger></TabsList>
    } /></Tabs>);
    const header = screen.getByRole('banner');
    expect(within(header).getByRole('tab', { name: 'Service list' })).toBeInTheDocument();
    expect(within(header).getByRole('tablist').parentElement).toHaveClass('[&>.og-tabs-list]:border-b-0');
  });

  it('preserves the header and filter layout without navigation', () => {
    render(<PageHeader title="Tools" extra={<input aria-label="Target" />} />);
    expect(within(screen.getByRole('banner')).getByRole('textbox', { name: 'Target' })).toBeInTheDocument();
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
  });
});
