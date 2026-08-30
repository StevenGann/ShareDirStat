import { IconFolder, IconList, IconTreemap } from './icons';

export type MobileTab = 'browse' | 'treemap' | 'types';

interface Props {
  tab: MobileTab;
  onChange: (tab: MobileTab) => void;
}

const TABS: { id: MobileTab; label: string; icon: (p: { className?: string }) => React.ReactNode }[] = [
  { id: 'browse', label: 'Browse', icon: IconFolder },
  { id: 'treemap', label: 'Treemap', icon: IconTreemap },
  { id: 'types', label: 'File types', icon: IconList },
];

/** Bottom navigation for the phone layout: one view at a time. */
export function TabBar({ tab, onChange }: Props) {
  return (
    <nav className="tabbar" aria-label="Views">
      {TABS.map((t) => (
        <button
          key={t.id}
          type="button"
          aria-current={tab === t.id ? 'true' : undefined}
          onClick={() => onChange(t.id)}
        >
          <t.icon />
          {t.label}
        </button>
      ))}
    </nav>
  );
}
