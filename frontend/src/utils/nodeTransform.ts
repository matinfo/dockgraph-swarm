import type { Node as RFNode } from '@xyflow/react';
import { projectOf } from './stack';
import type { DGNode, TaskInfo, SwarmNodeGroupData, TaskNodeData } from '../types';

/** Group id for tasks the scheduler hasn't placed on a node yet. */
export const UNASSIGNED_NODE_GROUP_ID = 'nodegroup:unassigned';

/** A running-desired swarm task together with the service it belongs to. */
export interface PlacedTask {
  service: DGNode;
  task: TaskInfo;
}

/** Leader first, then the other managers, then workers; by hostname within each. */
function compareSwarmNodes(a: DGNode, b: DGNode): number {
  const rank = (n: DGNode) => (n.swarmNode?.leader ? 0 : n.swarmNode?.role === 'manager' ? 1 : 2);
  return rank(a) - rank(b) || a.name.localeCompare(b.name);
}

/** Stack (no-stack last), then service name, then slot, then task id. */
function comparePlacedTasks(a: PlacedTask, b: PlacedTask): number {
  const sa = projectOf(a.service);
  const sb = projectOf(b.service);
  if (sa !== sb) {
    if (sa === undefined) return 1;
    if (sb === undefined) return -1;
    return sa.localeCompare(sb);
  }
  return (
    a.service.name.localeCompare(b.service.name) ||
    (a.task.slot ?? 0) - (b.task.slot ?? 0) ||
    (a.task.nodeId ?? '').localeCompare(b.task.nodeId ?? '') ||
    a.task.id.localeCompare(b.task.id)
  );
}

/** The swarm nodes in the graph, in display order (leader, managers, workers). */
export function listSwarmNodes(dgNodes: DGNode[]): DGNode[] {
  return dgNodes.filter((n) => n.type === 'swarmnode').sort(compareSwarmNodes);
}

/**
 * Places every task that should be running (desiredState "running") of the
 * services in `dgNodes` onto its swarm node. Keys are swarmnode graph ids, or
 * UNASSIGNED_NODE_GROUP_ID for tasks without a (known) node — typically
 * pending tasks the scheduler couldn't place. Each list is sorted by stack,
 * service and slot, so tasks read as grouped by service.
 */
export function placeTasks(dgNodes: DGNode[]): Map<string, PlacedTask[]> {
  const byHostname = new Map<string, string>();
  const byNodeId = new Map<string, string>();
  for (const n of dgNodes) {
    if (n.type !== 'swarmnode') continue;
    byHostname.set(n.name, n.id);
    if (n.swarmNode?.id) byNodeId.set(n.swarmNode.id, n.id);
  }

  const placed = new Map<string, PlacedTask[]>();
  for (const service of dgNodes) {
    if (service.type !== 'service') continue;
    for (const task of service.service?.tasks ?? []) {
      if (task.desiredState !== 'running') continue;
      const group =
        (task.nodeHostname && byHostname.get(task.nodeHostname)) ||
        (task.nodeId && byNodeId.get(task.nodeId)) ||
        UNASSIGNED_NODE_GROUP_ID;
      const list = placed.get(group) ?? [];
      list.push({ service, task });
      placed.set(group, list);
    }
  }
  for (const list of placed.values()) list.sort(comparePlacedTasks);
  return placed;
}

/** Label of a task card: `{service}.{slot}`, or the bare service name for global tasks. */
export function taskLabel(serviceName: string, task: TaskInfo): string {
  return task.slot ? `${serviceName}.${task.slot}` : serviceName;
}

/** The swarmnode graph id of the local daemon's node, if it is in the graph. */
function localGroupId(dgNodes: DGNode[], localNodeId: string | null | undefined): string | undefined {
  if (!localNodeId) return undefined;
  return dgNodes.find((n) => n.type === 'swarmnode' && n.swarmNode?.id === localNodeId)?.id;
}

/**
 * Converts the (stack-scoped) graph into the per-swarm-node view: one
 * `nodeGroup` box per swarm node (plus an Unassigned box when some tasks have
 * no node), each holding a `taskNode` card per task that should be running.
 * Standalone containers go into the box of the local node (`localNodeId`,
 * from /api/system/info), or stay free-standing while it is unknown.
 * Networks and volumes are hidden, and no edges are drawn: network wiring
 * means nothing in a placement view.
 */
export function toNodeGroupedFlowNodes(dgNodes: DGNode[], localNodeId?: string | null): RFNode[] {
  const swarmNodes = listSwarmNodes(dgNodes);
  const placed = placeTasks(dgNodes);
  const localGroup = localGroupId(dgNodes, localNodeId);
  const containers = dgNodes.filter((n) => n.type === 'container' && n.status !== 'not_running');

  const groups: RFNode[] = [];
  const children: RFNode[] = [];

  const addGroup = (id: string, dgNode: DGNode, unassigned: boolean) => {
    const tasks = placed.get(id) ?? [];
    const local = id === localGroup ? containers : [];
    const data: SwarmNodeGroupData = { dgNode, taskCount: tasks.length + local.length };
    if (unassigned) data.unassigned = true;
    groups.push({
      id,
      type: 'nodeGroup',
      position: { x: 0, y: 0 },
      data: data as unknown as Record<string, unknown>,
      style: { width: 280, height: 110 },
    });
    for (const { service, task } of tasks) {
      const taskId = `task:${task.id}`;
      const stack = projectOf(service);
      const taskData: TaskNodeData = {
        dgNode: { id: taskId, type: 'container', name: taskLabel(service.name, task), status: task.state, stack },
        task,
        serviceId: service.id,
        serviceName: service.name,
        stack,
      };
      children.push({
        id: taskId,
        type: 'taskNode',
        position: { x: 0, y: 0 },
        parentId: id,
        extent: 'parent',
        data: taskData as unknown as Record<string, unknown>,
      });
    }
    for (const c of local) {
      children.push({
        id: c.id,
        type: 'containerNode',
        position: { x: 0, y: 0 },
        parentId: id,
        extent: 'parent',
        data: { dgNode: c },
      });
    }
  };

  for (const n of swarmNodes) addGroup(n.id, n, false);
  if (placed.has(UNASSIGNED_NODE_GROUP_ID)) {
    addGroup(
      UNASSIGNED_NODE_GROUP_ID,
      { id: UNASSIGNED_NODE_GROUP_ID, type: 'swarmnode', name: 'Unassigned' },
      true,
    );
  }

  // Without a known local node, standalone containers stay free-standing.
  const free: RFNode[] = localGroup
    ? []
    : containers.map((c) => ({ id: c.id, type: 'containerNode', position: { x: 0, y: 0 }, data: { dgNode: c } }));

  return [...groups, ...children, ...free];
}

/**
 * Topology fingerprint of the per-node view: which cards exist and which box
 * holds each. Task moves and new tasks change it (the graph node ids don't,
 * since tasks live inside their service node), so they trigger a relayout.
 */
export function nodeGroupedTopologyKey(dgNodes: DGNode[], localNodeId?: string | null): string {
  return toNodeGroupedFlowNodes(dgNodes, localNodeId)
    .map((n) => `${n.id}>${n.parentId ?? ''}`)
    .sort()
    .join(',');
}

/** Node usage against its capacity, as percentages (undefined when unknown). */
export interface NodeUsage {
  /** CPU cores the node offers (NanoCPUs / 1e9). */
  cores?: number;
  cpuPercent?: number;
  memPercent?: number;
}

/**
 * Relates a node's aggregate stats to its capacity. Docker CPU percentages
 * count 100% per core, so the share of the node is cpuPercent / cores.
 */
export function nodeUsage(
  info: { nanoCpus?: number; memoryBytes?: number } | undefined,
  stats: { cpuPercent: number; memUsage: number } | undefined,
): NodeUsage {
  const cores = info?.nanoCpus ? info.nanoCpus / 1e9 : undefined;
  if (!stats) return { cores };
  const clamp = (v: number) => Math.max(0, Math.min(100, v));
  return {
    cores,
    cpuPercent: cores ? clamp(stats.cpuPercent / cores) : undefined,
    memPercent: info?.memoryBytes ? clamp((stats.memUsage / info.memoryBytes) * 100) : undefined,
  };
}

/** Formats a core count: "4 cores", "0.5 cores", "1 core". */
export function formatCores(cores: number): string {
  const v = Number.isInteger(cores) ? String(cores) : cores.toFixed(1);
  return `${v} ${cores === 1 ? 'core' : 'cores'}`;
}
