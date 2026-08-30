package model

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// Generation is the immutable result of one completed scan, plus the
// bookkeeping needed to apply deletes to it (§8.2). All read methods take a
// read lock, so queries never block each other and only a delete excludes
// them.
type Generation struct {
	meta  GenerationMeta
	basis Basis

	mu          sync.RWMutex
	nodes       []Node
	names       []byte
	exts        []ExtStat
	top         []uint32
	errs        []ScanError
	errsDropped uint64
	stats       Stats
	scannedAt   time.Time
	mutation    uint64
	topN        int
}

// Meta returns the scan metadata.
func (g *Generation) Meta() GenerationMeta { return g.meta }

// ID returns the generation id.
func (g *Generation) ID() string { return g.meta.ID }

// Basis returns the size basis the generation was sorted and indexed with.
func (g *Generation) Basis() Basis { return g.basis }

// ScannedAt returns the completion time of the scan.
func (g *Generation) ScannedAt() time.Time { return g.scannedAt }

// SetDuration records how long the scan took. It is only known after the
// crawl returns, so the builder cannot set it.
func (g *Generation) SetDuration(d time.Duration) {
	g.mu.Lock()
	g.meta.Duration = d
	g.mu.Unlock()
}

// Stats returns the whole-share totals.
func (g *Generation) Stats() Stats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.stats
}

// Mutation returns a counter that changes whenever the generation is
// modified after finalization; it is part of the ETag (§9.1).
func (g *Generation) Mutation() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mutation
}

// ETag returns a cache validator for the current content.
func (g *Generation) ETag() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return fmt.Sprintf(`"%s-%s-%d"`, g.meta.ShareID, g.meta.ID, g.mutation)
}

// NodeCount returns the number of nodes in the arena, tombstones included.
func (g *Generation) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// Errors returns up to limit recorded scan errors and the total count.
func (g *Generation) Errors(offset, limit int) (errs []ScanError, total int, dropped uint64) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	total = len(g.errs)
	if offset >= total {
		return nil, total, g.errsDropped
	}
	end := min(offset+limit, total)
	out := make([]ScanError, end-offset)
	copy(out, g.errs[offset:end])
	return out, total, g.errsDropped
}

// rawName returns the raw bytes of a node's name. Caller must hold the lock.
func (g *Generation) rawName(idx uint32) []byte {
	n := &g.nodes[idx]
	return g.names[n.NameOff : n.NameOff+uint32(n.NameLen)]
}

// pathOf builds the share-relative path of a node. Caller must hold the lock.
func (g *Generation) pathOf(idx uint32) string {
	if idx == 0 {
		return ""
	}
	var parts []string
	for i := idx; i != 0 && i != NoIndex; i = g.nodes[i].Parent {
		parts = append(parts, string(g.rawName(i)))
	}
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, "/")
}

// Resolve maps a share-relative path to a node index. The empty path is the
// share root. Paths are matched byte for byte, so names that are not valid
// UTF-8 resolve as long as the client sends the same bytes back.
func (g *Generation) Resolve(path string) (uint32, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.resolve(path)
}

func (g *Generation) resolve(path string) (uint32, bool) {
	path = strings.Trim(path, "/")
	if path == "" {
		return 0, true
	}
	cur := uint32(0)
	for len(path) > 0 {
		var seg string
		if i := strings.IndexByte(path, '/'); i >= 0 {
			seg, path = path[:i], path[i+1:]
		} else {
			seg, path = path, ""
		}
		if seg == "" || seg == "." {
			continue
		}
		n := &g.nodes[cur]
		found := false
		for i := n.FirstChild; i < n.FirstChild+n.ChildCount; i++ {
			if g.nodes[i].Kind == KindDeleted {
				continue
			}
			if string(g.rawName(i)) == seg {
				cur, found = i, true
				break
			}
		}
		if !found {
			return 0, false
		}
	}
	return cur, true
}

// info builds the API view of a node. Caller must hold the lock.
func (g *Generation) info(idx uint32) NodeInfo {
	n := &g.nodes[idx]
	raw := g.rawName(idx)
	name, valid := safeName(raw)
	rawCopy := make([]byte, len(raw))
	copy(rawCopy, raw)
	ni := NodeInfo{
		Index:      idx,
		Name:       name,
		NameRaw:    rawCopy,
		NameValid:  valid,
		Path:       g.pathOf(idx),
		Kind:       n.Kind,
		Flags:      n.Flags,
		Size:       n.Size,
		Alloc:      n.Alloc,
		Mtime:      time.Unix(n.Mtime, 0).UTC(),
		Mode:       n.Mode,
		UID:        n.UID,
		GID:        n.GID,
		Files:      n.Files,
		Dirs:       n.Dirs,
		ChildCount: n.ChildCount,
	}
	if n.Kind != KindDir {
		ni.Ext = extensionOf(name)
	}
	return ni
}

// Info returns the API view of the node at a path.
func (g *Generation) Info(path string) (NodeInfo, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	idx, ok := g.resolve(path)
	if !ok {
		return NodeInfo{}, false
	}
	return g.info(idx), true
}

// Ancestors returns the breadcrumb from the share root down to (excluding)
// the node itself.
func (g *Generation) Ancestors(path string) ([]NodeInfo, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	idx, ok := g.resolve(path)
	if !ok {
		return nil, false
	}
	var chain []uint32
	for i := g.nodes[idx].Parent; i != NoIndex; i = g.nodes[i].Parent {
		chain = append(chain, i)
		if i == 0 {
			break
		}
	}
	out := make([]NodeInfo, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		out = append(out, g.info(chain[i]))
	}
	return out, true
}

// Extensions returns the share-wide extension table.
func (g *Generation) Extensions() []ExtStat {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]ExtStat, len(g.exts))
	copy(out, g.exts)
	return out
}

// ExtensionsUnder computes the extension table for one subtree. The
// share-wide table is precomputed; subtrees are walked on demand.
func (g *Generation) ExtensionsUnder(path string) ([]ExtStat, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	root, ok := g.resolve(path)
	if !ok {
		return nil, false
	}
	if root == 0 {
		out := make([]ExtStat, len(g.exts))
		copy(out, g.exts)
		return out, true
	}
	byExt := map[string]*ExtStat{}
	g.walk(root, func(idx uint32) bool {
		n := &g.nodes[idx]
		if n.Kind == KindDir || n.Kind == KindDeleted {
			return true
		}
		ext := extensionOf(string(g.rawName(idx)))
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
		return true
	})
	out := make([]ExtStat, 0, len(byExt))
	for _, e := range byExt {
		out = append(out, *e)
	}
	sortExtStats(out)
	return out, true
}

// walk visits root and every descendant in depth-first order. The callback
// returns false to skip a directory's children. Caller must hold the lock.
func (g *Generation) walk(root uint32, fn func(uint32) bool) {
	stack := []uint32{root}
	for len(stack) > 0 {
		idx := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !fn(idx) {
			continue
		}
		n := &g.nodes[idx]
		for i := n.FirstChild; i < n.FirstChild+n.ChildCount; i++ {
			if g.nodes[i].Kind != KindDeleted {
				stack = append(stack, i)
			}
		}
	}
}

func sortExtStats(s []ExtStat) {
	slicesSortFunc(s, func(a, b ExtStat) int {
		switch {
		case a.Size > b.Size:
			return -1
		case a.Size < b.Size:
			return 1
		}
		return strings.Compare(a.Ext, b.Ext)
	})
}

// Remove tombstones a subtree and subtracts its totals from every ancestor,
// keeping the live model consistent with the filesystem after a delete
// (§8.2). It returns the apparent and allocated bytes reclaimed.
func (g *Generation) Remove(path string) (freed, freedAlloc uint64, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	idx, found := g.resolve(path)
	if !found || idx == 0 {
		return 0, 0, false
	}
	n := &g.nodes[idx]
	freed, freedAlloc = n.Size, n.Alloc
	var files, dirs uint32
	if n.Kind == KindDir {
		files, dirs = n.Files, n.Dirs+1
	} else {
		files = 1
	}
	if n.Flags.Has(FlagHardlinkDup) {
		freed, freedAlloc = 0, 0
	}

	for p := n.Parent; p != NoIndex; p = g.nodes[p].Parent {
		a := &g.nodes[p]
		a.Size -= min(a.Size, freed)
		a.Alloc -= min(a.Alloc, freedAlloc)
		a.Files -= min(a.Files, files)
		a.Dirs -= min(a.Dirs, dirs)
	}
	// Tombstone the subtree so listings, resolution and search skip it.
	g.walk(idx, func(i uint32) bool {
		g.nodes[i].Kind = KindDeleted
		return true
	})
	g.stats.Size -= min(g.stats.Size, freed)
	g.stats.Alloc -= min(g.stats.Alloc, freedAlloc)
	g.stats.Files -= min(g.stats.Files, uint64(files))
	if dirs > 0 {
		g.stats.Dirs -= min(g.stats.Dirs, uint64(dirs))
	}
	g.mutation++
	g.top = nil // rebuilt lazily by Top; the deleted node may have been in it
	return freed, freedAlloc, true
}

// ErrSpliceTooLarge means a subtree rescan would push the arena past its
// uint32 addressing limit. Splicing only ever appends — the replaced nodes
// become tombstones — so an arena that has absorbed many subtree rescans
// grows until the next full scan rebuilds it from scratch.
var ErrSpliceTooLarge = errors.New("arena is full; run a full scan to compact it")

// ErrSpliceTarget means the path does not exist in this generation.
var ErrSpliceTarget = errors.New("splice target not found")

// Splice replaces the subtree at path with the contents of sub, which must
// be a generation rooted at that same directory. It is how a subtree rescan
// (FR-SCAN-06) updates the live model: the new nodes are appended to the
// arena, the old ones are tombstoned, and the aggregates of every ancestor
// are adjusted by the difference.
func (g *Generation) Splice(path string, sub *Generation) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	sub.mu.RLock()
	defer sub.mu.RUnlock()

	idx, ok := g.resolve(path)
	if !ok {
		return fmt.Errorf("%w: %s", ErrSpliceTarget, path)
	}
	if len(sub.nodes) == 0 {
		return fmt.Errorf("%w: rescan produced no nodes", ErrSpliceTarget)
	}
	if len(g.nodes)+len(sub.nodes) > MaxNodes || len(g.names)+len(sub.names) > MaxNameBytes {
		return ErrSpliceTooLarge
	}
	target := &g.nodes[idx]
	oldSize, oldAlloc := target.Size, target.Alloc
	oldFiles, oldDirs := target.Files, target.Dirs

	// Tombstone whatever is currently below the target.
	for i := target.FirstChild; i < target.FirstChild+target.ChildCount; i++ {
		g.walk(i, func(j uint32) bool {
			g.nodes[j].Kind = KindDeleted
			return true
		})
	}

	// Append sub's nodes (all but its root) with rebased indices.
	nodeBase := uint32(len(g.nodes)) //nolint:gosec // bounded by MaxNodes above
	nameBase := uint32(len(g.names)) //nolint:gosec // bounded by MaxNameBytes above
	g.names = append(g.names, sub.names...)
	for j := 1; j < len(sub.nodes); j++ {
		n := sub.nodes[j]
		if n.Parent == 0 {
			n.Parent = idx
		} else {
			n.Parent = nodeBase + n.Parent - 1
		}
		if n.ChildCount > 0 {
			n.FirstChild = nodeBase + n.FirstChild - 1
		}
		n.NameOff += nameBase
		g.nodes = append(g.nodes, n)
	}

	subRoot := &sub.nodes[0]
	target = &g.nodes[idx] // the append above may have moved the arena
	if subRoot.ChildCount > 0 {
		target.FirstChild = nodeBase + subRoot.FirstChild - 1
	} else {
		target.FirstChild = 0
	}
	target.ChildCount = subRoot.ChildCount
	target.Size, target.Alloc = subRoot.Size, subRoot.Alloc
	target.Files, target.Dirs = subRoot.Files, subRoot.Dirs
	target.Mtime, target.Mode, target.UID, target.GID = subRoot.Mtime, subRoot.Mode, subRoot.UID, subRoot.GID
	target.Flags = subRoot.Flags
	target.Kind = subRoot.Kind

	applyDelta(target.Size, oldSize, func(d uint64, add bool) { adjust(g, idx, d, add, fieldSize) })
	applyDelta(target.Alloc, oldAlloc, func(d uint64, add bool) { adjust(g, idx, d, add, fieldAlloc) })
	applyDelta(uint64(target.Files), uint64(oldFiles), func(d uint64, add bool) { adjust(g, idx, d, add, fieldFiles) })
	applyDelta(uint64(target.Dirs), uint64(oldDirs), func(d uint64, add bool) { adjust(g, idx, d, add, fieldDirs) })

	g.errs = append(g.errs, sub.errs...)
	if len(g.errs) > MaxErrors {
		g.errsDropped += uint64(len(g.errs) - MaxErrors) //nolint:gosec // len > MaxErrors here
		g.errs = g.errs[:MaxErrors]
	}
	g.mutation++
	g.sortChildrenUnder(idx)
	g.recomputeStats()
	g.buildIndexes(g.topN)
	return nil
}

type aggField int

const (
	fieldSize aggField = iota
	fieldAlloc
	fieldFiles
	fieldDirs
)

func applyDelta(newV, oldV uint64, fn func(uint64, bool)) {
	if newV >= oldV {
		fn(newV-oldV, true)
	} else {
		fn(oldV-newV, false)
	}
}

// adjust walks from a node's parent to the root applying a delta.
func adjust(g *Generation, from uint32, delta uint64, add bool, f aggField) {
	if delta == 0 {
		return
	}
	for p := g.nodes[from].Parent; p != NoIndex; p = g.nodes[p].Parent {
		n := &g.nodes[p]
		switch f {
		case fieldSize:
			n.Size = addSub(n.Size, delta, add)
		case fieldAlloc:
			n.Alloc = addSub(n.Alloc, delta, add)
		case fieldFiles:
			n.Files = clampU32(addSub(uint64(n.Files), delta, add))
		case fieldDirs:
			n.Dirs = clampU32(addSub(uint64(n.Dirs), delta, add))
		}
	}
}

// clampU32 narrows a counter that cannot legitimately exceed the node
// count, saturating rather than wrapping if it ever did.
func clampU32(v uint64) uint32 {
	if v > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}

func addSub(v, d uint64, add bool) uint64 {
	if add {
		return v + d
	}
	return v - min(v, d)
}

// sortChildrenUnder re-sorts every directory in a subtree after a splice.
func (g *Generation) sortChildrenUnder(root uint32) {
	stack := []uint32{root}
	for len(stack) > 0 {
		idx := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := g.nodes[idx]
		if n.ChildCount >= 2 {
			g.sortRange(n.FirstChild, n.FirstChild+n.ChildCount)
		}
		n = g.nodes[idx]
		for i := n.FirstChild; i < n.FirstChild+n.ChildCount; i++ {
			if g.nodes[i].Kind == KindDir {
				stack = append(stack, i)
			}
		}
	}
}

// recomputeStats refreshes the whole-share totals from the root node.
func (g *Generation) recomputeStats() {
	var st Stats
	for i := range g.nodes {
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
			continue
		}
		if n.Flags.Has(FlagHardlinkDup) {
			st.HardlinkDups++
		}
	}
	if st.Dirs > 0 {
		st.Dirs--
	}
	st.Size, st.Alloc = g.nodes[0].Size, g.nodes[0].Alloc
	st.Errors = uint64(len(g.errs)) + g.errsDropped
	st.Excluded = g.stats.Excluded
	st.MaxDepth = g.stats.MaxDepth
	g.stats = st
}

// Raw is the serialisable form of a generation. It exposes the internal
// arrays so the snapshot package can encode them without copying; callers
// must treat every slice as read-only.
type Raw struct {
	Meta          GenerationMeta
	Basis         Basis
	Nodes         []Node
	Names         []byte
	Exts          []ExtStat
	Top           []uint32
	Errors        []ScanError
	ErrorsDropped uint64
	Stats         Stats
	TopN          int
}

// Export returns the generation's arrays for serialisation.
func (g *Generation) Export() Raw {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return Raw{
		Meta:          g.meta,
		Basis:         g.basis,
		Nodes:         g.nodes,
		Names:         g.names,
		Exts:          g.exts,
		Top:           g.top,
		Errors:        g.errs,
		ErrorsDropped: g.errsDropped,
		Stats:         g.stats,
		TopN:          g.topN,
	}
}

// FromRaw rebuilds a generation from deserialised arrays. The arrays are
// adopted, not copied.
func FromRaw(r Raw) *Generation {
	return &Generation{
		meta:        r.Meta,
		basis:       r.Basis,
		nodes:       r.Nodes,
		names:       r.Names,
		exts:        r.Exts,
		top:         r.Top,
		errs:        r.Errors,
		errsDropped: r.ErrorsDropped,
		stats:       r.Stats,
		scannedAt:   r.Meta.ScannedAt,
		topN:        r.TopN,
	}
}
