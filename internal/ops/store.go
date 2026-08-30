// Package ops implements the file operations: delete (with preview,
// confirmation tokens, audit log and model reconciliation) and download
// (single file with range support, and streamed ZIP). Specification §11.
//
// Every filesystem access goes through a Store, which wraps os.Root. On
// Linux that resolves each path with openat2(RESOLVE_BENEATH), so no
// sequence of symlinks, races or crafted names can reach outside the share
// directory (FR-DEL-03).
package ops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

// Errors returned when a request names something that must not be touched.
var (
	// ErrUnsafePath means the path was rejected before it ever reached the
	// filesystem: it was absolute, contained "..", or held a NUL byte.
	ErrUnsafePath = errors.New("unsafe path")
	// ErrIsShareRoot means the request named the share root itself.
	ErrIsShareRoot = errors.New("the share root itself cannot be modified or downloaded")
	// ErrMountPoint means the target is on a different filesystem.
	ErrMountPoint = errors.New("path is a mount point of another filesystem")
	// ErrNotFound means the path does not exist on disk right now.
	ErrNotFound = errors.New("path not found on disk")
	// ErrNotAFile means a regular file was required.
	ErrNotAFile = errors.New("not a regular file")
	// ErrNotADirectory means a directory was required.
	ErrNotADirectory = errors.New("not a directory")
	// ErrReadOnly means the filesystem or its mount will not accept writes.
	ErrReadOnly = errors.New("path is not writable")
)

// CleanRel validates a share-relative path and returns it in canonical form
// (no leading or trailing separator, no empty or "." segments).
//
// It rejects anything that could denote a location outside the share before
// the value is handed to the filesystem at all. os.Root would refuse these
// too; rejecting here gives a precise error and keeps hostile input out of
// the audit log's "resolved path" field.
// Backslash is deliberately *not* rejected: it is an ordinary byte in a Linux
// filename ("AC\DC - Back in Black.flac"), the scanner indexes such files, and
// treating it as a separator would make them permanently undeletable and
// undownloadable. Confinement does not depend on it -- segments are split on
// "/" alone, ".." is rejected by exact match, and every operation runs under
// os.Root, which resolves beneath the share root.
func CleanRel(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: contains a NUL byte", ErrUnsafePath)
	}
	if strings.HasPrefix(p, "/") && strings.Trim(p, "/") != "" {
		// A leading separator is tolerated as a typo, an absolute-looking
		// path with a drive or UNC prefix is not.
		p = strings.TrimLeft(p, "/")
	}
	out := make([]string, 0, 8)
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("%w: contains a %q segment", ErrUnsafePath, "..")
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/"), nil
}

// Store is a share's directory, opened so that every operation is confined
// beneath it.
type Store struct {
	ShareID string
	// Path is the absolute path of the share root as configured.
	Path string

	root *os.Root
	dev  uint64
}

// OpenStore opens a share root for file operations.
func OpenStore(shareID, dir string) (*Store, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open share root %s: %w", dir, err)
	}
	s := &Store{ShareID: shareID, Path: dir, root: root}
	if fi, err := root.Stat("."); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			s.dev = uint64(st.Dev)
		}
	}
	return s, nil
}

// Close releases the root handle.
func (s *Store) Close() error { return s.root.Close() }

// Lstat stats a path without following a final symlink.
func (s *Store) Lstat(rel string) (fs.FileInfo, error) {
	if rel == "" {
		return s.root.Stat(".")
	}
	fi, err := s.root.Lstat(rel)
	if err != nil {
		return nil, translate(err)
	}
	return fi, nil
}

// OpenFile opens a regular file for reading. O_NOFOLLOW closes the window in
// which the entry could be swapped for a symlink between the check and the
// open.
func (s *Store) OpenFile(rel string) (*os.File, error) {
	if rel == "" {
		return nil, ErrIsShareRoot
	}
	f, err := s.root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, translate(err)
	}
	return f, nil
}

// ReadDir lists a directory beneath the share.
func (s *Store) ReadDir(rel string) ([]fs.DirEntry, error) {
	name := rel
	if name == "" {
		name = "."
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, translate(err)
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, translate(err)
	}
	return entries, nil
}

// Remove unlinks a single file, symlink or empty directory.
func (s *Store) Remove(rel string) error {
	if rel == "" {
		return ErrIsShareRoot
	}
	if err := s.root.Remove(rel); err != nil {
		return translate(err)
	}
	return nil
}

// Rename moves a path within the share. Both sides are resolved beneath the
// root, so a rename can never move data out of the share.
func (s *Store) Rename(from, to string) error {
	if from == "" || to == "" {
		return ErrIsShareRoot
	}
	if err := s.root.Rename(from, to); err != nil {
		return translate(err)
	}
	return nil
}

// MkdirAll creates a directory and its parents inside the share.
func (s *Store) MkdirAll(rel string, perm fs.FileMode) error {
	if rel == "" {
		return nil
	}
	if err := s.root.MkdirAll(rel, perm); err != nil {
		return translate(err)
	}
	return nil
}

// SameDevice reports whether a stat result is on the share's own filesystem.
// os.Root deliberately does not stop at mount boundaries, so nested mounts
// are excluded here instead (FR-DEL-03).
func (s *Store) SameDevice(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || s.dev == 0 {
		return true // no device information: do not block on a guess
	}
	return uint64(st.Dev) == s.dev
}

// CheckWritable reports whether the parent directory of rel will accept an
// unlink, which is what actually governs deletion on POSIX. A read-only
// mount, a missing write bit, or the wrong owner all surface here rather
// than half-way through a recursive delete (FR-DEL-01).
func (s *Store) CheckWritable(rel string) error {
	parent := path.Dir(rel)
	if parent == "." || rel == "" {
		parent = ""
	}
	name := parent
	if name == "" {
		name = "."
	}
	f, err := s.root.Open(name)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = f.Close() }()

	// faccessat against the opened directory keeps the check on the same
	// object the delete will use.
	if err := unixAccessW(int(f.Fd())); err != nil {
		return fmt.Errorf("%w: %w", ErrReadOnly, err)
	}
	return nil
}

// rootEscapeMessage is how os.Root reports that a path would leave the root.
// The error is an unexported errors.errorString with no sentinel to match
// on, so the text is matched instead; TestEscapeIsClassifiedAsUnsafe fails
// loudly if a future Go release rewords it, rather than letting traversal
// attempts quietly turn into HTTP 500s.
const rootEscapeMessage = "escapes from"

// isRootEscape reports whether an error is os.Root refusing to leave the share.
func isRootEscape(err error) bool {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Err != nil {
		return strings.Contains(pe.Err.Error(), rootEscapeMessage)
	}
	return strings.Contains(err.Error(), rootEscapeMessage)
}

// translate maps filesystem errors onto the package's sentinels so callers
// (and the API) can classify them without string matching of their own.
func translate(err error) error {
	// Both the sentinel and the original are wrapped: callers classify with
	// errors.Is, while Errno still finds the syscall error underneath.
	switch {
	case err == nil:
		return nil
	case isRootEscape(err):
		return fmt.Errorf("%w: %w", ErrUnsafePath, err)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.EXDEV):
		return fmt.Errorf("%w: %w", ErrUnsafePath, err)
	default:
		return err
	}
}

// Errno extracts the symbolic errno name from an error, for the audit log
// and the API's per-path results.
func Errno(err error) string {
	var e syscall.Errno
	if !errors.As(err, &e) {
		return ""
	}
	switch e {
	case syscall.EACCES:
		return "EACCES"
	case syscall.EPERM:
		return "EPERM"
	case syscall.ENOENT:
		return "ENOENT"
	case syscall.ENOTDIR:
		return "ENOTDIR"
	case syscall.EISDIR:
		return "EISDIR"
	case syscall.ENOTEMPTY:
		return "ENOTEMPTY"
	case syscall.EROFS:
		return "EROFS"
	case syscall.EBUSY:
		return "EBUSY"
	case syscall.EIO:
		return "EIO"
	case syscall.ELOOP:
		return "ELOOP"
	case syscall.EXDEV:
		return "EXDEV"
	case syscall.ESTALE:
		return "ESTALE"
	case syscall.ENOSPC:
		return "ENOSPC"
	case syscall.EMFILE:
		return "EMFILE"
	case 0:
		return ""
	}
	return e.Error()
}
