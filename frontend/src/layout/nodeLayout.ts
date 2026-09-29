import type { Node as RFNode } from '@xyflow/react';
import {
  CONTAINER_NODE_HEIGHT,
  NODE_BOX_HEADER_HEIGHT,
  NODE_BOX_MIN_HEIGHT,
  SERVICE_CARD_HEADER_HEIGHT,
  SERVICE_CARD_PADDING_BOTTOM,
  SERVICE_CARD_ROW_HEIGHT,
} from '../utils/constants';
import type { NodeServiceCardData } from '../types';

/**
 * Deterministic layout of the per-swarm-node graph view (no ELK: the view has
 * no edges, and a fixed grid is both simpler and exactly controllable).
 *
 *   ┌ Managers ─────────────────────────┐   all managers on one row
 *   │ [box] [box] [box]                 │
 *   └───────────────────────────────────┘
 *   ┌ Workers ──────────────────────────┐   workers wrap every 5 boxes
 *   │ [box] [box] ... [box]  (5 max)    │
 *   │ [box] [box]                       │
 *   └───────────────────────────────────┘
 *   [Unassigned]                            free-standing, below workers
 *   [container] [container]                 free standalone containers
 *
 * Every swarm node box has the same width; boxes in a row share their top
 * edge and are stretched to the row's tallest box. Service cards (and local
 * standalone containers) stack vertically at the full inner width of a box.
 * Sections are left-aligned and stacked with SECTION_GAP between them.
 */

/** Width of every swarm node box. */
export const NODE_BOX_WIDTH = 300;
/** Inner padding of a swarm node box around its cards. */
export const NODE_BOX_PADDING = 12;
/** Vertical gap between cards inside a box. */
export const CARD_GAP = 8;
/** Gaps between boxes inside a role group. */
export const BOX_GAP_X = 20;
export const BOX_GAP_Y = 20;
/** Maximum worker boxes per row before wrapping. Managers never wrap. */
export const MAX_WORKERS_PER_ROW = 5;
/** Role group padding: room for the title tab on top, a grab margin around. */
export const ROLE_GROUP_PADDING_TOP = 40;
export const ROLE_GROUP_PADDING_X = 18;
export const ROLE_GROUP_PADDING_BOTTOM = 18;
/** Vertical gap between the Managers, Workers and Unassigned sections. */
export const SECTION_GAP = 40;
/** Horizontal gap between free-standing containers. */
const FREE_GAP = 20;
/** Width of free-standing standalone containers. */
const FREE_NODE_WIDTH = 220;

/** Width of the cards inside a box. */
export const CARD_WIDTH = NODE_BOX_WIDTH - 2 * NODE_BOX_PADDING;

/** Height of a service card holding `rows` task rows. */
export function serviceCardHeight(rows: number): number {
  return SERVICE_CARD_HEADER_HEIGHT + rows * SERVICE_CARD_ROW_HEIGHT + SERVICE_CARD_PADDING_BOTTOM;
}

function itemHeight(n: RFNode): number {
  if (n.type === 'nodeServiceCard') {
    return serviceCardHeight((n.data as unknown as NodeServiceCardData).tasks.length);
  }
  return CONTAINER_NODE_HEIGHT;
}

interface Size { width: number; height: number }

/**
 * Positions the nodes produced by toNodeGroupedFlowNodes. Returns new node
 * objects (inputs are not mutated) with `position` relative to their parent
 * and explicit `style` width/height on groups and boxes.
 */
export function layoutNodeGroups(nodes: RFNode[]): RFNode[] {
  const childrenOf = new Map<string, RFNode[]>();
  for (const n of nodes) {
    if (!n.parentId) continue;
    const list = childrenOf.get(n.parentId) ?? [];
    list.push(n);
    childrenOf.set(n.parentId, list);
  }

  const out = new Map<string, RFNode>();
  const place = (n: RFNode, x: number, y: number, size?: Size, extra?: Record<string, unknown>) => {
    out.set(n.id, {
      ...n,
      position: { x, y },
      ...(size ? { style: { ...n.style, width: size.width, height: size.height } } : null),
      ...(extra ? { data: { ...n.data, ...extra } } : null),
    });
  };

  // Natural height of each swarm node box, from its stacked cards.
  const boxHeight = (box: RFNode): number => {
    const items = childrenOf.get(box.id) ?? [];
    if (items.length === 0) return NODE_BOX_MIN_HEIGHT;
    const content = items.reduce((h, c) => h + itemHeight(c), 0) + CARD_GAP * (items.length - 1);
    return Math.max(NODE_BOX_MIN_HEIGHT, NODE_BOX_HEADER_HEIGHT + content + NODE_BOX_PADDING);
  };

  const placeBoxContent = (box: RFNode) => {
    let y = NODE_BOX_HEADER_HEIGHT;
    for (const item of childrenOf.get(box.id) ?? []) {
      place(item, NODE_BOX_PADDING, y, undefined, { nodeWidth: CARD_WIDTH });
      y += itemHeight(item) + CARD_GAP;
    }
  };

  /** Lays boxes out in rows of `perRow` at (x0, y0); returns the grid size. */
  const placeGrid = (boxes: RFNode[], perRow: number, x0: number, y0: number): Size => {
    let y = y0;
    let width = 0;
    for (let i = 0; i < boxes.length; i += perRow) {
      const row = boxes.slice(i, i + perRow);
      const rowH = Math.max(...row.map(boxHeight));
      row.forEach((box, j) => {
        place(box, x0 + j * (NODE_BOX_WIDTH + BOX_GAP_X), y, { width: NODE_BOX_WIDTH, height: rowH });
        placeBoxContent(box);
      });
      width = Math.max(width, row.length * NODE_BOX_WIDTH + (row.length - 1) * BOX_GAP_X);
      y += rowH + BOX_GAP_Y;
    }
    return { width, height: boxes.length > 0 ? y - BOX_GAP_Y - y0 : 0 };
  };

  let cursorY = 0;
  const roleGroups = nodes.filter((n) => n.type === 'roleGroup');
  for (const group of roleGroups) {
    const boxes = childrenOf.get(group.id) ?? [];
    const manager = (group.data as { role?: string }).role === 'manager';
    const perRow = manager ? Math.max(1, boxes.length) : MAX_WORKERS_PER_ROW;
    const grid = placeGrid(boxes, perRow, ROLE_GROUP_PADDING_X, ROLE_GROUP_PADDING_TOP);
    const size = {
      width: grid.width + 2 * ROLE_GROUP_PADDING_X,
      height: ROLE_GROUP_PADDING_TOP + grid.height + ROLE_GROUP_PADDING_BOTTOM,
    };
    place(group, 0, cursorY, size);
    cursorY += size.height + SECTION_GAP;
  }

  // Free-standing boxes (Unassigned) in their own row below the role groups.
  const freeBoxes = nodes.filter((n) => n.type === 'nodeGroup' && !n.parentId);
  if (freeBoxes.length > 0) {
    const grid = placeGrid(freeBoxes, MAX_WORKERS_PER_ROW, 0, cursorY);
    cursorY += grid.height + SECTION_GAP;
  }

  // Standalone containers while the local node is unknown.
  let x = 0;
  for (const n of nodes) {
    if (n.parentId || out.has(n.id) || n.type === 'roleGroup' || n.type === 'nodeGroup') continue;
    place(n, x, cursorY, undefined, { nodeWidth: FREE_NODE_WIDTH });
    x += FREE_NODE_WIDTH + FREE_GAP;
  }

  // Keep the input order (parents before children).
  return nodes.map((n) => out.get(n.id) ?? n);
}
