package model

import (
	"testing"
	"time"
	"unsafe"
)

// buildGen assembles a small tree directly through the Builder, without
// touching a filesystem, so query behaviour can be asserted exactly.
//
//	/            9200
//	  b          3000   b1.mkv 3000
//	  top.iso    5000
//	  a          1200   a1.mkv 1000, a2.txt 200
func buildGen(t *testing.T) *Generation {
	t.Helper()
	b := NewBuilder("s", "/root", BasisApparent, 0, 16)
	kids, err := b.AddChildren(b.Root(), []Entry{
		{Name: "a", Kind: KindDir, Mtime: 100},
		{Name: "b", Kind: KindDir, Mtime: 200},
		{Name: "top.iso", Kind: KindFile, Size: 5000, Alloc: 5120, Mtime: 300},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, bIdx := kids[0], kids[1]
	if _, err := b.AddChildren(a, []Entry{
		{Name: "a1.mkv", Kind: KindFile, Size: 1000, Alloc: 1024, Mtime: 10},
		{Name: "a2.txt", Kind: KindFile, Size: 200, Alloc: 512, Mtime: 20},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddChildren(bIdx, []Entry{
		{Name: "b1.mkv", Kind: KindFile, Size: 3000, Alloc: 3072, Mtime: 30},
	}, nil); err != nil {
		t.Fatal(err)
	}
	return b.Finalize(GenerationMeta{ID: "g1", ScannedAt: time.Unix(1000, 0)}, 10)
}

func TestNodeStaysCompact(t *testing.T) {
	// NFR-3 budgets ~100 bytes per node including its name; the fixed part
	// must stay at 64 bytes or the memory target is unreachable.
	if got := unsafe.Sizeof(Node{}); got > 64 {
		t.Errorf("sizeof(Node) = %d, want <= 64", got)
	}
}

func TestAggregation(t *testing.T) {
	g := buildGen(t)
	st := g.Stats()
	if st.Size != 9200 {
		t.Errorf("total = %d, want 9200", st.Size)
	}
	if st.Alloc != 5120+1024+512+3072 {
		t.Errorf("alloc = %d", st.Alloc)
	}
	if st.Files != 4 || st.Dirs != 2 {
		t.Errorf("files=%d dirs=%d, want 4, 2", st.Files, st.Dirs)
	}
	if st.MaxDepth != 2 {
		t.Errorf("max depth = %d, want 2", st.MaxDepth)
	}
	a, ok := g.Info("a")
	if !ok || a.Size != 1200 || a.Files != 2 || a.Dirs != 0 {
		t.Errorf("a = %+v", a)
	}
}

func TestResolveAndPaths(t *testing.T) {
	g := buildGen(t)
	for _, p := range []string{"", "a", "a/a1.mkv", "b/b1.mkv", "top.iso"} {
		n, ok := g.Info(p)
		if !ok {
			t.Fatalf("resolve %q failed", p)
		}
		if n.Path != p {
			t.Errorf("path round-trip: got %q want %q", n.Path, p)
		}
	}
	if _, ok := g.Info("nope"); ok {
		t.Error("resolved a path that does not exist")
	}
	if _, ok := g.Info("a/deep/missing"); ok {
		t.Error("resolved a missing nested path")
	}
	anc, ok := g.Ancestors("a/a1.mkv")
	if !ok || len(anc) != 2 || anc[0].Path != "" || anc[1].Path != "a" {
		t.Errorf("ancestors = %+v", anc)
	}
}

func TestTreeSortingAndPaging(t *testing.T) {
	g := buildGen(t)
	res, ok := g.Tree(ListOptions{Sort: SortSize, Desc: true, Limit: 10})
	if !ok {
		t.Fatal("tree failed")
	}
	if got := []string{res.Children[0].Name, res.Children[1].Name, res.Children[2].Name}; got[0] != "top.iso" || got[1] != "b" || got[2] != "a" {
		t.Errorf("size desc order = %v", got)
	}
	if res.Total != 3 {
		t.Errorf("total = %d", res.Total)
	}

	asc, _ := g.Tree(ListOptions{Sort: SortSize, Desc: false, Limit: 10})
	if asc.Children[0].Name != "a" {
		t.Errorf("size asc first = %q, want a", asc.Children[0].Name)
	}
	byName, _ := g.Tree(ListOptions{Sort: SortName, Desc: false, Limit: 10})
	if byName.Children[0].Name != "a" || byName.Children[2].Name != "top.iso" {
		t.Errorf("name order = %+v", byName.Children)
	}
	byTime, _ := g.Tree(ListOptions{Sort: SortMtime, Desc: true, Limit: 10})
	if byTime.Children[0].Name != "top.iso" {
		t.Errorf("mtime desc first = %q", byTime.Children[0].Name)
	}

	page, _ := g.Tree(ListOptions{Sort: SortSize, Desc: true, Limit: 1, Offset: 1})
	if len(page.Children) != 1 || page.Children[0].Name != "b" || page.Total != 3 {
		t.Errorf("paging = %+v total=%d", page.Children, page.Total)
	}
	past, _ := g.Tree(ListOptions{Limit: 10, Offset: 99, Sort: SortSize, Desc: true})
	if len(past.Children) != 0 || past.Total != 3 {
		t.Errorf("offset past the end should be empty with a real total: %+v", past)
	}

	deep, _ := g.Tree(ListOptions{Sort: SortSize, Desc: true, Limit: 10, Depth: 2})
	var found bool
	for _, c := range deep.Children {
		if c.Name == "a" {
			found = len(c.Children) == 2
		}
	}
	if !found {
		t.Error("depth=2 should include grandchildren")
	}
}

func TestAllocatedBasisReorders(t *testing.T) {
	b := NewBuilder("s", "/r", BasisApparent, 0, 8)
	// A sparse file: large apparent size, tiny allocation.
	if _, err := b.AddChildren(b.Root(), []Entry{
		{Name: "sparse.img", Kind: KindFile, Size: 1 << 30, Alloc: 4096},
		{Name: "real.bin", Kind: KindFile, Size: 1 << 20, Alloc: 1 << 20},
	}, nil); err != nil {
		t.Fatal(err)
	}
	g := b.Finalize(GenerationMeta{ID: "g"}, 10)

	apparent, _ := g.Tree(ListOptions{Basis: BasisApparent, Sort: SortSize, Desc: true, Limit: 10})
	if apparent.Children[0].Name != "sparse.img" {
		t.Errorf("apparent basis first = %q", apparent.Children[0].Name)
	}
	allocated, _ := g.Tree(ListOptions{Basis: BasisAllocated, Sort: SortSize, Desc: true, Limit: 10})
	if allocated.Children[0].Name != "real.bin" {
		t.Errorf("allocated basis first = %q, want real.bin", allocated.Children[0].Name)
	}
}

func TestTreemapConservesArea(t *testing.T) {
	g := buildGen(t)
	root, emitted, ok := g.Treemap(TreemapOptions{MinFraction: 0, MaxNodes: 100, MaxDepth: 8})
	if !ok {
		t.Fatal("treemap failed")
	}
	// root + a, b, top.iso + a1.mkv, a2.txt, b1.mkv
	if emitted != 7 {
		t.Errorf("emitted = %d, want 7", emitted)
	}
	var check func(n *TreemapNode)
	check = func(n *TreemapNode) {
		if n.Kind != KindDir {
			return
		}
		var sum uint64
		for _, c := range n.Children {
			sum += c.Size
			check(c)
		}
		if n.Truncated != nil {
			sum += n.Truncated.Size
		}
		if sum != n.Size {
			t.Errorf("%q: children total %d != own size %d (area not conserved)", n.Path, sum, n.Size)
		}
	}
	check(root)
}

func TestTreemapPruning(t *testing.T) {
	g := buildGen(t)
	// A threshold above "a" (1200/9200 = 13%) must prune it into the
	// truncated bucket rather than dropping its area.
	root, _, ok := g.Treemap(TreemapOptions{MinFraction: 0.2, MaxNodes: 100, MaxDepth: 8})
	if !ok {
		t.Fatal("treemap failed")
	}
	if root.Truncated == nil || root.Truncated.Children != 1 || root.Truncated.Size != 1200 {
		t.Errorf("truncated = %+v, want 1 child of 1200 bytes", root.Truncated)
	}
	for _, c := range root.Children {
		if c.Name == "a" {
			t.Error("node below the threshold should not be emitted")
		}
	}

	capped, emitted, _ := g.Treemap(TreemapOptions{MaxNodes: 2, MaxDepth: 8})
	if emitted != 2 {
		t.Errorf("max_nodes ignored: emitted %d", emitted)
	}
	if capped.Truncated == nil {
		t.Error("nodes dropped by the budget must be reported as truncated")
	}

	shallow, _, _ := g.Treemap(TreemapOptions{MaxNodes: 100, MaxDepth: 1})
	for _, c := range shallow.Children {
		if len(c.Children) > 0 {
			t.Error("max_depth=1 must not emit grandchildren")
		}
	}
}

func TestTopAndExtensions(t *testing.T) {
	g := buildGen(t)
	top, ok := g.Top("", 2, false)
	if !ok || len(top) != 2 || top[0].Name != "top.iso" || top[1].Name != "b1.mkv" {
		t.Errorf("top = %+v", top)
	}
	dirs, _ := g.Top("", 1, true)
	if len(dirs) != 1 || dirs[0].Name != "b" {
		t.Errorf("top dirs = %+v", dirs)
	}
	sub, _ := g.Top("a", 5, false)
	if len(sub) != 2 || sub[0].Name != "a1.mkv" {
		t.Errorf("top under a = %+v", sub)
	}

	exts := g.Extensions()
	if len(exts) != 3 || exts[0].Ext != "iso" || exts[0].Size != 5000 {
		t.Errorf("extensions = %+v", exts)
	}
	if exts[1].Ext != "mkv" || exts[1].Files != 2 || exts[1].Size != 4000 {
		t.Errorf("mkv row = %+v", exts[1])
	}
	subExts, ok := g.ExtensionsUnder("a")
	if !ok || len(subExts) != 2 {
		t.Errorf("extensions under a = %+v", subExts)
	}
}

func TestExtensionOf(t *testing.T) {
	cases := map[string]string{
		"movie.MKV": "mkv", "archive.tar.gz": "gz", "noext": "", ".bashrc": "",
		".config.yaml": "yaml", "trailing.": "", "x." + string(make([]byte, 20)): "",
	}
	for in, want := range cases {
		if got := extensionOf(in); got != want {
			t.Errorf("extensionOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearch(t *testing.T) {
	g := buildGen(t)
	res, ok := g.Search(SearchOptions{Query: "mkv", Limit: 10})
	if !ok || res.Total != 2 {
		t.Errorf("substring search: %+v", res)
	}
	if res.Matches[0].Name != "b1.mkv" {
		t.Errorf("results should be largest first, got %q", res.Matches[0].Name)
	}
	glob, _ := g.Search(SearchOptions{Query: "a?.*", Limit: 10})
	if glob.Total != 2 {
		t.Errorf("glob search found %d, want 2", glob.Total)
	}
	sized, _ := g.Search(SearchOptions{MinSize: 2000, Limit: 10})
	if sized.Total != 3 { // top.iso, b, b1.mkv
		t.Errorf("min_size search found %d, want 3", sized.Total)
	}
	files, _ := g.Search(SearchOptions{MinSize: 2000, Kind: "file", Limit: 10})
	if files.Total != 2 {
		t.Errorf("kind=file found %d, want 2", files.Total)
	}
	scoped, _ := g.Search(SearchOptions{Query: "", Path: "a", Limit: 10})
	if scoped.Total != 2 {
		t.Errorf("scoped search found %d, want 2", scoped.Total)
	}
	byExt, _ := g.Search(SearchOptions{Ext: "iso", Limit: 10})
	if byExt.Total != 1 || byExt.Matches[0].Name != "top.iso" {
		t.Errorf("ext search = %+v", byExt)
	}
	capped, _ := g.Search(SearchOptions{Query: "", Limit: 1})
	if !capped.Truncated || len(capped.Matches) != 1 {
		t.Errorf("limit should truncate: %+v", capped)
	}
}

func TestRemoveUpdatesAncestors(t *testing.T) {
	g := buildGen(t)
	freed, freedAlloc, ok := g.Remove("a/a1.mkv")
	if !ok || freed != 1000 || freedAlloc != 1024 {
		t.Fatalf("remove = %d/%d ok=%v", freed, freedAlloc, ok)
	}
	a, _ := g.Info("a")
	if a.Size != 200 || a.Files != 1 {
		t.Errorf("a after remove = size %d files %d, want 200, 1", a.Size, a.Files)
	}
	if st := g.Stats(); st.Size != 8200 {
		t.Errorf("total after remove = %d, want 8200", st.Size)
	}
	if _, ok := g.Info("a/a1.mkv"); ok {
		t.Error("removed node still resolves")
	}
	res, _ := g.Tree(ListOptions{Path: "a", Sort: SortSize, Desc: true, Limit: 10})
	if res.Total != 1 {
		t.Errorf("removed node still listed: %+v", res.Children)
	}
	if g.Mutation() == 0 {
		t.Error("mutation counter should advance")
	}

	// Removing a directory takes its whole subtree with it.
	if _, _, ok := g.Remove("b"); !ok {
		t.Fatal("remove dir failed")
	}
	if st := g.Stats(); st.Size != 5200 {
		t.Errorf("total after removing b = %d, want 5200", st.Size)
	}
	if _, ok := g.Info("b/b1.mkv"); ok {
		t.Error("descendant of a removed directory still resolves")
	}
	if _, _, ok := g.Remove(""); ok {
		t.Error("the share root must never be removable")
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"*.mkv", "movie.mkv", true},
		{"*.mkv", "movie.mp4", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*", "anything", true},
		{"**", "anything", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "abcx", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.name); got != c.want {
			t.Errorf("matchGlob(%q,%q) = %v", c.pat, c.name, got)
		}
	}
}
