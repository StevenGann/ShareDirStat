package media

// probeFLAC handles FLAC. The mandatory first metadata block, STREAMINFO,
// carries the sample rate (20 bits) and total sample count (36 bits); a total
// of zero means the encoder did not know the length, which reports false. A
// leading ID3v2 tag is tolerated.
func probeFLAC(p *probeReader) (uint32, bool) {
	off := skipID3v2(p)
	// "fLaC" magic + 4-byte block header + the 18 STREAMINFO bytes we need.
	var h [26]byte
	b := p.readInto(off, h[:])
	if len(b) < 26 || string(b[0:4]) != "fLaC" {
		return 0, false
	}
	if b[4]&0x7F != 0 { // STREAMINFO must be the first block (type 0)
		return 0, false
	}
	blockLen := int(b[5])<<16 | int(b[6])<<8 | int(b[7])
	if blockLen < 34 {
		return 0, false
	}
	// STREAMINFO layout from byte 10 of its payload (byte 18 of b): a 64-bit
	// big-endian field packing rate:20 | channels-1:3 | bps-1:5 | total:36.
	var v uint64
	for _, c := range b[18:26] {
		v = v<<8 | uint64(c)
	}
	rate := v >> 44
	total := v & (1<<36 - 1)
	if rate == 0 || total == 0 {
		return 0, false
	}
	return toSeconds(float64(total) / float64(rate))
}
