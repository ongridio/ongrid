type Point = { x: number; y: number };
type Box = Point & { width: number; height: number };

// Choose the facing sides from live node positions, including after dragging.
export function facingSides(source: Point, target: Point): [string, string] {
  const dx = target.x - source.x, dy = target.y - source.y;
  return Math.abs(dx) / 160 >= Math.abs(dy) / 44
    ? dx >= 0 ? ['right', 'left'] : ['left', 'right']
    : dy >= 0 ? ['bottom', 'top'] : ['top', 'bottom'];
}

// Orthogonal visibility grid around padded node bounds. The caller supplies
// live positions so dragging an unrelated node also updates the route.
export function routeAroundNodes(start: Point, end: Point, nodes: Box[]): Point[] | null {
  const boxes = nodes.map((n) => ({ x: n.x - 12, y: n.y - 12, right: n.x + n.width + 12, bottom: n.y + n.height + 12 }));
  const xs = [...new Set([start.x, end.x, ...boxes.flatMap((b) => [b.x, b.right])])].sort((a, b) => a - b);
  const ys = [...new Set([start.y, end.y, ...boxes.flatMap((b) => [b.y, b.bottom])])].sort((a, b) => a - b);
  const point = (id: number): Point => ({ x: xs[id % xs.length], y: ys[Math.floor(id / xs.length)] });
  const blocked = (a: Point, b: Point) => boxes.some((r) => a.x === b.x
    ? a.x > r.x && a.x < r.right && Math.max(a.y, b.y) > r.y && Math.min(a.y, b.y) < r.bottom
    : a.y > r.y && a.y < r.bottom && Math.max(a.x, b.x) > r.x && Math.min(a.x, b.x) < r.right);
  const first = ys.indexOf(start.y) * xs.length + xs.indexOf(start.x);
  const last = ys.indexOf(end.y) * xs.length + xs.indexOf(end.x);
  const distance = (a: Point, b: Point) => Math.abs(a.x - b.x) + Math.abs(a.y - b.y);
  const costs = new Map([[first, 0]]);
  const previous = new Map<number, number>();
  const open = [{ id: first, cost: 0, estimate: distance(start, end) }];
  // ponytail: quadratic grid for small topology views; use a sparse visibility
  // graph if profiling shows routing dominates large graphs.
  while (open.length) {
    open.sort((a, b) => b.estimate - a.estimate);
    const current = open.pop()!;
    if (current.cost !== costs.get(current.id)) continue;
    if (current.id === last) {
      const path = [end];
      let id = last;
      while (id !== first) {
        id = previous.get(id)!;
        path.push(point(id));
      }
      path.reverse();
      return path.filter((p, i) => i === 0 || i === path.length - 1 ||
        !((path[i - 1].x === p.x && p.x === path[i + 1].x) || (path[i - 1].y === p.y && p.y === path[i + 1].y)));
    }
    const x = current.id % xs.length;
    const y = Math.floor(current.id / xs.length);
    const neighbors = [x > 0 ? current.id - 1 : -1, x + 1 < xs.length ? current.id + 1 : -1,
      y > 0 ? current.id - xs.length : -1, y + 1 < ys.length ? current.id + xs.length : -1];
    const a = point(current.id);
    for (const id of neighbors) {
      if (id < 0) continue;
      const b = point(id);
      if (blocked(a, b)) continue;
      const cost = current.cost + distance(a, b);
      if (cost >= (costs.get(id) ?? Infinity)) continue;
      costs.set(id, cost);
      previous.set(id, current.id);
      open.push({ id, cost, estimate: cost + distance(b, end) });
    }
  }
  return null;
}
