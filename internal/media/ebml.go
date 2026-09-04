package media

import (
	"encoding/binary"
	"math"
	"math/bits"
)

// Matroska/WebM element IDs (with the VINT length marker kept, as they appear
// on the wire).
const (
	ebmlHeaderID     = 0x1A45DFA3
	segmentID        = 0x18538067
	infoID           = 0x1549A966
	clusterID        = 0x1F43B675
	timestampScaleID = 0x2AD7B1
	durationID       = 0x4489
)

// probeEBML handles Matroska and WebM (mkv, mka, webm). It verifies the EBML
// header, then walks the Segment's top-level children by seeking until it
// reaches the Info element, and computes Duration × TimestampScale. Segments
// with unknown size (streamed files) are descended into; the walk gives up at
// the first Cluster, since a well-formed file places Info before the media
// data.
func probeEBML(p *probeReader) (uint32, bool) {
	id, sz, dataOff, ok := readElement(p, 0)
	if !ok || id != ebmlHeaderID || sz < 0 {
		return 0, false
	}
	// Locate the Segment; tolerate a couple of stray elements before it.
	off := dataOff + sz
	for i := 0; i < 4; i++ {
		id, sz, dataOff, ok = readElement(p, off)
		if !ok {
			return 0, false
		}
		if id == segmentID {
			segEnd := p.size
			if sz >= 0 && dataOff+sz < segEnd {
				segEnd = dataOff + sz
			}
			return probeSegment(p, dataOff, segEnd)
		}
		if sz < 0 { // unknown size on a non-Segment element: cannot skip
			return 0, false
		}
		off = dataOff + sz
	}
	return 0, false
}

// probeSegment walks the direct children of the Segment looking for Info.
func probeSegment(p *probeReader, off, end int64) (uint32, bool) {
	const maxChildren = 64
	for i := 0; i < maxChildren && off < end; i++ {
		id, sz, dataOff, ok := readElement(p, off)
		if !ok || sz < 0 {
			return 0, false
		}
		switch id {
		case infoID:
			return probeInfo(p, dataOff, sz)
		case clusterID:
			return 0, false
		}
		off = dataOff + sz
	}
	return 0, false
}

// probeInfo reads the whole Info payload (it is a small element; the read is
// capped) and extracts TimestampScale and Duration. Duration is a float32 or
// float64 count of TimestampScale ticks; the scale defaults to 1_000_000 ns.
func probeInfo(p *probeReader, off, sz int64) (uint32, bool) {
	const maxInfo = 16 << 10
	if sz <= 0 || sz > maxInfo {
		return 0, false
	}
	b := p.read(off, int(sz))
	if int64(len(b)) < sz {
		return 0, false
	}
	scale := uint64(1_000_000)
	duration := math.NaN()
	for pos, i := 0, 0; pos < len(b) && i < 64; i++ {
		id, n, ok := vint(b[pos:], true)
		if !ok {
			return 0, false
		}
		length, m, unknown, ok := vintSize(b[pos+n:])
		if !ok || unknown {
			return 0, false
		}
		val := pos + n + m
		if length > int64(len(b)-val) {
			return 0, false
		}
		body := b[val : val+int(length)]
		switch id {
		case timestampScaleID:
			if len(body) == 0 || len(body) > 8 {
				return 0, false
			}
			scale = 0
			for _, c := range body {
				scale = scale<<8 | uint64(c)
			}
		case durationID:
			switch len(body) {
			case 4:
				duration = float64(math.Float32frombits(binary.BigEndian.Uint32(body)))
			case 8:
				duration = math.Float64frombits(binary.BigEndian.Uint64(body))
			default:
				return 0, false
			}
		}
		pos = val + int(length)
	}
	if scale == 0 || math.IsNaN(duration) {
		return 0, false
	}
	return toSeconds(duration * float64(scale) / 1e9)
}

// readElement reads one element header at off: its ID, payload size (-1 for
// unknown size) and payload offset.
func readElement(p *probeReader, off int64) (id uint64, size, dataOff int64, ok bool) {
	var h [12]byte // max 4-byte ID + max 8-byte size
	b := p.readInto(off, h[:])
	if b == nil {
		return 0, 0, 0, false
	}
	id, n, ok := vint(b, true)
	if !ok || n > 4 {
		return 0, 0, 0, false
	}
	length, m, unknown, ok := vintSize(b[n:])
	if !ok {
		return 0, 0, 0, false
	}
	dataOff = off + int64(n) + int64(m)
	if unknown {
		return id, -1, dataOff, true
	}
	if dataOff+length > p.size {
		return 0, 0, 0, false
	}
	return id, length, dataOff, true
}

// vint decodes an EBML variable-length integer. keepMarker preserves the
// length-marker bit (element IDs are compared with it kept); size fields
// strip it.
func vint(b []byte, keepMarker bool) (val uint64, n int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	n = bits.LeadingZeros8(b[0]) + 1 // 1..8
	if n > len(b) {
		return 0, 0, false
	}
	val = uint64(b[0])
	if !keepMarker {
		val &= 0xFF >> n
	}
	for i := 1; i < n; i++ {
		val = val<<8 | uint64(b[i])
	}
	return val, n, true
}

// vintSize decodes an element size field, reporting the all-ones
// "unknown size" encoding separately.
func vintSize(b []byte) (size int64, n int, unknown, ok bool) {
	v, n, ok := vint(b, false)
	if !ok {
		return 0, 0, false, false
	}
	if v == 1<<(7*n)-1 {
		return 0, n, true, true
	}
	if v > math.MaxInt64 {
		return 0, 0, false, false
	}
	return int64(v), n, false, true
}
