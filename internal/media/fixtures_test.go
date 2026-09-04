package media

// Synthetic fixture builders: each returns a minimal but structurally valid
// file of its format with a known duration, so tests can assert exact
// seconds. Keeping them as code (not testdata files) makes the adversarial
// truncation/mutation sweeps cheap and self-contained.

import (
	"encoding/binary"
	"math"
)

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func le16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
func le64(v uint64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); return b }
func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// --- ISO BMFF ---

func mp4Box(typ string, parts ...[]byte) []byte {
	body := cat(parts...)
	return cat(be32(uint32(8+len(body))), []byte(typ), body)
}

// mp4Box64 encodes the box with a 64-bit largesize (size field == 1).
func mp4Box64(typ string, parts ...[]byte) []byte {
	body := cat(parts...)
	return cat(be32(1), []byte(typ), be64(uint64(16+len(body))), body)
}

func mvhdV0(timescale, duration uint32) []byte {
	p := make([]byte, 100) // version/flags + times + timescale + duration + rate etc.
	binary.BigEndian.PutUint32(p[12:], timescale)
	binary.BigEndian.PutUint32(p[16:], duration)
	return mp4Box("mvhd", p)
}

func mvhdV1(timescale uint32, duration uint64) []byte {
	p := make([]byte, 112)
	p[0] = 1
	binary.BigEndian.PutUint32(p[20:], timescale)
	binary.BigEndian.PutUint64(p[24:], duration)
	return mp4Box("mvhd", p)
}

// mp4File places moov after a small mdat, the layout that forces a probe to
// walk past the media data.
func mp4File(mvhd []byte) []byte {
	return cat(
		mp4Box("ftyp", []byte("isomiso2avc1mp41")),
		mp4Box("mdat", make([]byte, 900)),
		mp4Box("moov", mvhd),
	)
}

// --- EBML (Matroska/WebM) ---

// ebmlID emits an element ID in its wire form (minimal big-endian bytes).
func ebmlID(id uint32) []byte {
	switch {
	case id > 0xFFFFFF:
		return be32(id)
	case id > 0xFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xFF:
		return be16(uint16(id))
	default:
		return []byte{byte(id)}
	}
}

// ebmlEl encodes an element with an 8-byte size vint (always valid, and it
// exercises the long-size path).
func ebmlEl(id uint32, body []byte) []byte {
	sz := be64(uint64(len(body)))
	sz[0] = 0x01 // 8-byte size marker; payload lengths here fit in 56 bits
	return cat(ebmlID(id), sz, body)
}

// mkvFile builds EBML header + Segment. duration is encoded as float64 when
// wide is true, float32 otherwise; scale 0 omits TimestampScale (default
// 1_000_000 ns). unknownSegment uses the all-ones unknown-size encoding on
// the Segment, as streamed files do.
func mkvFile(duration float64, wide bool, scale uint64, unknownSegment bool) []byte {
	var dur []byte
	if wide {
		dur = ebmlEl(durationID, be64(math.Float64bits(duration)))
	} else {
		dur = ebmlEl(durationID, be32(math.Float32bits(float32(duration))))
	}
	info := dur
	if scale != 0 {
		info = cat(ebmlEl(timestampScaleID, be64(scale)), dur)
	}
	segBody := cat(
		ebmlEl(0x114D9B74, make([]byte, 41)), // SeekHead the walk must skip
		ebmlEl(infoID, info),
		ebmlEl(0x1654AE6B, make([]byte, 200)), // Tracks
	)
	seg := ebmlEl(segmentID, segBody)
	if unknownSegment {
		seg = cat(ebmlID(segmentID), []byte{0xFF}, segBody)
	}
	return cat(ebmlEl(ebmlHeaderID, []byte{0x42, 0x86, 0x81, 0x01}), seg)
}

// --- MPEG audio ---

// mp3Frame417 is one 417-byte MPEG-1 layer III frame: 44.1 kHz, 128 kbit/s,
// stereo, no padding.
func mp3Frame417() []byte {
	f := make([]byte, 417)
	f[0], f[1], f[2], f[3] = 0xFF, 0xFB, 0x90, 0x00
	return f
}

func mp3CBR(frames int) []byte {
	var out []byte
	for i := 0; i < frames; i++ {
		out = append(out, mp3Frame417()...)
	}
	return out
}

// mp3Xing embeds a Xing tag (frame count only) in the first frame.
func mp3Xing(frames uint32) []byte {
	first := mp3Frame417()
	copy(first[36:], "Xing")
	binary.BigEndian.PutUint32(first[40:], 1) // flags: frames present
	binary.BigEndian.PutUint32(first[44:], frames)
	return cat(first, mp3CBR(3))
}

// id3v2 wraps data in an ID3v2.4 tag of the given payload size.
func id3v2(payload int, data []byte) []byte {
	h := []byte{'I', 'D', '3', 4, 0, 0,
		byte(payload >> 21 & 0x7F), byte(payload >> 14 & 0x7F),
		byte(payload >> 7 & 0x7F), byte(payload & 0x7F)}
	return cat(h, make([]byte, payload), data)
}

// --- FLAC ---

func flacFile(rate uint32, total uint64) []byte {
	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:], 4096) // min/max block size
	binary.BigEndian.PutUint16(si[2:], 4096)
	packed := uint64(rate)<<44 | uint64(1)<<41 | uint64(15)<<36 | total&(1<<36-1)
	binary.BigEndian.PutUint64(si[10:], packed)
	return cat([]byte("fLaC"), []byte{0x80, 0, 0, 34}, si, make([]byte, 256))
}

// --- Ogg ---

func oggPage(hdrType byte, granule uint64, serial, seq uint32, packets ...[]byte) []byte {
	var segs, body []byte
	for _, pk := range packets {
		n := len(pk)
		for n >= 255 {
			segs = append(segs, 255)
			n -= 255
		}
		segs = append(segs, byte(n))
		body = append(body, pk...)
	}
	h := make([]byte, 27)
	copy(h, "OggS")
	h[5] = hdrType
	binary.LittleEndian.PutUint64(h[6:], granule)
	binary.LittleEndian.PutUint32(h[14:], serial)
	binary.LittleEndian.PutUint32(h[18:], seq)
	h[26] = byte(len(segs))
	return cat(h, segs, body)
}

func vorbisIDPacket(rate uint32) []byte {
	p := make([]byte, 30)
	p[0] = 1
	copy(p[1:], "vorbis")
	p[11] = 2 // channels
	binary.LittleEndian.PutUint32(p[12:], rate)
	p[29] = 1 // framing bit
	return p
}

func opusIDPacket(preSkip uint16) []byte {
	p := make([]byte, 19)
	copy(p, "OpusHead")
	p[8], p[9] = 1, 2 // version, channels
	binary.LittleEndian.PutUint16(p[10:], preSkip)
	binary.LittleEndian.PutUint32(p[12:], 44100) // original input rate
	return p
}

func theoraIDPacket(fpsNum, fpsDen uint32, kfgShift uint) []byte {
	p := make([]byte, 42)
	p[0] = 0x80
	copy(p[1:], "theora")
	p[7], p[8] = 3, 2 // version 3.2.x
	binary.BigEndian.PutUint32(p[22:], fpsNum)
	binary.BigEndian.PutUint32(p[26:], fpsDen)
	p[40] = byte(kfgShift >> 3 & 0x03)
	p[41] = byte(kfgShift & 0x07 << 5)
	return p
}

// oggVorbisFile: BOS ID page, a mid data page, filler, and a final EOS page
// whose granule (total samples) determines the duration.
func oggVorbisFile(rate uint32, granule uint64) []byte {
	const serial = 0x11111111
	return cat(
		oggPage(0x02, 0, serial, 0, vorbisIDPacket(rate)),
		oggPage(0, granule/2, serial, 1, make([]byte, 300)),
		make([]byte, 700),
		oggPage(0x04, granule, serial, 2, make([]byte, 40)),
	)
}

func oggOpusFile(preSkip uint16, granule uint64) []byte {
	const serial = 0x22222222
	return cat(
		oggPage(0x02, 0, serial, 0, opusIDPacket(preSkip)),
		make([]byte, 900),
		oggPage(0x04, granule, serial, 1, make([]byte, 40)),
	)
}

func speexIDPacket(rate uint32) []byte {
	p := make([]byte, 80)
	copy(p, "Speex   ")
	binary.LittleEndian.PutUint32(p[36:], rate)
	return p
}

func oggSpeexFile(rate uint32, granule uint64) []byte {
	const serial = 0x55555555
	return cat(
		oggPage(0x02, 0, serial, 0, speexIDPacket(rate)),
		make([]byte, 500),
		oggPage(0x04, granule, serial, 1, make([]byte, 40)),
	)
}

// oggTheoraVorbisFile multiplexes video and audio; the Theora granule must
// win even though the Vorbis stream's last page comes later in the file.
func oggTheoraVorbisFile(theoraGranule, vorbisGranule uint64) []byte {
	const vSerial, aSerial = 0x33333333, 0x44444444
	return cat(
		oggPage(0x02, 0, vSerial, 0, theoraIDPacket(30, 1, 6)),
		oggPage(0x02, 0, aSerial, 0, vorbisIDPacket(44100)),
		make([]byte, 600),
		oggPage(0x04, theoraGranule, vSerial, 5, make([]byte, 30)),
		oggPage(0x04, vorbisGranule, aSerial, 6, make([]byte, 30)),
	)
}

// --- RIFF / IFF ---

func wavFile(byteRate, dataSize uint32, actualData int) []byte {
	fmtPayload := cat(le16(1), le16(2), le32(byteRate/4), le32(byteRate), le16(4), le16(16))
	body := cat([]byte("WAVE"),
		[]byte("fmt "), le32(16), fmtPayload,
		[]byte("data"), le32(dataSize), make([]byte, actualData))
	return cat([]byte("RIFF"), le32(uint32(len(body))), body)
}

func aviFile(usPerFrame, totalFrames uint32) []byte {
	avih := make([]byte, 56)
	binary.LittleEndian.PutUint32(avih[0:], usPerFrame)
	binary.LittleEndian.PutUint32(avih[16:], totalFrames)
	hdrl := cat([]byte("LIST"), le32(uint32(4+8+len(avih))), []byte("hdrl"),
		[]byte("avih"), le32(uint32(len(avih))), avih)
	movi := cat([]byte("LIST"), le32(4+200), []byte("movi"), make([]byte, 200))
	body := cat([]byte("AVI "), hdrl, movi)
	return cat([]byte("RIFF"), le32(uint32(len(body))), body)
}

// f80 encodes an 80-bit IEEE 754 extended float (the AIFF sample-rate type).
func f80(v float64) []byte {
	b := make([]byte, 10)
	if v <= 0 {
		return b
	}
	frac, exp := math.Frexp(v)
	binary.BigEndian.PutUint16(b, uint16(16382+exp))
	binary.BigEndian.PutUint64(b[2:], uint64(frac*math.Ldexp(1, 64)))
	return b
}

func aiffFile(rate float64, frames uint32) []byte {
	comm := cat(be16(2), be32(frames), be16(16), f80(rate))
	ssnd := cat([]byte("SSND"), be32(108), make([]byte, 108))
	body := cat([]byte("AIFF"), []byte("COMM"), be32(uint32(len(comm))), comm, ssnd)
	return cat([]byte("FORM"), be32(uint32(len(body))), body)
}

// --- ASF ---

func asfFile(play100ns, prerollMS uint64, flags uint32) []byte {
	fp := make([]byte, 80)
	binary.LittleEndian.PutUint64(fp[40:], play100ns)
	binary.LittleEndian.PutUint64(fp[56:], prerollMS)
	binary.LittleEndian.PutUint32(fp[64:], flags)
	dummy := cat(make([]byte, 16), le64(40), make([]byte, 16))
	fileProps := cat(asfFilePropsGUID[:], le64(uint64(24+len(fp))), fp)
	objects := cat(dummy, fileProps)
	return cat(asfHeaderGUID[:], le64(uint64(30+len(objects))), le32(2),
		[]byte{0x01, 0x02}, objects)
}

// --- ADTS ---

// adtsFile builds fixed 200-byte MPEG-4 ADTS frames: AAC LC, 44.1 kHz.
func adtsFile(frames int) []byte {
	f := make([]byte, 200)
	f[0], f[1] = 0xFF, 0xF1
	f[2] = 0x50             // profile LC, sampling index 4 (44100)
	f[3] = 0x80             // 2 channels; frame length high bits 0
	f[4] = 200 >> 3         // frame length middle bits (low 3 bits are zero)
	f[5], f[6] = 0x1F, 0xFC // buffer fullness, single raw data block
	var out []byte
	for i := 0; i < frames; i++ {
		out = append(out, f...)
	}
	return out
}
