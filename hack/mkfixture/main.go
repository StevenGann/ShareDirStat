// Command mkfixture generates a deterministic filesystem tree for tests and
// benchmarks (specification §15.2). It writes a manifest.json next to the
// tree with the expected totals so the scanner can be checked against it.
//
//	go run ./hack/mkfixture -root /tmp/fixture -files 100000 -depth 5 -width 6
//
// Files are created with Truncate (sparse) by default so a million-file tree
// costs almost no disk; -write-data fills them with real bytes instead.
// Edge cases are enabled individually (-hardlinks, -symlink-loop,
// -unreadable, -wide, -nonutf8, -longnames, -sparse).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

// Manifest is written to <root>/../<root-name>.manifest.json.
type Manifest struct {
	Root           string `json:"root"`
	Seed           uint64 `json:"seed"`
	Files          int64  `json:"files"`
	Dirs           int64  `json:"dirs"`
	Symlinks       int64  `json:"symlinks"`
	ApparentBytes  int64  `json:"apparent_bytes"`
	HardlinkDups   int64  `json:"hardlink_dups"`
	HardlinkBytes  int64  `json:"hardlink_dup_bytes"` // bytes that must NOT be double counted
	UnreadableDirs int64  `json:"unreadable_dirs"`
	WideDirEntries int    `json:"wide_dir_entries"`
	WriteData      bool   `json:"write_data"`
}

type gen struct {
	rng   *rand.Rand
	m     Manifest
	write bool
	buf   []byte
}

func main() {
	root := flag.String("root", "", "directory to create (must not exist)")
	files := flag.Int("files", 10000, "approximate number of regular files")
	depth := flag.Int("depth", 4, "maximum directory depth")
	width := flag.Int("width", 5, "subdirectories per directory")
	seed := flag.Uint64("seed", 42, "random seed")
	maxSize := flag.Int64("max-size", 64<<20, "largest file size in bytes (log-uniform distribution)")
	writeData := flag.Bool("write-data", false, "write real bytes instead of sparse truncate")
	hardlinks := flag.Int("hardlinks", 0, "number of extra hard links to create")
	symlinkLoop := flag.Bool("symlink-loop", false, "add a directory symlink cycle and a dangling symlink")
	unreadable := flag.Bool("unreadable", false, "add a directory with mode 000 (skipped when running as root)")
	wide := flag.Int("wide", 0, "add one directory with this many entries")
	nonUTF8 := flag.Bool("nonutf8", false, "add files with non-UTF-8 names")
	longNames := flag.Bool("longnames", false, "add a 255-byte file name")
	sparse := flag.Bool("sparse", false, "add a file with a large apparent size and one written block")
	flag.Parse()

	if *root == "" {
		fmt.Fprintln(os.Stderr, "-root is required")
		os.Exit(2)
	}
	if _, err := os.Stat(*root); err == nil {
		fmt.Fprintf(os.Stderr, "%s already exists; refusing to overwrite\n", *root)
		os.Exit(2)
	}
	g := &gen{rng: rand.New(rand.NewPCG(*seed, *seed^0x9e3779b97f4a7c15)), write: *writeData}
	g.m.Root, g.m.Seed, g.m.WriteData = *root, *seed, *writeData
	if *writeData {
		g.buf = make([]byte, 1<<20)
		for i := range g.buf {
			g.buf[i] = byte(i)
		}
	}

	must(os.MkdirAll(*root, 0o755))
	g.m.Dirs++ // root itself

	// Distribute files over a tree of width^depth directories.
	dirs := g.tree(*root, *depth, *width)
	perDir := int(math.Ceil(float64(*files) / float64(len(dirs))))
	for _, d := range dirs {
		for i := 0; i < perDir; i++ {
			g.file(filepath.Join(d, fmt.Sprintf("file-%05d.%s", i, g.ext())), g.size(*maxSize))
		}
	}

	if *hardlinks > 0 {
		g.hardlinks(dirs, *hardlinks)
	}
	if *symlinkLoop {
		g.symlinkLoop(*root)
	}
	if *unreadable {
		g.unreadable(*root)
	}
	if *wide > 0 {
		g.wide(*root, *wide)
	}
	if *nonUTF8 {
		g.file(filepath.Join(*root, "latin1-caf\xe9.txt"), 1000)
		g.file(filepath.Join(*root, "bad-\xff\xfe-bytes.bin"), 2000)
	}
	if *longNames {
		g.file(filepath.Join(*root, strings.Repeat("n", 250)+".long"), 3000)
	}
	if *sparse {
		p := filepath.Join(*root, "sparse-1GiB.img")
		f, err := os.Create(p)
		must(err)
		must(f.Truncate(1 << 30))
		_, err = f.WriteAt([]byte("data"), 0)
		must(err)
		must(f.Close())
		g.m.Files++
		g.m.ApparentBytes += 1 << 30
	}

	out, _ := json.MarshalIndent(g.m, "", "  ")
	manifest := filepath.Join(filepath.Dir(*root), filepath.Base(*root)+".manifest.json")
	must(os.WriteFile(manifest, out, 0o644))
	fmt.Printf("fixture: %s\nmanifest: %s\n%s\n", *root, manifest, out)
}

func (g *gen) tree(root string, depth, width int) []string {
	dirs := []string{root}
	frontier := []string{root}
	for d := 1; d <= depth; d++ {
		var next []string
		for _, p := range frontier {
			for i := 0; i < width; i++ {
				child := filepath.Join(p, fmt.Sprintf("d%d-%02d", d, i))
				must(os.Mkdir(child, 0o755))
				g.m.Dirs++
				dirs = append(dirs, child)
				next = append(next, child)
			}
		}
		frontier = next
	}
	return dirs
}

var exts = []string{"mkv", "mp4", "jpg", "flac", "txt", "pdf", "iso", "zip", "log", ""}

func (g *gen) ext() string {
	e := exts[g.rng.IntN(len(exts))]
	if e == "" {
		return "noext"
	}
	return e
}

// size draws a log-uniform size in [0, max]; a few files are empty.
func (g *gen) size(max int64) int64 {
	if g.rng.IntN(50) == 0 {
		return 0
	}
	lo, hi := math.Log(64), math.Log(float64(max))
	return int64(math.Exp(lo + g.rng.Float64()*(hi-lo)))
}

func (g *gen) file(path string, size int64) {
	f, err := os.Create(path)
	must(err)
	if g.write {
		remaining := size
		for remaining > 0 {
			n := int64(len(g.buf))
			if remaining < n {
				n = remaining
			}
			_, err := f.Write(g.buf[:n])
			must(err)
			remaining -= n
		}
	} else if size > 0 {
		must(f.Truncate(size))
	}
	must(f.Close())
	g.m.Files++
	g.m.ApparentBytes += size
}

func (g *gen) hardlinks(dirs []string, n int) {
	for i := 0; i < n; i++ {
		src := filepath.Join(dirs[g.rng.IntN(len(dirs))], "file-00000.mkv")
		st, err := os.Stat(src)
		if err != nil {
			continue
		}
		dst := filepath.Join(dirs[g.rng.IntN(len(dirs))], fmt.Sprintf("hardlink-%03d.mkv", i))
		if err := os.Link(src, dst); err != nil {
			continue
		}
		g.m.HardlinkDups++
		g.m.HardlinkBytes += st.Size()
	}
}

func (g *gen) symlinkLoop(root string) {
	a := filepath.Join(root, "loop-a")
	b := filepath.Join(root, "loop-b")
	must(os.Mkdir(a, 0o755))
	must(os.Mkdir(b, 0o755))
	g.m.Dirs += 2
	must(os.Symlink("../loop-b", filepath.Join(a, "to-b")))
	must(os.Symlink("../loop-a", filepath.Join(b, "to-a")))
	must(os.Symlink("does-not-exist", filepath.Join(root, "dangling")))
	must(os.Symlink("..", filepath.Join(root, "to-parent")))
	g.m.Symlinks += 4
}

func (g *gen) unreadable(root string) {
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "note: running as root; unreadable directory would still be readable, skipping")
		return
	}
	p := filepath.Join(root, "unreadable")
	must(os.Mkdir(p, 0o755))
	g.file(filepath.Join(p, "hidden-from-scan.bin"), 12345)
	g.m.Files--
	g.m.ApparentBytes -= 12345 // not visible to a non-root scanner
	must(os.Chmod(p, 0o000))
	g.m.Dirs++
	g.m.UnreadableDirs++
}

func (g *gen) wide(root string, n int) {
	p := filepath.Join(root, "wide")
	must(os.Mkdir(p, 0o755))
	g.m.Dirs++
	for i := 0; i < n; i++ {
		g.file(filepath.Join(p, fmt.Sprintf("w%07d.txt", i)), 100)
	}
	g.m.WideDirEntries = n
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
