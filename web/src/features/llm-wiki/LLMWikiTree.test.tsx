import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { LLMWikiTree } from './LLMWikiTree';
import type { LLMWikiNode } from './types';

describe('LLMWikiTree', () => {
  const nodes: LLMWikiNode[] = [
    {
      id: 'raw-network',
      parent_id: '',
      layer: 'raw',
      kind: 'folder',
      name: 'network',
      relative_path: 'network',
      has_children: false,
      child_count: 0,
      document_count: 1,
    },
  ];

  it('localizes source directory names without changing file names', async () => {
    render(
      <LLMWikiTree
        nodes={nodes}
        documentCounts={{ raw: 1, wiki: 0 }}
        activeDirectory={null}
        activeLayer="all"
        onSelectDirectory={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByRole('button', { name: /^原始来源/ }));

    expect(screen.getByText('网络')).toBeInTheDocument();
    expect(screen.queryByText('Network')).not.toBeInTheDocument();
  });

  it('starts collapsed and toggles children from the folder row', async () => {
    render(
      <LLMWikiTree
        nodes={nodes}
        documentCounts={{ raw: 1, wiki: 0 }}
        activeDirectory={null}
        activeLayer="all"
        onSelectDirectory={vi.fn()}
      />,
    );

    const rawRoot = screen.getByRole('button', { name: /^原始来源/ });
    expect(rawRoot).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('网络')).not.toBeInTheDocument();

    await userEvent.click(rawRoot);
    expect(rawRoot).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('网络')).toBeInTheDocument();

    await userEvent.click(rawRoot);
    expect(rawRoot).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('网络')).not.toBeInTheDocument();
  });

  // FolderNode (pages/Knowledge.tsx) puts its folder icon 42px into the row,
  // behind a 6px inset, a 22px expand chevron, a 4px gap and the icon button's
  // 10px padding. The Wiki roots started at 0px, so 原始来源/生成页面 sat flush
  // left under the section header while the folders above them were inset.
  it('insets rows like the knowledge-base tree so the folder rows line up', async () => {
    render(
      <LLMWikiTree
        nodes={nodes}
        documentCounts={{ raw: 1, wiki: 0 }}
        activeDirectory={null}
        activeLayer="all"
        onSelectDirectory={vi.fn()}
      />,
    );

    const rawRoot = screen.getByRole('button', { name: /^原始来源/ });
    expect(rawRoot).toHaveStyle({ paddingLeft: '42px' });

    await userEvent.click(rawRoot);
    expect(screen.getByRole('button', { name: /^网络/ })).toHaveStyle({ paddingLeft: '54px' });
  });
});
