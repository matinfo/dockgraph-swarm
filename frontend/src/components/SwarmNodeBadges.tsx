import { memo } from 'react';
import { useTheme } from '../theme';
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

/** "manager ★" (leader) / "manager" / "worker" chip. */
export const SwarmRoleBadge = memo(function SwarmRoleBadge({ info }: { info: SwarmNodeInfo | undefined }) {
  const { theme } = useTheme();
  if (!info) return null;
  const manager = info.role === 'manager';
  return (
    <span
      data-testid="role-badge"
      title={info.leader ? 'Manager (leader)' : manager ? 'Manager' : 'Worker'}
      style={{
        ...chip,
        color: manager ? theme.accent : theme.nodeSubtext,
        border: `1px solid ${manager ? theme.accentSoft : theme.panelBorder}`,
        background: manager ? theme.accentSoft : 'transparent',
      }}
    >
      {info.role}{info.leader ? ' ★' : ''}
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
