import type { DGNode, DGEdge, TaskInfo } from '../types';

/** Label Docker Compose sets on every resource of a project. */
export const COMPOSE_PROJECT_LABEL = 'com.docker.compose.project';
/** Label `docker stack deploy` sets on every resource of a swarm stack. */
export const STACK_NAMESPACE_LABEL = 'com.docker.stack.namespace';

/**
 * Scope key for workloads that belong to no compose project or stack. The
 * leading underscore can't start a real project/stack name, so it never
 * collides with one.
 */
export const STANDALONE_STACK = '_standalone';

/**
 * Returns the compose project or swarm stack a node belongs to, or undefined
 * when it has none. The backend-resolved `stack` wins; the labels are a
 * fallback for payloads that predate it.
 */
export function projectOf(node: DGNode): string | undefined {
  return (
    node.stack ||
    node.labels?.[COMPOSE_PROJECT_LABEL] ||
    node.labels?.[STACK_NAMESPACE_LABEL] ||
    undefined
  );
}

/** True for nodes that run code (containers and swarm services). */
export function isWorkload(node: DGNode): boolean {
  return node.type === 'container' || node.type === 'service';
}

export interface StackSummary {
  /** Stack/project name, or STANDALONE_STACK for workloads with no project. */
  name: string;
  running: number;
  total: number;
}

/**
 * Summarises every stack/project present in the graph with its running and
 * total workload counts, sorted by name. Workloads with no project are
 * grouped under STANDALONE_STACK, listed last.
 */
export function listStacks(nodes: DGNode[]): StackSummary[] {
  const groups = new Map<string, StackSummary>();
  for (const n of nodes) {
    if (!isWorkload(n)) continue;
    const name = projectOf(n) ?? STANDALONE_STACK;
    const group = groups.get(name) ?? { name, running: 0, total: 0 };
    group.total++;
    if (n.status === 'running') group.running++;
    groups.set(name, group);
  }
  return Array.from(groups.values()).sort((a, b) => {
    if (a.name === STANDALONE_STACK) return 1;
    if (b.name === STANDALONE_STACK) return -1;
    return a.name.localeCompare(b.name);
  });
}

/** True when a node belongs to the given scope (a stack name or STANDALONE_STACK). */
export function inStack(node: DGNode, stack: string): boolean {
  const project = projectOf(node);
  return stack === STANDALONE_STACK ? project === undefined : project === stack;
}

/**
 * Narrows the graph to one stack. Keeps the stack's own nodes, plus any
 * network or volume a kept workload references (e.g. an external network),
 * plus the edges whose ends both survive. A null stack returns the input.
 *
 * For the standalone scope only workloads without a project are kept as
 * "own" nodes; networks and volumes come in only when referenced, so
 * project-owned resources don't leak into it.
 */
export function filterGraphByStack(
  nodes: DGNode[],
  edges: DGEdge[],
  stack: string | null,
): { nodes: DGNode[]; edges: DGEdge[] } {
  if (!stack) return { nodes, edges };

  const kept = new Set<string>();
  for (const n of nodes) {
    if (isWorkload(n) ? inStack(n, stack) : stack !== STANDALONE_STACK && inStack(n, stack)) {
      kept.add(n.id);
    }
  }

  // Pull in the networks and volumes referenced by kept workloads.
  const workloadIds = new Set(nodes.filter((n) => isWorkload(n) && kept.has(n.id)).map((n) => n.id));
  for (const n of nodes) {
    if (workloadIds.has(n.id) && n.networkId) kept.add(n.networkId);
  }
  for (const e of edges) {
    if (e.type === 'secondary_network' && workloadIds.has(e.source)) kept.add(e.target);
    if (e.type === 'volume_mount' && workloadIds.has(e.target)) kept.add(e.source);
  }

  const scopedNodes = nodes.filter((n) => kept.has(n.id));
  const present = new Set(scopedNodes.map((n) => n.id));
  const scopedEdges = edges.filter((e) => present.has(e.source) && present.has(e.target));
  return { nodes: scopedNodes, edges: scopedEdges };
}

/**
 * Strips the `{stack}_` prefix from a resource name so that the same logical
 * network in different stacks (e.g. `shop_backend`, `blog_backend`) hashes to
 * the same colour. Names without a known prefix are returned unchanged.
 */
export function stripStackPrefix(name: string, stack: string | undefined): string {
  if (stack && name.startsWith(`${stack}_`) && name.length > stack.length + 1) {
    return name.slice(stack.length + 1);
  }
  return name;
}

/** Matches the `.{slot|nodeId}.{taskId}` suffix of a swarm task container name. */
const TASK_SUFFIX = /\.[a-z0-9]+\.[a-z0-9]{20,}$/i;

/**
 * Resolves a cross-reference (`type:name`) to a graph node ID. Docker and
 * compose often refer to workloads by short name (`web`) while graph nodes
 * carry the prefixed form (`shop_web` for a swarm service, `shop-web-1` for a
 * compose container, `shop_web.1.<taskid>` for a task container). Candidates
 * in `contextStack` win over same-named workloads in other stacks, and a
 * container reference may resolve to a service (and vice versa). Returns
 * `targetId` unchanged when nothing matches.
 */
export function resolveNodeRef(nodes: DGNode[], targetId: string, contextStack?: string): string {
  if (nodes.some((n) => n.id === targetId)) return targetId;
  const sepIdx = targetId.indexOf(':');
  if (sepIdx < 0) return targetId;
  const type = targetId.slice(0, sepIdx);
  const name = targetId.slice(sepIdx + 1);
  const baseName = name.replace(TASK_SUFFIX, '');

  const types = type === 'container' ? ['container', 'service']
    : type === 'service' ? ['service', 'container']
    : [type];

  let best: { id: string; score: number } | null = null;
  for (const n of nodes) {
    const typeRank = types.indexOf(n.type);
    if (typeRank < 0) continue;

    let score = 0;
    if (n.name === name) score = 400;
    else if (n.name === baseName) score = 350;
    else if (contextStack && (n.name === `${contextStack}_${name}` || n.name.startsWith(`${contextStack}-${name}-`))) score = 300;
    else if (n.name.endsWith(`_${name}`) || n.name.endsWith(`-${name}`)) {
      score = contextStack && projectOf(n) === contextStack ? 200 : 100;
    }
    if (score === 0) continue;
    score -= typeRank;
    if (!best || score > best.score) best = { id: n.id, score };
  }
  return best ? best.id : targetId;
}

/**
 * The container name Docker gives a task: `{service}.{slot}.{taskId}` for
 * replicated services and `{service}.{nodeId}.{taskId}` for global ones.
 * Per-task stats are keyed by it.
 */
export function taskContainerName(service: string, task: TaskInfo): string {
  return `${service}.${task.slot ? task.slot : task.nodeId ?? ''}.${task.id}`;
}
