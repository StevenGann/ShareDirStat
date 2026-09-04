package media

import "encoding/binary"

// oggStream describes one logical stream identified from a BOS page.
type oggStream struct {
	serial   uint32
	kind     int // oggTheora > oggVorbis > ... is also the preference order
	rate     float64
	preSkip  uint64 // Opus: samples to subtract from the granule
	kfgShift uint   // Theora: keyframe granule shift
}

// Stream kinds, in descending preference: a video stream's duration is the
// file's duration when both are present.
const (
	oggTheora = iota
	oggVorbis
	oggOpus
	oggSpeex
)

// probeOgg handles the Ogg family (ogg, oga, ogv, opus, spx). It reads the
// beginning-of-stream pages to learn each stream's codec and rate, then scans
// the last 64 KiB for the final "OggS" page of the preferred stream and
// converts its granule position: samples/rate for Vorbis and Speex,
// (granules−pre-skip)/48000 for Opus, and frame count/fps for Theora (granule
// split by the keyframe shift from the ID header).
func probeOgg(p *probeReader) (uint32, bool) {
	streams := oggBOSStreams(p)
	if len(streams) == 0 {
		return 0, false
	}
	// Tail scan: the last valid page per serial wins; false syncs are
	// filtered by requiring a known serial and a plausible page header.
	const window = 64 << 10
	start := p.size - window
	if start < 0 {
		start = 0
	}
	buf := p.read(start, window)
	granules := map[uint32]uint64{}
	for i := 0; i+27 <= len(buf); i++ {
		if buf[i] != 'O' || buf[i+1] != 'g' || buf[i+2] != 'g' || buf[i+3] != 'S' || buf[i+4] != 0 {
			continue
		}
		granule := binary.LittleEndian.Uint64(buf[i+6 : i+14])
		serial := binary.LittleEndian.Uint32(buf[i+14 : i+18])
		if granule == ^uint64(0) { // page holds no packet end
			continue
		}
		for _, s := range streams {
			if s.serial == serial {
				granules[serial] = granule
			}
		}
	}
	best := -1
	for i, s := range streams {
		if _, ok := granules[s.serial]; !ok {
			continue
		}
		if best < 0 || s.kind < streams[best].kind {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	s := streams[best]
	g := granules[s.serial]
	if s.rate <= 0 {
		return 0, false
	}
	switch s.kind {
	case oggOpus:
		if g <= s.preSkip {
			return 0, false
		}
		return toSeconds(float64(g-s.preSkip) / 48000)
	case oggTheora:
		if s.kfgShift > 62 {
			return 0, false
		}
		frames := (g >> s.kfgShift) + (g & (1<<s.kfgShift - 1)) + 1
		return toSeconds(float64(frames) / s.rate)
	default:
		return toSeconds(float64(g) / s.rate)
	}
}

// oggBOSStreams reads the first pages of the file and returns the streams
// whose ID headers it recognizes. All BOS pages precede any data page, so a
// small bounded window and page count suffice.
func oggBOSStreams(p *probeReader) []oggStream {
	buf := p.read(0, 8<<10)
	var streams []oggStream
	off := 0
	for page := 0; page < 8 && off+27 <= len(buf); page++ {
		if string(buf[off:off+4]) != "OggS" || buf[off+4] != 0 {
			return streams
		}
		flags := buf[off+5]
		serial := binary.LittleEndian.Uint32(buf[off+14 : off+18])
		nsegs := int(buf[off+26])
		body := off + 27 + nsegs
		if body > len(buf) {
			return streams
		}
		bodyLen := 0
		for _, l := range buf[off+27 : body] {
			bodyLen += int(l)
		}
		end := body + bodyLen
		if end > len(buf) {
			end = len(buf)
		}
		if flags&0x02 == 0 { // first non-BOS page: every stream is known
			return streams
		}
		if s, ok := oggIdentify(buf[body:end], serial); ok {
			streams = append(streams, s)
		}
		off = body + bodyLen
	}
	return streams
}

// oggIdentify decodes the codec ID header found in a BOS page body.
func oggIdentify(b []byte, serial uint32) (oggStream, bool) {
	s := oggStream{serial: serial}
	switch {
	case len(b) >= 16 && string(b[0:7]) == "\x01vorbis":
		s.kind = oggVorbis
		s.rate = float64(binary.LittleEndian.Uint32(b[12:16]))
	case len(b) >= 12 && string(b[0:8]) == "OpusHead":
		s.kind = oggOpus
		s.rate = 48000 // Opus granules are always 48 kHz regardless of input
		s.preSkip = uint64(binary.LittleEndian.Uint16(b[10:12]))
	case len(b) >= 42 && string(b[0:7]) == "\x80theora":
		s.kind = oggTheora
		num := binary.BigEndian.Uint32(b[22:26])
		den := binary.BigEndian.Uint32(b[26:30])
		if den == 0 {
			return s, false
		}
		s.rate = float64(num) / float64(den)
		s.kfgShift = uint(b[40]&0x03)<<3 | uint(b[41]>>5)
	case len(b) >= 40 && string(b[0:8]) == "Speex   ":
		s.kind = oggSpeex
		s.rate = float64(binary.LittleEndian.Uint32(b[36:40]))
	default:
		return s, false
	}
	return s, true
}
