package media

import "encoding/binary"

// ASF object GUIDs, as they appear on disk (mixed-endian GUID encoding).
var (
	asfHeaderGUID = [16]byte{
		0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11,
		0xA6, 0xD9, 0x00, 0xAA, 0x00, 0x62, 0xCE, 0x6C,
	}
	asfFilePropsGUID = [16]byte{
		0xA1, 0xDC, 0xAB, 0x8C, 0x47, 0xA9, 0xCF, 0x11,
		0x8E, 0xE4, 0x00, 0xC0, 0x0C, 0x20, 0x53, 0x65,
	}
)

// probeASF handles ASF (wma, wmv, asf). It walks the header objects to the
// File Properties Object, whose Play Duration (100 ns units) includes the
// Preroll (milliseconds), which is subtracted back out. Broadcast files
// (flags bit 0) carry an unreliable duration and report false.
func probeASF(p *probeReader) (uint32, bool) {
	var h [30]byte
	b := p.readInto(0, h[:])
	if len(b) < 30 || [16]byte(b[0:16]) != asfHeaderGUID {
		return 0, false
	}
	count := binary.LittleEndian.Uint32(b[24:28])
	if count > 64 {
		count = 64
	}
	off := int64(30)
	for i := uint32(0); i < count && off+24 <= p.size; i++ {
		room, ok := remaining(off, p.size)
		if !ok {
			return 0, false
		}
		var oh [92]byte // object header + the File Properties fields we need
		ob := p.readInto(off, oh[:])
		if len(ob) < 24 {
			return 0, false
		}
		size := binary.LittleEndian.Uint64(ob[16:24])
		if size < 24 || size > room {
			return 0, false
		}
		if [16]byte(ob[0:16]) == asfFilePropsGUID {
			// Payload offsets: play duration at 40, preroll at 56,
			// flags at 64 (all after the 24-byte object header).
			if len(ob) < 92 || size < 92 {
				return 0, false
			}
			play := binary.LittleEndian.Uint64(ob[24+40 : 24+48])
			preroll := binary.LittleEndian.Uint64(ob[24+56 : 24+64])
			flags := binary.LittleEndian.Uint32(ob[24+64 : 24+68])
			if flags&1 != 0 { // broadcast: duration field is not meaningful
				return 0, false
			}
			if preroll > play/10_000 { // also guards the multiply below
				return 0, false
			}
			prerollUnits := preroll * 10_000 // ms → 100 ns units
			if play <= prerollUnits {
				return 0, false
			}
			return toSeconds(float64(play-prerollUnits) / 1e7)
		}
		step, ok := asOffset(size)
		if !ok {
			return 0, false
		}
		off += step
	}
	return 0, false
}
