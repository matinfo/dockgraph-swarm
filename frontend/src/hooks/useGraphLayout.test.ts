// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest';
import { cleanup, renderHook, waitFor } from '@testing-library/react';
import type { DGEdge, DGNode } from '../types';

// ELK needs a real layout engine; place nodes as given.
vi.mock('../layout/elk', () => ({
  computeLayout: vi.fn(async (nodes: unknown[], edges: unknown[]) => ({ nodes, edges })),
}));

import { useGraphLayout } from './useGraphLayout';

afterEach(cleanup);

const web: DGNode = { id: 'container:web', type: 'container', name: 'web', status: 'running' };
const db: DGNode = { id: 'container:db', type: 'container', name: 'db', status: 'running' };
const dep: DGEdge = { id: 'e:dep:web:db', type: 'depends_on', source: 'container:web', target: 'container:db' };

type Props = { nodes: DGNode[]; edges: DGEdge[]; groupBy?: 'network' | 'node' };
const render = (initialProps: Props) =>
  renderHook((p: Props) => useGraphLayout(p.nodes, p.edges, '#000', '#fff', p.groupBy ?? 'network'), { initialProps });

describe('useGraphLayout', () => {
  it('lays out a topology', async () => {
    const { result } = render({ nodes: [web, db], edges: [dep] });
    await waitFor(() => expect(result.current.nodes.length).toBeGreaterThan(0));
    expect(result.current.edges.length).toBeGreaterThan(0);
  });

  it('clears the previous graph when the topology becomes empty', async () => {
    const { result, rerender } = render({ nodes: [web, db], edges: [dep] });
    await waitFor(() => expect(result.current.nodes.length).toBeGreaterThan(0));

    rerender({ nodes: [], edges: [] }); // e.g. a stack with no resources
    await waitFor(() => expect(result.current.nodes).toEqual([]));
    expect(result.current.edges).toEqual([]);
    expect(result.current.layoutBusy).toBe(false);
    expect(result.current.layoutError).toBe(false);
  });

  it('lays out again when nodes come back', async () => {
    const { result, rerender } = render({ nodes: [web, db], edges: [dep] });
    await waitFor(() => expect(result.current.nodes.length).toBeGreaterThan(0));
    rerender({ nodes: [], edges: [] });
    await waitFor(() => expect(result.current.nodes).toEqual([]));
    rerender({ nodes: [web], edges: [] });
    await waitFor(() => expect(result.current.nodes.some((n) => n.id === 'container:web')).toBe(true));
  });
});
