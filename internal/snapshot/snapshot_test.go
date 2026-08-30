package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

func sampleGeneration(t *testing.T, nodes int) *model.Generation {
	t.Helper()
	b := model.NewBuilder("media", "/shares/media", model.BasisApparent, 0, nodes+8)
	dirs, err := b.AddChildren(b.Root(), []model.Entry{
		{Name: "Movies", Kind: model.KindDir, Mtime: 1000},
		{Name: "weird\xff\xfename", Kind: model.KindFile, Size: 7, Mtime: 1001},
		{Name: "link", Kind: model.KindSymlink, Size: 5, Mtime: 1002},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	kids := make([]model.Entry, nodes)
	for i := range kids {
		kids[i] = model.Entry{
			Name:  filepath.Base(strings.Repeat("x", i%7+1)) + ".mkv",
			Kind:  model.KindFile,
			Size:  uint64((i + 1) * 1000),
			Alloc: uint64((i + 1) * 1024),
			Mtime: int64(1700000000 + i),
			Mode:  0o644,
			UID:   1000,
			GID:   1000,
		}
	}
	if _, err := b.AddChildren(dirs[0], kids, nil); err != nil {
		t.Fatal(err)
	}
	b.AddError(model.ScanError{Path: "/shares/media/locked", Op: "open", Errno: "EACCES", Message: "permission denied"})
	return b.Finalize(model.GenerationMeta{
		ID: "01J6X4TEST", ScannedAt: time.Unix(1700000000, 0).UTC(),
		Duration: 3 * time.Second, Trigger: "manual", ScanID: "scan-1",
	}, 10)
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	orig := sampleGeneration(t, 50)

	size, err := st.Save(orig)
	if err != nil {
		t.Fatal(err)
	}
	if size <= 0 {
		t.Error("snapshot is empty")
	}

	got, hdr, err := st.Load("media")
	if err != nil {
		t.Fatal(err)
	}
	if hdr.ShareID != "media" || hdr.RootPath != "/shares/media" {
		t.Errorf("header = %+v", hdr)
	}
	if got.ID() != orig.ID() || got.Basis() != orig.Basis() {
		t.Errorf("meta lost: %+v", got.Meta())
	}
	if got.Stats() != orig.Stats() {
		t.Errorf("stats:\n got %+v\nwant %+v", got.Stats(), orig.Stats())
	}
	if got.NodeCount() != orig.NodeCount() {
		t.Errorf("node count %d != %d", got.NodeCount(), orig.NodeCount())
	}
	if !got.ScannedAt().Equal(orig.ScannedAt()) {
		t.Errorf("scannedAt %v != %v", got.ScannedAt(), orig.ScannedAt())
	}

	// Every node must survive byte for byte, names included.
	for _, p := range []string{"", "Movies", "weird\xff\xfename", "link"} {
		a, ok1 := orig.Info(p)
		b, ok2 := got.Info(p)
		if !ok1 || !ok2 {
			t.Fatalf("path %q: resolved %v/%v", p, ok1, ok2)
		}
		if a.Size != b.Size || a.Alloc != b.Alloc || a.Kind != b.Kind || a.Mtime != b.Mtime ||
			a.UID != b.UID || a.GID != b.GID || a.Mode != b.Mode || a.Files != b.Files || a.Dirs != b.Dirs {
			t.Errorf("node %q differs:\n got %+v\nwant %+v", p, b, a)
		}
		if string(a.NameRaw) != string(b.NameRaw) {
			t.Errorf("node %q: raw name not preserved", p)
		}
	}
	if n, _ := got.Info("weird\xff\xfename"); n.NameValid {
		t.Error("non-UTF-8 name should be reported as invalid")
	}

	origExt, gotExt := orig.Extensions(), got.Extensions()
	if len(origExt) != len(gotExt) || origExt[0] != gotExt[0] {
		t.Errorf("extension table lost: %+v vs %+v", gotExt, origExt)
	}
	if tops, _ := got.Top("", 5, false, model.BasisApparent); len(tops) != 5 {
		t.Errorf("top list lost: %+v", tops)
	}
	errs, total, _ := got.Errors(0, 10)
	if total != 1 || errs[0].Errno != "EACCES" {
		t.Errorf("errors lost: %+v", errs)
	}
}

func TestLoadMissingIsNotAnError(t *testing.T) {
	st, _ := NewStore(t.TempDir())
	g, h, err := st.Load("absent")
	if err != nil || g != nil || h != nil {
		t.Errorf("missing snapshot: %v %v %v", g, h, err)
	}
}

func TestCorruptionDetected(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(dir)
	if _, err := st.Save(sampleGeneration(t, 20)); err != nil {
		t.Fatal(err)
	}
	path := st.Path("media")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("flipped byte", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[len(bad)-20] ^= 0xff
		if err := os.WriteFile(path, bad, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Load("media"); err == nil {
			t.Fatal("corrupt payload accepted")
		}
	})

	t.Run("truncated", func(t *testing.T) {
		if err := os.WriteFile(path, good[:len(good)/2], 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Load("media"); err == nil {
			t.Fatal("truncated snapshot accepted")
		}
	})

	t.Run("not a snapshot", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("hello, this is not a snapshot at all"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := st.Load("media")
		if !errors.Is(err, ErrBadMagic) {
			t.Fatalf("want ErrBadMagic, got %v", err)
		}
	})

	t.Run("future version", func(t *testing.T) {
		bad := append([]byte(nil), good...)
		bad[8] = 99 // format version
		if err := os.WriteFile(path, bad, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := st.Load("media")
		if !errors.Is(err, ErrVersion) {
			t.Fatalf("want ErrVersion, got %v", err)
		}
	})
}

func TestQuarantine(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(dir)
	if _, err := st.Save(sampleGeneration(t, 5)); err != nil {
		t.Fatal(err)
	}
	moved, err := st.Quarantine("media", "format version 99")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.Path("media")); !os.IsNotExist(err) {
		t.Error("original snapshot should be gone")
	}
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("quarantined file missing: %v", err)
	}
	reason, err := os.ReadFile(moved + ".reason.txt")
	if err != nil || !strings.Contains(string(reason), "99") {
		t.Errorf("reason file = %q %v", reason, err)
	}
	g, _, err := st.Load("media")
	if g != nil || err != nil {
		t.Errorf("after quarantine the share should look unscanned: %v %v", g, err)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(dir)
	if _, err := st.Save(sampleGeneration(t, 10)); err != nil {
		t.Fatal(err)
	}
	first, _, err := st.Load("media")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(sampleGeneration(t, 30)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(st.Dir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly one snapshot file, got %d", len(entries))
	}
	second, _, err := st.Load("media")
	if err != nil {
		t.Fatal(err)
	}
	if second.NodeCount() <= first.NodeCount() {
		t.Error("second save did not replace the first")
	}
}

func TestCompressionIsWorthwhile(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(dir)
	g := sampleGeneration(t, 20000)
	size, err := st.Save(g)
	if err != nil {
		t.Fatal(err)
	}
	uncompressed := int64(g.NodeCount()) * NodeRecordSize
	if size >= uncompressed {
		t.Errorf("snapshot %d bytes is no smaller than the raw arena %d", size, uncompressed)
	}
	perNode := float64(size) / float64(g.NodeCount())
	t.Logf("%d nodes: %d bytes (%.1f bytes/node compressed)", g.NodeCount(), size, perNode)
	if perNode > 40 {
		t.Errorf("%.1f bytes per node is worse than the 30 B/node design estimate", perNode)
	}
}

// A snapshot's declared array lengths are cross-checked only against a JSON
// header in the same file, and the CRC that would reject a forgery is not
// verified until the very end. Decoding must therefore never size an
// allocation from a declared length: a corrupt file has to fail as an error
// the caller can quarantine, not as a makeslice panic or an OOM kill, both of
// which bypass FR-DATA-02 and turn one bad file into a boot loop.
func TestReadPayloadRefusesImplausibleLengths(t *testing.T) {
	u64 := func(v uint64) []byte {
		return []byte{byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
			byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
	cases := map[string]struct {
		header  Header
		payload []byte
	}{
		"node count beyond the arena limit": {
			header:  Header{Nodes: 1 << 48},
			payload: u64(1 << 48),
		},
		"node count with no nodes behind it": {
			header:  Header{Nodes: 500_000_000},
			payload: u64(500_000_000),
		},
		"name bytes beyond the arena limit": {
			header:  Header{Nodes: 0, NameBytes: 1 << 40},
			payload: append(u64(0), u64(1<<40)...),
		},
		"name bytes with no bytes behind them": {
			header:  Header{Nodes: 0, NameBytes: 1 << 30},
			payload: append(u64(0), u64(1<<30)...),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked instead of returning an error: %v", r)
				}
			}()
			if _, err := readPayload(strings.NewReader(string(tc.payload)), tc.header); err == nil {
				t.Fatal("want an error, got nil")
			} else if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("want ErrCorrupt, got %v", err)
			}
		})
	}
}
