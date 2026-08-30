package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

// buildTree creates a small tree with the awkward cases the scanner has to
// get right: exclusions, symlinks, hard links and an unreadable directory.
func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(p string, size int) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(full)
		if err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if err := f.Truncate(int64(size)); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mk("a/f1.txt", 1000)
	mk("a/f2.mkv", 2000)
	mk("a/sub/f3.mkv", 3000)
	mk("b/big.iso", 10000)
	mk("b/empty", 0)
	mk(".cache/junk.bin", 5000)
	if err := os.Symlink("a", filepath.Join(root, "link-to-a")); err != nil {
		t.Fatal(err)
	}
	return root
}

// buildHardLinkTree returns a tree where one inode has two names.
func buildHardLinkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "y"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x/original"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "x/original"), filepath.Join(root, "y/second-link")); err != nil {
		t.Fatal(err)
	}
	return root
}

func run(t *testing.T, root string, mutate func(*Options)) *model.Generation {
	t.Helper()
	opts := Options{
		ShareID:     "test",
		Root:        root,
		Basis:       model.BasisApparent,
		Concurrency: 4,
		Excludes:    []string{"**/.cache"},
		TopN:        10,
	}
	if mutate != nil {
		mutate(&opts)
	}
	gen, err := Run(context.Background(), opts, model.GenerationMeta{ID: "g1", ScannedAt: time.Now()}, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return gen
}

func TestScanTotals(t *testing.T) {
	gen := run(t, buildTree(t), nil)
	st := gen.Stats()

	// 1000 + 2000 + 3000 + 10000 + 0 (empty) + symlink target length ("a" = 1).
	// The excluded .cache tree contributes nothing.
	const wantSize = 1000 + 2000 + 3000 + 10000 + 1
	if st.Size != wantSize {
		t.Errorf("Size = %d, want %d", st.Size, wantSize)
	}
	if st.Symlinks != 1 {
		t.Errorf("Symlinks = %d, want 1", st.Symlinks)
	}
	// a/f1, a/f2, a/sub/f3, b/big, b/empty, link-to-a
	if st.Files != 6 {
		t.Errorf("Files = %d, want 6", st.Files)
	}
	// a, a/sub, b  (.cache excluded)
	if st.Dirs != 3 {
		t.Errorf("Dirs = %d, want 3", st.Dirs)
	}
	if st.Excluded != 1 {
		t.Errorf("Excluded = %d, want 1", st.Excluded)
	}
	if _, ok := gen.Resolve(".cache"); ok {
		t.Error("excluded directory must not be in the tree")
	}
}

func TestScanAggregatesMatchChildren(t *testing.T) {
	gen := run(t, buildTree(t), nil)
	a, ok := gen.Info("a")
	if !ok {
		t.Fatal("missing a")
	}
	if a.Size != 6000 {
		t.Errorf("a.Size = %d, want 6000", a.Size)
	}
	if a.Files != 3 || a.Dirs != 1 {
		t.Errorf("a counts = %d files, %d dirs; want 3, 1", a.Files, a.Dirs)
	}
	b, _ := gen.Info("b")
	if b.Size != 10000 {
		t.Errorf("b.Size = %d, want 10000", b.Size)
	}
	if b.Files != 2 {
		t.Errorf("b.Files = %d, want 2", b.Files)
	}
}

// A hard-linked inode occupies its space once, so it must be counted once
// (FR-SCAN-17). Which of the names is treated as the original depends on the
// order the workers happen to reach them, so the test asserts the invariant
// rather than a particular winner.
func TestScanHardLinkCountedOnce(t *testing.T) {
	gen := run(t, buildHardLinkTree(t), func(o *Options) { o.Excludes = nil })
	st := gen.Stats()
	if st.Size != 1000 {
		t.Errorf("total Size = %d, want 1000 (the inode counted once)", st.Size)
	}
	if st.HardlinkDups != 1 {
		t.Errorf("HardlinkDups = %d, want 1", st.HardlinkDups)
	}
	if st.Files != 2 {
		t.Errorf("Files = %d, want 2 (both names are listed)", st.Files)
	}

	orig, ok1 := gen.Info("x/original")
	second, ok2 := gen.Info("y/second-link")
	if !ok1 || !ok2 {
		t.Fatal("both names must appear in the tree")
	}
	dups := 0
	for _, n := range []model.NodeInfo{orig, second} {
		if n.Flags.Has(model.FlagHardlinkDup) {
			dups++
		}
		if n.Size != 1000 {
			t.Errorf("%s: display size = %d, want 1000", n.Path, n.Size)
		}
	}
	if dups != 1 {
		t.Errorf("exactly one name should be flagged as a duplicate, got %d", dups)
	}
	// Whichever name won, the two parents together account for 1000 bytes.
	x, _ := gen.Info("x")
	y, _ := gen.Info("y")
	if x.Size+y.Size != 1000 {
		t.Errorf("x.Size+y.Size = %d, want 1000", x.Size+y.Size)
	}
}

func TestScanChildrenSortedBySize(t *testing.T) {
	gen := run(t, buildTree(t), nil)
	res, ok := gen.Tree(model.ListOptions{Path: "", Sort: model.SortSize, Desc: true, Limit: 100})
	if !ok {
		t.Fatal("tree")
	}
	var sizes []uint64
	for _, c := range res.Children {
		sizes = append(sizes, c.Size)
	}
	for i := 1; i < len(sizes); i++ {
		if sizes[i-1] < sizes[i] {
			t.Fatalf("children not sorted descending: %v", sizes)
		}
	}
	if res.Children[0].Name != "b" {
		t.Errorf("largest child = %q, want b", res.Children[0].Name)
	}
}

func TestScanSymlinkNotFollowed(t *testing.T) {
	gen := run(t, buildTree(t), nil)
	n, ok := gen.Info("link-to-a")
	if !ok {
		t.Fatal("missing symlink")
	}
	if n.Kind != model.KindSymlink {
		t.Errorf("kind = %v, want symlink", n.Kind)
	}
	if n.ChildCount != 0 {
		t.Error("symlink must not be descended into")
	}
}

func TestScanFollowSymlinksBreaksCycles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d/f"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink pointing at its own ancestor would loop forever.
	if err := os.Symlink("..", filepath.Join(root, "d/up")); err != nil {
		t.Fatal(err)
	}
	done := make(chan *model.Generation, 1)
	go func() {
		done <- run(t, root, func(o *Options) { o.FollowSymlinks = true; o.Excludes = nil })
	}()
	select {
	case gen := <-done:
		if gen.Stats().Size < 100 {
			t.Errorf("expected the real file to be counted, got %d", gen.Stats().Size)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("scan did not terminate: symlink cycle was followed")
	}
}

func TestScanUnreadableDirectoryIsPartialNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions are not enforced")
	}
	root := buildTree(t)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "x"), make([]byte, 42), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	gen := run(t, root, nil)
	if gen.Stats().Errors == 0 {
		t.Error("expected the permission error to be recorded")
	}
	errs, total, _ := gen.Errors(0, 10)
	if total == 0 || !strings.Contains(errs[0].Path, "locked") {
		t.Errorf("errors = %+v", errs)
	}
	if errs[0].Errno != "EACCES" {
		t.Errorf("errno = %q, want EACCES", errs[0].Errno)
	}
	n, _ := gen.Info("locked")
	if !n.Flags.Has(model.FlagPartial) {
		t.Error("unreadable directory should be flagged partial")
	}
	root0, _ := gen.Info("")
	if !root0.Flags.Has(model.FlagPartial) {
		t.Error("partial should propagate to ancestors")
	}
	// The rest of the tree must still be complete.
	if a, _ := gen.Info("a"); a.Size != 6000 {
		t.Errorf("sibling subtree damaged: a.Size = %d", a.Size)
	}
}

func TestScanCancel(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		d := filepath.Join(root, "d", string(rune('a'+i%26)), string(rune('a'+i/26)))
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "f"), make([]byte, 100), 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Options{ShareID: "t", Root: root, Concurrency: 2}, model.GenerationMeta{}, nil)
	if err == nil {
		t.Fatal("cancelled scan must return an error, not a partial generation")
	}
}

func TestScanProgressAndPause(t *testing.T) {
	root := buildTree(t)
	var calls atomic.Int64
	ctrl := NewController()
	gen, err := Run(context.Background(), Options{
		ShareID: "t", Root: root, Concurrency: 2, TopN: 5,
		ProgressInterval: time.Millisecond,
		OnProgress:       func(Progress) { calls.Add(1) },
	}, model.GenerationMeta{ID: "g"}, ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 {
		t.Error("expected at least the final progress callback")
	}
	if gen.Stats().Files == 0 {
		t.Error("no files scanned")
	}
	ctrl.Pause()
	if !ctrl.Paused() {
		t.Error("controller should report paused")
	}
	ctrl.Resume()
}

func TestScanNodeLimit(t *testing.T) {
	root := buildTree(t)
	_, err := Run(context.Background(), Options{
		ShareID: "t", Root: root, Concurrency: 1, MaxNodes: 3,
	}, model.GenerationMeta{}, nil)
	if err == nil {
		t.Fatal("expected the node limit to abort the scan")
	}
	if !strings.Contains(err.Error(), "node limit") {
		t.Errorf("error = %v; want the node limit to be named", err)
	}
}

func TestAllocatedBasis(t *testing.T) {
	gen := run(t, buildTree(t), func(o *Options) { o.Basis = model.BasisAllocated })
	// Sparse files: apparent size is large, allocated is ~0. The scanner must
	// record both independently.
	st := gen.Stats()
	if st.Size == 0 {
		t.Error("apparent size should still be recorded under the allocated basis")
	}
	if st.Alloc == st.Size {
		t.Logf("alloc %d == size %d (filesystem may not report sparseness)", st.Alloc, st.Size)
	}
}

func TestSubtreeScanAndSplice(t *testing.T) {
	root := buildTree(t)
	gen := run(t, root, nil)
	before := gen.Stats().Size

	// Grow the subtree on disk, then rescan just that subtree.
	if err := os.WriteFile(filepath.Join(root, "a/new.mkv"), make([]byte, 7000), 0o644); err != nil {
		t.Fatal(err)
	}
	sub, err := Run(context.Background(), Options{
		ShareID: "test", Root: filepath.Join(root, "a"), RelRoot: "a",
		Concurrency: 2, TopN: 10,
	}, model.GenerationMeta{ID: "g2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := gen.Splice("a", sub); err != nil {
		t.Fatalf("splice failed: %v", err)
	}
	a, _ := gen.Info("a")
	if a.Size != 13000 {
		t.Errorf("after splice a.Size = %d, want 13000", a.Size)
	}
	if got, want := gen.Stats().Size, before+7000; got != want {
		t.Errorf("share total = %d, want %d", got, want)
	}
	if _, ok := gen.Info("a/new.mkv"); !ok {
		t.Error("new file not visible after splice")
	}
	if _, ok := gen.Info("a/sub/f3.mkv"); !ok {
		t.Error("existing descendant lost after splice")
	}
	if _, ok := gen.Info("b/big.iso"); !ok {
		t.Error("splice damaged an unrelated subtree")
	}
	// Sibling totals must be untouched.
	if b, _ := gen.Info("b"); b.Size != 10000 {
		t.Errorf("sibling b.Size = %d, want 10000", b.Size)
	}
}
