package ops

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/share"
)

// Errors specific to the trash.
var (
	ErrTrashDisabled = errors.New("trash mode is not enabled")
	ErrRestoreExists = errors.New("something already exists at the original location")
)

// TrashItem is one entry recovered from the trash.
type TrashItem struct {
	// Batch is the timestamp directory the item was deleted into.
	Batch string `json:"batch"`
	// TrashPath is the item's share-relative path inside the trash.
	TrashPath string `json:"trash_path"`
	// OriginalPath is where it would be restored to.
	OriginalPath string    `json:"original_path"`
	DeletedAt    time.Time `json:"deleted_at"`
	Kind         string    `json:"kind"`
	Size         uint64    `json:"size"`
	// Blocked is set when the original location is occupied again.
	Blocked bool `json:"blocked"`
}

// ListTrash returns everything currently in a share's trash, newest first.
func (m *Manager) ListTrash(shareID string) ([]TrashItem, error) {
	if !m.TrashEnabled() {
		return nil, ErrTrashDisabled
	}
	_, s, err := m.lookup(shareID)
	if err != nil {
		return nil, err
	}
	batches, err := s.ReadDir(TrashDir)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return []TrashItem{}, nil
		}
		return nil, err
	}

	out := []TrashItem{}
	for _, b := range batches {
		if !b.IsDir() {
			continue
		}
		stamp, err := time.Parse("20060102T150405Z", b.Name())
		if err != nil {
			continue
		}
		entries, err := readManifest(s, b.Name())
		if err != nil {
			m.log.Warn("could not read a trash manifest", "share", shareID, "batch", b.Name(), "error", err)
			continue
		}
		if len(entries) == 0 {
			m.log.Warn("trash batch has no manifest, so its contents cannot be listed for restore",
				"share", shareID, "batch", b.Name())
			continue
		}
		for _, e := range entries {
			trashPath := path.Join(TrashDir, b.Name(), e.Path)
			if _, err := s.Lstat(trashPath); err != nil {
				continue // already purged or restored
			}
			deletedAt := e.DeletedAt
			if deletedAt.IsZero() {
				deletedAt = stamp
			}
			item := TrashItem{
				Batch:        b.Name(),
				TrashPath:    trashPath,
				OriginalPath: e.Path,
				DeletedAt:    deletedAt,
				Kind:         e.Kind,
				Size:         e.Size,
			}
			if _, err := s.Lstat(e.Path); err == nil {
				item.Blocked = true
			}
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DeletedAt.Equal(out[j].DeletedAt) {
			return out[i].DeletedAt.After(out[j].DeletedAt)
		}
		return out[i].OriginalPath < out[j].OriginalPath
	})
	return out, nil
}

// RestoreTrash moves an item back to where it came from.
func (m *Manager) RestoreTrash(shareID, trashPath string, client ClientInfo) (*TrashItem, error) {
	if !m.TrashEnabled() {
		return nil, ErrTrashDisabled
	}
	if err := m.CanDelete(shareID); err != nil {
		return nil, err
	}
	_, s, err := m.lookup(shareID)
	if err != nil {
		return nil, err
	}
	clean, err := CleanRel(trashPath)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(clean, TrashDir+"/") {
		return nil, fmt.Errorf("%w: %s is not in the trash", ErrUnsafePath, clean)
	}
	// Strip ".sharedirstat-trash/<batch>/" to recover the original path.
	rest := strings.TrimPrefix(clean, TrashDir+"/")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return nil, fmt.Errorf("%w: %s names a batch, not an item", ErrUnsafePath, clean)
	}
	original := rest[slash+1:]
	if original == "" {
		return nil, fmt.Errorf("%w: %s names a batch, not an item", ErrUnsafePath, clean)
	}
	if path.Base(clean) == ManifestName {
		return nil, fmt.Errorf("%w: the manifest is not a restorable item", ErrUnsafePath)
	}

	if _, err := s.Lstat(original); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrRestoreExists, original)
	}
	fi, err := s.Lstat(clean)
	if err != nil {
		return nil, err
	}
	if parent := path.Dir(original); parent != "." {
		if err := s.MkdirAll(parent, 0o755); err != nil {
			return nil, fmt.Errorf("recreate the original folder: %w", err)
		}
	}
	if err := s.Rename(clean, original); err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}

	item := &TrashItem{TrashPath: clean, OriginalPath: original, Kind: entryKind(fi), Size: uint64(max(fi.Size(), 0))}
	if err := m.audit.Append(AuditEntry{
		Share: shareID, Path: original, Kind: item.Kind, Size: item.Size,
		Outcome: "restored", Trash: clean,
		ClientIP: client.IP, UserAgent: client.UserAgent, Forwarded: client.Forwarded,
	}); err != nil {
		m.log.Error("could not write the audit record", "share", shareID, "path", original, "error", err)
	}
	m.log.Info("restored from trash", "share", shareID, "path", original, "from", clean, "client", client.IP)

	// The restored item is on disk again but not in the results; a rescan of
	// its parent is the honest way to bring the sizes back.
	if m.hooks.StartRescan != nil {
		parent := path.Dir(original)
		if parent == "." {
			parent = ""
		}
		if err := m.hooks.StartRescan(shareID, parent); err != nil {
			m.log.Warn("rescan after restore not started", "share", shareID, "path", parent, "error", err)
		}
	}
	return item, nil
}

// EmptyTrash removes a share's trash entirely, or one batch of it.
func (m *Manager) EmptyTrash(shareID, batch string, client ClientInfo) (int, error) {
	if !m.TrashEnabled() {
		return 0, ErrTrashDisabled
	}
	if err := m.CanDelete(shareID); err != nil {
		return 0, err
	}
	_, s, err := m.lookup(shareID)
	if err != nil {
		return 0, err
	}
	target := TrashDir
	if batch != "" {
		clean, err := CleanRel(batch)
		if err != nil {
			return 0, err
		}
		if strings.Contains(clean, "/") {
			return 0, fmt.Errorf("%w: %s is not a batch name", ErrUnsafePath, clean)
		}
		if _, err := time.Parse("20060102T150405Z", clean); err != nil {
			return 0, fmt.Errorf("%w: %s is not a batch name", ErrUnsafePath, clean)
		}
		target = path.Join(TrashDir, clean)
	}
	var r removal
	removeTree(s, target, &r)
	if err := m.audit.Append(AuditEntry{
		Share: shareID, Path: target, Kind: "dir",
		Outcome: "emptied", Removed: r.removed, Failed: r.failed,
		ClientIP: client.IP, UserAgent: client.UserAgent, Forwarded: client.Forwarded,
	}); err != nil {
		m.log.Error("could not write the audit record", "share", shareID, "error", err)
	}
	m.log.Info("emptied trash", "share", shareID, "target", target, "removed", r.removed, "failed", r.failed)
	return r.removed, nil
}

// trashShares is used by PurgeTrash to skip shares that cannot be written.
func (m *Manager) trashShares() []*share.Share {
	out := []*share.Share{}
	for _, sh := range m.reg.All() {
		if sh.Config.CanDelete() && sh.State() != share.StateUnavailable {
			out = append(out, sh)
		}
	}
	return out
}
