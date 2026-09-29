import { memo } from 'react';
import type { NodeProps } from '@xyflow/react';
import { NodeHandles } from './NodeHandles';
import { StatsMini } from './StatsMini';
import { STATUS_COLORS, stackColor } from '../utils/colors';
import { useTheme } from '../theme';
import { TASK_NODE_HEIGHT, INACTIVE_OPACITY } from '../utils/constants';
import type { TaskNodeData } from '../types';

const ellipsis: React.CSSProperties = {
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
};

/** Colour of a task state dot. */
const TASK_STATE_COLORS: Record<string, string> = {
  running: STATUS_COLORS.running,
  complete: STATUS_COLORS.created,
  failed: STATUS_COLORS.exited,
  rejected: STATUS_COLORS.exited,
  shutdown: STATUS_COLORS.not_running,
};

/**
 * Compact card for one swarm task in the per-node view: `{service}.{slot}`,
 * a stripe in its stack colour, a state dot and the task's live stats when
 * the node agent reports them. Clicking it opens the owning service.
 */
export const TaskNode = memo(function TaskNode({ data }: NodeProps) {
  const { dgNode, task, serviceId, stack, nodeWidth, stats, onInfoClick } = data as unknown as TaskNodeData;
  const { theme } = useTheme();
  const w = nodeWidth ?? 200;
  const state = task.state ?? 'unknown';
  const stateColor = TASK_STATE_COLORS[state] ?? STATUS_COLORS.unhealthy;
  const running = state === 'running';
  const open = onInfoClick ? () => onInfoClick(serviceId) : undefined;

  return (
    <div
      data-testid="task-node"
      role={open ? 'button' : undefined}
      tabIndex={open ? 0 : undefined}
      onClick={open}
      onKeyDown={open ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); } } : undefined}
      title={`${dgNode.name} — ${state}${task.error ? `: ${task.error}` : ''}${stack ? ` (stack ${stack})` : ''}`}
      style={{
        background: theme.nodeBg,
        border: `1px solid ${theme.nodeBorder}`,
        borderLeft: `3px solid ${stackColor(stack)}`,
        borderRadius: 6,
        padding: '5px 9px',
        width: w,
        height: TASK_NODE_HEIGHT,
        boxSizing: 'border-box',
        overflow: 'hidden',
        opacity: running ? 1 : INACTIVE_OPACITY,
        cursor: open ? 'pointer' : 'default',
      }}
    >
      <NodeHandles />
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
        <span
          data-testid="task-state-dot"
          title={state}
          style={{ width: 6, height: 6, borderRadius: '50%', background: stateColor, flexShrink: 0 }}
        />
        <span
          style={{
            ...ellipsis,
            minWidth: 0,
            fontSize: 11.5,
            fontWeight: 600,
            fontFamily: 'var(--dg-font-mono)',
            color: theme.nodeText,
          }}
        >
          {dgNode.name}
        </span>
      </div>
      {stats ? (
        <StatsMini stats={stats} />
      ) : (
        <div style={{ ...ellipsis, marginTop: 3, fontSize: 9.5, color: task.error ? theme.danger : theme.nodeSubtext }}>
          {task.error ?? state}
        </div>
      )}
    </div>
  );
});
