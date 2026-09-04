package media

import "encoding/binary"

// mp3Frame describes one decoded MPEG audio frame header.
type mp3Frame struct {
	version    int // 1, 2, or 25 (MPEG-2.5)
	layer      int // 1..3
	bitrate    int // bits per second
	sampleRate int
	samples    int // samples per frame
	frameLen   int // bytes, including the header
	mono       bool
}

// mp3Bitrates holds bitrate table columns in kbit/s, indexed 1..14.
var mp3Bitrates = map[[2]int][15]int{
	{1, 1}: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
	{1, 2}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
	{1, 3}: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
	{2, 1}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
	{2, 2}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	{2, 3}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
}

var mp3Rates = map[int][3]int{
	1:  {44100, 48000, 32000},
	2:  {22050, 24000, 16000},
	25: {11025, 12000, 8000},
}

// parseMP3Header decodes a 4-byte MPEG audio frame header, rejecting reserved
// and free-format values.
func parseMP3Header(b []byte) (mp3Frame, bool) {
	var f mp3Frame
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return f, false
	}
	switch (b[1] >> 3) & 3 {
	case 0:
		f.version = 25
	case 2:
		f.version = 2
	case 3:
		f.version = 1
	default:
		return f, false
	}
	switch (b[1] >> 1) & 3 {
	case 1:
		f.layer = 3
	case 2:
		f.layer = 2
	case 3:
		f.layer = 1
	default:
		return f, false
	}
	brIdx := int(b[2] >> 4)
	srIdx := int(b[2]>>2) & 3
	if brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return f, false
	}
	tblVer := f.version
	if tblVer == 25 {
		tblVer = 2
	}
	f.bitrate = mp3Bitrates[[2]int{tblVer, f.layer}][brIdx] * 1000
	f.sampleRate = mp3Rates[f.version][srIdx]
	f.mono = b[3]>>6 == 3
	switch {
	case f.layer == 1:
		f.samples = 384
	case f.layer == 2 || f.version == 1:
		f.samples = 1152
	default: // MPEG-2/2.5 layer III
		f.samples = 576
	}
	padding := int(b[2]>>1) & 1
	if f.layer == 1 {
		f.frameLen = (12*f.bitrate/f.sampleRate + padding) * 4
	} else {
		f.frameLen = f.samples/8*f.bitrate/f.sampleRate + padding
	}
	if f.frameLen < 4 {
		return f, false
	}
	return f, true
}

// probeMP3 handles MPEG audio (mp3). It skips a leading ID3v2 tag, scans a
// bounded window for a valid frame header, and prefers an exact frame count
// from a Xing/Info or VBRI tag; otherwise it makes a CBR estimate from the
// first frame's bitrate, after validating a second consecutive frame header
// to reject false syncs.
func probeMP3(p *probeReader) (uint32, bool) {
	start := skipID3v2(p)
	const window = 64 << 10
	buf := p.read(start, window)
	for i := 0; i+4 <= len(buf); i++ {
		f, ok := parseMP3Header(buf[i:])
		if !ok {
			continue
		}
		if sec, ok := mp3VBRDuration(buf[i:], f); ok {
			return toSeconds(sec)
		}
		// No VBR tag: require a second consecutive frame that agrees on
		// version, layer and sample rate before trusting a CBR estimate.
		off := start + int64(i)
		var next []byte
		if i+f.frameLen+4 <= len(buf) {
			next = buf[i+f.frameLen : i+f.frameLen+4]
		} else {
			var h [4]byte
			next = p.readInto(off+int64(f.frameLen), h[:])
		}
		n, ok := parseMP3Header(next)
		if !ok || n.version != f.version || n.layer != f.layer || n.sampleRate != f.sampleRate {
			continue
		}
		return toSeconds(float64(p.size-off) * 8 / float64(f.bitrate))
	}
	return 0, false
}

// mp3VBRDuration returns the exact duration from a Xing/Info or VBRI tag in
// the frame beginning at b[0], when present with a frame count.
func mp3VBRDuration(b []byte, f mp3Frame) (float64, bool) {
	// Xing/Info sits after the side info block, whose length depends on
	// MPEG version and channel mode.
	xing := 4
	switch {
	case f.version == 1 && f.mono:
		xing += 17
	case f.version == 1:
		xing += 32
	case f.mono:
		xing += 9
	default:
		xing += 17
	}
	if xing+12 <= len(b) {
		tag := string(b[xing : xing+4])
		if tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(b[xing+4 : xing+8])
			if flags&1 != 0 {
				frames := binary.BigEndian.Uint32(b[xing+8 : xing+12])
				return float64(frames) * float64(f.samples) / float64(f.sampleRate), true
			}
		}
	}
	// VBRI (Fraunhofer) sits at a fixed 32-byte offset past the header.
	const vbri = 4 + 32
	if vbri+18 <= len(b) && string(b[vbri:vbri+4]) == "VBRI" {
		frames := binary.BigEndian.Uint32(b[vbri+14 : vbri+18])
		return float64(frames) * float64(f.samples) / float64(f.sampleRate), true
	}
	return 0, false
}
