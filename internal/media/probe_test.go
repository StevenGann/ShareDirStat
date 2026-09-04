package media

import (
	"bytes"
	"sync"
	"testing"
)

// fixture pairs a synthetic file with the exact result Probe must return.
type fixture struct {
	name string
	ext  string
	data []byte
	want uint32
	ok   bool
}

// fixtures is shared by the exact-duration test and the adversarial sweeps.
func fixtures() []fixture {
	return []fixture{
		{"mp4 moov after mdat v0", "mp4", mp4File(mvhdV0(1000, 90000)), 90, true},
		{"mp4 rounds half up", "m4v", mp4File(mvhdV0(1000, 90500)), 91, true},
		{"mp4 sub-half-second is 1", "mov", mp4File(mvhdV0(1000, 400)), 1, true},
		{"mp4 mvhd v1", "m4a", mp4File(mvhdV1(90000, 90000*63+45000)), 64, true},
		{"mp4 64-bit moov size", "3gp",
			cat(mp4Box("ftyp", []byte("isomiso2avc1mp41")),
				mp4Box("mdat", make([]byte, 900)),
				mp4Box64("moov", mvhdV0(1000, 90000))), 90, true},
		{"mp4 timescale zero", "mp4", mp4File(mvhdV0(0, 90000)), 0, false},
		{"mp4 duration overflows uint32", "m4b", mp4File(mvhdV1(1, 1<<40)), 0, false},

		{"mkv float64 explicit scale", "mkv", mkvFile(120000, true, 1_000_000, false), 120, true},
		{"mkv float32 default scale", "webm", mkvFile(30000, false, 0, false), 30, true},
		{"mkv rounds half up", "mka", mkvFile(45500, true, 0, false), 46, true},
		{"mkv unknown-size segment", "mkv", mkvFile(120000, true, 1_000_000, true), 120, true},
		{"mkv custom scale", "mkv", mkvFile(60000, true, 500_000, false), 30, true},

		// 200 frames x 417 bytes at 128 kbit/s: 83400*8/128000 = 5.2125 s.
		{"mp3 CBR", "mp3", mp3CBR(200), 5, true},
		// 3830 frames x 1152 samples at 44.1 kHz = 100.04 s.
		{"mp3 Xing VBR", "mp3", mp3Xing(3830), 100, true},
		{"mp3 ID3v2 then Xing", "mp3", id3v2(2000, mp3Xing(3830)), 100, true},
		{"mp3 ID3v2 then CBR", "mp3", id3v2(300, mp3CBR(200)), 5, true},

		{"flac", "flac", flacFile(44100, 441000), 10, true},
		{"flac with ID3v2", "flac", id3v2(500, flacFile(44100, 441000)), 10, true},
		{"flac unknown total samples", "flac", flacFile(44100, 0), 0, false},

		{"ogg vorbis", "ogg", oggVorbisFile(44100, 441000), 10, true},
		{"oga vorbis", "oga", oggVorbisFile(48000, 48000*7), 7, true},
		// (960312 - 312 pre-skip) / 48000 = 20 s.
		{"opus granule tail page", "opus", oggOpusFile(312, 960312), 20, true},
		// Theora granule (100<<6)|25 at 30 fps: 126 frames = 4.2 s; the
		// Vorbis granule would say 25 s and must lose to the video stream.
		{"ogv theora over vorbis", "ogv", oggTheoraVorbisFile(100<<6|25, 44100*25), 4, true},
		{"spx speex", "spx", oggSpeexFile(16000, 16000*9), 9, true},

		{"wav", "wav", wavFile(8000, 24000, 24000), 3, true},
		{"wav streamed data size", "wav", wavFile(8000, 0xFFFFFFFF, 16000), 2, true},
		{"wav zero byte rate", "wav", wavFile(0, 24000, 24000), 0, false},

		// 33333 us x 3000 frames = 99.999 s.
		{"avi", "avi", aviFile(33333, 3000), 100, true},
		{"avi zero frames", "avi", aviFile(33333, 0), 0, false},

		{"aiff 80-bit float rate", "aiff", aiffFile(44100, 132300), 3, true},
		{"aif odd rate", "aif", aiffFile(22050.5, 132300), 6, true},

		// Play 98 s minus 3 s preroll = 95 s; without the subtraction this
		// would come out 98.
		{"asf preroll subtraction", "wma", asfFile(980_000_000, 3000, 0x02), 95, true},
		{"asf broadcast flag", "wmv", asfFile(980_000_000, 3000, 0x03), 0, false},

		// 100 x 200-byte frames, 1024 samples each at 44.1 kHz = 2.32 s.
		{"adts estimate", "aac", adtsFile(100), 2, true},
		{"aac without adts sync", "aac", []byte("this is not ADTS audio data at all........"), 0, false},

		{"empty", "mp4", nil, 0, false},
		{"tiny", "mp3", []byte{0xFF}, 0, false},
		{"tiny wav", "wav", []byte("RIFF"), 0, false},
	}
}

func TestKnownExt(t *testing.T) {
	for _, ext := range []string{
		"mp4", "m4a", "m4v", "m4b", "mov", "3gp", "3g2",
		"mkv", "mka", "webm", "mp3", "flac",
		"ogg", "oga", "ogv", "opus", "spx",
		"wav", "avi", "aiff", "aif", "wma", "wmv", "asf", "aac",
	} {
		if !KnownExt(ext) {
			t.Errorf("KnownExt(%q) = false, want true", ext)
		}
	}
	for _, ext := range []string{"", "txt", "MP3", ".mp3", "mp5", "jpeg"} {
		if KnownExt(ext) {
			t.Errorf("KnownExt(%q) = true, want false", ext)
		}
	}
}

func TestProbe(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.name, func(t *testing.T) {
			sec, ok := Probe(bytes.NewReader(f.data), int64(len(f.data)), f.ext)
			if sec != f.want || ok != f.ok {
				t.Fatalf("Probe(%s %q, %d bytes) = (%d, %v), want (%d, %v)",
					f.name, f.ext, len(f.data), sec, ok, f.want, f.ok)
			}
		})
	}
}

// TestProbeTruncated feeds every prefix of every fixture to its parser. Any
// result is acceptable except a panic or a nonsense success (ok with 0
// seconds).
func TestProbeTruncated(t *testing.T) {
	for _, f := range fixtures() {
		for i := 0; i <= len(f.data); i++ {
			sec, ok := Probe(bytes.NewReader(f.data[:i]), int64(i), f.ext)
			if ok && sec == 0 {
				t.Fatalf("%s truncated to %d bytes: ok with 0 seconds", f.name, i)
			}
		}
	}
}

// TestProbeMutated flips bytes throughout each fixture (with a stride cap on
// the big ones) and asserts no panic and no zero-second success.
func TestProbeMutated(t *testing.T) {
	for _, f := range fixtures() {
		if len(f.data) == 0 {
			continue
		}
		stride := len(f.data)/2048 + 1
		buf := make([]byte, len(f.data))
		for i := 0; i < len(f.data); i += stride {
			copy(buf, f.data)
			buf[i] ^= 0xFF
			sec, ok := Probe(bytes.NewReader(buf), int64(len(buf)), f.ext)
			if ok && sec == 0 {
				t.Fatalf("%s with byte %d flipped: ok with 0 seconds", f.name, i)
			}
		}
	}
}

// TestProbeCrossFormat probes every fixture under every extension; the wrong
// parser must fail or produce a bounded result without panicking.
func TestProbeCrossFormat(t *testing.T) {
	exts := []string{
		"mp4", "m4a", "m4v", "m4b", "mov", "3gp", "3g2", "mkv", "mka",
		"webm", "mp3", "flac", "ogg", "oga", "ogv", "opus", "spx", "wav",
		"avi", "aiff", "aif", "wma", "wmv", "asf", "aac",
	}
	for _, f := range fixtures() {
		for _, ext := range exts {
			sec, ok := Probe(bytes.NewReader(f.data), int64(len(f.data)), ext)
			if ok && sec == 0 {
				t.Fatalf("%s as %q: ok with 0 seconds", f.name, ext)
			}
		}
	}
}

// TestProbeGarbage runs every parser over deterministic pseudo-random bytes
// and a few pathological constants.
func TestProbeGarbage(t *testing.T) {
	garbage := make([]byte, 64<<10)
	state := uint32(0x2545F491)
	for i := range garbage {
		state = state*1664525 + 1013904223
		garbage[i] = byte(state >> 24)
	}
	inputs := [][]byte{
		garbage,
		bytes.Repeat([]byte{0xFF}, 4096),
		bytes.Repeat([]byte{0x00}, 4096),
		bytes.Repeat([]byte("OggS"), 1024),
		bytes.Repeat([]byte("RIFF"), 1024),
	}
	exts := []string{
		"mp4", "mkv", "mp3", "flac", "ogg", "opus", "ogv", "spx", "wav",
		"avi", "aiff", "wma", "aac",
	}
	for gi, g := range inputs {
		for _, ext := range exts {
			sec, ok := Probe(bytes.NewReader(g), int64(len(g)), ext)
			if ok && sec == 0 {
				t.Fatalf("garbage input %d as %q: ok with 0 seconds", gi, ext)
			}
		}
	}
}

// TestProbeLyingSize passes a size larger than the actual data, as a scanner
// might after a file shrinks mid-crawl.
func TestProbeLyingSize(t *testing.T) {
	for _, f := range fixtures() {
		Probe(bytes.NewReader(f.data), int64(len(f.data))+100_000, f.ext)
		Probe(bytes.NewReader(f.data), 1<<62, f.ext)
	}
}

func TestProbeNilAndBadArgs(t *testing.T) {
	if sec, ok := Probe(nil, 100, "mp3"); ok || sec != 0 {
		t.Fatalf("Probe(nil) = (%d, %v)", sec, ok)
	}
	if sec, ok := Probe(bytes.NewReader(nil), 0, "mp3"); ok || sec != 0 {
		t.Fatalf("Probe(size 0) = (%d, %v)", sec, ok)
	}
	if sec, ok := Probe(bytes.NewReader([]byte{1}), -5, "mp3"); ok || sec != 0 {
		t.Fatalf("Probe(negative size) = (%d, %v)", sec, ok)
	}
	if sec, ok := Probe(bytes.NewReader([]byte{1}), 1, "nope"); ok || sec != 0 {
		t.Fatalf("Probe(unknown ext) = (%d, %v)", sec, ok)
	}
}

// TestProbeConcurrent exercises Probe from several goroutines so the race
// detector can see any shared mutable state.
func TestProbeConcurrent(t *testing.T) {
	fixes := fixtures()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, f := range fixes {
				sec, ok := Probe(bytes.NewReader(f.data), int64(len(f.data)), f.ext)
				if sec != f.want || ok != f.ok {
					t.Errorf("%s: (%d, %v), want (%d, %v)", f.name, sec, ok, f.want, f.ok)
				}
			}
		}()
	}
	wg.Wait()
}

func TestFloat80(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{44100, 44100}, {48000, 48000}, {8000, 8000}, {22050.5, 22050.5},
		{11025, 11025}, {96000, 96000}, {1, 1},
	}
	for _, c := range cases {
		if got := float80(f80(c.in)); got != c.want {
			t.Errorf("float80(f80(%v)) = %v", c.in, got)
		}
	}
	if got := float80(make([]byte, 10)); got != 0 {
		t.Errorf("float80(zero bytes) = %v, want 0", got)
	}
	if got := float80([]byte{0x7F, 0xFF, 1, 2, 3, 4, 5, 6, 7, 8}); got == got {
		t.Errorf("float80(exp 0x7FFF) = %v, want NaN", got)
	}
	if got := float80(nil); got == got {
		t.Errorf("float80(nil) = %v, want NaN", got)
	}
}
