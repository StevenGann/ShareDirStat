package media

import (
	"encoding/binary"
	"math"
)

// maxChunks bounds every chunk walk in the RIFF/IFF family.
const maxChunks = 64

// probeWAV handles RIFF WAVE (wav): duration = data chunk size ÷ the fmt
// chunk's average bytes per second. Streamed files that declare a data size
// of 0 or 0xFFFFFFFF fall back to the bytes remaining after the data header.
func probeWAV(p *probeReader) (uint32, bool) {
	var h [12]byte
	b := p.readInto(0, h[:])
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, false
	}
	var byteRate uint32
	var dataLen float64 = -1
	off := int64(12)
	for i := 0; i < maxChunks && off+8 <= p.size; i++ {
		var ch [8]byte
		cb := p.readInto(off, ch[:])
		if len(cb) < 8 {
			break
		}
		id := string(cb[0:4])
		sz := binary.LittleEndian.Uint32(cb[4:8])
		switch id {
		case "fmt ":
			var fb [12]byte
			f := p.readInto(off+8, fb[:])
			if len(f) < 12 || sz < 12 {
				return 0, false
			}
			byteRate = binary.LittleEndian.Uint32(f[8:12])
		case "data":
			dataLen = float64(sz)
			if sz == 0 || sz == 0xFFFFFFFF || off+8+int64(sz) > p.size {
				dataLen = float64(p.size - (off + 8))
			}
		}
		if byteRate != 0 && dataLen >= 0 {
			return toSeconds(dataLen / float64(byteRate))
		}
		off += 8 + int64(sz) + int64(sz&1) // chunks are word-aligned
	}
	return 0, false
}

// probeAVI handles RIFF AVI (avi): duration = dwMicroSecPerFrame ×
// dwTotalFrames from the avih chunk inside the hdrl list.
func probeAVI(p *probeReader) (uint32, bool) {
	var h [12]byte
	b := p.readInto(0, h[:])
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "AVI " {
		return 0, false
	}
	off := int64(12)
	for i := 0; i < maxChunks && off+12 <= p.size; i++ {
		var ch [12]byte
		cb := p.readInto(off, ch[:])
		if len(cb) < 12 {
			break
		}
		sz := binary.LittleEndian.Uint32(cb[4:8])
		if string(cb[0:4]) == "LIST" && string(cb[8:12]) == "hdrl" {
			return probeAVIHeaderList(p, off+12, int64(sz)-4)
		}
		off += 8 + int64(sz) + int64(sz&1)
	}
	return 0, false
}

// probeAVIHeaderList walks the hdrl list's chunks to the avih header.
func probeAVIHeaderList(p *probeReader, off, length int64) (uint32, bool) {
	end := off + length
	if end > p.size {
		end = p.size
	}
	for i := 0; i < maxChunks && off+8 <= end; i++ {
		var ch [28]byte
		cb := p.readInto(off, ch[:])
		if len(cb) < 8 {
			break
		}
		sz := binary.LittleEndian.Uint32(cb[4:8])
		if string(cb[0:4]) == "avih" {
			// Payload: dwMicroSecPerFrame at 0, dwTotalFrames at 16.
			if len(cb) < 28 || sz < 20 {
				return 0, false
			}
			usPerFrame := binary.LittleEndian.Uint32(cb[8:12])
			frames := binary.LittleEndian.Uint32(cb[24:28])
			if usPerFrame == 0 || frames == 0 {
				return 0, false
			}
			return toSeconds(float64(usPerFrame) * float64(frames) / 1e6)
		}
		off += 8 + int64(sz) + int64(sz&1)
	}
	return 0, false
}

// probeAIFF handles AIFF and AIFF-C (aiff, aif): duration = the COMM chunk's
// numSampleFrames ÷ sampleRate, the latter stored as an 80-bit IEEE 754
// extended float. Chunk sizes are big-endian, unlike RIFF.
func probeAIFF(p *probeReader) (uint32, bool) {
	var h [12]byte
	b := p.readInto(0, h[:])
	if len(b) < 12 || string(b[0:4]) != "FORM" {
		return 0, false
	}
	if ft := string(b[8:12]); ft != "AIFF" && ft != "AIFC" {
		return 0, false
	}
	off := int64(12)
	for i := 0; i < maxChunks && off+8 <= p.size; i++ {
		var ch [26]byte
		cb := p.readInto(off, ch[:])
		if len(cb) < 8 {
			break
		}
		sz := binary.BigEndian.Uint32(cb[4:8])
		if string(cb[0:4]) == "COMM" {
			// Payload: numChannels u16, numSampleFrames u32,
			// sampleSize u16, sampleRate float80.
			if len(cb) < 26 || sz < 18 {
				return 0, false
			}
			frames := binary.BigEndian.Uint32(cb[10:14])
			rate := float80(cb[16:26])
			if frames == 0 || rate <= 0 || math.IsInf(rate, 0) || math.IsNaN(rate) {
				return 0, false
			}
			return toSeconds(float64(frames) / rate)
		}
		off += 8 + int64(sz) + int64(sz&1)
	}
	return 0, false
}

// float80 decodes a 10-byte IEEE 754 extended-precision float (sign 1,
// exponent 15, mantissa 64 with an explicit integer bit) as used by AIFF
// sample rates. Infinities and NaNs decode to NaN for the caller to reject.
func float80(b []byte) float64 {
	if len(b) < 10 {
		return math.NaN()
	}
	exp := int(b[0]&0x7F)<<8 | int(b[1])
	mant := binary.BigEndian.Uint64(b[2:10])
	var v float64
	switch {
	case exp == 0 && mant == 0:
		v = 0
	case exp == 0x7FFF:
		return math.NaN()
	default:
		v = math.Ldexp(float64(mant), exp-16383-63)
	}
	if b[0]&0x80 != 0 {
		v = -v
	}
	return v
}
