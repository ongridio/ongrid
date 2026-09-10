import { expect, it } from 'vitest';
import { facingSides, routeAroundNodes } from './route';

it('routes service-to-device edges around clusters and recomputes after dragging', () => {
  const start = { x: 120, y: 108 };
  const end = { x: 360, y: 384 };
  const nodes = [
    { x: 40, y: 40, width: 160, height: 44 },
    { x: 40, y: 224, width: 160, height: 44 },
    { x: 280, y: 224, width: 160, height: 44 },
    { x: 280, y: 408, width: 160, height: 44 },
  ];
  for (const shift of [0, 80]) {
    const moved = nodes.map((n, i) => i === 1 ? { ...n, x: n.x + shift } : n);
    for (const [from, to] of [[start, end], [end, start]]) {
      const route = routeAroundNodes(from, to, moved)!;
      expect(route[0]).toEqual(from);
      expect(route.at(-1)).toEqual(to);
      for (let i = 1; i < route.length; i++) {
        const a = route[i - 1], b = route[i];
        expect(a.x === b.x || a.y === b.y).toBe(true);
        for (const n of moved) {
          const crosses = a.x === b.x
            ? a.x > n.x && a.x < n.x + n.width && Math.max(a.y, b.y) > n.y && Math.min(a.y, b.y) < n.y + n.height
            : a.y > n.y && a.y < n.y + n.height && Math.max(a.x, b.x) > n.x && Math.min(a.x, b.x) < n.x + n.width;
          expect(crosses).toBe(false);
        }
      }
    }
  }
  expect(routeAroundNodes({ x: 0, y: 0 }, { x: 0, y: 100 }, [])).toEqual([{ x: 0, y: 0 }, { x: 0, y: 100 }]);
});

it('uses facing handles and straight routes for side-by-side nodes', () => {
  const source = { x: 40, y: 40 }, target = { x: 310, y: 40 };
  expect(facingSides(source, target)).toEqual(['right', 'left']);
  expect(facingSides(target, source)).toEqual(['left', 'right']);
  expect(facingSides(source, { x: 40, y: 200 })).toEqual(['bottom', 'top']);
  expect(facingSides({ x: 40, y: 200 }, source)).toEqual(['top', 'bottom']);
  expect(routeAroundNodes({ x: 224, y: 62 }, { x: 286, y: 62 },
    [source, target].map((p) => ({ ...p, width: 160, height: 44 })))).toEqual([{ x: 224, y: 62 }, { x: 286, y: 62 }]);
});
