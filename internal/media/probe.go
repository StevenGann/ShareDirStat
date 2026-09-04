// Package media extracts playing time from media files so the scanner can
// build a size-per-minute metric (one Probe call per media file during a
// crawl).
//
// Every parser reads only small bounded regions of the file — headers at the
// start, and for a few formats a window at the end — never the whole file. A
// hard per-file budget (readBudget) caps the total bytes read no matter what
// the file's own length fields claim, so a corrupt 4 GiB box size cannot
// cause a 4 GiB read or allocation. All parsers are written to return
// (0, false) rather than panic on truncated, corrupt or adversarial input.
package media

import (
	"io"
	"math"
)

// readBudget caps the total bytes any one Probe call may read. Typical files
// need a few KiB; the worst cases (MP3 sync scan, Ogg tail scan, ADTS
// sampling) stay well under this.
const readBudget = 4 << 20

// maxRead caps a single read. No parser needs a larger contiguous window.
const maxRead = 512 << 10

// parsers maps a lower-case extension (no dot) to its format parser. Multiple
// extensions share one parser per container family.
var parsers = map[string]func(*probeReader) (uint32, bool){
	// ISO base media file format.
	"mp4": probeISOBMFF, "m4a": probeISOBMFF, "m4v": probeISOBMFF,
	"m4b": probeISOBMFF, "mov": probeISOBMFF, "3gp": probeISOBMFF,
	"3g2": probeISOBMFF,
	// Matroska / WebM (EBML).
	"mkv": probeEBML, "mka": probeEBML, "webm": probeEBML,
	// MPEG audio.
	"mp3": probeMP3,
	// FLAC.
	"flac": probeFLAC,
	// Ogg family.
	"ogg": probeOgg, "oga": probeOgg, "ogv": probeOgg, "opus": probeOgg,
	"spx": probeOgg,
	// RIFF / IFF family.
	"wav": probeWAV, "avi": probeAVI, "aiff": probeAIFF, "aif": probeAIFF,
	// ASF.
	"wma": probeASF, "wmv": probeASF, "asf": probeASF,
	// Raw ADTS AAC.
	"aac": probeADTS,
}

// KnownExt reports whether ext — lower-case, without the leading dot — is a
// media type Probe understands.
func KnownExt(ext string) bool {
	_, ok := parsers[ext]
	return ok
}

// Probe examines an open media file and returns its playing time in seconds
// (rounded to nearest, minimum 1 for any positive duration), or ok=false when
// the duration cannot be determined. ext selects the parser (lower-case, no
// dot); size is the file's size in bytes. Probe seeks within r but reads only
// small bounded regions; it never reads the whole file.
func Probe(r io.ReadSeeker, size int64, ext string) (seconds uint32, ok bool) {
	parse := parsers[ext]
	if parse == nil || r == nil || size <= 0 {
		return 0, false
	}
	return parse(&probeReader{r: r, size: size, budget: readBudget})
}

// probeReader wraps the file with the size the caller reported and the
// remaining read budget. It is the only path parsers read through, so the
// budget bounds every parser uniformly.
type probeReader struct {
	r      io.ReadSeeker
	size   int64
	budget int64
}

// readInto reads into b starting at absolute offset off and returns the
// prefix of b actually filled (shorter near EOF, nil on any failure or when
// the budget is spent). Callers must bounds-check the returned length; a
// stack array passed here makes header reads allocation-free.
func (p *probeReader) readInto(off int64, b []byte) []byte {
	n := int64(len(b))
	if n == 0 || n > maxRead || off < 0 || off >= p.size {
		return nil
	}
	if rem := p.size - off; n > rem {
		n = rem
	}
	if n > p.budget {
		return nil
	}
	p.budget -= n
	if _, err := p.r.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	got, _ := io.ReadFull(p.r, b[:n])
	if got <= 0 {
		return nil
	}
	return b[:got]
}

// read is readInto with a fresh buffer, for window-sized reads. n is capped
// by maxRead and the remaining budget like every other read.
func (p *probeReader) read(off int64, n int) []byte {
	if n <= 0 || n > maxRead {
		return nil
	}
	if rem := p.size - off; off >= 0 && int64(n) > rem {
		n = int(rem)
	}
	if n <= 0 {
		return nil
	}
	return p.readInto(off, make([]byte, n))
}

// toSeconds converts a duration in seconds to the Probe result: rounded half
// up, minimum 1 for any positive duration; NaN, Inf, zero, negative and
// values that overflow uint32 report false.
func toSeconds(sec float64) (uint32, bool) {
	if math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 {
		return 0, false
	}
	r := math.Floor(sec + 0.5)
	if r < 1 {
		r = 1
	}
	if r > math.MaxUint32 {
		return 0, false
	}
	return uint32(r), true
}

// skipID3v2 returns the offset of the first byte after an ID3v2 tag at the
// start of the file (0 when none). MP3, FLAC and ADTS files commonly carry
// one; the footer flag adds a 10-byte trailer to the tag itself.
func skipID3v2(p *probeReader) int64 {
	var h [10]byte
	b := p.readInto(0, h[:])
	if len(b) < 10 || b[0] != 'I' || b[1] != 'D' || b[2] != '3' {
		return 0
	}
	// Tag size is a 28-bit syncsafe integer excluding the 10-byte header;
	// masking each byte to 7 bits tolerates writers that set the high bit.
	sz := int64(b[6]&0x7F)<<21 | int64(b[7]&0x7F)<<14 |
		int64(b[8]&0x7F)<<7 | int64(b[9]&0x7F)
	off := 10 + sz
	if b[5]&0x10 != 0 { // footer present
		off += 10
	}
	if off >= p.size {
		return 0
	}
	return off
}
