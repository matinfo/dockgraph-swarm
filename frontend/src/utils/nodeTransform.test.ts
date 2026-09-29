import { describe, it, expect } from 'vitest';
import {
  toNodeGroupedFlowNodes,
  nodeGroupedTopologyKey,
  placeTasks,
  nodeUsage,
  UNASSIGNED_NODE_GROUP_ID,
} from './nodeTransform';
import { filterGraphByStack } from './stack';
import type { DGNode, TaskNodeData, SwarmNodeGroupData } from '../types';

const mgr: DGNode = {
  id: 'swarmnode:mgr', type: 'swarmnode', name: 'mgr', status: 'ready',
  swarmNode: { id: 'n1', role: 'manager', leader: true, availability: 'active', state: 'ready', nanoCpus: 4e9, memoryBytes: 8e9 },
};
const wrk: DGNode = {
  id: 'swarmnode:wrk', type: 'swarmnode', name: 'wrk', status: 'ready',
  swarmNode: { id: 'n2', role: 'worker', leader: false, availability: 'drain', state: 'ready' },
};
const web: DGNode = {
  id: 'service:shop_web', type: 'service', name: 'shop_web', status: 'running', stack: 'shop',
  service: {
    mode: 'replicated',
    replicas: { running: 2, desired: 3 },
    tasks: [
      { id: 't2', slot: 2, nodeId: 'n2', nodeHostname: 'wrk', state: 'running', desiredState: 'running' },
      { id: 't1', slot: 1, nodeId: 'n1', nodeHostname: 'mgr', state: 'running', desiredState: 'running' },
      { id: 't3', slot: 3, state: 'pending', desiredState: 'running' },
      { id: 'old', slot: 1, nodeId: 'n1', nodeHostname: 'mgr', state: 'shutdown', desiredState: 'shutdown' },
    ],
  },
};
const api: DGNode = {
  id: 'service:blog_api', type: 'service', name: 'blog_api', status: 'running', stack: 'blog',
  service: {
    mode: 'global',
    replicas: { running: 1, desired: 1 },
    // Matched by node id only (no hostname).
    tasks: [{ id: 'g1', nodeId: 'n1', state: 'running', desiredState: 'running' }],
  },
};
const lone: DGNode = { id: 'container:lone', type: 'container', name: 'lone', status: 'running' };
const ghost: DGNode = { id: 'container:ghost', type: 'container', name: 'ghost', status: 'not_running' };
const net: DGNode = { id: 'network:shop_net', type: 'network', name: 'shop_net', stack: 'shop' };

const all = [web, api, lone, ghost, net, wrk, mgr];

describe('toNodeGroupedFlowNodes', () => {
  it('creates one box per swarm node, leader first, and an Unassigned box for pending tasks', () => {
    const rf = toNodeGroupedFlowNodes(all, 'n1');
    const groups = rf.filter((n) => n.type === 'nodeGroup');
    expect(groups.map((g) => g.id)).toEqual(['swarmnode:mgr', 'swarmnode:wrk', UNASSIGNED_NODE_GROUP_ID]);
    expect((groups[2].data as unknown as SwarmNodeGroupData).unassigned).toBe(true);
  });

  it('places running-desired tasks by hostname or node id, sorted by stack then service then slot', () => {
    const rf = toNodeGroupedFlowNodes(all, 'n1');
    const inMgr = rf.filter((n) => n.parentId === 'swarmnode:mgr');
    // blog < shop; standalone containers after the tasks.
    expect(inMgr.map((n) => n.id)).toEqual(['task:g1', 'task:t1', 'container:lone']);
    expect(rf.filter((n) => n.parentId === 'swarmnode:wrk').map((n) => n.id)).toEqual(['task:t2']);
    expect(rf.filter((n) => n.parentId === UNASSIGNED_NODE_GROUP_ID).map((n) => n.id)).toEqual(['task:t3']);
    expect(rf.some((n) => n.id === 'task:old')).toBe(false);

    const t1 = rf.find((n) => n.id === 'task:t1')!;
    expect(t1.type).toBe('taskNode');
    const d = t1.data as unknown as TaskNodeData;
    expect(d.dgNode.name).toBe('shop_web.1');
    expect(d.serviceId).toBe('service:shop_web');
    expect(d.stack).toBe('shop');
    const g1 = rf.find((n) => n.id === 'task:g1')!.data as unknown as TaskNodeData;
    expect(g1.dgNode.name).toBe('blog_api');
    expect((rf.find((n) => n.id === 'swarmnode:mgr')!.data as unknown as SwarmNodeGroupData).taskCount).toBe(3);
  });

  it('hides networks, volumes and not-running containers', () => {
    const rf = toNodeGroupedFlowNodes(all, 'n1');
    expect(rf.some((n) => n.id === 'network:shop_net')).toBe(false);
    expect(rf.some((n) => n.id === 'container:ghost')).toBe(false);
  });

  it('leaves standalone containers free-standing while the local node is unknown', () => {
    const rf = toNodeGroupedFlowNodes(all, null);
    const lc = rf.find((n) => n.id === 'container:lone')!;
    expect(lc.parentId).toBeUndefined();
    expect((rf.find((n) => n.id === 'swarmnode:mgr')!.data as unknown as SwarmNodeGroupData).taskCount).toBe(2);
  });

  it('keeps empty node boxes when scoped to a stack', () => {
    const { nodes } = filterGraphByStack(all, [], 'blog');
    const rf = toNodeGroupedFlowNodes(nodes, 'n1');
    expect(rf.filter((n) => n.type === 'nodeGroup').map((n) => n.id)).toEqual(['swarmnode:mgr', 'swarmnode:wrk']);
    expect(rf.filter((n) => n.type === 'taskNode').map((n) => n.id)).toEqual(['task:g1']);
    expect(rf.some((n) => n.id === 'container:lone')).toBe(false);
  });
});

describe('nodeGroupedTopologyKey', () => {
  it('changes when a task moves to another node', () => {
    const before = nodeGroupedTopologyKey(all, 'n1');
    const moved: DGNode = {
      ...web,
      service: { ...web.service!, tasks: web.service!.tasks!.map((t) => (t.id === 't2' ? { ...t, nodeId: 'n1', nodeHostname: 'mgr' } : t)) },
    };
    expect(nodeGroupedTopologyKey([moved, api, lone, wrk, mgr], 'n1')).not.toBe(before);
  });
});

describe('placeTasks', () => {
  it('sends tasks on an unknown node to Unassigned', () => {
    const svc: DGNode = { ...web, service: { ...web.service!, tasks: [{ id: 'x', slot: 1, nodeId: 'gone', desiredState: 'running' }] } };
    expect(placeTasks([svc, mgr]).get(UNASSIGNED_NODE_GROUP_ID)?.map((p) => p.task.id)).toEqual(['x']);
  });
});

describe('nodeUsage', () => {
  it('relates stats to capacity', () => {
    expect(nodeUsage(mgr.swarmNode, { cpuPercent: 200, memUsage: 2e9 })).toEqual({ cores: 4, cpuPercent: 50, memPercent: 25 });
    expect(nodeUsage(wrk.swarmNode, undefined)).toEqual({ cores: undefined });
  });
});
