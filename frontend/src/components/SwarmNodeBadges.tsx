import { memo } from 'react';
import { useTheme } from '../theme';
import { swarmRoleColor, swarmNodeStateColor } from '../utils/colors';
import type { SwarmNodeInfo } from '../types';

const chip: React.CSSProperties = {
  flexShrink: 0,
  fontFamily: 'var(--dg-font-mono)',
  fontSize: 9,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
  borderRadius: 3,
  padding: '0 4px',
  lineHeight: '13px',
  whiteSpace: 'nowrap',
};

/** Short role labels for tight headers, so the hostname keeps its room. */
const COMPACT_ROLE = { manager: 'mgr', worker: 'wkr' } as const;

/** "manager ★" (leader) / "manager" / "worker" chip; `compact` shows "mgr ★" / "wkr". */
export const SwarmRoleBadge = memo(function SwarmRoleBadge({ info, compact = false }: { info: SwarmNodeInfo | undefined; compact?: boolean }) {
  const { theme } = useTheme();
  if (!info) return null;
  const manager = info.role === 'manager';
  const color = swarmRoleColor(manager ? 'manager' : 'worker', theme.mode);
  return (
    <span
      data-testid="role-badge"
      title={info.leader ? 'Manager (leader)' : manager ? 'Manager' : 'Worker'}
      style={{
        ...chip,
        color,
        border: `1px solid ${color}55`,
        background: manager ? `${color}1f` : 'transparent',
      }}
    >
      {compact ? COMPACT_ROLE[manager ? 'manager' : 'worker'] : info.role}{info.leader ? ' ★' : ''}
    </span>
  );
});

/** Availability chip, shown only when the node is paused or drained. */
export const SwarmAvailabilityChip = memo(function SwarmAvailabilityChip({ info }: { info: SwarmNodeInfo | undefined }) {
  const { theme } = useTheme();
  if (!info || !info.availability || info.availability === 'active') return null;
  const color = info.availability === 'drain' ? theme.danger : theme.warning;
  return (
    <span
      data-testid="availability-chip"
      title={`Availability: ${info.availability}`}
      style={{ ...chip, color, border: `1px solid ${color}88`, background: `${color}1f` }}
    >
      {info.availability}
    </span>
  );
});

/** State chip, shown only when the node is not ready (down, disconnected...). */
export const SwarmStateChip = memo(function SwarmStateChip({ state }: { state: string | undefined }) {
  if (!state || state === 'ready') return null;
  const color = swarmNodeStateColor(state);
  return (
    <span
      data-testid="state-chip"
      title={`State: ${state}`}
      style={{ ...chip, color, border: `1px solid ${color}88`, background: `${color}1f` }}
    >
      {state}
    </span>
  );
});
