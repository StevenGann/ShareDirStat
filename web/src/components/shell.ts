import type { Basis, Node, ShareInfo } from '../api';
import type { UrlState } from '../urlState';
import type { ColorScheme } from '../treemap/colors';
import type { ResultsMode } from './ResultsView';
import type { SortField } from './Tree';

/**
 * Everything a layout shell needs. App owns all of this state; the desktop
 * and phone shells are alternative presentations of the same contract, so
 * switching layouts (a window resize, a rotation) never loses selection,
 * results or scan state.
 */
export interface ShellProps {
  share: ShareInfo;
  shares: ShareInfo[];
  busy: boolean;

  view: UrlState;
  selected: Node | null;
  selection: Node[];
  expanded: Set<string>;
  results: ResultsMode | null;
  highlightExt: string | null;

  /** Touch multi-select mode: rows toggle membership instead of replacing. */
  selectMode: boolean;
  setSelectMode: (v: boolean) => void;

  basis: Basis;
  columns: string[];
  scheme: ColorScheme;
  cushion: boolean;
  sort: SortField;
  desc: boolean;
  topHeight: number;
  leftWidth: number;

  onSelectShare: (id: string) => void;
  onScan: (path?: string) => void;
  onCancelScan: () => void;
  onPauseToggle: () => void;

  selectNode: (node: Node, mode?: 'replace' | 'toggle' | 'range') => void;
  /** Adds many nodes to the selection at once, without moving the focus. */
  selectMany: (nodes: Node[]) => void;
  zoom: (path: string) => void;
  reveal: (node: Node) => void;
  setResults: (r: ResultsMode | null) => void;
  setExpanded: (next: Set<string>) => void;
  onSortChange: (sort: SortField, desc: boolean) => void;

  setBasis: (b: Basis) => void;
  setScheme: (s: ColorScheme) => void;
  setCushion: (v: boolean) => void;
  toggleColumn: (key: string) => void;
  setTopHeight: (v: number) => void;
  setLeftWidth: (v: number) => void;
  setHighlightExt: (ext: string | null) => void;

  openHistory: () => void;
  openTrash: () => void;
  requestDelete: (nodes: Node[]) => void;
  openMenu: (node: Node, x: number, y: number) => void;
}
