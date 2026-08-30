import { useCallback, useMemo } from 'react';
import { api, type Basis } from '../api';
import { formatBytes, formatCount, formatPercent } from '../format';
import { useAsyncData, useNow } from '../hooks';
import { useResolvedDark } from '../theme';
import { IconSearch } from './icons';
import {
  depthColor,
  extensionColor,
  MTIME_BUCKETS,
  mtimeColor,
  type ColorScheme,
} from '../treemap/colors';

interface Props {
  shareId: string;
  generation: string | null;
  basis: Basis;
  scheme: ColorScheme;
  /** The treemap's current root; the table describes that subtree. */
  root: string;
  selected: string | null;
  onSelect: (ext: string | null) => void;
  onShowFiles: (ext: string) => void;
}

/** A short, human name for the extensions people actually meet on a NAS. */
const DESCRIPTIONS: Record<string, string> = {
  mkv: 'Matroska video', mp4: 'MPEG-4 video', avi: 'AVI video', mov: 'QuickTime video',
  m4v: 'iTunes video', ts: 'MPEG transport stream', webm: 'WebM video', wmv: 'Windows Media video',
  flac: 'FLAC audio', mp3: 'MP3 audio', m4a: 'AAC audio', wav: 'WAV audio', ogg: 'Ogg audio',
  jpg: 'JPEG image', jpeg: 'JPEG image', png: 'PNG image', gif: 'GIF image', heic: 'HEIF image',
  webp: 'WebP image', tif: 'TIFF image', tiff: 'TIFF image', raw: 'Camera raw', cr2: 'Canon raw',
  nef: 'Nikon raw', dng: 'Adobe raw',
  iso: 'Disc image', img: 'Disk image', vmdk: 'VM disk', qcow2: 'QEMU disk', vhd: 'Virtual disk',
  zip: 'Zip archive', gz: 'Gzip archive', xz: 'XZ archive', bz2: 'Bzip2 archive',
  tar: 'Tar archive', '7z': '7-Zip archive', rar: 'RAR archive', zst: 'Zstandard archive',
  pdf: 'PDF document', epub: 'EPUB book', docx: 'Word document', xlsx: 'Excel workbook',
  txt: 'Plain text', md: 'Markdown', csv: 'CSV data', json: 'JSON data', xml: 'XML data',
  log: 'Log file', db: 'Database', sqlite: 'SQLite database', bak: 'Backup',
  bin: 'Binary', so: 'Shared library', deb: 'Debian package', apk: 'Android package',
};

export function Extensions({
  shareId,
  generation,
  basis,
  scheme,
  root,
  selected,
  onSelect,
  onShowFiles,
}: Props) {
  const dark = useResolvedDark();
  const load = useCallback(async () => (await api.extensions(shareId, root)).extensions, [shareId, root]);
  const { data: rows, error } = useAsyncData(
    `${shareId}|${generation ?? ''}|${root}`,
    load,
    Boolean(generation),
  );

  const total = useMemo(
    () => (rows ?? []).reduce((sum, r) => sum + (basis === 'allocated' ? r.alloc : r.size), 0),
    [rows, basis],
  );

  if (scheme === 'mtime') {
    return <MtimeLegend dark={dark} />;
  }
  if (scheme === 'depth') {
    return <DepthLegend dark={dark} />;
  }

  return (
    <section className="ext-pane" aria-label="Space by file type">
      <div className="pane-head">
        <h2>File types</h2>
        {selected && (
          <button type="button" className="link" onClick={() => onSelect(null)}>
            Clear highlight
          </button>
        )}
      </div>

      {error && (
        <div className="banner error" role="alert">
          {error}
        </div>
      )}
      {!error && !generation && (
        <p className="muted pane-note">Scan this share to see file types.</p>
      )}
      {rows && rows.length === 0 && <p className="muted pane-note">No files here.</p>}

      {rows && rows.length > 0 && (
        <div className="pane-scroll">
          <table className="ext-table">
            <thead>
              <tr>
                <th className="col-swatch"><span className="sr-only">Colour</span></th>
                <th>Type</th>
                <th className="num">Files</th>
                <th className="num">Size</th>
                <th className="num">%</th>
                <th className="col-action"><span className="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const size = basis === 'allocated' ? r.alloc : r.size;
                const share = total > 0 ? size / total : 0;
                const isSelected = selected === r.ext;
                return (
                  <tr
                    key={r.ext || '(none)'}
                    className={isSelected ? 'selected' : undefined}
                    onClick={() => onSelect(isSelected ? null : r.ext)}
                    onDoubleClick={() => onShowFiles(r.ext)}
                    tabIndex={0}
                    aria-selected={isSelected}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        onSelect(isSelected ? null : r.ext);
                      }
                    }}
                    title={
                      r.ext
                        ? `${DESCRIPTIONS[r.ext] ?? `.${r.ext} files`} — click to highlight, double-click to list`
                        : 'Files with no extension'
                    }
                  >
                    <td className="col-swatch">
                      <span
                        className="swatch"
                        style={{ background: extensionColor(r.ext, dark) }}
                        aria-hidden="true"
                      />
                    </td>
                    <td className="ext-name">
                      <span className="mono">{r.ext ? `.${r.ext}` : '(none)'}</span>
                      {r.ext && DESCRIPTIONS[r.ext] && (
                        <span className="muted ext-desc"> {DESCRIPTIONS[r.ext]}</span>
                      )}
                    </td>
                    <td className="num">{formatCount(r.files)}</td>
                    <td className="num">{formatBytes(size)}</td>
                    <td className="num">
                      <span className="bar" aria-hidden="true">
                        <span style={{ width: `${Math.min(share * 100, 100).toFixed(1)}%` }} />
                      </span>
                      {formatPercent(share)}
                    </td>
                    <td className="col-action">
                      {r.ext !== '' && (
                        <button
                          type="button"
                          className="icon-button"
                          aria-label={`List .${r.ext} files`}
                          title={`List the .${r.ext} files`}
                          onClick={(e) => {
                            e.stopPropagation();
                            onShowFiles(r.ext);
                          }}
                        >
                          <IconSearch />
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function MtimeLegend({ dark }: { dark: boolean }) {
  const now = useNow();
  const ages = [1, 20, 100, 300, 700, 2000];
  return (
    <section className="ext-pane" aria-label="Colour legend">
      <div className="pane-head">
        <h2>Age</h2>
      </div>
      <ul className="legend">
        {MTIME_BUCKETS.map((label, i) => (
          <li key={label}>
            <span
              className="swatch"
              style={{ background: mtimeColor(now - (ages[i] as number) * 86_400_000, now, dark) }}
              aria-hidden="true"
            />
            {label}
          </li>
        ))}
      </ul>
      <p className="muted pane-note">Cells are coloured by when the file was last modified.</p>
    </section>
  );
}

function DepthLegend({ dark }: { dark: boolean }) {
  return (
    <section className="ext-pane" aria-label="Colour legend">
      <div className="pane-head">
        <h2>Depth</h2>
      </div>
      <ul className="legend">
        {[0, 1, 2, 3, 4, 5].map((d) => (
          <li key={d}>
            <span className="swatch" style={{ background: depthColor(d, dark) }} aria-hidden="true" />
            {d === 0 ? 'top level' : `${d} deep`}
          </li>
        ))}
      </ul>
      <p className="muted pane-note">Cells are coloured by how deep they sit in the tree.</p>
    </section>
  );
}
