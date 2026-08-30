package model

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// TestMemoryPerNode measures the steady-state cost of a generation and
// enforces the budget in NFR-3: at most 100 bytes per node including names,
// so 10 M nodes fit in about 1 GiB on a Raspberry Pi.
func TestMemoryPerNode(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large arena")
	}
	const (
		dirs          = 2000
		filesPerDir   = 250
		totalNodes    = dirs*filesPerDir + dirs + 1
		budgetPerNode = 100
	)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	b := NewBuilder("bench", "/shares/bench", BasisApparent, 0, totalNodes)
	dirEntries := make([]Entry, dirs)
	for i := range dirEntries {
		dirEntries[i] = Entry{Name: fmt.Sprintf("directory-%05d", i), Kind: KindDir, Mtime: 1700000000}
	}
	dirIdx, err := b.AddChildren(b.Root(), dirEntries, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]Entry, filesPerDir)
	for _, d := range dirIdx {
		for i := range files {
			// A 20-character name is the typical case the budget assumes.
			files[i] = Entry{
				Name:  fmt.Sprintf("episode-s%02de%03d.mkv", i%30, i),
				Kind:  KindFile,
				Size:  uint64(i+1) * 4096,
				Alloc: uint64(i+1) * 4096,
				Mtime: 1700000000,
				Mode:  0o644, UID: 1000, GID: 1000,
			}
		}
		if _, err := b.AddChildren(d, files, nil); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	g := b.Finalize(GenerationMeta{ID: "bench", ScannedAt: time.Now()}, 1000)
	finalize := time.Since(start)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	used := after.HeapAlloc - min(before.HeapAlloc, after.HeapAlloc)
	perNode := float64(used) / float64(g.NodeCount())

	t.Logf("%d nodes: %.1f MiB resident, %.1f bytes/node, finalize %v",
		g.NodeCount(), float64(used)/(1<<20), perNode, finalize)

	if g.NodeCount() != totalNodes {
		t.Fatalf("built %d nodes, want %d", g.NodeCount(), totalNodes)
	}
	if perNode > budgetPerNode {
		t.Errorf("%.1f bytes per node exceeds the %d byte budget (NFR-3)", perNode, budgetPerNode)
	}
	// Keep the arena alive across the measurement.
	runtime.KeepAlive(g)
}

// BenchmarkQueries measures the read paths that NFR-5 puts a latency budget
// on, against an arena of a realistic shape.
func BenchmarkQueries(b *testing.B) {
	builder := NewBuilder("bench", "/r", BasisApparent, 0, 200000)
	dirEntries := make([]Entry, 400)
	for i := range dirEntries {
		dirEntries[i] = Entry{Name: fmt.Sprintf("dir-%04d", i), Kind: KindDir}
	}
	idx, _ := builder.AddChildren(builder.Root(), dirEntries, nil)
	files := make([]Entry, 500)
	for _, d := range idx {
		for i := range files {
			files[i] = Entry{Name: fmt.Sprintf("file-%05d.mkv", i), Kind: KindFile, Size: uint64(i) * 1024}
		}
		_, _ = builder.AddChildren(d, files, nil)
	}
	g := builder.Finalize(GenerationMeta{ID: "b"}, 1000)

	b.Run("tree-500-children", func(b *testing.B) {
		for range b.N {
			if _, ok := g.Tree(ListOptions{Path: "dir-0100", Sort: SortSize, Desc: true, Limit: 500}); !ok {
				b.Fatal("miss")
			}
		}
	})
	b.Run("treemap-10000", func(b *testing.B) {
		for range b.N {
			if _, _, ok := g.Treemap(TreemapOptions{MaxNodes: 10000, MaxDepth: 8}); !ok {
				b.Fatal("miss")
			}
		}
	})
	b.Run("top-100", func(b *testing.B) {
		for range b.N {
			if _, ok := g.Top("", 100, false, g.Basis()); !ok {
				b.Fatal("miss")
			}
		}
	})
	b.Run("search", func(b *testing.B) {
		for range b.N {
			if _, ok := g.Search(SearchOptions{Query: "file-004", Limit: 200}); !ok {
				b.Fatal("miss")
			}
		}
	})
	b.Run("resolve-deep", func(b *testing.B) {
		for range b.N {
			if _, ok := g.Info("dir-0399/file-00499.mkv"); !ok {
				b.Fatal("miss")
			}
		}
	})
}
