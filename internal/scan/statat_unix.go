//go:build linux || darwin

package scan

import (
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// lstatEntry stats name inside the directory open on fd, without following a
// final symlink. dir is unused here and carried only for the portable
// fallback in statat_other.go.
//
// Statting relative to the directory descriptor is what makes the crawl cheap:
// os.Lstat on a joined absolute path makes the kernel re-resolve every path
// component for every entry, and allocates both the joined string and an
// os.fileStat wrapper on the way. fstatat resolves one component against an
// already-open directory and fills a stack value.
func lstatEntry(fd int, _, name string) (rawStat, fs.FileMode, bool, error) {
	return statEntryAt(fd, name, unix.AT_SYMLINK_NOFOLLOW)
}

// statEntry is lstatEntry but follows a final symlink, for follow_symlinks.
func statEntry(fd int, _, name string) (rawStat, fs.FileMode, bool, error) {
	return statEntryAt(fd, name, 0)
}

func statEntryAt(fd int, name string, flags int) (rawStat, fs.FileMode, bool, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(fd, name, &st, flags); err != nil {
		return rawStat{}, 0, false, err
	}
	return fromUnixStat(&st), modeFromRaw(uint32(st.Mode)), true, nil
}

// openEntry opens name inside the directory open on fd for reading, without
// following a final symlink, for the media-duration probe. O_NONBLOCK guards
// against the entry having been swapped for a FIFO between the lstat and the
// open, which would otherwise block the worker indefinitely; the flag has no
// effect on the regular file the scanner expects.
func openEntry(fd int, _, name string) (*os.File, error) {
	pfd, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(pfd), name), nil
}

// fromUnixStat converts the x/sys/unix stat used by the fd-relative path.
// x/sys/unix normalises the field names across platforms, but not their
// widths, so every conversion is explicit: darwin's Dev is int32 and its Mode
// and Nlink are uint16, and 32-bit Linux narrows several fields again.
func fromUnixStat(st *unix.Stat_t) rawStat {
	return rawStat{
		Dev:    uint64(st.Dev),
		Ino:    uint64(st.Ino),
		Nlink:  uint64(st.Nlink),
		Size:   int64(st.Size),
		Blocks: int64(st.Blocks),
		Mode:   uint32(st.Mode),
		UID:    st.Uid,
		GID:    st.Gid,
		Mtime:  int64(st.Mtim.Sec),
	}
}

// modeFromRaw converts a raw st_mode into the portable fs.FileMode bits the
// scanner classifies on. It mirrors what os.Lstat does internally, which is
// the code path this replaces.
func modeFromRaw(m uint32) fs.FileMode {
	mode := fs.FileMode(m & 0o777)
	switch m & unix.S_IFMT {
	case unix.S_IFBLK:
		mode |= fs.ModeDevice
	case unix.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFSOCK:
		mode |= fs.ModeSocket
	case unix.S_IFREG:
		// The zero value already means "regular file".
	}
	if m&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if m&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if m&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}
