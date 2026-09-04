package scan

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

// wavBytes builds a minimal RIFF/WAVE file whose duration is
// dataLen/byteRate seconds.
func wavBytes(byteRate uint32, dataLen int) []byte {
	le := binary.LittleEndian
	body := make([]byte, 44+dataLen)
	copy(body[0:], "RIFF")
	le.PutUint32(body[4:], uint32(36+dataLen)) //nolint:gosec // test sizes are tiny
	copy(body[8:], "WAVE")
	copy(body[12:], "fmt ")
	le.PutUint32(body[16:], 16) // fmt chunk length
	le.PutUint16(body[20:], 1)  // PCM
	le.PutUint16(body[22:], 1)  // mono
	le.PutUint32(body[24:], 8000)
	le.PutUint32(body[28:], byteRate)
	le.PutUint16(body[32:], 1)
	le.PutUint16(body[34:], 8)
	copy(body[36:], "data")
	le.PutUint32(body[40:], uint32(dataLen)) //nolint:gosec // test sizes are tiny
	return body
}

// The crawl reads media playing times when asked to (FR-SCAN-25): a media
// file gets its duration, a non-media file none, a corrupt media file is
// silently treated as non-media, and directories aggregate.
func TestScanProbesMediaDurations(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Music"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 3 s and 2 s of "audio", one text file, one file lying about being WAV.
	if err := os.WriteFile(filepath.Join(root, "Music", "a.wav"), wavBytes(8000, 24000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Music", "b.wav"), wavBytes(8000, 16000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake.wav"), []byte("not really audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	gen, err := Run(context.Background(), Options{
		ShareID:        "s",
		Root:           root,
		Basis:          model.BasisApparent,
		Concurrency:    2,
		MediaDurations: true,
	}, model.GenerationMeta{ID: "g"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]uint32{
		"Music/a.wav": 3,
		"Music/b.wav": 2,
		"Music":       5,
		"":            5,
		"readme.txt":  0,
		"fake.wav":    0,
	} {
		n, ok := gen.Info(path)
		if !ok {
			t.Fatalf("resolve %q failed", path)
		}
		if n.Dur != want {
			t.Errorf("%q duration = %d, want %d", path, n.Dur, want)
		}
	}
	if st := gen.Stats(); st.MediaDur != 5 {
		t.Errorf("share media duration = %d, want 5", st.MediaDur)
	}

	// The same tree scanned with the feature off carries no media data.
	gen, err = Run(context.Background(), Options{
		ShareID:     "s",
		Root:        root,
		Basis:       model.BasisApparent,
		Concurrency: 2,
	}, model.GenerationMeta{ID: "g2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := gen.Info(""); n.Dur != 0 {
		t.Errorf("durations collected although disabled: %d", n.Dur)
	}
	if raw := gen.Export(); raw.Durs != nil {
		t.Error("duration array allocated although disabled")
	}
}
