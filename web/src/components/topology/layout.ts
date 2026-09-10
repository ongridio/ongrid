type LayoutNode = { id: string; type: string; x: number; y: number };

// Keep the three resource columns stable even when an upstream node is absent.
// Dagre still chooses row order; only resolve collisions introduced by snapping.
export function alignResourceColumns(nodes: LayoutNode[], width: number, height: number) {
  const columns: Record<string, number> = { service: 0, device: 1, network_device: 1, cluster: 2 };
  const otherXs = [...new Set(nodes.filter((n) => columns[n.type] === undefined).map((n) => n.x))].sort((a, b) => a - b);
  const bottom = new Map<number, number>();
  const positions = new Map<string, { x: number; y: number }>();
  for (const node of [...nodes].sort((a, b) => a.y - b.y || a.x - b.x || a.id.localeCompare(b.id))) {
    const column = columns[node.type] ?? 3 + otherXs.indexOf(node.x);
    const x = 40 + column * (width + 110);
    const y = Math.max(node.y, bottom.get(column) ?? 40);
    positions.set(node.id, { x, y });
    bottom.set(column, y + height + 80);
  }
  return positions;
}
