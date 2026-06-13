import { describe, expect, it } from 'vitest';
import { splitFrontmatter, stripFrontmatter } from './frontmatter';

describe('splitFrontmatter', () => {
  it('parses scalar and inline-array values, body excludes the block', () => {
    const md = '---\ntitle: Linux Memory Model\ntags: [linux, memory]\n---\n\n# 正文\n';
    const fm = splitFrontmatter(md);
    expect(fm).not.toBeNull();
    expect(fm!.meta).toEqual([
      ['title', 'Linux Memory Model'],
      ['tags', ['linux', 'memory']],
    ]);
    expect(fm!.body).toBe('\n# 正文\n');
  });

  it('unquotes values and tolerates CRLF / 注释 / 空行', () => {
    const md = '---\r\ntitle: "Hello"\r\n# comment\r\n\r\nauthor: \'张三\'\r\n---\r\nbody';
    const fm = splitFrontmatter(md);
    expect(fm!.meta).toEqual([
      ['title', 'Hello'],
      ['author', '张三'],
    ]);
    expect(fm!.body).toBe('body');
  });

  it('handles a frontmatter-only document (closing --- at EOF)', () => {
    const fm = splitFrontmatter('---\ntitle: t\n---');
    expect(fm!.meta).toEqual([['title', 't']]);
    expect(fm!.body).toBe('');
  });

  it('returns null when the document does not start with ---', () => {
    expect(splitFrontmatter('# title\n---\nkey: value\n---\n')).toBeNull();
  });

  it('returns null on nested YAML（缩进行）— caller falls back to raw render', () => {
    expect(splitFrontmatter('---\nmeta:\n  nested: true\n---\nbody')).toBeNull();
  });

  it('returns null for a leading thematic break with no key: value content', () => {
    expect(splitFrontmatter('---\n\n---\nbody')).toBeNull();
  });
});

describe('stripFrontmatter', () => {
  it('returns the body when frontmatter exists', () => {
    expect(stripFrontmatter('---\na: 1\n---\nbody')).toBe('body');
  });

  it('returns input unchanged when there is none', () => {
    expect(stripFrontmatter('plain **md**')).toBe('plain **md**');
  });
});
