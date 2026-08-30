package model

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"
)

// ErrTooManyNodes is returned when a scan exceeds scan.max_nodes_per_share.
var ErrTooManyNodes = errors.New("share exceeds the configured node limit")

// ErrArenaFull is returned when a share would need more nodes or more name
// bytes than a uint32 index can address. Both are hard limits of the arena
// layout, independent of scan.max_nodes_per_share.
var ErrArenaFull = errors.New("share is too large for the in-memory arena")

// MaxNodes and MaxNameBytes are the arena's addressing limits.
const (
	MaxNodes     = math.MaxUint32 - 1
	MaxNameBytes = math.MaxUint32 - 1
)

// Builder accumulates nodes during a parallel crawl and produces a finalized
// Generation. It is safe for concurrent use by the scanner's workers.
//
// Each directory's children are appended as one batch, which is what makes
// them contiguous in the arena. Because a directory is always appended
// before its own children, a node's parent index is always smaller than its
// own — the property the bottom-up aggregation pass relies on.
type Builder struct {
	shareID  string
	rootPath string
	basis    Basis
	maxNodes int

	mu    sync.Mutex
	nodes []Node
	names []byte
	errs  []ScanError
	// errsDropped counts errors beyond the retention cap.
	errsDropped uint64
	excluded    uint64
	overflow    bool
}

// MaxErrors is the number of scan errors retained in full (FR-SCAN-08).
const MaxErrors = 10000

// NewBuilder starts a new generation for a share. maxNodes <= 0 disables the limit.
func NewBuilder(shareID, rootPath string, basis Basis, maxNodes int, sizeHint int) *Builder {
	if sizeHint < 1024 {
		sizeHint = 1024
	}
	b := &Builder{
		shareID:  shareID,
		rootPath: rootPath,
		basis:    basis,
		maxNodes: maxNodes,
		nodes:    make([]Node, 0, sizeHint),
		names:    make([]byte, 0, sizeHint*16),
	}
	// The root node carries the share's display root; its name is empty so
	// that share-relative paths never begin with a separator.
	b.nodes = append(b.nodes, Node{Parent: NoIndex, Kind: KindDir})
	return b
}

// Root returns the index of the root node (always 0).
func (b *Builder) Root() uint32 { return 0 }

// AddChildren appends the entries of one directory as a contiguous block and
// links them to their parent. It returns the index of each appended child in
// the same order as entries.
//
// The returned slice is owned by the caller. A nil slice with a nil error
// means the directory was empty.
func (b *Builder) AddChildren(parent uint32, entries []Entry, out []uint32) ([]uint32, error) {
	if len(entries) == 0 {
		return out[:0], nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.maxNodes > 0 && len(b.nodes)+len(entries) > b.maxNodes {
		b.overflow = true
		return nil, fmt.Errorf("%w (%d nodes)", ErrTooManyNodes, b.maxNodes)
	}
	// Node indices and name offsets are uint32; overflowing either would
	// silently corrupt the tree, so refuse instead.
	if len(b.nodes)+len(entries) > MaxNodes {
		b.overflow = true
		return nil, fmt.Errorf("%w: more than %d nodes", ErrArenaFull, MaxNodes)
	}
	var nameBytes int
	for i := range entries {
		nameBytes += len(entries[i].Name)
	}
	if len(b.names)+nameBytes > MaxNameBytes {
		b.overflow = true
		return nil, fmt.Errorf("%w: file names exceed %d bytes in total", ErrArenaFull, MaxNameBytes)
	}

	first := uint32(len(b.nodes)) //nolint:gosec // bounded by MaxNodes above
	out = out[:0]
	for i := range entries {
		e := &entries[i]
		off := uint32(len(b.names)) //nolint:gosec // bounded by MaxNameBytes above
		b.names = append(b.names, e.Name...)
		b.nodes = append(b.nodes, Node{
			Size:    e.Size,
			Alloc:   e.Alloc,
			Mtime:   e.Mtime,
			Parent:  parent,
			NameOff: off,
			UID:     e.UID,
			GID:     e.GID,
			NameLen: uint16(min(len(e.Name), 1<<16-1)),
			Mode:    e.Mode,
			Kind:    e.Kind,
			Flags:   e.Flags,
		})
		out = append(out, first+uint32(i))
	}
	p := &b.nodes[parent]
	p.FirstChild = first
	p.ChildCount = uint32(len(entries)) //nolint:gosec // bounded by MaxNodes above
	return out, nil
}

// SetFlags ORs flags into a node.
func (b *Builder) SetFlags(idx uint32, f Flags) {
	b.mu.Lock()
	b.nodes[idx].Flags |= f
	b.mu.Unlock()
}

// SetRootMeta records the root directory's own stat data.
func (b *Builder) SetRootMeta(e Entry) {
	b.mu.Lock()
	n := &b.nodes[0]
	n.Mtime, n.Mode, n.UID, n.GID, n.Alloc = e.Mtime, e.Mode, e.UID, e.GID, e.Alloc
	b.mu.Unlock()
}

// AddError records a scan failure. Errors past MaxErrors are counted only.
func (b *Builder) AddError(e ScanError) {
	b.mu.Lock()
	if len(b.errs) < MaxErrors {
		b.errs = append(b.errs, e)
	} else {
		b.errsDropped++
	}
	b.mu.Unlock()
}

// AddExcluded counts entries skipped by an exclusion pattern.
func (b *Builder) AddExcluded(n uint64) {
	b.mu.Lock()
	b.excluded += n
	b.mu.Unlock()
}

// Len returns the number of nodes appended so far (progress reporting).
func (b *Builder) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.nodes)
}

// GenerationMeta describes the scan that produced a generation.
type GenerationMeta struct {
	ID        string        `json:"id"`
	ShareID   string        `json:"share_id"`
	RootPath  string        `json:"root_path"`
	Basis     string        `json:"basis"`
	ScannedAt time.Time     `json:"scanned_at"`
	Duration  time.Duration `json:"duration"`
	Trigger   string        `json:"trigger"`
	ScanID    string        `json:"scan_id"`
}

// Finalize computes aggregates, sorts children by size, builds the derived
// indexes and returns the immutable generation. The Builder must not be used
// afterwards.
func (b *Builder) Finalize(meta GenerationMeta, topN int) *Generation {
	b.mu.Lock()
	nodes, names, errs := b.nodes, b.names, b.errs
	dropped, excluded := b.errsDropped, b.excluded
	b.nodes, b.names, b.errs = nil, nil, nil
	b.mu.Unlock()

	meta.ShareID = b.shareID
	meta.RootPath = b.rootPath
	meta.Basis = b.basis.String()

	g := &Generation{
		meta:        meta,
		basis:       b.basis,
		nodes:       nodes,
		names:       names,
		errs:        errs,
		errsDropped: dropped,
		scannedAt:   meta.ScannedAt,
		topN:        topN,
	}
	g.stats.Excluded = excluded
	g.aggregate()
	g.sortChildren()
	g.buildIndexes(topN)
	return g
}

// aggregate walks the arena backwards, folding each node's totals into its
// parent. Children always have a higher index than their parent, so one
// reverse pass is enough (§7.2).
func (g *Generation) aggregate() {
	var st Stats
	depth := make([]uint32, len(g.nodes))
	for i := len(g.nodes) - 1; i >= 0; i-- {
		n := &g.nodes[i]
		switch n.Kind {
		case KindDir:
			st.Dirs++
		case KindSymlink:
			st.Symlinks++
			st.Files++
		case KindFile:
			st.Files++
		case KindOther:
			st.Others++
			st.Files++
		case KindDeleted:
		}
		if n.Flags.Has(FlagHardlinkDup) {
			st.HardlinkDups++
		}
		if n.Parent == NoIndex {
			continue
		}
		p := &g.nodes[n.Parent]
		// Hard-link duplicates keep their real size for display but must not
		// be counted twice in any aggregate (FR-SCAN-17).
		if !n.Flags.Has(FlagHardlinkDup) {
			p.Size += n.Size
			p.Alloc += n.Alloc
		}
		if n.Kind == KindDir {
			p.Dirs += n.Dirs + 1
			p.Files += n.Files
			if n.Flags.Has(FlagPartial) {
				p.Flags |= FlagPartial
			}
		} else {
			p.Files++
		}
	}
	// Depth is a forward pass: parents precede children.
	for i := range g.nodes {
		n := &g.nodes[i]
		if n.Parent != NoIndex {
			depth[i] = depth[n.Parent] + 1
			if depth[i] > st.MaxDepth {
				st.MaxDepth = depth[i]
			}
		}
	}
	st.Dirs-- // the root is not "beneath" anything
	if len(g.nodes) > 0 {
		st.Size, st.Alloc = g.nodes[0].Size, g.nodes[0].Alloc
	}
	st.Errors = uint64(len(g.errs)) + g.errsDropped
	st.Excluded = g.stats.Excluded
	g.stats = st
}

// sortChildren orders every directory's children by size descending so that
// "largest first" listings are a slice, not a sort. Sorting moves nodes, so
// the parent index of every grandchild has to be repaired afterwards.
func (g *Generation) sortChildren() {
	for i := range g.nodes {
		first, count := g.nodes[i].FirstChild, g.nodes[i].ChildCount
		if count < 2 {
			continue
		}
		g.sortRange(first, first+count)
	}
}

// sortRange sorts one directory's contiguous child block by size descending
// and repairs the parent index of every grandchild the sort moved.
func (g *Generation) sortRange(lo, hi uint32) {
	slices.SortFunc(g.nodes[lo:hi], func(a, b Node) int {
		as, bs := a.Sized(g.basis), b.Sized(g.basis)
		switch {
		case as > bs:
			return -1
		case as < bs:
			return 1
		}
		// Deterministic tie-break so snapshots round-trip byte for byte.
		an := g.names[a.NameOff : a.NameOff+uint32(a.NameLen)]
		bn := g.names[b.NameOff : b.NameOff+uint32(b.NameLen)]
		return slices.Compare(an, bn)
	})
	for j := lo; j < hi; j++ {
		c := &g.nodes[j]
		for k := c.FirstChild; k < c.FirstChild+c.ChildCount; k++ {
			g.nodes[k].Parent = j
		}
	}
}

// buildIndexes computes the extension table and the largest-files list.
func (g *Generation) buildIndexes(topN int) {
	byExt := make(map[string]*ExtStat, 64)
	top := make([]uint32, 0, topN+1)
	worst := uint64(0)

	for i := range g.nodes {
		n := &g.nodes[i]
		if n.Kind != KindFile && n.Kind != KindSymlink && n.Kind != KindOther {
			continue
		}
		name := g.rawName(uint32(i))
		ext := extensionOf(string(name))
		e := byExt[ext]
		if e == nil {
			e = &ExtStat{Ext: ext}
			byExt[ext] = e
		}
		e.Files++
		if !n.Flags.Has(FlagHardlinkDup) {
			e.Size += n.Size
			e.Alloc += n.Alloc
		}
		if topN > 0 {
			s := n.Sized(g.basis)
			if len(top) < topN || s > worst {
				top = insertTop(g, top, uint32(i), topN)
				worst = g.nodes[top[len(top)-1]].Sized(g.basis)
			}
		}
	}

	g.exts = make([]ExtStat, 0, len(byExt))
	for _, e := range byExt {
		g.exts = append(g.exts, *e)
	}
	slices.SortFunc(g.exts, func(a, b ExtStat) int {
		switch {
		case a.Size > b.Size:
			return -1
		case a.Size < b.Size:
			return 1
		}
		return slices.Compare([]byte(a.Ext), []byte(b.Ext))
	})
	g.top = top
}

// insertTop keeps top sorted by size descending, bounded to n entries.
func insertTop(g *Generation, top []uint32, idx uint32, n int) []uint32 {
	s := g.nodes[idx].Sized(g.basis)
	pos, _ := slices.BinarySearchFunc(top, s, func(e uint32, target uint64) int {
		es := g.nodes[e].Sized(g.basis)
		switch {
		case es > target:
			return -1
		case es < target:
			return 1
		}
		return 0
	})
	top = slices.Insert(top, pos, idx)
	if len(top) > n {
		top = top[:n]
	}
	return top
}
