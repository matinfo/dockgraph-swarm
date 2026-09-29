import { describe, it, expect } from 'vitest';
import type { Node as RFNode } from '@xyflow/react';
import {
  layoutNodeGroups,
  serviceCardHeight,
  NODE_BOX_WIDTH,
  NODE_BOX_PADDING,
  BOX_GAP_X,
  CARD_GAP,
  CARD_WIDTH,
  MAX_WORKERS_PER_ROW,
  ROLE_GROUP_PADDING_X,
  ROLE_GROUP_PADDING_TOP,
  SECTION_GAP,
} from './nodeLayout';
import { toNodeGroupedFlowNodes, UNASSIGNED_NODE_GROUP_ID } from '../utils/nodeTransform';
import { NODE_BOX_HEADER_HEIGHT, NODE_BOX_MIN_HEIGHT } from '../utils/constants';
import type { DGNode, TaskInfo } from '../types';

function swarmNode(name: string, role: 'manager' | 'worker'): DGNode {
  return {
    id: `swarmnode:${name}`, type: 'swarmnode', name, status: 'ready',
    swarmNode: { id: name, role, leader: false, availability: 'active', state: 'ready' },
  };
}

function service(name: string, hosts: string[]): DGNode {
  const tasks: TaskInfo[] = hosts.map((h, i) => ({
    id: `${name}-${i}`, slot: i + 1, nodeHostname: h, state: 'running', desiredState: 'running',
  }));
  return { id: `service:${name}`, type: 'service', name, status: 'running', service: { mode: 'replicated', tasks } };
}

const byId = (nodes: RFNode[], id: string) => nodes.find((n) => n.id === id)!;
const size = (n: RFNode) => ({ w: Number(n.style?.width), h: Number(n.style?.height) });

describe('layoutNodeGroups', () => {
  const managers = ['m1', 'm2', 'm3'].map((n) => swarmNode(n, 'manager'));
  const workers = Array.from({ length: 9 }, (_, i) => swarmNode(`w${i + 1}`, 'worker'));
  // Two tasks of "web" on w1 makes its box (and so its row) taller.
  const web = service('web', ['m1', 'w1', 'w1', 'w9']);
  const pending: DGNode = {
    ...service('stuck', []),
    service: { tasks: [{ id: 'p', slot: 1, state: 'pending', desiredState: 'running' }] },
  };
  const laid = layoutNodeGroups(toNodeGroupedFlowNodes([...managers, ...workers, web, pending], null));

  it('puts every manager on the top row and the Managers group first', () => {
    const mg = byId(laid, 'rolegroup:manager');
    expect(mg.position).toEqual({ x: 0, y: 0 });
    const ys = new Set(managers.map((m) => byId(laid, m.id).position.y));
    expect(ys).toEqual(new Set([ROLE_GROUP_PADDING_TOP]));
    expect(managers.map((m) => byId(laid, m.id).position.x)).toEqual([0, 1, 2].map(
      (i) => ROLE_GROUP_PADDING_X + i * (NODE_BOX_WIDTH + BOX_GAP_X),
    ));
    expect(size(mg).w).toBe(3 * NODE_BOX_WIDTH + 2 * BOX_GAP_X + 2 * ROLE_GROUP_PADDING_X);
  });

  it(`wraps workers every ${MAX_WORKERS_PER_ROW} boxes below the managers`, () => {
    const mg = byId(laid, 'rolegroup:manager');
    const wg = byId(laid, 'rolegroup:worker');
    expect(wg.position.y).toBe(size(mg).h + SECTION_GAP);

    const pos = workers.map((w) => byId(laid, w.id).position);
    const rows = [...new Set(pos.map((p) => p.y))];
    expect(rows).toHaveLength(2);
    expect(pos.filter((p) => p.y === rows[0])).toHaveLength(7);
    expect(pos.filter((p) => p.y === rows[1])).toHaveLength(2);
    // Columns line up across rows.
    expect(pos[7].x).toBe(pos[0].x);
    expect(size(wg).w).toBe(7 * NODE_BOX_WIDTH + 6 * BOX_GAP_X + 2 * ROLE_GROUP_PADDING_X);
  });

  it('gives every box the same width and every box in a row the row height', () => {
    const boxes = laid.filter((n) => n.type === 'nodeGroup');
    expect(new Set(boxes.map((b) => size(b).w))).toEqual(new Set([NODE_BOX_WIDTH]));
    const firstRow = workers.slice(0, 7).map((w) => size(byId(laid, w.id)).h);
    // w1 holds a two-task card, which sets the row height for all seven.
    const expected = NODE_BOX_HEADER_HEIGHT + serviceCardHeight(2) + NODE_BOX_PADDING;
    expect(new Set(firstRow)).toEqual(new Set([Math.max(NODE_BOX_MIN_HEIGHT, expected)]));
  });

  it('stacks cards at the full inner width of their box', () => {
    const card = byId(laid, 'nodesvc:w1:web');
    expect(card.position).toEqual({ x: NODE_BOX_PADDING, y: NODE_BOX_HEADER_HEIGHT });
    expect((card.data as { nodeWidth?: number }).nodeWidth).toBe(CARD_WIDTH);
    expect(CARD_WIDTH).toBe(NODE_BOX_WIDTH - 2 * NODE_BOX_PADDING);
  });

  it('stacks several cards with a constant gap', () => {
    const nodes = layoutNodeGroups(toNodeGroupedFlowNodes([
      swarmNode('m1', 'manager'), service('a', ['m1', 'm1']), service('b', ['m1']),
    ], null));
    const a = byId(nodes, 'nodesvc:m1:a');
    const b = byId(nodes, 'nodesvc:m1:b');
    expect(b.position.y).toBe(a.position.y + serviceCardHeight(2) + CARD_GAP);
    expect(size(byId(nodes, 'swarmnode:m1')).h).toBe(
      NODE_BOX_HEADER_HEIGHT + serviceCardHeight(2) + CARD_GAP + serviceCardHeight(1) + NODE_BOX_PADDING,
    );
  });

  it('places the Unassigned box free-standing below the workers', () => {
    const wg = byId(laid, 'rolegroup:worker');
    const un = byId(laid, UNASSIGNED_NODE_GROUP_ID);
    expect(un.parentId).toBeUndefined();
    expect(un.position).toEqual({ x: 0, y: wg.position.y + size(wg).h + SECTION_GAP });
  });

  it('is deterministic and keeps the input order', () => {
    const input = toNodeGroupedFlowNodes([...managers, ...workers, web], null);
    const a = layoutNodeGroups(input);
    const b = layoutNodeGroups(input);
    expect(a).toEqual(b);
    expect(a.map((n) => n.id)).toEqual(input.map((n) => n.id));
    // Inputs are not mutated.
    expect(input.every((n) => n.position.x === 0 && n.position.y === 0)).toBe(true);
  });

  it('gives empty boxes the minimum height', () => {
    const nodes = layoutNodeGroups(toNodeGroupedFlowNodes([swarmNode('m1', 'manager')], null));
    expect(size(byId(nodes, 'swarmnode:m1')).h).toBe(NODE_BOX_MIN_HEIGHT);
  });
});
