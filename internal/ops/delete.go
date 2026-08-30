package ops

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

// Delete protocol constants (§11.1).
const (
	// TokenTTL is how long a confirmation token stays valid.
	TokenTTL = 5 * time.Minute
	// MaxDeletePaths bounds one delete request.
	MaxDeletePaths = 1000
	// MaxEntryErrors is how many per-entry failures are reported for one target.
	MaxEntryErrors = 50
)

// Errors specific to the delete protocol.
var (
	ErrDeleteDisabled = errors.New("deleting is not enabled for this share")
	ErrBadToken       = errors.New("confirmation token is invalid, expired, or already used")
	ErrTooManyPaths   = errors.New("too many paths in one request")
	ErrNoPaths        = errors.New("no paths given")
	ErrDeleteBusy     = errors.New("another delete is already running on this share")
	ErrScanInProgress = errors.New("a rescan of this subtree is in progress")
)

// Outcome of deleting one target.
const (
	OutcomeDeleted = "deleted"
	OutcomePartial = "partial"
	OutcomeFailed  = "failed"
)

// Target describes one path a delete would affect.
type Target struct {
	Path     string   `json:"path"`
	Kind     string   `json:"kind"`
	Size     uint64   `json:"size"`
	Alloc    uint64   `json:"alloc"`
	Files    uint32   `json:"files"`
	Dirs     uint32   `json:"dirs"`
	Exists   bool     `json:"exists"`
	Warnings []string `json:"warnings"`
}

// Preview is the answer to POST /delete/preview: exactly what would go, and
// the token needed to actually do it.
type Preview struct {
	Share       string    `json:"share"`
	Generation  string    `json:"generation"`
	Targets     []Target  `json:"targets"`
	TotalSize   uint64    `json:"total_size"`
	TotalAlloc  uint64    `json:"total_alloc"`
	TotalFiles  uint32    `json:"total_files"`
	TotalDirs   uint32    `json:"total_dirs"`
	Confirm     string    `json:"confirm"`
	ExpiresAt   time.Time `json:"expires_at"`
	ConfirmMode string    `json:"confirm_mode"`
	// NameToType is the name the user must type to confirm, when
	// confirm_mode is "name" and the request is destructive enough to
	// warrant it. Empty means a simple confirmation is enough.
	NameToType string `json:"name_to_type"`
	// Trash reports whether the delete will move to trash instead of unlink.
	Trash bool `json:"trash"`
}

// EntryError is one child that could not be removed.
type EntryError struct {
	Path    string `json:"path"`
	Errno   string `json:"errno,omitempty"`
	Message string `json:"message"`
}

// PathResult is the outcome for one requested path.
type PathResult struct {
	Path       string       `json:"path"`
	Outcome    string       `json:"outcome"`
	Freed      uint64       `json:"freed"`
	FreedAlloc uint64       `json:"freed_alloc"`
	Removed    int          `json:"removed"`
	Failed     int          `json:"failed"`
	Errno      string       `json:"errno,omitempty"`
	Message    string       `json:"message,omitempty"`
	Trash      string       `json:"trash,omitempty"`
	Errors     []EntryError `json:"errors,omitempty"`
}

// DeleteResult is the answer to POST /delete.
type DeleteResult struct {
	Share      string       `json:"share"`
	Results    []PathResult `json:"results"`
	FreedTotal uint64       `json:"freed_total"`
	// Reconcile lists paths whose subtree is being rescanned because the
	// delete only partly succeeded (FR-DEL-05).
	Reconcile []string `json:"reconcile"`
}

// ClientInfo is what the audit log records about the requester.
type ClientInfo struct {
	IP        string
	UserAgent string
	Forwarded string
}

// --- confirmation tokens ---------------------------------------------------

type tokenItem struct {
	shareID    string
	generation string
	paths      []string
	nameToType string
	trash      bool
	expires    time.Time
}

// tokenStore hands out single-use confirmation tokens bound to an exact set
// of paths, so a preview of one file can never be replayed to delete another.
type tokenStore struct {
	mu    sync.Mutex
	items map[string]*tokenItem
}

func newTokenStore() *tokenStore {
	return &tokenStore{items: make(map[string]*tokenItem)}
}

func (t *tokenStore) issue(it *tokenItem) string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	id := base64.RawURLEncoding.EncodeToString(b[:])

	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, v := range t.items {
		if now.After(v.expires) {
			delete(t.items, k)
		}
	}
	t.items[id] = it
	return id
}

// consume validates a token against the request it is being used for and
// retires it. A token is good for exactly one delete of exactly one path set.
func (t *tokenStore) consume(id, shareID string, paths []string) (*tokenItem, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	it, ok := t.items[id]
	if !ok {
		return nil, ErrBadToken
	}
	delete(t.items, id)
	if time.Now().After(it.expires) {
		return nil, fmt.Errorf("%w: it expired at %s", ErrBadToken, it.expires.Format(time.RFC3339))
	}
	if it.shareID != shareID {
		return nil, fmt.Errorf("%w: it was issued for a different share", ErrBadToken)
	}
	if !samePaths(it.paths, paths) {
		return nil, fmt.Errorf("%w: it was issued for a different set of paths", ErrBadToken)
	}
	return it, nil
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// normalizePaths validates, canonicalises, sorts and de-duplicates a request's
// paths. Sorting makes token comparison order-independent; de-duplication
// stops a repeated path from being counted twice in the totals.
func normalizePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, ErrNoPaths
	}
	if len(paths) > MaxDeletePaths {
		return nil, fmt.Errorf("%w: %d paths, the limit is %d", ErrTooManyPaths, len(paths), MaxDeletePaths)
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		clean, err := CleanRel(p)
		if err != nil {
			return nil, err
		}
		if clean == "" {
			return nil, ErrIsShareRoot
		}
		if _, dup := seen[clean]; dup {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out, nil
}

// dropNested removes paths that are already covered by an ancestor in the
// same request, so a delete of "a" and "a/b" does not try to remove "a/b"
// after "a" has taken it with the rest of the subtree.
func dropNested(sorted []string) []string {
	out := make([]string, 0, len(sorted))
	for _, p := range sorted {
		if len(out) > 0 {
			prev := out[len(out)-1]
			if p == prev || strings.HasPrefix(p, prev+"/") {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// --- recursive removal -----------------------------------------------------

// removal accumulates the outcome of taking one target apart.
type removal struct {
	removed int
	failed  int
	errs    []EntryError
}

func (r *removal) fail(p string, err error) {
	r.failed++
	if len(r.errs) < MaxEntryErrors {
		r.errs = append(r.errs, EntryError{Path: p, Errno: Errno(err), Message: err.Error()})
	}
}

// removeTree deletes rel and everything under it, depth first, reporting each
// failure rather than stopping at the first one (FR-DEL-04, FR-DEL-05).
//
// Directories are recursed into only when Lstat says they really are
// directories: a symlink is unlinked, never followed, so a link inside the
// share can never redirect the delete at its target.
func removeTree(s *Store, rel string, r *removal) {
	fi, err := s.Lstat(rel)
	if err != nil {
		r.fail(rel, err)
		return
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		if err := s.Remove(rel); err != nil {
			r.fail(rel, err)
			return
		}
		r.removed++
		return
	}

	// A nested mount must not be descended into: its contents belong to a
	// different filesystem the operator did not offer up (FR-DEL-03).
	if !s.SameDevice(fi) {
		r.fail(rel, ErrMountPoint)
		return
	}

	entries, err := s.ReadDir(rel)
	if err != nil {
		r.fail(rel, err)
		return
	}
	for _, e := range entries {
		child := path.Join(rel, e.Name())
		if e.IsDir() {
			removeTree(s, child, r)
			continue
		}
		if err := s.Remove(child); err != nil {
			r.fail(child, err)
			continue
		}
		r.removed++
	}

	// The directory itself goes last, and only if it is now empty.
	if err := s.Remove(rel); err != nil {
		r.fail(rel, err)
		return
	}
	r.removed++
}

// describe builds a Target from the model, falling back to the filesystem
// when the path is not in the current results (it may have been created
// since the last scan).
func describe(gen *model.Generation, s *Store, rel string) Target {
	t := Target{Path: rel, Warnings: []string{}}

	fi, statErr := s.Lstat(rel)
	t.Exists = statErr == nil

	if gen != nil {
		if info, ok := gen.Info(rel); ok {
			t.Kind = info.Kind.String()
			t.Size, t.Alloc = info.Size, info.Alloc
			t.Files, t.Dirs = info.Files, info.Dirs
			if info.Kind == model.KindDir {
				t.Dirs++ // the directory itself goes too
			}
			if info.Flags.Has(model.FlagPartial) {
				t.Warnings = append(t.Warnings,
					"parts of this folder could not be read during the last scan, so the size shown is incomplete")
			}
			if info.Flags.Has(model.FlagHardlinkDup) {
				t.Warnings = append(t.Warnings,
					"this is another name for a file counted elsewhere; deleting it frees no space")
			}
			if info.Flags.Has(model.FlagMountPoint) {
				t.Warnings = append(t.Warnings, "this is a mount point and will not be deleted")
			}
		} else {
			t.Warnings = append(t.Warnings, "not present in the last scan; its size is unknown")
		}
	}

	if !t.Exists {
		t.Warnings = append(t.Warnings, "no longer on disk")
		return t
	}
	if t.Kind == "" {
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			t.Kind = "symlink"
		case fi.IsDir():
			t.Kind = "dir"
		case fi.Mode().IsRegular():
			t.Kind = "file"
			t.Size = uint64(max(fi.Size(), 0))
		default:
			t.Kind = "other"
		}
	}
	if fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 && !s.SameDevice(fi) {
		t.Warnings = append(t.Warnings, "this is a mount point of another filesystem and will not be deleted")
	}
	return t
}

// ConfirmWord is what the user types to confirm a destructive batch that has
// no single name to quote.
const ConfirmWord = "delete"

// needsTypedName decides what the user must type to confirm, returning ""
// when a plain acknowledgement is enough (FR-DEL-02).
//
// The rule: one directory is confirmed by typing its own name, because that
// is unambiguous and impossible to do by reflex. Anything else destructive
// enough to warrant friction, a batch containing a directory or touching
// more than a hundred files, is confirmed by typing "delete". A handful of
// individually chosen files needs no typing: the modal already lists exactly
// what will go.
func needsTypedName(targets []Target, mode string) string {
	if mode != "name" || len(targets) == 0 {
		return ""
	}
	if len(targets) == 1 && targets[0].Kind == "dir" {
		return path.Base(targets[0].Path)
	}
	var totalFiles uint32
	hasDir := false
	for _, t := range targets {
		totalFiles += t.Files
		if t.Kind == "dir" {
			hasDir = true
		}
	}
	if hasDir || totalFiles > 100 || len(targets) > 10 {
		return ConfirmWord
	}
	return ""
}
