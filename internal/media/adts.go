package media

// adtsRates maps the ADTS sampling_frequency_index to Hz (0 = reserved).
var adtsRates = [16]uint32{
	96000, 88200, 64000, 48000, 44100, 32000,
	24000, 22050, 16000, 12000, 11025, 8000, 7350,
}

// probeADTS handles raw ADTS AAC (aac). ADTS carries no total duration, so
// this is a documented estimate: it parses up to 500 frames from a 256 KiB
// window at the start, averages their byte length, extrapolates the frame
// count over the whole payload, and multiplies by 1024 samples per frame.
// VBR files therefore land near, not exactly on, the true duration. Files
// that do not begin with an ADTS sync (after an optional ID3v2 tag) report
// false.
func probeADTS(p *probeReader) (uint32, bool) {
	start := skipID3v2(p)
	const (
		window    = 256 << 10
		maxFrames = 500
	)
	buf := p.read(start, window)
	if len(buf) < 7 || buf[0] != 0xFF || buf[1]&0xF6 != 0xF0 {
		return 0, false
	}
	rate := adtsRates[buf[2]>>2&0x0F]
	if rate == 0 {
		return 0, false
	}
	frames := 0
	consumed := 0
	for frames < maxFrames && consumed+7 <= len(buf) {
		b := buf[consumed:]
		if b[0] != 0xFF || b[1]&0xF6 != 0xF0 {
			break
		}
		frameLen := int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5])>>5
		if frameLen < 7 {
			return 0, false
		}
		frames++
		consumed += frameLen
	}
	if frames == 0 {
		return 0, false
	}
	avg := float64(consumed) / float64(frames)
	total := float64(p.size-start) / avg
	return toSeconds(total * 1024 / float64(rate))
}
