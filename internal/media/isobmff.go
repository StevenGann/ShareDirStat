package media

import "encoding/binary"

// probeISOBMFF handles the ISO base media file format family (mp4, m4a, m4v,
// m4b, mov, 3gp, 3g2). It walks top-level boxes by seeking — so a moov placed
// after a multi-gigabyte mdat is still found with a handful of tiny reads —
// and reads moov/mvhd: timescale plus duration in that timescale.
func probeISOBMFF(p *probeReader) (uint32, bool) {
	const maxBoxes = 128
	var off int64
	for i := 0; i < maxBoxes && off < p.size; i++ {
		typ, dataOff, dataLen, ok := readBoxHeader(p, off, p.size)
		if !ok {
			return 0, false
		}
		if typ == "moov" {
			return probeMoov(p, dataOff, dataLen)
		}
		off = dataOff + dataLen
	}
	return 0, false
}

// readBoxHeader parses one box header at off (bounded by end) and returns
// the type plus payload offset and length. It handles 64-bit sizes (size==1)
// and size==0 ("extends to end of file"), and rejects any box that escapes
// [off, end) so a corrupt size field terminates the walk instead of running
// away.
func readBoxHeader(p *probeReader, off, end int64) (typ string, dataOff, dataLen int64, ok bool) {
	room, ok := remaining(off, end)
	if !ok {
		return "", 0, 0, false
	}
	var h [16]byte
	b := p.readInto(off, h[:])
	if len(b) < 8 {
		return "", 0, 0, false
	}
	size := int64(binary.BigEndian.Uint32(b[0:4]))
	typ = string(b[4:8])
	hdr := int64(8)
	switch size {
	case 0:
		size = end - off
	case 1:
		if len(b) < 16 {
			return "", 0, 0, false
		}
		wide := binary.BigEndian.Uint64(b[8:16])
		if wide > room {
			return "", 0, 0, false
		}
		if size, ok = asOffset(wide); !ok {
			return "", 0, 0, false
		}
		hdr = 16
	}
	if size < hdr || off+size > end {
		return "", 0, 0, false
	}
	return typ, off + hdr, size - hdr, true
}

// probeMoov walks the children of moov looking for mvhd and decodes its
// timescale and duration (version 0: 32-bit, version 1: 64-bit).
func probeMoov(p *probeReader, off, length int64) (uint32, bool) {
	const maxChildren = 64
	end := off + length
	for i := 0; i < maxChildren && off < end; i++ {
		typ, dataOff, dataLen, ok := readBoxHeader(p, off, end)
		if !ok {
			return 0, false
		}
		if typ == "mvhd" {
			return readMvhd(p, dataOff, dataLen)
		}
		off = dataOff + dataLen
	}
	return 0, false
}

func readMvhd(p *probeReader, off, length int64) (uint32, bool) {
	// Version 1 needs 32 payload bytes (version/flags + 8-byte times +
	// timescale + 8-byte duration); version 0 needs 20.
	var h [32]byte
	b := p.readInto(off, h[:])
	if len(b) < 20 || length < 20 {
		return 0, false
	}
	var timescale uint32
	var duration uint64
	switch b[0] {
	case 0:
		timescale = binary.BigEndian.Uint32(b[12:16])
		duration = uint64(binary.BigEndian.Uint32(b[16:20]))
		if duration == 0xFFFFFFFF { // unknown-duration sentinel
			return 0, false
		}
	case 1:
		if len(b) < 32 || length < 32 {
			return 0, false
		}
		timescale = binary.BigEndian.Uint32(b[20:24])
		duration = binary.BigEndian.Uint64(b[24:32])
		if duration == ^uint64(0) {
			return 0, false
		}
	default:
		return 0, false
	}
	if timescale == 0 {
		return 0, false
	}
	return toSeconds(float64(duration) / float64(timescale))
}
