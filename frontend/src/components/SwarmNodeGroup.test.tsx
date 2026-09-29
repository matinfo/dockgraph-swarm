// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup } from '@testing-library/react';

afterEach(() => cleanup());

vi.mock('@xyflow/react', () => ({
  Handle: () => null,
  Position: { Top: 'top', Bottom: 'bottom' },
  useStore: vi.fn(() => false),
}));

import { ThemeProvider } from '../theme';
import { SwarmNodeGroup } from './SwarmNodeGroup';
import { TaskNode } from './TaskNode';
import type { DGNode, SwarmNodeGroupData, TaskNodeData } from '../types';
import type { ContainerStatsData } from '../types/stats';
import type { NodeProps } from '@xyflow/react';

const stats: ContainerStatsData = {
  cpuPercent: 200, cpuThrottled: 0, memUsage: 2 * 1024 ** 3, memLimit: 0,
  netRx: 0, netTx: 0, netRxErrors: 0, netTxErrors: 0, blockRead: 0, blockWrite: 0, pids: 0,
};

const mgr: DGNode = {
  id: 'swarmnode:mgr', type: 'swarmnode', name: 'mgr', status: 'ready',
  swarmNode: { id: 'n1', role: 'manager', leader: true, availability: 'active', state: 'ready', nanoCpus: 4e9, memoryBytes: 8 * 1024 ** 3 },
};

function renderGroup(data: Partial<SwarmNodeGroupData>) {
  const props = { data: { dgNode: mgr, taskCount: 3, ...data } } as unknown as NodeProps;
  return render(<ThemeProvider><SwarmNodeGroup {...props} /></ThemeProvider>);
}

describe('SwarmNodeGroup', () => {
  it('shows hostname, leader badge, task count and resource bars', () => {
    renderGroup({ stats });
    expect(screen.getByText('mgr')).toBeDefined();
    expect(screen.getByTestId('role-badge').textContent).toBe('manager ★');
    expect(screen.getByTestId('task-count').textContent).toBe('3 tasks');
    expect(screen.getByText('CPU')).toBeDefined();
    expect(screen.getByText('50%')).toBeDefined();
    expect(screen.queryByTestId('availability-chip')).toBeNull();
    expect(screen.queryByTestId('no-agent')).toBeNull();
  });

  it('shows a drain chip and "no agent" without stats', () => {
    renderGroup({
      dgNode: { ...mgr, swarmNode: { ...mgr.swarmNode!, role: 'worker', leader: false, availability: 'drain' } },
    });
    expect(screen.getByTestId('role-badge').textContent).toBe('worker');
    expect(screen.getByTestId('availability-chip').textContent).toBe('drain');
    expect(screen.getByTestId('no-agent').textContent).toContain('no agent');
  });

  it('opens the node panel from the header', () => {
    const onInfoClick = vi.fn();
    renderGroup({ onInfoClick });
    fireEvent.click(screen.getByText('mgr'));
    expect(onInfoClick).toHaveBeenCalledWith('swarmnode:mgr');
  });

  it('renders the Unassigned box without an inspect action', () => {
    const onInfoClick = vi.fn();
    renderGroup({
      dgNode: { id: 'nodegroup:unassigned', type: 'swarmnode', name: 'Unassigned' },
      unassigned: true,
      taskCount: 1,
      onInfoClick,
    });
    fireEvent.click(screen.getByText('Unassigned'));
    expect(onInfoClick).not.toHaveBeenCalled();
    expect(screen.getByText('Tasks waiting for a node')).toBeDefined();
    expect(screen.queryByTestId('role-badge')).toBeNull();
  });
});

describe('TaskNode', () => {
  const base: TaskNodeData = {
    dgNode: { id: 'task:t1', type: 'container', name: 'shop_web.1', status: 'running', stack: 'shop' },
    task: { id: 't1', slot: 1, state: 'running', desiredState: 'running' },
    serviceId: 'service:shop_web',
    serviceName: 'shop_web',
    stack: 'shop',
  };
  function renderTask(data: Partial<TaskNodeData>) {
    const props = { data: { ...base, nodeWidth: 180, ...data } } as unknown as NodeProps;
    return render(<ThemeProvider><TaskNode {...props} /></ThemeProvider>);
  }

  it('shows the task label and stats', () => {
    renderTask({ stats: { ...stats, cpuPercent: 12 } });
    expect(screen.getByText('shop_web.1')).toBeDefined();
    expect(screen.getByText(/12%/)).toBeDefined();
  });

  it('shows the state or error without stats', () => {
    renderTask({ task: { id: 't1', slot: 1, state: 'pending', desiredState: 'running', error: 'no suitable node' } });
    expect(screen.getByText('no suitable node')).toBeDefined();
  });

  it('opens the owning service when clicked', () => {
    const onInfoClick = vi.fn();
    renderTask({ onInfoClick });
    fireEvent.click(screen.getByTestId('task-node'));
    expect(onInfoClick).toHaveBeenCalledWith('service:shop_web');
  });
});
