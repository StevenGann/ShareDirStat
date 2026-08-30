package ops

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sync"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/events"
	"github.com/StevenGann/ShareDirStat/internal/share"
)

// TrashDir is the directory deleted items are moved to when trash mode is on.
// It is excluded from scans so the trash never shows up as usage twice.
const TrashDir = ".sharedirstat-trash"

// Hooks let the caller observe operations without this package importing the
// metrics registry.
type Hooks struct {
	OnDelete   func(shareID, outcome string, freed uint64)
	OnDownload func(shareID, kind string, bytes int64)
	// StartRescan queues a reconcile rescan of a subtree after a delete only
	// partly succeeded (FR-DEL-05).
	StartRescan func(shareID, path string) error
	// RunningScanPath reports the subtree a scan is currently rescanning, if
	// any. A full scan reports ("", true).
	RunningScanPath func(shareID string) (string, bool)
	// MarkDirty says the in-memory results no longer match the share's
	// snapshot, so it should be rewritten (FR-DATA-04).
	MarkDirty func(shareID string)
}

// Manager owns the per-share file handles, the confirmation tokens and the
// audit log.
type Manager struct {
	cfg   *config.Config
	reg   *share.Registry
	bus   events.Publisher
	log   *slog.Logger
	audit *AuditLog
	toks  *tokenStore
	hooks Hooks

	mu       sync.Mutex
	stores   map[string]*Store
	deleting map[string]bool
	closed   bool
}

// NewManager opens the audit log and prepares the per-share stores.
func NewManager(cfg *config.Config, reg *share.Registry, bus events.Publisher, log *slog.Logger, hooks Hooks) (*Manager, error) {
	audit, err := NewAuditLog(cfg.DataDir, DefaultAuditMaxBytes, DefaultAuditKeep)
	if err != nil {
		return nil, err
	}
	return &Manager{
		cfg: cfg, reg: reg, bus: bus, log: log, audit: audit,
		toks:     newTokenStore(),
		hooks:    hooks,
		stores:   make(map[string]*Store),
		deleting: make(map[string]bool),
	}, nil
}

// Close releases every share handle and the audit log.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	stores := m.stores
	m.stores = map[string]*Store{}
	m.mu.Unlock()
	for _, s := range stores {
		_ = s.Close()
	}
	return m.audit.Close()
}

// Audit exposes the log so the API can serve recent deletions.
func (m *Manager) Audit() *AuditLog { return m.audit }

// store returns the share's Store, opening it on first use.
func (m *Manager) store(sh *share.Share) (*Store, error) {
	id := sh.Config.ID
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("server is shutting down")
	}
	if s, ok := m.stores[id]; ok {
		return s, nil
	}
	s, err := OpenStore(id, sh.Config.Path)
	if err != nil {
		return nil, err
	}
	m.stores[id] = s
	return s, nil
}

// lookup resolves a share and its store.
func (m *Manager) lookup(shareID string) (*share.Share, *Store, error) {
	sh, ok := m.reg.Get(shareID)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrShareNotFound, shareID)
	}
	if sh.State() == share.StateUnavailable {
		return nil, nil, fmt.Errorf("%w: %s", ErrShareUnavailable, sh.Snapshot().Error)
	}
	s, err := m.store(sh)
	if err != nil {
		return nil, nil, err
	}
	return sh, s, nil
}

// Errors about the share itself rather than the operation.
var (
	ErrShareNotFound    = errors.New("share not found")
	ErrShareUnavailable = errors.New("share path is not available")
	ErrDownloadDisabled = errors.New("downloading is not enabled for this share")
	ErrZipDisabled      = errors.New("ZIP downloads are not enabled")
)

// CanDelete reports why deleting is not possible, or nil when it is. All
// three gates from FR-DEL-01 are checked: the global switch, the per-share
// opt-in, and whether the filesystem will actually accept an unlink.
func (m *Manager) CanDelete(shareID string) error {
	if m.cfg.Operations.Readonly {
		return fmt.Errorf("%w: the server is running in read-only mode", ErrDeleteDisabled)
	}
	sh, s, err := m.lookup(shareID)
	if err != nil {
		return err
	}
	if !sh.Config.CanDelete() {
		return fmt.Errorf("%w: set allow_delete on the share to enable it", ErrDeleteDisabled)
	}
	if err := s.CheckWritable(""); err != nil {
		return fmt.Errorf("%w: %w", ErrDeleteDisabled, err)
	}
	return nil
}

// TrashEnabled reports whether deletes move to trash rather than unlink.
func (m *Manager) TrashEnabled() bool {
	return m.cfg.Operations.Delete.Trash.Enabled
}

// Preview resolves what a delete would affect and issues a single-use
// confirmation token bound to exactly those paths (FR-DEL-02).
func (m *Manager) Preview(shareID string, paths []string) (*Preview, error) {
	if err := m.CanDelete(shareID); err != nil {
		return nil, err
	}
	sh, s, err := m.lookup(shareID)
	if err != nil {
		return nil, err
	}
	clean, err := normalizePaths(paths)
	if err != nil {
		return nil, err
	}
	clean = dropNested(clean)

	gen := sh.Generation()
	p := &Preview{
		Share:       shareID,
		Targets:     make([]Target, 0, len(clean)),
		ConfirmMode: m.cfg.Operations.Delete.ConfirmMode,
		Trash:       m.TrashEnabled(),
		ExpiresAt:   time.Now().Add(TokenTTL).UTC(),
	}
	if gen != nil {
		p.Generation = gen.ID()
	}
	for _, rel := range clean {
		t := describe(gen, s, rel)
		p.Targets = append(p.Targets, t)
		p.TotalSize += t.Size
		p.TotalAlloc += t.Alloc
		p.TotalFiles += t.Files
		p.TotalDirs += t.Dirs
	}
	p.NameToType = needsTypedName(p.Targets, p.ConfirmMode)
	p.Confirm = m.toks.issue(&tokenItem{
		shareID:    shareID,
		generation: p.Generation,
		paths:      clean,
		nameToType: p.NameToType,
		trash:      p.Trash,
		expires:    p.ExpiresAt,
	})
	return p, nil
}

// Delete removes the given paths after validating the confirmation token.
func (m *Manager) Delete(shareID string, paths []string, token string, client ClientInfo) (*DeleteResult, error) {
	if err := m.CanDelete(shareID); err != nil {
		return nil, err
	}
	sh, s, err := m.lookup(shareID)
	if err != nil {
		return nil, err
	}
	clean, err := normalizePaths(paths)
	if err != nil {
		return nil, err
	}
	clean = dropNested(clean)

	if _, err := m.toks.consume(token, shareID, clean); err != nil {
		return nil, err
	}

	// A subtree rescan of an ancestor would splice stale children back over
	// the delete, so the two are not allowed to overlap (FR-DEL-08). A full
	// scan is fine: it simply will not find what has gone.
	if m.hooks.RunningScanPath != nil {
		if sub, running := m.hooks.RunningScanPath(shareID); running && sub != "" {
			for _, rel := range clean {
				if rel == sub || isUnder(rel, sub) || isUnder(sub, rel) {
					return nil, fmt.Errorf("%w: %s is being rescanned", ErrScanInProgress, sub)
				}
			}
		}
	}

	// Deletes on one share run one at a time (FR-DEL-09).
	m.mu.Lock()
	if m.deleting[shareID] {
		m.mu.Unlock()
		return nil, ErrDeleteBusy
	}
	m.deleting[shareID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.deleting, shareID)
		m.mu.Unlock()
	}()

	res := &DeleteResult{Share: shareID, Results: make([]PathResult, 0, len(clean))}
	gen := sh.Generation()
	modelChanged := false
	var trashStamp string
	if m.TrashEnabled() {
		trashStamp = time.Now().UTC().Format("20060102T150405Z")
	}

	for _, rel := range clean {
		target := describe(gen, s, rel)
		pr := m.deleteOne(s, rel, target, trashStamp)

		// Keep the live model in step so the UI updates without a rescan.
		if pr.Outcome == OutcomeDeleted && gen != nil {
			if freed, freedAlloc, ok := gen.Remove(rel); ok {
				pr.Freed, pr.FreedAlloc = freed, freedAlloc
				modelChanged = true
			}
		}
		if pr.Outcome != OutcomeDeleted {
			// The model and the filesystem now disagree about this subtree;
			// a rescan is the only honest way to reconcile them.
			if m.hooks.StartRescan != nil {
				parent := path.Dir(rel)
				if parent == "." {
					parent = ""
				}
				if err := m.hooks.StartRescan(shareID, parent); err != nil {
					m.log.Warn("reconcile rescan not started", "share", shareID, "path", parent, "error", err)
				} else {
					res.Reconcile = append(res.Reconcile, parent)
				}
			}
		}

		res.Results = append(res.Results, pr)
		res.FreedTotal += pr.Freed

		if err := m.audit.Append(AuditEntry{
			Share: shareID, Path: rel, Kind: target.Kind,
			Size: pr.Freed, Alloc: pr.FreedAlloc,
			Files: target.Files, Dirs: target.Dirs,
			Outcome: pr.Outcome, Removed: pr.Removed, Failed: pr.Failed,
			Errno: pr.Errno, Error: pr.Message, Trash: pr.Trash,
			ClientIP: client.IP, UserAgent: client.UserAgent, Forwarded: client.Forwarded,
		}); err != nil {
			m.log.Error("could not write the audit record", "share", shareID, "path", rel, "error", err)
		}
		if m.hooks.OnDelete != nil {
			m.hooks.OnDelete(shareID, pr.Outcome, pr.Freed)
		}
		m.log.Info("delete", "share", shareID, "path", rel, "outcome", pr.Outcome,
			"freed", pr.Freed, "removed", pr.Removed, "failed", pr.Failed, "client", client.IP)
	}

	// The snapshot on disk still describes the tree as it was; without this
	// a restart would resurrect everything just deleted (FR-DATA-04).
	if modelChanged && m.hooks.MarkDirty != nil {
		m.hooks.MarkDirty(shareID)
	}

	if m.bus != nil {
		m.bus.Publish(events.NodeDeleted, map[string]any{
			"share_id": shareID,
			"paths":    clean,
			"freed":    res.FreedTotal,
			"results":  res.Results,
		})
	}
	return res, nil
}

// deleteOne removes a single target, either by moving it to the trash or by
// taking it apart entry by entry.
func (m *Manager) deleteOne(s *Store, rel string, target Target, trashStamp string) PathResult {
	pr := PathResult{Path: rel, Outcome: OutcomeFailed}

	fi, err := s.Lstat(rel)
	if err != nil {
		pr.Errno, pr.Message = Errno(err), err.Error()
		return pr
	}
	// Refuse to delete a mount point: its contents are another filesystem.
	if fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 && !s.SameDevice(fi) {
		pr.Errno, pr.Message = "", ErrMountPoint.Error()
		return pr
	}

	if trashStamp != "" {
		dest, err := m.moveToTrash(s, rel, trashStamp, target)
		if err != nil {
			pr.Errno, pr.Message = Errno(err), err.Error()
			return pr
		}
		pr.Outcome, pr.Trash, pr.Removed = OutcomeDeleted, dest, 1
		pr.Freed, pr.FreedAlloc = target.Size, target.Alloc
		return pr
	}

	var r removal
	removeTree(s, rel, &r)
	pr.Removed, pr.Failed, pr.Errors = r.removed, r.failed, r.errs
	switch {
	case r.failed == 0:
		pr.Outcome = OutcomeDeleted
		pr.Freed, pr.FreedAlloc = target.Size, target.Alloc
	case r.removed > 0:
		pr.Outcome = OutcomePartial
		if len(r.errs) > 0 {
			pr.Errno, pr.Message = r.errs[0].Errno, r.errs[0].Message
		}
	default:
		pr.Outcome = OutcomeFailed
		if len(r.errs) > 0 {
			pr.Errno, pr.Message = r.errs[0].Errno, r.errs[0].Message
		}
	}
	return pr
}

// moveToTrash renames a target under <share>/.sharedirstat-trash/<stamp>/,
// preserving its original path so a restore knows where it came from.
//
// The rename is within the same filesystem, so it is atomic and free. If it
// would cross a device the operation fails rather than silently copying
// gigabytes (FR-DEL-07).
func (m *Manager) moveToTrash(s *Store, rel, stamp string, target Target) (string, error) {
	dest := path.Join(TrashDir, stamp, rel)
	if err := s.MkdirAll(path.Dir(dest), 0o700); err != nil {
		return "", fmt.Errorf("prepare trash directory: %w", err)
	}
	if err := s.Rename(rel, dest); err != nil {
		return "", fmt.Errorf("move to trash: %w", err)
	}
	// The layout alone cannot say what was deleted: a batch holding
	// Movies/2019/big.mkv could be that one file, or the whole Movies folder.
	// Recording it removes the guesswork from restore.
	if err := appendManifest(s, stamp, manifestEntry{
		Path: rel, Kind: target.Kind, Size: target.Size, DeletedAt: time.Now().UTC(),
	}); err != nil {
		m.log.Warn("could not record the trash manifest entry; the item will not be listed for restore",
			"share", s.ShareID, "path", rel, "error", err)
	}
	return dest, nil
}

// PurgeTrash removes trash batches older than the configured retention.
func (m *Manager) PurgeTrash() {
	if !m.TrashEnabled() {
		return
	}
	cutoff := time.Now().Add(-m.cfg.Operations.Delete.Trash.Retention.D())
	for _, sh := range m.trashShares() {
		s, err := m.store(sh)
		if err != nil {
			continue
		}
		entries, err := s.ReadDir(TrashDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			stamp, err := time.Parse("20060102T150405Z", e.Name())
			if err != nil || !stamp.Before(cutoff) {
				continue
			}
			var r removal
			removeTree(s, path.Join(TrashDir, e.Name()), &r)
			m.log.Info("purged trash batch", "share", sh.Config.ID, "batch", e.Name(),
				"removed", r.removed, "failed", r.failed)
		}
	}
}

// isUnder reports whether child is strictly inside parent.
func isUnder(child, parent string) bool {
	if parent == "" {
		return child != ""
	}
	return len(child) > len(parent)+1 && child[:len(parent)+1] == parent+"/"
}

// --- downloads -------------------------------------------------------------

// OpenForDownload opens a regular file for streaming to the client.
func (m *Manager) OpenForDownload(shareID, rel string) (*os.File, fs.FileInfo, error) {
	sh, s, err := m.lookup(shareID)
	if err != nil {
		return nil, nil, err
	}
	if !sh.Config.CanDownload() {
		return nil, nil, fmt.Errorf("%w: set allow_download on the share to enable it", ErrDownloadDisabled)
	}
	clean, err := CleanRel(rel)
	if err != nil {
		return nil, nil, err
	}
	if clean == "" {
		return nil, nil, ErrIsShareRoot
	}

	// The download endpoint does not consult the scan results: a file created
	// after the last scan is still downloadable (FR-DL-05).
	fi, err := s.Lstat(clean)
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: %s is a %s", ErrNotAFile, clean, kindOf(fi))
	}
	f, err := s.OpenFile(clean)
	if err != nil {
		return nil, nil, err
	}
	// Re-stat through the descriptor: this is the object that will actually
	// be sent, whatever the name may point at now.
	fi, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, ErrNotAFile
	}
	return f, fi, nil
}

// CanZip reports whether ZIP downloads are available for a share.
func (m *Manager) CanZip(shareID string) error {
	sh, _, err := m.lookup(shareID)
	if err != nil {
		return err
	}
	if !sh.Config.CanDownload() {
		return fmt.Errorf("%w: set allow_download on the share to enable it", ErrDownloadDisabled)
	}
	if !m.cfg.Operations.Download.ZipEnabled {
		return ErrZipDisabled
	}
	return nil
}

// kindOf names a file type for an error message ("x is a directory"), where
// the default case means "not the regular file we needed".
func kindOf(fi fs.FileInfo) string {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return "symlink"
	case fi.IsDir():
		return "directory"
	default:
		return "special file"
	}
}

// entryKind classifies an entry for the API, using the same vocabulary as
// the scanner: dir, file, symlink or other.
func entryKind(fi fs.FileInfo) string {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return "symlink"
	case fi.IsDir():
		return "dir"
	case fi.Mode().IsRegular():
		return "file"
	default:
		return "other"
	}
}

// noteDownload records download bytes for the metrics hooks.
func (m *Manager) noteDownload(shareID, kind string, n int64) {
	if m.hooks.OnDownload != nil {
		m.hooks.OnDownload(shareID, kind, n)
	}
}
