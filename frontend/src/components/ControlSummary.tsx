import { memo } from 'react';
import type { NodeProps } from '@xyflow/react';
import { STATUS_COLORS } from '../utils/colors';
import { SWARM_CONTROL_PORT } from '../utils/swarmLinks';
import { STATUS_DOT_SIZE } from '../utils/constants';
import { chip } from '../utils/chipStyle';
import { useTheme } from '../theme';
import type { ControlSummaryData } from '../types';

/**
 * Free-standing badge of the per-node view, centered in the gap between the
 * Managers and Workers groups (see layout/nodeLayout.ts). The control link
 * fans out to one spoke per worker (see SwarmLinkEdge), so this is the only
 * place carrying an aggregate reading — kept to a glanceable status dot plus
 * the TCP port, same chip look as the node boxes' own badges; the exact
 * ready/down count moves to the tooltip instead of cluttering the label.
 */
export const ControlSummary = memo(function ControlSummary({ data }: NodeProps) {
  const { ready, total, healthy } = data as unknown as ControlSummaryData;
  const { theme } = useTheme();
  const color = healthy ? theme.edgeSignal : STATUS_COLORS.exited;
  const title = healthy
    ? `Control plane: ${ready}/${total} worker${total === 1 ? '' : 's'} ready on TCP ${SWARM_CONTROL_PORT}`
    : `Control plane: ${total - ready}/${total} worker${total === 1 ? '' : 's'} down`;

  return (
    <div
      data-testid="control-summary"
      title={title}
      // Centers the badge on the (x, y) anchor nodeLayout.ts computed, the
      // same translate(-50%, -50%) trick the old edge label used.
      style={{
        transform: 'translate(-50%, -50%)',
        display: 'inline-flex',
        alignItems: 'center',
        gap: 5,
        pointerEvents: 'none',
      }}
    >
      <span
        aria-hidden="true"
        style={{ width: STATUS_DOT_SIZE, height: STATUS_DOT_SIZE, borderRadius: '50%', background: color, flex: '0 0 auto' }}
      />
      <span style={{ ...chip, color, border: `1px solid ${color}55`, background: `${color}1f` }}>
        TCP {SWARM_CONTROL_PORT}
      </span>
    </div>
  );
});
