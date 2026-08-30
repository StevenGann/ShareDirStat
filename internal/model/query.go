package model

import (
	"slices"
	"strings"
	"time"
)

// SortField selects the ordering of a child listing.
type SortField string

// Sort fields accepted by the tree endpoint.
const (
	SortSize  SortField = "size"
	SortName  SortField = "name"
	SortMtime SortField = "mtime"
	SortFiles SortField = "files"
)

// ParseSort validates a sort field, defaulting to size.
func ParseSort(s string) (SortField, bool) {
	switch SortField(s) {
	case "", SortSize:
		return SortSize, true
	case SortName:
		return SortName, true
	case SortMtime:
		return SortMtime, true
	case SortFiles:
		return SortFiles, true
	}
	return SortSize, false
}

// ListOptions controls a tree listing.
type ListOptions struct {
	Path   string
	Basis  Basis
	Sort   SortField
	Desc   bool
	Offset int
	Limit  int
	Depth  int
}

// TreeChild is one child in a tree listing, optionally carrying its own
// children when depth > 1.
type TreeChild struct {
	NodeInfo
	Children []TreeChild
	Total    int
}

// TreeResult is the answer to a tree listing.
type TreeResult struct {
	Node      NodeInfo
	Ancestors []NodeInfo
	Children  []TreeChild
	Total     int
	Offset    int
	Limit     int
}

// Tree lists the node at opts.Path with its children (§9.3).
func (g *Generation) Tree(opts ListOptions) (TreeResult, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	idx, ok := g.resolve(opts.Path)
	if !ok {
		return TreeResult{}, false
	}
	if opts.Limit <= 0 {
		opts.Limit = 500
	}
	if opts.Depth <= 0 {
		opts.Depth = 1
	}

	res := TreeResult{Node: g.info(idx), Offset: opts.Offset, Limit: opts.Limit}
	for p := g.nodes[idx].Parent; p != NoIndex; p = g.nodes[p].Parent {
		res.Ancestors = append(res.Ancestors, g.info(p))
		if p == 0 {
			break
		}
	}
	slices.Reverse(res.Ancestors)

	res.Children, res.Total = g.children(idx, opts, opts.Depth)
	return res, true
}

// children returns one page of a directory's children, recursing while depth
// allows. Caller must hold the lock.
func (g *Generation) children(idx uint32, opts ListOptions, depth int) ([]TreeChild, int) {
	order := g.childOrder(idx, opts)
	total := len(order)
	if opts.Offset >= total {
		return nil, total
	}
	end := min(opts.Offset+opts.Limit, total)
	page := order[opts.Offset:end]

	out := make([]TreeChild, 0, len(page))
	for _, ci := range page {
		tc := TreeChild{NodeInfo: g.info(ci)}
		if depth > 1 && g.nodes[ci].Kind == KindDir && g.nodes[ci].ChildCount > 0 {
			sub := opts
			sub.Offset = 0
			tc.Children, tc.Total = g.children(ci, sub, depth-1)
		} else if g.nodes[ci].Kind == KindDir {
			tc.Total = int(g.nodes[ci].ChildCount)
		}
		out = append(out, tc)
	}
	return out, total
}

// childOrder returns the child indices of a directory in the requested
// order. The natural arena order (size descending in the generation's own
// basis) is returned as a slice without sorting; anything else is sorted on
// demand. Caller must hold the lock.
func (g *Generation) childOrder(idx uint32, opts ListOptions) []uint32 {
	n := &g.nodes[idx]
	live := make([]uint32, 0, n.ChildCount)
	for i := n.FirstChild; i < n.FirstChild+n.ChildCount; i++ {
		if g.nodes[i].Kind != KindDeleted {
			live = append(live, i)
		}
	}
	natural := opts.Sort == SortSize && opts.Desc && opts.Basis == g.basis
	if natural {
		return live
	}
	cmp := func(a, b uint32) int {
		na, nb := &g.nodes[a], &g.nodes[b]
		var c int
		switch opts.Sort {
		case SortName:
			c = slices.Compare(g.rawName(a), g.rawName(b))
		case SortMtime:
			c = compareInt64(na.Mtime, nb.Mtime)
		case SortFiles:
			c = compareUint64(uint64(na.Files), uint64(nb.Files))
		default:
			c = compareUint64(na.Sized(opts.Basis), nb.Sized(opts.Basis))
		}
		if c == 0 {
			c = slices.Compare(g.rawName(a), g.rawName(b))
			if opts.Desc {
				return -c
			}
			return c
		}
		if opts.Desc {
			return -c
		}
		return c
	}
	slices.SortFunc(live, cmp)
	return live
}

func compareUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Truncated describes the children a treemap response left out, so the
// client can draw a single "other" cell and conserve area (Appendix D).
type Truncated struct {
	Children int    `json:"children"`
	Size     uint64 `json:"size"`
	Alloc    uint64 `json:"alloc"`
}

// TreemapNode is one rectangle of the pruned treemap tree.
type TreemapNode struct {
	NodeInfo
	Children  []*TreemapNode
	Truncated *Truncated
}

// TreemapOptions controls treemap pruning.
type TreemapOptions struct {
	Path        string
	Basis       Basis
	MinFraction float64
	MaxNodes    int
	MaxDepth    int
}

// Treemap returns the subtree at opts.Path pruned for rendering (§9.3).
func (g *Generation) Treemap(opts TreemapOptions) (*TreemapNode, int, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	root, ok := g.resolve(opts.Path)
	if !ok {
		return nil, 0, false
	}
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = 10000
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 8
	}
	rootSize := g.nodes[root].Sized(opts.Basis)
	threshold := uint64(0)
	if opts.MinFraction > 0 {
		threshold = uint64(opts.MinFraction * float64(rootSize))
	}

	out := &TreemapNode{NodeInfo: g.info(root)}
	emitted := 1
	type item struct {
		idx   uint32
		dst   *TreemapNode
		depth int
	}
	queue := []item{{root, out, 0}}

	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		n := &g.nodes[it.idx]
		if n.Kind != KindDir || n.ChildCount == 0 {
			continue
		}
		var trunc Truncated
		for i := n.FirstChild; i < n.FirstChild+n.ChildCount; i++ {
			c := &g.nodes[i]
			if c.Kind == KindDeleted {
				continue
			}
			size := c.Sized(opts.Basis)
			if emitted >= opts.MaxNodes || it.depth+1 > opts.MaxDepth || size < threshold {
				trunc.Children++
				trunc.Size += c.Size
				trunc.Alloc += c.Alloc
				continue
			}
			child := &TreemapNode{NodeInfo: g.info(i)}
			it.dst.Children = append(it.dst.Children, child)
			emitted++
			if c.Kind == KindDir && c.ChildCount > 0 {
				queue = append(queue, item{i, child, it.depth + 1})
			}
		}
		if trunc.Children > 0 {
			t := trunc
			it.dst.Truncated = &t
		}
	}
	return out, emitted, true
}

// Top returns the largest files in the share, or beneath path when given.
func (g *Generation) Top(path string, n int, dirsOnly bool) ([]NodeInfo, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if n <= 0 {
		n = 100
	}
	root, ok := g.resolve(path)
	if !ok {
		return nil, false
	}
	// The share-wide file list is precomputed unless a delete invalidated it.
	if root == 0 && !dirsOnly && g.top != nil {
		out := make([]NodeInfo, 0, min(n, len(g.top)))
		for _, idx := range g.top {
			if len(out) == n {
				break
			}
			if g.nodes[idx].Kind == KindDeleted {
				continue
			}
			out = append(out, g.info(idx))
		}
		return out, true
	}

	top := make([]uint32, 0, n+1)
	g.walk(root, func(idx uint32) bool {
		if idx == root {
			return true
		}
		k := g.nodes[idx].Kind
		if dirsOnly != (k == KindDir) {
			return true
		}
		if len(top) < n || g.nodes[idx].Sized(g.basis) > g.nodes[top[len(top)-1]].Sized(g.basis) {
			top = insertTop(g, top, idx, n)
		}
		return true
	})
	out := make([]NodeInfo, 0, len(top))
	for _, idx := range top {
		out = append(out, g.info(idx))
	}
	return out, true
}

// SearchOptions filters a search (§9.3).
type SearchOptions struct {
	Query       string
	Path        string
	Kind        string
	Ext         string
	MinSize     uint64
	MaxSize     uint64
	MtimeBefore time.Time
	MtimeAfter  time.Time
	Basis       Basis
	Limit       int
	Deadline    time.Time
}

// SearchResult is the answer to a search.
type SearchResult struct {
	Matches   []NodeInfo
	Total     int
	Truncated bool
	Scanned   int
}

// Search scans the arena for matching names. It is linear and bounded by
// both a result limit and a deadline; hitting either sets Truncated.
func (g *Generation) Search(opts SearchOptions) (SearchResult, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	root, ok := g.resolve(opts.Path)
	if !ok {
		return SearchResult{}, false
	}
	if opts.Limit <= 0 {
		opts.Limit = 200
	}
	pattern := strings.ToLower(opts.Query)
	glob := strings.ContainsAny(pattern, "*?")

	var res SearchResult
	top := make([]uint32, 0, opts.Limit+1)
	const deadlineCheckEvery = 4096

	g.walk(root, func(idx uint32) bool {
		if idx == root {
			return true
		}
		res.Scanned++
		if res.Scanned%deadlineCheckEvery == 0 && !opts.Deadline.IsZero() && time.Now().After(opts.Deadline) {
			res.Truncated = true
			return false
		}
		n := &g.nodes[idx]
		switch opts.Kind {
		case "file":
			if n.Kind == KindDir {
				return true
			}
		case "dir":
			if n.Kind != KindDir {
				return true
			}
		}
		size := n.Sized(opts.Basis)
		if size < opts.MinSize || (opts.MaxSize > 0 && size > opts.MaxSize) {
			return true
		}
		mt := time.Unix(n.Mtime, 0)
		if !opts.MtimeAfter.IsZero() && mt.Before(opts.MtimeAfter) {
			return true
		}
		if !opts.MtimeBefore.IsZero() && mt.After(opts.MtimeBefore) {
			return true
		}
		name := string(g.rawName(idx))
		lower := strings.ToLower(name)
		if opts.Ext != "" && extensionOf(lower) != strings.ToLower(opts.Ext) {
			return true
		}
		if pattern != "" {
			if glob {
				if !matchGlob(pattern, lower) {
					return true
				}
			} else if !strings.Contains(lower, pattern) {
				return true
			}
		}
		res.Total++
		if len(top) < opts.Limit || size > g.nodes[top[len(top)-1]].Sized(opts.Basis) {
			top = insertTop(g, top, idx, opts.Limit)
		}
		return true
	})
	if res.Total > len(top) {
		res.Truncated = true
	}
	res.Matches = make([]NodeInfo, 0, len(top))
	for _, idx := range top {
		res.Matches = append(res.Matches, g.info(idx))
	}
	return res, true
}

// matchGlob matches a name against a pattern containing * and ?. It never
// crosses a path separator because it is applied to single names.
func matchGlob(pattern, name string) bool {
	pi, ni := 0, 0
	star, match := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pattern) && (pattern[pi] == name[ni] || pattern[pi] == '?'):
			pi++
			ni++
		case pi < len(pattern) && pattern[pi] == '*':
			star = pi
			match = ni
			pi++
		case star >= 0:
			pi = star + 1
			match++
			ni = match
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
