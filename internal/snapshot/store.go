package snapshot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/StevenGann/ShareDirStat/internal/model"
	"github.com/StevenGann/ShareDirStat/internal/version"
)

// Extension is the snapshot file suffix.
const Extension = ".sds"

// CompressionLevel trades CPU for size; level 3 keeps a Pi 5 comfortably
// ahead of its storage while roughly halving the file.
var CompressionLevel = zstd.SpeedDefault

// Store reads and writes snapshots under a data directory.
type Store struct {
	dir string
}

// NewStore creates the snapshots directory if needed.
func NewStore(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "snapshots")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("snapshot directory: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the snapshots directory.
func (s *Store) Dir() string { return s.dir }

// Path returns the snapshot path for a share.
func (s *Store) Path(shareID string) string {
	return filepath.Join(s.dir, shareID+Extension)
}

// Save writes a generation atomically: temp file, fsync, rename, fsync dir.
func (s *Store) Save(g *model.Generation) (int64, error) {
	raw := g.Export()
	h := Header{
		ShareID:    raw.Meta.ShareID,
		RootPath:   raw.Meta.RootPath,
		Generation: raw.Meta.ID,
		ScanID:     raw.Meta.ScanID,
		Trigger:    raw.Meta.Trigger,
		Basis:      raw.Basis.String(),
		ScannedAt:  raw.Meta.ScannedAt,
		DurationMS: raw.Meta.Duration.Milliseconds(),
		Nodes:      uint64(len(raw.Nodes)),
		NameBytes:  uint64(len(raw.Names)),
		TopN:       raw.TopN,
		AppVersion: version.Version,
		Stats:      raw.Stats,
	}

	final := s.Path(raw.Meta.ShareID)
	tmp, err := os.CreateTemp(s.dir, "."+raw.Meta.ShareID+".*.tmp")
	if err != nil {
		return 0, fmt.Errorf("create temporary snapshot: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName) // no-op once the rename has succeeded
	}()

	if err := writeHeader(tmp, h, flagZstd); err != nil {
		return 0, fmt.Errorf("write snapshot header: %w", err)
	}
	zw, err := zstd.NewWriter(tmp, zstd.WithEncoderLevel(CompressionLevel))
	if err != nil {
		return 0, err
	}
	if err := writePayload(zw, raw); err != nil {
		_ = zw.Close()
		return 0, fmt.Errorf("write snapshot payload: %w", err)
	}
	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("finish snapshot compression: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return 0, fmt.Errorf("sync snapshot: %w", err)
	}
	size, _ := tmp.Seek(0, io.SeekCurrent)
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return 0, fmt.Errorf("install snapshot: %w", err)
	}
	syncDir(s.dir)
	return size, nil
}

// syncDir flushes a directory entry so the rename survives a power cut.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// Load reads the snapshot for a share. A missing file returns
// (nil, nil, nil): the share is simply unscanned.
func (s *Store) Load(shareID string) (*model.Generation, *Header, error) {
	path := s.Path(shareID)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	h, flags, err := readHeader(f)
	if err != nil {
		return nil, nil, err
	}
	var r io.Reader = f
	if flags&flagZstd != 0 {
		zr, zerr := zstd.NewReader(f)
		if zerr != nil {
			return nil, &h, zerr
		}
		defer zr.Close()
		r = zr
	}
	raw, err := readPayload(r, h)
	if err != nil {
		return nil, &h, err
	}
	if len(raw.Nodes) == 0 {
		return nil, &h, fmt.Errorf("%w: snapshot has no nodes", ErrCorrupt)
	}
	return model.FromRaw(raw), &h, nil
}

// Quarantine renames a snapshot that cannot be used, so the next scan can
// write a fresh one without the bad file being retried every start
// (FR-DATA-02).
func (s *Store) Quarantine(shareID, reason string) (string, error) {
	src := s.Path(shareID)
	dst := fmt.Sprintf("%s.incompatible-%s", src, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	_ = os.WriteFile(dst+".reason.txt", []byte(reason+"\n"), 0o600)
	return dst, nil
}

// Remove deletes a share's snapshot, if any.
func (s *Store) Remove(shareID string) error {
	err := os.Remove(s.Path(shareID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
