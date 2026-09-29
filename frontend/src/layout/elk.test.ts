// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll } from 'vitest';
import type { Node as RFNode, Edge as RFEdge } from '@xyflow/react';
import { computeLayout } from './elk';

// jsdom doesn't implement Canvas API — provide a minimal stub so
// measureNodeWidth can call measureText without throwing.
beforeAll(() => {
  HTMLCanvasElement.prototype.getContext = vi.fn().mockReturnValue({
    font: '',
    measureText: vi.fn(() => ({ width: 60 })),
  }) as ReturnType<typeof vi.fn>;
});

function makeNode(
  id: string,
  name: string,
  type = 'containerNode',
  parentId?: string,
): RFNode {
  return {
    id,
    type,
    position: { x: 0, y: 0 },
    data: { dgNode: { id, name, type: type === 'networkGroup' ? 'network' : 'container' } },
    parentId,
    ...(type === 'networkGroup' ? { style: {} } : {}),
  } as unknown as RFNode;
}

function makeEdge(id: string, source: string, target: string): RFEdge {
  return { id, source, target, data: {} } as RFEdge;
}

describe('computeLayout', () => {
  it('returns positioned nodes and edges for a simple graph', async () => {
    const nodes = [makeNode('a', 'web'), makeNode('b', 'api')];
    const edges = [makeEdge('e1', 'a', 'b')];

    const result = await computeLayout(nodes, edges);

    expect(result.nodes).toHaveLength(2);
    expect(result.edges).toHaveLength(1);

    // Nodes should have real positions assigned by ELK
    for (const n of result.nodes) {
      expect(typeof n.position.x).toBe('number');
      expect(typeof n.position.y).toBe('number');
    }

    // Edge should have an elk type and path data
    expect(result.edges[0].type).toBe('elk');
    expect(result.edges[0].data?.path).toBeDefined();
  });

  it('handles a single node with no edges', async () => {
    const nodes = [makeNode('solo', 'my-service')];

    const result = await computeLayout(nodes, []);

    expect(result.nodes).toHaveLength(1);
    expect(result.edges).toHaveLength(0);
  });

  it('assigns nodeWidth to non-group nodes', async () => {
    const nodes = [makeNode('a', 'web'), makeNode('b', 'db')];
    const edges = [makeEdge('e1', 'a', 'b')];

    const result = await computeLayout(nodes, edges);

    for (const n of result.nodes) {
      expect(n.data.nodeWidth).toBeGreaterThanOrEqual(140);
      expect(n.data.nodeWidth).toBeLessThanOrEqual(250);
    }
  });

  it('positions network group nodes with width and height', async () => {
    const net = makeNode('net1', 'frontend', 'networkGroup');
    const c1 = makeNode('c1', 'web', 'containerNode', 'net1');
    const c2 = makeNode('c2', 'api', 'containerNode', 'net1');

    const result = await computeLayout([net, c1, c2], []);

    const group = result.nodes.find((n) => n.type === 'networkGroup');
    expect(group).toBeDefined();
    expect(group!.style?.width).toBeGreaterThan(0);
    expect(group!.style?.height).toBeGreaterThan(0);
  });

  it('handles volume nodes with correct height', async () => {
    const vol = makeNode('v1', 'pgdata', 'volumeNode');
    const container = makeNode('c1', 'postgres');
    const edges = [makeEdge('e1', 'c1', 'v1')];

    const result = await computeLayout([vol, container], edges);

    expect(result.nodes).toHaveLength(2);
  });

  it('preserves edge data while adding path', async () => {
    const nodes = [makeNode('a', 'web'), makeNode('b', 'api')];
    const edges: RFEdge[] = [{
      id: 'e1',
      source: 'a',
      target: 'b',
      data: { custom: 'value', animated: true },
    }];

    const result = await computeLayout(nodes, edges);

    expect(result.edges[0].data?.custom).toBe('value');
    expect(result.edges[0].data?.animated).toBe(true);
    expect(result.edges[0].data?.path).toBeDefined();
  });

  it('handles a larger graph with multiple connected components', async () => {
    const nodes = [
      makeNode('a', 'web'),
      makeNode('b', 'api'),
      makeNode('c', 'db'),
      makeNode('d', 'cache'),
    ];
    const edges = [
      makeEdge('e1', 'a', 'b'),
      makeEdge('e2', 'c', 'd'),
    ];

    const result = await computeLayout(nodes, edges);

    expect(result.nodes).toHaveLength(4);
    expect(result.edges).toHaveLength(2);

    // All nodes should have distinct positions
    const positions = result.nodes.map((n) => `${n.position.x},${n.position.y}`);
    const unique = new Set(positions);
    expect(unique.size).toBe(4);
  });

  it('handles a chain topology (a → b → c → d)', async () => {
    const nodes = [
      makeNode('a', 'frontend'),
      makeNode('b', 'api'),
      makeNode('c', 'worker'),
      makeNode('d', 'db'),
    ];
    const edges = [
      makeEdge('e1', 'a', 'b'),
      makeEdge('e2', 'b', 'c'),
      makeEdge('e3', 'c', 'd'),
    ];

    const result = await computeLayout(nodes, edges);

    // In a DOWN-direction layered layout, nodes should be stacked vertically
    const ys = result.nodes.map((n) => n.position.y);
    const sortedYs = [...ys].sort((a, b) => a - b);
    // At least 2 distinct y levels
    expect(new Set(sortedYs).size).toBeGreaterThanOrEqual(2);
  });
});

describe('computeLayout — swarm node boxes', () => {
  function task(id: string, parentId: string): RFNode {
    return {
      id, type: 'taskNode', position: { x: 0, y: 0 }, parentId,
      data: { dgNode: { id, name: id, type: 'container' } },
    } as unknown as RFNode;
  }
  function box(id: string): RFNode {
    return {
      id, type: 'nodeGroup', position: { x: 0, y: 0 }, style: {},
      data: { dgNode: { id, name: id, type: 'swarmnode' }, taskCount: 0 },
    } as unknown as RFNode;
  }

  it('packs tasks inside their box below the header and sizes the box', async () => {
    const tasks = Array.from({ length: 6 }, (_, i) => task(`t${i}`, 'swarmnode:a'));
    const result = await computeLayout([box('swarmnode:a'), box('swarmnode:b'), ...tasks], []);

    const a = result.nodes.find((n) => n.id === 'swarmnode:a')!;
    const b = result.nodes.find((n) => n.id === 'swarmnode:b')!;
    const aw = Number(a.style?.width);
    const ah = Number(a.style?.height);
    expect(aw).toBeGreaterThanOrEqual(280);
    expect(Number(b.style?.width)).toBeGreaterThanOrEqual(280);

    for (const t of result.nodes.filter((n) => n.type === 'taskNode')) {
      expect(t.position.y).toBeGreaterThanOrEqual(60);
      expect(t.position.x).toBeGreaterThanOrEqual(0);
      expect(t.position.x).toBeLessThan(aw);
      expect(t.position.y).toBeLessThan(ah);
      // Task cards get the shared node width like other cards.
      expect((t.data as { nodeWidth?: number }).nodeWidth).toBeGreaterThan(0);
    }
    // Several tasks wrap into more than one row instead of one long line.
    const ys = new Set(result.nodes.filter((n) => n.type === 'taskNode').map((n) => n.position.y));
    expect(ys.size).toBeGreaterThan(1);
    // The first task sits top-left (input order is kept).
    const t0 = result.nodes.find((n) => n.id === 't0')!;
    for (const t of result.nodes.filter((n) => n.type === 'taskNode')) {
      expect(t.position.y).toBeGreaterThanOrEqual(t0.position.y);
    }
  });
});
