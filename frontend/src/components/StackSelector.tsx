import { memo, useEffect, useRef, useState } from "react";
import { useTheme } from "../theme";
import { STANDALONE_STACK, type StackSummary } from "../utils/stack";

interface Props {
  /** Every stack in the (unscoped) graph, from listStacks. */
  stacks: StackSummary[];
  /** Selected stack, or null for all. */
  selected: string | null;
  onSelect: (stack: string | null) => void;
}

interface Option {
  key: string | null;
  label: string;
  running: number;
  total: number;
}

function stackLabel(name: string): string {
  return name === STANDALONE_STACK ? "Standalone" : name;
}

/**
 * Header dropdown that scopes the graph, table, dashboard and logs to one
 * compose project / swarm stack. Each entry shows its running/total workloads.
 * Hidden when there is nothing to choose between.
 */
export const StackSelector = memo(function StackSelector({ stacks, selected, onSelect }: Props) {
  const { theme } = useTheme();
  const [open, setOpen] = useState(false);
  const [hoveredKey, setHoveredKey] = useState<string | null | undefined>(undefined);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handleClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", handleClick);
    document.addEventListener("keydown", handleKey);
    return () => {
      document.removeEventListener("mousedown", handleClick);
      document.removeEventListener("keydown", handleKey);
    };
  }, [open]);

  const hasNamedStack = stacks.some((s) => s.name !== STANDALONE_STACK);
  if (!hasNamedStack && !selected) return null;

  const all: Option = {
    key: null,
    label: "All stacks",
    running: stacks.reduce((n, s) => n + s.running, 0),
    total: stacks.reduce((n, s) => n + s.total, 0),
  };
  const options: Option[] = [
    all,
    ...stacks.map((s) => ({ key: s.name, label: stackLabel(s.name), running: s.running, total: s.total })),
  ];
  // Keep a URL-selected stack visible even before (or after) it has workloads.
  if (selected && !stacks.some((s) => s.name === selected)) {
    options.push({ key: selected, label: stackLabel(selected), running: 0, total: 0 });
  }
  const active = options.find((o) => o.key === selected) ?? all;

  return (
    <div ref={ref} style={{ position: "relative", flex: "0 1 auto", minWidth: 0 }}>
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label="Stack"
        title="Scope the view to one stack or project"
        onClick={() => setOpen((v) => !v)}
        style={{
          display: "flex",
          alignItems: "center",
          gap: 6,
          maxWidth: 220,
          background: selected ? theme.rowHover : theme.canvasBg,
          color: selected ? theme.nodeText : theme.panelText,
          border: `1px solid ${selected ? theme.accent : theme.panelBorder}`,
          borderRadius: 8,
          padding: "5px 10px",
          fontSize: 12,
          cursor: "pointer",
          outline: "none",
        }}
      >
        <span style={{ color: theme.nodeSubtext, fontSize: 11 }}>Stack</span>
        <span style={{ fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {active.label}
        </span>
        <span aria-hidden="true" style={{ color: theme.nodeSubtext, fontSize: 9 }}>{open ? "▲" : "▼"}</span>
      </button>
      {open && (
        <div
          role="listbox"
          aria-label="Stacks"
          style={{
            position: "absolute",
            top: "calc(100% + 4px)",
            left: 0,
            minWidth: 220,
            maxHeight: 360,
            overflowY: "auto",
            background: theme.panelBg,
            border: `1px solid ${theme.panelBorder}`,
            borderRadius: 6,
            zIndex: 20,
            boxShadow: theme.mode === "dark" ? "0 4px 12px rgba(0,0,0,0.4)" : "0 4px 12px rgba(0,0,0,0.1)",
          }}
        >
          {options.map((opt) => {
            const isActive = opt.key === selected;
            const isHovered = opt.key === hoveredKey;
            let bg = "transparent";
            if (isActive) bg = theme.panelBorder;
            else if (isHovered) bg = theme.rowHover;
            return (
              <div
                key={opt.key ?? "__all"}
                role="option"
                aria-selected={isActive}
                onClick={() => { onSelect(opt.key); setOpen(false); }}
                onMouseEnter={() => setHoveredKey(opt.key)}
                onMouseLeave={() => setHoveredKey(undefined)}
                style={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  gap: 12,
                  padding: "6px 12px",
                  fontSize: 12,
                  fontWeight: isActive ? 600 : 400,
                  cursor: "pointer",
                  color: isActive ? theme.accent : theme.panelText,
                  background: bg,
                  whiteSpace: "nowrap",
                  fontStyle: opt.key === STANDALONE_STACK ? "italic" : "normal",
                }}
              >
                <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{opt.label}</span>
                <span style={{ fontFamily: "var(--dg-font-mono)", fontSize: 11, color: theme.nodeSubtext, fontStyle: "normal" }}>
                  {opt.running}/{opt.total}
                </span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
});
