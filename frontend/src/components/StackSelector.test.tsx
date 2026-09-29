// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup } from '@testing-library/react';
import { ThemeProvider } from '../theme';
import { StackSelector } from './StackSelector';
import { STANDALONE_STACK, type StackSummary } from '../utils/stack';

afterEach(() => cleanup());

const stacks: StackSummary[] = [
  { name: 'blog', running: 1, total: 2 },
  { name: 'shop', running: 3, total: 3 },
  { name: STANDALONE_STACK, running: 0, total: 1 },
];

function renderSelector(props: Partial<React.ComponentProps<typeof StackSelector>> = {}) {
  const onSelect = vi.fn();
  render(
    <ThemeProvider>
      <StackSelector stacks={stacks} selected={null} onSelect={onSelect} {...props} />
    </ThemeProvider>,
  );
  return onSelect;
}

describe('StackSelector', () => {
  it('shows "All stacks" when nothing is selected', () => {
    renderSelector();
    expect(screen.getByRole('button', { name: 'Stack' }).textContent).toContain('All stacks');
  });

  it('lists every stack with running/total counts, plus All and Standalone', () => {
    renderSelector();
    fireEvent.click(screen.getByRole('button', { name: 'Stack' }));
    const options = screen.getAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual([
      'All stacks4/6',
      'blog1/2',
      'shop3/3',
      'Standalone0/1',
    ]);
  });

  it('selects a stack and closes', () => {
    const onSelect = renderSelector();
    fireEvent.click(screen.getByRole('button', { name: 'Stack' }));
    fireEvent.click(screen.getByText('shop'));
    expect(onSelect).toHaveBeenCalledWith('shop');
    expect(screen.queryByRole('listbox')).toBeNull();
  });

  it('selects All as null and Standalone as its scope key', () => {
    const onSelect = renderSelector({ selected: 'shop' });
    fireEvent.click(screen.getByRole('button', { name: 'Stack' }));
    fireEvent.click(screen.getByText('All stacks'));
    expect(onSelect).toHaveBeenLastCalledWith(null);
    fireEvent.click(screen.getByRole('button', { name: 'Stack' }));
    fireEvent.click(screen.getByText('Standalone'));
    expect(onSelect).toHaveBeenLastCalledWith(STANDALONE_STACK);
  });

  it('marks the selected stack and shows it on the trigger', () => {
    renderSelector({ selected: 'blog' });
    const trigger = screen.getByRole('button', { name: 'Stack' });
    expect(trigger.textContent).toContain('blog');
    fireEvent.click(trigger);
    expect(screen.getByRole('option', { selected: true }).textContent).toContain('blog');
  });

  it('keeps a URL-selected stack that is not (yet) in the graph', () => {
    renderSelector({ selected: 'ghost' });
    expect(screen.getByRole('button', { name: 'Stack' }).textContent).toContain('ghost');
  });

  it('renders nothing when there are only standalone workloads', () => {
    const { container } = render(
      <ThemeProvider>
        <StackSelector stacks={[{ name: STANDALONE_STACK, running: 1, total: 1 }]} selected={null} onSelect={vi.fn()} />
      </ThemeProvider>,
    );
    expect(container.innerHTML).toBe('');
  });
});
