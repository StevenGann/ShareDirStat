package snapshot

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

func mediaGeneration(t *testing.T) *model.Generation {
	t.Helper()
	b := model.NewBuilder("media", "/shares/media", model.BasisApparent, 0, 16)
	b.CollectMedia()
	dirs, err := b.AddChildren(b.Root(), []model.Entry{
		{Name: "Movies", Kind: model.KindDir, Mtime: 1000},
		{Name: "notes.txt", Kind: model.KindFile, Size: 10, Mtime: 1001},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddChildren(dirs[0], []model.Entry{
		{Name: "big.mkv", Kind: model.KindFile, Size: 6000, Dur: 60, Mtime: 1002},
		{Name: "small.mkv", Kind: model.KindFile, Size: 3000, Dur: 120, Mtime: 1003},
	}, nil); err != nil {
		t.Fatal(err)
	}
	return b.Finalize(model.GenerationMeta{
		ID: "01J6XMEDIA", ScannedAt: time.Unix(1700000000, 0).UTC(), Trigger: "manual", ScanID: "scan-m",
	}, 10)
}

// fileVersion reads the formatVersion field straight out of a snapshot file.
func fileVersion(t *testing.T, path string) uint16 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 10 {
		t.Fatalf("snapshot too short: %d bytes", len(raw))
	}
	return binary.LittleEndian.Uint16(raw[8:10])
}

func TestMediaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	orig := mediaGeneration(t)
	if _, err := st.Save(orig); err != nil {
		t.Fatal(err)
	}
	if v := fileVersion(t, st.Path("media")); v != FormatVersion {
		t.Errorf("media snapshot written as version %d, want %d", v, FormatVersion)
	}

	loaded, _, err := st.Load("media")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]struct {
		dur   uint32
		bytes uint64
	}{
		"Movies/big.mkv":   {60, 6000},
		"Movies/small.mkv": {120, 3000},
		"Movies":           {180, 9000},
		"":                 {180, 9000},
		"notes.txt":        {0, 0},
	} {
		n, ok := loaded.Info(path)
		if !ok {
			t.Fatalf("resolve %q failed after load", path)
		}
		if n.Dur != want.dur || n.MediaSize != want.bytes {
			t.Errorf("%q media = %ds/%dB, want %ds/%dB", path, n.Dur, n.MediaSize, want.dur, want.bytes)
		}
	}
	if st := loaded.Stats(); st.MediaDur != 180 || st.MediaSize != 9000 {
		t.Errorf("stats media = %ds/%dB after load", st.MediaDur, st.MediaSize)
	}
}

// A generation without media arrays must keep writing the old layout, so a
// share that never grew them stays readable by an older binary (FR-DATA-02
// applies only when the layout actually changed).
func TestMediaFreeSnapshotStaysVersion1(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(sampleGeneration(t, 5)); err != nil {
		t.Fatal(err)
	}
	if v := fileVersion(t, st.Path("media")); v != minFormatVersion {
		t.Errorf("media-free snapshot written as version %d, want %d", v, minFormatVersion)
	}
	loaded, _, err := st.Load("media")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := loaded.Info(""); n.Dur != 0 || n.MediaSize != 0 {
		t.Errorf("version-1 load grew media data: %+v", n)
	}
}
