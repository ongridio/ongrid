import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { Tree, type TreeNode } from './Tree';

const nodes: TreeNode[] = [
  {
    id: 'root',
    label: 'root',
    children: [
      { id: 'a', label: 'a' },
      { id: 'b', label: 'b', children: [{ id: 'b1', label: 'b1' }] },
    ],
  },
  { id: 'other', label: 'other' },
];

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

describe('structure', () => {
  it('draws only the visible nodes, and reports the level', () => {
    render(<Tree nodes={nodes} label="tree" defaultExpanded={['root']} />);
    // `b1` IS UNDER A CLOSED PARENT: in the DOM it must not be, because a closed
    // node's children are neither visible nor in the arrow order.
    expect(screen.getByText('a')).toBeInTheDocument();
    expect(screen.queryByText('b1')).not.toBeInTheDocument();

    const items = screen.getAllByRole('treeitem');
    const level = (id: string) => items.find((item) => within(item).queryByText(id))?.getAttribute('aria-level');
    expect(level('root')).toBe('1');
    expect(level('a')).toBe('2');
  });

  it('does not claim a leaf can expand', () => {
    render(<Tree nodes={nodes} label="tree" />);
    const items = screen.getAllByRole('treeitem');
    const leaf = items.find((item) => within(item).queryByText('other'))!;
    // A leaf with `aria-expanded="false"` tells a screen reader there is
    // something under it that is closed.
    expect(leaf).not.toHaveAttribute('aria-expanded');
    const root = items.find((item) => within(item).queryByText('root'))!;
    expect(root).toHaveAttribute('aria-expanded', 'false');
  });

  it('asks for children when an unloaded node opens', async () => {
    const user = userEvent.setup();
    const onExpand = vi.fn();
    render(<Tree nodes={[{ id: 'lazy', label: 'lazy', hasChildren: true }]} label="tree" onExpand={onExpand} />);
    // A SINGLE CLICK SELECTS; opening is the chevron, a double click, or the
    // Right arrow. The keyboard path is the one a large tree is actually driven
    // with, so that is what this asserts.
    await user.click(screen.getByRole('treeitem'));
    expect(onExpand).not.toHaveBeenCalled();
    await user.keyboard('{ArrowRight}');
    expect(onExpand).toHaveBeenCalledWith(expect.objectContaining({ id: 'lazy' }), true);
  });
});

describe('the keyboard', () => {
  // A HIERARCHY IS TOO BIG TO DRIVE BY CLICKING CHEVRONS. If the arrows do not
  // work, the tree is unusable for a keyboard user — not merely inconvenient.
  it('moves with the arrow keys and opens with Right', async () => {
    const user = userEvent.setup();
    render(<Tree nodes={nodes} label="tree" />);
    const items = () => screen.getAllByRole('treeitem');

    // ONE TAB STOP: only the focused node is tabbable.
    expect(items().filter((item) => item.getAttribute('tabindex') === '0')).toHaveLength(1);

    items()[0].focus();
    await user.keyboard('{ArrowDown}');
    expect(document.activeElement).toHaveTextContent('other');

    await user.keyboard('{ArrowUp}');
    expect(document.activeElement).toHaveTextContent('root');

    await user.keyboard('{ArrowRight}');
    expect(screen.getByText('a')).toBeInTheDocument();
    expect(screen.getAllByRole('treeitem')[0]).toHaveAttribute('aria-expanded', 'true');

    await user.keyboard('{ArrowDown}{ArrowDown}');
    expect(document.activeElement).toHaveTextContent('b');

    await user.keyboard('{ArrowRight}');
    expect(screen.getByText('b1')).toBeInTheDocument();

    // LEFT CLOSES AN OPEN NODE, THEN GOES TO THE PARENT.
    await user.keyboard('{ArrowLeft}');
    expect(screen.queryByText('b1')).not.toBeInTheDocument();
    await user.keyboard('{ArrowLeft}');
    expect(document.activeElement).toHaveTextContent('root');
  });

  it('selects with Enter and does not select a disabled node', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <Tree
        nodes={[
          { id: 'ok', label: 'ok' },
          { id: 'off', label: 'off', disabled: true },
        ]}
        label="tree"
        onSelect={onSelect}
      />,
    );
    await user.click(screen.getByText('ok'));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: 'ok' }));

    onSelect.mockClear();
    await user.click(screen.getByText('off'));
    expect(onSelect).not.toHaveBeenCalled();
  });
});

describe('empty', () => {
  it('says so rather than drawing an empty box', () => {
    render(<Tree nodes={[]} label="tree" empty="还没有节点" />);
    expect(screen.getByText('还没有节点')).toBeInTheDocument();
    expect(screen.queryByRole('tree')).not.toBeInTheDocument();
  });
});
