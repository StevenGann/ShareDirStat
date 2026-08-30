import type { Basis, ShareInfo } from '../api';
import type { ColorScheme } from '../treemap/colors';
import { Sheet } from './Sheet';
import { ThemeSelect } from './ShareBar';

interface Props {
  share: ShareInfo;
  basis: Basis;
  scheme: ColorScheme;
  cushion: boolean;
  setBasis: (b: Basis) => void;
  setScheme: (s: ColorScheme) => void;
  setCushion: (v: boolean) => void;
  onLargest: () => void;
  onHistory: () => void;
  onTrash: () => void;
  onClose: () => void;
}

/** The phone stand-in for the desktop toolbar: view options and the
 *  secondary views, one thumb-sized row each. */
export function SettingsSheet({
  share,
  basis,
  scheme,
  cushion,
  setBasis,
  setScheme,
  setCushion,
  onLargest,
  onHistory,
  onTrash,
  onClose,
}: Props) {
  return (
    <Sheet title="View options" variant="bottom" onClose={onClose}>
      <div className="sheet-options">
        <fieldset className="segmented">
          <legend className="sr-only">Size basis</legend>
          {(['apparent', 'allocated'] as const).map((b) => (
            <label key={b} className={basis === b ? 'on' : ''}>
              <input type="radio" name="basis" checked={basis === b} onChange={() => setBasis(b)} />
              {b === 'apparent' ? 'Apparent size' : 'On disk'}
            </label>
          ))}
        </fieldset>

        <label className="field">
          <span className="sr-only">Colour by</span>
          <select value={scheme} onChange={(e) => setScheme(e.target.value as ColorScheme)}>
            <option value="extension">Colour by type</option>
            <option value="depth">Colour by depth</option>
            <option value="mtime">Colour by age</option>
          </select>
        </label>

        <label className="check">
          <input type="checkbox" checked={cushion} onChange={(e) => setCushion(e.target.checked)} />
          Cushions
        </label>

        <ThemeSelect />
      </div>

      <div className="sheet-actions">
        <button
          type="button"
          onClick={() => {
            onClose();
            onLargest();
          }}
        >
          Largest files
        </button>
        <button
          type="button"
          onClick={() => {
            onClose();
            onHistory();
          }}
        >
          Scan history
          {share.stats && share.stats.errors > 0 ? ` · ${share.stats.errors} errors` : ''}
        </button>
        {share.trash_enabled && (
          <button
            type="button"
            onClick={() => {
              onClose();
              onTrash();
            }}
          >
            Trash
          </button>
        )}
      </div>
    </Sheet>
  );
}
