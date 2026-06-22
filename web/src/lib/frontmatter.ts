// YAML frontmatter 的轻量解析：只为「展示」服务，不追求完整 YAML 语义。
// md 文档若以 --- 包裹的元信息块开头（title/tags/...），直接交给
// ReactMarkdown 会被当成 setext 标题渲染成粗体大字 —— 业界通行做法
// （GitHub/Obsidian）是把 frontmatter 从正文剥离、单独弱化展示。

export interface ParsedFrontmatter {
  /** 保序的 key/value 列表；数组值（tags 等）解析为 string[] */
  meta: [string, string | string[]][];
  /** 剥离 frontmatter 后的正文 */
  body: string;
}

function unquote(s: string): string {
  if (s.length >= 2 && ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'")))) {
    return s.slice(1, -1);
  }
  return s;
}

function parseValue(raw: string): string | string[] {
  const v = raw.trim();
  // 行内数组：[a, b, c]
  if (v.startsWith('[') && v.endsWith(']')) {
    return v
      .slice(1, -1)
      .split(',')
      .map((s) => unquote(s.trim()))
      .filter(Boolean);
  }
  return unquote(v);
}

/**
 * 解析文档开头的 YAML frontmatter。无 frontmatter、或块内出现解析不了的
 * 行（嵌套结构等）时返回 null —— 调用方按原文渲染，保证不弄坏内容。
 */
export function splitFrontmatter(md: string): ParsedFrontmatter | null {
  const m = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/.exec(md);
  if (!m) return null;
  const meta: [string, string | string[]][] = [];
  for (const line of m[1].split(/\r?\n/)) {
    if (line.trim() === '' || line.trim().startsWith('#')) continue;
    // 仅支持顶层 key: value；缩进行（嵌套/多行块）超出展示需求，放弃解析
    if (/^\s/.test(line)) return null;
    const idx = line.indexOf(':');
    if (idx <= 0) return null;
    const key = line.slice(0, idx).trim();
    meta.push([key, parseValue(line.slice(idx + 1))]);
  }
  if (meta.length === 0) return null;
  return { meta, body: md.slice(m[0].length) };
}

/** 仅取正文（搜索预览等场景用），无 frontmatter 时原样返回 */
export function stripFrontmatter(md: string): string {
  return splitFrontmatter(md)?.body ?? md;
}
