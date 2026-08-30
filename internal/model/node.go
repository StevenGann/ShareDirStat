// Package model holds the in-memory tree for a scanned share (specification §8.1).
//
// A Generation is a GC-friendly arena: nodes live in one contiguous slice
// addressed by uint32 index, names live in one shared byte arena, and the
// children of a directory occupy a contiguous index range sorted by size.
// There are no per-node pointers, so the garbage collector has almost
// nothing to trace even with tens of millions of nodes.
package model

import (
	"time"
	"unicode/utf8"
)

// NoIndex marks the absent parent of the root node.
const NoIndex = ^uint32(0)

// Kind is the type of a filesystem entry.
type Kind uint8

// Node kinds. KindDeleted is a tombstone left behind by Remove.
const (
	KindDir Kind = iota
	KindFile
	KindSymlink
	KindOther
	KindDeleted
)

// String returns the API representation of the kind.
func (k Kind) String() string {
	switch k {
	case KindDir:
		return "dir"
	case KindFile:
		return "file"
	case KindSymlink:
		return "symlink"
	case KindOther:
		return "other"
	case KindDeleted:
		return "deleted"
	}
	return "unknown"
}

// Flags is a bit set of per-node properties.
type Flags uint8

// Node flags.
const (
	// FlagPartial marks a directory where at least one entry could not be read.
	FlagPartial Flags = 1 << iota
	// FlagMountPoint marks a directory on a different filesystem that was not descended into.
	FlagMountPoint
	// FlagHardlinkDup marks an additional link to an inode already counted in this share.
	FlagHardlinkDup
	// FlagExcluded marks a directory that had entries skipped by an exclusion pattern.
	FlagExcluded
	// FlagUnscanned marks a directory that was never read (mount point, node limit, cancellation).
	FlagUnscanned
)

// Has reports whether all bits of f are set.
func (fl Flags) Has(f Flags) bool { return fl&f == f }

// Basis selects which of the two recorded sizes is authoritative for
// sorting and display (§7.6).
type Basis uint8

// Size bases.
const (
	// BasisApparent uses st_size, the logical length of the file.
	BasisApparent Basis = iota
	// BasisAllocated uses st_blocks*512, the space actually occupied on disk.
	BasisAllocated
)

// ParseBasis converts the configuration/query string form.
func ParseBasis(s string) (Basis, bool) {
	switch s {
	case "apparent", "":
		return BasisApparent, true
	case "allocated":
		return BasisAllocated, true
	}
	return BasisApparent, false
}

// String returns the API representation.
func (b Basis) String() string {
	if b == BasisAllocated {
		return "allocated"
	}
	return "apparent"
}

// Node is one filesystem entry. Field order is chosen to pack into 64 bytes
// on 64-bit platforms; keep it that way (see NodeSize in the tests).
type Node struct {
	// Size is the apparent size for files, the aggregate apparent size for directories.
	Size uint64
	// Alloc is the allocated size for files, the aggregate allocated size for directories.
	Alloc uint64
	// Mtime is the modification time in Unix seconds.
	Mtime int64

	// Parent is the index of the containing directory, NoIndex for the root.
	Parent uint32
	// NameOff is the offset of the name in the generation's name arena.
	NameOff uint32
	// FirstChild is the index of the first child; children occupy
	// [FirstChild, FirstChild+ChildCount). Directories only.
	FirstChild uint32
	// ChildCount is the number of direct children.
	ChildCount uint32
	// Files is the aggregate number of non-directory entries beneath a directory.
	Files uint32
	// Dirs is the aggregate number of directories beneath a directory.
	Dirs uint32
	// UID and GID are the owning user and group ids.
	UID uint32
	GID uint32

	// NameLen is the length of the name in bytes.
	NameLen uint16
	// Mode holds the permission bits and setuid/setgid/sticky bits.
	Mode uint16

	// Kind is the entry type.
	Kind Kind
	// Flags holds the per-node property bits.
	Flags Flags
}

// Sized returns the size of the node in the given basis.
func (n *Node) Sized(b Basis) uint64 {
	if b == BasisAllocated {
		return n.Alloc
	}
	return n.Size
}

// IsDir reports whether the node is a directory.
func (n *Node) IsDir() bool { return n.Kind == KindDir }

// Entry is one filesystem entry handed to a Builder by the scanner.
type Entry struct {
	Name  string
	Kind  Kind
	Flags Flags
	Size  uint64
	Alloc uint64
	Mtime int64
	Mode  uint16
	UID   uint32
	GID   uint32
}

// NodeInfo is the resolved, API-facing view of a node.
type NodeInfo struct {
	Index      uint32
	Name       string
	NameRaw    []byte
	NameValid  bool
	Path       string
	Kind       Kind
	Flags      Flags
	Size       uint64
	Alloc      uint64
	Mtime      time.Time
	Mode       uint16
	UID        uint32
	GID        uint32
	Files      uint32
	Dirs       uint32
	ChildCount uint32
	Ext        string
}

// Stats summarises a whole generation.
type Stats struct {
	Files        uint64 `json:"files"`
	Dirs         uint64 `json:"dirs"`
	Symlinks     uint64 `json:"symlinks"`
	Others       uint64 `json:"others"`
	Size         uint64 `json:"size"`
	Alloc        uint64 `json:"alloc"`
	HardlinkDups uint64 `json:"hardlink_dups"`
	Excluded     uint64 `json:"excluded"`
	Errors       uint64 `json:"errors"`
	MaxDepth     uint32 `json:"max_depth"`
}

// ExtStat is one row of the extension table (§8.1).
type ExtStat struct {
	Ext   string `json:"ext"`
	Files uint64 `json:"files"`
	Size  uint64 `json:"size"`
	Alloc uint64 `json:"alloc"`
}

// ScanError records one failure encountered during a crawl (FR-SCAN-08).
type ScanError struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	Errno   string `json:"errno"`
	Message string `json:"message"`
}

// safeName renders a raw filename for JSON: valid UTF-8 is returned as-is,
// invalid byte sequences are replaced so the string can be marshalled, and
// the caller also receives the raw bytes for addressing (FR-SCAN-20).
func safeName(raw []byte) (string, bool) {
	if utf8.Valid(raw) {
		return string(raw), true
	}
	return string([]rune(string(raw))), false
}

// extensionOf returns the lower-cased extension of a file name: the text
// after the final dot, at most 16 bytes, empty when there is none. A name
// that begins with a dot and contains no other dot has no extension.
func extensionOf(name string) string {
	dot := -1
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			dot = i
			break
		}
	}
	if dot <= 0 || dot == len(name)-1 {
		return ""
	}
	ext := name[dot+1:]
	if len(ext) > 16 {
		return ""
	}
	for i := 0; i < len(ext); i++ {
		c := ext[i]
		if c >= 'A' && c <= 'Z' {
			return lowerASCII(ext)
		}
		if c < 0x20 || c == '/' {
			return ""
		}
	}
	return ext
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
