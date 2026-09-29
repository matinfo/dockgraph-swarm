export interface PortMapping {
  host: number;
  container: number;
  protocol?: string;
}

export interface ComposeConfig {
  service: string;
  command?: string[];
  entrypoint?: string[];
  environment?: Record<string, string>;
  labels?: Record<string, string>;
  restart?: string;
  dependsOn?: string[];
  volumes?: import('./types/stats').Mount[];
  networks?: string[];
  user?: string;
  workingDir?: string;
  privileged?: boolean;
  readOnly?: boolean;
  capAdd?: string[];
  capDrop?: string[];
}

/** Number of running tasks versus the desired count for a swarm service. */
export interface ReplicaCount {
  running: number;
  desired: number;
}

/** A single swarm task of a service, placed on a cluster node. */
export interface TaskInfo {
  id: string;
  slot?: number;
  nodeId?: string;
  nodeHostname?: string;
  state?: string;
  desiredState?: string;
  containerId?: string;
  error?: string;
  timestamp?: string;
}

/** Swarm service state carried by "service" nodes. */
export interface ServiceInfo {
  /** "replicated" | "global" | "replicated-job" | "global-job". */
  mode?: string;
  replicas: ReplicaCount;
  tasks?: TaskInfo[];
  /** Rolling-update state (e.g. "updating", "completed"); empty when none ran. */
  updateStatus?: string;
}

export interface DGNode {
  id: string;
  type: 'container' | 'service' | 'network' | 'volume';
  name: string;
  image?: string;
  status?: string;
  ports?: PortMapping[];
  labels?: Record<string, string>;
  networkId?: string;
  driver?: string;
  subnet?: string;
  gateway?: string;
  source?: string;
  createdAt?: string;
  compose?: ComposeConfig;
  /** Compose project or swarm stack namespace the node belongs to. */
  stack?: string;
  service?: ServiceInfo;
}

export interface DGEdge {
  id: string;
  type: 'volume_mount' | 'depends_on' | 'secondary_network';
  source: string;
  target: string;
  mountPath?: string;
}

export interface GraphSnapshot {
  nodes: DGNode[];
  edges: DGEdge[];
}

export interface DeltaUpdate {
  nodesAdded?: DGNode[];
  nodesRemoved?: string[];
  nodesUpdated?: DGNode[];
  edgesAdded?: DGEdge[];
  edgesRemoved?: string[];
}

export type WireMessage =
  | { type: 'snapshot'; version: number; data: GraphSnapshot }
  | { type: 'delta'; version: number; data: DeltaUpdate }
  | { type: 'auth_expired'; version: number }
  | { type: 'stats'; version: number; data: import('./types/stats').StatsMessage };

// Node data types used by React Flow custom components

export interface ContainerNodeData {
  dgNode: DGNode;
  nodeWidth?: number;
  stats?: import('./types/stats').ContainerStatsData;
  onInfoClick?: (containerId: string) => void;
}

export interface ServiceNodeData {
  dgNode: DGNode;
  nodeWidth?: number;
  stats?: import('./types/stats').ContainerStatsData;
  onInfoClick?: (serviceId: string) => void;
}

export interface VolumeNodeData {
  dgNode: DGNode;
  nodeWidth?: number;
  onInfoClick?: (volumeId: string) => void;
}

export interface NetworkGroupData {
  dgNode: DGNode;
  onInfoClick?: (networkId: string) => void;
}

/** A container using a named volume, with its mount path. */
export interface VolumeMount {
  node: DGNode;
  mountPath: string;
}

/** Typed payload for ELK-routed edges. */
export interface ElkEdgeData {
  path?: string;
  edgeType?: string;
  active?: boolean;
  animated?: boolean;
  nodeCount?: number;
}

export type { ContainerStatsData, StatsMessage, ContainerDetail, Mount, LogLine, VolumeDetail, NetworkDetail } from './types/stats';
