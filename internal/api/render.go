package api

import (
	"encoding/base64"
	"strconv"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

// flagsJSON is the API shape of a node's property bits.
type flagsJSON struct {
	Partial     bool `json:"partial"`
	MountPoint  bool `json:"mountpoint"`
	HardlinkDup bool `json:"hardlink_dup"`
	Excluded    bool `json:"excluded"`
	Unscanned   bool `json:"unscanned"`
}

// nodeJSON is the API representation of one filesystem entry (§9.2).
type nodeJSON struct {
	Name     string    `json:"name"`
	NameB64  string    `json:"name_b64,omitempty"`
	Path     string    `json:"path"`
	Kind     string    `json:"kind"`
	Size     uint64    `json:"size"`
	Alloc    uint64    `json:"alloc"`
	Mtime    time.Time `json:"mtime"`
	Mode     string    `json:"mode"`
	Perms    string    `json:"perms"`
	UID      uint32    `json:"uid"`
	GID      uint32    `json:"gid"`
	Owner    string    `json:"owner,omitempty"`
	Group    string    `json:"group,omitempty"`
	Ext      string    `json:"ext,omitempty"`
	Flags    flagsJSON `json:"flags"`
	Files    uint32    `json:"files"`
	Dirs     uint32    `json:"dirs"`
	Children uint32    `json:"children"`
	// Duration is the media playing time in seconds (a file's own; the
	// aggregate beneath a directory) and MediaSize the bytes it covers.
	// Both are absent when nothing beneath the node has a known duration.
	Duration    uint32           `json:"duration,omitempty"`
	MediaSize   uint64           `json:"media_size,omitempty"`
	PctOfParent float64          `json:"pct_of_parent"`
	PctOfShare  float64          `json:"pct_of_share"`
	SubChildren []nodeJSON       `json:"children_list,omitempty"`
	SubTotal    int              `json:"children_total,omitempty"`
	Truncated   *model.Truncated `json:"truncated,omitempty"`
}

// renderer turns model views into API shapes, resolving owner names and
// percentages against the enclosing directory and the share total.
type renderer struct {
	owners     *OwnerResolver
	basis      model.Basis
	shareTotal uint64
}

func (r renderer) sized(n *model.NodeInfo) uint64 {
	if r.basis == model.BasisAllocated {
		return n.Alloc
	}
	return n.Size
}

// node renders one node. parentSize is used for pct_of_parent; pass 0 when
// there is no meaningful parent.
func (r renderer) node(n model.NodeInfo, parentSize uint64) nodeJSON {
	out := nodeJSON{
		Name:      n.Name,
		Path:      n.Path,
		Kind:      n.Kind.String(),
		Size:      n.Size,
		Alloc:     n.Alloc,
		Mtime:     n.Mtime,
		Mode:      "0" + strconv.FormatUint(uint64(n.Mode), 8),
		Perms:     permString(n.Kind, n.Mode),
		UID:       n.UID,
		GID:       n.GID,
		Ext:       n.Ext,
		Files:     n.Files,
		Dirs:      n.Dirs,
		Children:  n.ChildCount,
		Duration:  n.Dur,
		MediaSize: n.MediaSize,
		Flags: flagsJSON{
			Partial:     n.Flags.Has(model.FlagPartial),
			MountPoint:  n.Flags.Has(model.FlagMountPoint),
			HardlinkDup: n.Flags.Has(model.FlagHardlinkDup),
			Excluded:    n.Flags.Has(model.FlagExcluded),
			Unscanned:   n.Flags.Has(model.FlagUnscanned),
		},
	}
	if !n.NameValid {
		// The display name has replacement characters in it; the raw bytes
		// let the client still address the node (FR-SCAN-20).
		out.NameB64 = base64.StdEncoding.EncodeToString(n.NameRaw)
	}
	if r.owners != nil {
		out.Owner, out.Group = r.owners.Lookup(n.UID, n.GID)
	}
	size := r.sized(&n)
	if parentSize > 0 {
		out.PctOfParent = float64(size) / float64(parentSize)
	}
	if r.shareTotal > 0 {
		out.PctOfShare = float64(size) / float64(r.shareTotal)
	}
	return out
}

func (r renderer) nodes(list []model.NodeInfo, parentSize uint64) []nodeJSON {
	out := make([]nodeJSON, 0, len(list))
	for _, n := range list {
		out = append(out, r.node(n, parentSize))
	}
	return out
}

// treeChildren renders a listing, recursing into nested children.
func (r renderer) treeChildren(list []model.TreeChild, parentSize uint64) []nodeJSON {
	out := make([]nodeJSON, 0, len(list))
	for _, c := range list {
		j := r.node(c.NodeInfo, parentSize)
		if len(c.Children) > 0 {
			j.SubChildren = r.treeChildren(c.Children, r.sized(&c.NodeInfo))
			j.SubTotal = c.Total
		}
		out = append(out, j)
	}
	return out
}

// treemap renders the pruned treemap tree.
func (r renderer) treemap(n *model.TreemapNode, parentSize uint64) nodeJSON {
	j := r.node(n.NodeInfo, parentSize)
	j.Truncated = n.Truncated
	own := r.sized(&n.NodeInfo)
	for _, c := range n.Children {
		j.SubChildren = append(j.SubChildren, r.treemap(c, own))
	}
	j.SubTotal = len(n.Children)
	return j
}

// permString renders mode bits the way ls does, so the UI can show a
// familiar drwxr-xr-x (FR-UI-05).
func permString(k model.Kind, mode uint16) string {
	var b [10]byte
	switch k {
	case model.KindDir:
		b[0] = 'd'
	case model.KindSymlink:
		b[0] = 'l'
	case model.KindOther:
		b[0] = '?'
	default:
		b[0] = '-'
	}
	const rwx = "rwxrwxrwx"
	for i := 0; i < 9; i++ {
		if mode&(1<<uint(8-i)) != 0 {
			b[i+1] = rwx[i]
		} else {
			b[i+1] = '-'
		}
	}
	if mode&0o4000 != 0 {
		b[3] = setBit(b[3], 's', 'S')
	}
	if mode&0o2000 != 0 {
		b[6] = setBit(b[6], 's', 'S')
	}
	if mode&0o1000 != 0 {
		b[9] = setBit(b[9], 't', 'T')
	}
	return string(b[:])
}

func setBit(cur, lower, upper byte) byte {
	if cur == 'x' {
		return lower
	}
	return upper
}
