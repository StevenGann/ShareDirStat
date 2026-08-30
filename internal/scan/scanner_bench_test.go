package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

// benchTree builds a wide tree once per process and reuses it: the point of
// the benchmark is the per-entry cost of the crawl, not the fixture.
var (
	benchOnce sync.Once
	benchRoot string
	benchN    int
)

func benchFixture(tb testing.TB) (string, int) {
	tb.Helper()
	benchOnce.Do(func() {
		root, err := os.MkdirTemp("", "sds-bench-*")
		if err != nil {
			tb.Fatalf("fixture: %v", err)
		}
		// fan-out 10 over 4 levels, 30 files each: ~33k files in ~1.1k dirs.
		const fan, files = 10, 30
		payload := make([]byte, 512)
		count := 0
		var mk func(dir string, depth int)
		mk = func(dir string, depth int) {
			for i := 0; i < files; i++ {
				p := filepath.Join(dir, "f"+strconv.Itoa(i)+".dat")
				if err := os.WriteFile(p, payload, 0o644); err != nil {
					tb.Fatalf("fixture: %v", err)
				}
				count++
			}
			if depth == 0 {
				return
			}
			for i := 0; i < fan; i++ {
				sub := filepath.Join(dir, "d"+strconv.Itoa(i))
				if err := os.Mkdir(sub, 0o755); err != nil {
					tb.Fatalf("fixture: %v", err)
				}
				count++
				mk(sub, depth-1)
			}
		}
		mk(root, 3)
		benchRoot, benchN = root, count
	})
	return benchRoot, benchN
}

// The shipped default_excludes are non-empty, so a real scan always pays the
// matcher. Both variants are measured, or the exclusion path in the hot loop
// never gets profiled at all.
var benchExcludes = []string{
	"**/.sharedirstat-trash", "**/.snapshot", "**/@eaDir",
	"**/#recycle", "**/.Trash-*", "**/lost+found",
}

func BenchmarkScan(b *testing.B) {
	root, n := benchFixture(b)
	b.Logf("fixture: %d entries under %s", n, root)
	for _, conc := range []int{1, 4} {
		for _, v := range []struct {
			name     string
			excludes []string
		}{{"no-excludes", nil}, {"default-excludes", benchExcludes}} {
			b.Run(fmt.Sprintf("concurrency=%d/%s", conc, v.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					gen, err := Run(context.Background(), Options{
						ShareID:     "bench",
						Root:        root,
						Basis:       model.BasisApparent,
						Concurrency: conc,
						Excludes:    v.excludes,
						SizeHint:    n + n/8,
					}, model.GenerationMeta{}, nil)
					if err != nil {
						b.Fatalf("scan: %v", err)
					}
					if got := gen.NodeCount(); got != n+1 {
						b.Fatalf("scanned %d nodes, want %d", got, n+1)
					}
				}
				b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
			})
		}
	}
}
