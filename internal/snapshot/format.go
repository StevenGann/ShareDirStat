// Package snapshot persists a model.Generation to the data directory and
// loads it back at startup (specification §8.4, docs/SNAPSHOT_FORMAT.md).
//
// A snapshot is written to a temporary file, fsynced and atomically renamed,
// so a crash mid-write always leaves the previous snapshot intact (NFR-8).
// Loading and saving never happen on the request path.
package snapshot

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

// Magic identifies a ShareDirStat snapshot file.
var Magic = [8]byte{'S', 'D', 'S', 'S', 'N', 'A', 'P', 0x01}

// FormatVersion is bumped whenever the on-disk layout changes
// incompatibly. A file with any other supported version still loads; an
// unknown version is ignored (FR-DATA-02).
//
// Version 2 appends the optional media sections (per-node durations and
// media sizes) after the dropped-error count. A generation without media
// data is still written as version 1, so shares that never grew the arrays
// keep snapshots an older binary can read.
const FormatVersion uint16 = 2

// minFormatVersion is the oldest snapshot layout this build still reads.
const minFormatVersion uint16 = 1

// Compression flags.
const (
	flagZstd uint16 = 1 << 0
)

// NodeRecordSize is the fixed on-disk size of one node.
const NodeRecordSize = 64

// maxHeaderBytes bounds the JSON header in both directions, so a corrupt
// length field cannot drive a huge allocation on read.
const maxHeaderBytes = 1 << 20

// ErrBadMagic means the file is not a snapshot at all.
var ErrBadMagic = errors.New("not a ShareDirStat snapshot")

// ErrVersion means the snapshot was written by an incompatible version.
var ErrVersion = errors.New("unsupported snapshot format version")

// ErrCorrupt means the payload failed its checksum or ended early.
var ErrCorrupt = errors.New("snapshot is corrupt")

// Header is the uncompressed metadata block at the head of every snapshot.
type Header struct {
	ShareID    string      `json:"share_id"`
	RootPath   string      `json:"root_path"`
	Generation string      `json:"generation"`
	ScanID     string      `json:"scan_id"`
	Trigger    string      `json:"trigger"`
	Basis      string      `json:"basis"`
	ScannedAt  time.Time   `json:"scanned_at"`
	DurationMS int64       `json:"duration_ms"`
	Nodes      uint64      `json:"nodes"`
	NameBytes  uint64      `json:"name_bytes"`
	TopN       int         `json:"top_n"`
	AppVersion string      `json:"app_version"`
	Stats      model.Stats `json:"stats"`
}

// encodeNode writes one node into a fixed-size record.
func encodeNode(buf []byte, n *model.Node) {
	binary.LittleEndian.PutUint64(buf[0:], n.Size)
	binary.LittleEndian.PutUint64(buf[8:], n.Alloc)
	// Mtime is stored as raw two's-complement bits and read back the same
	// way, so pre-1970 timestamps survive the round trip.
	binary.LittleEndian.PutUint64(buf[16:], uint64(n.Mtime)) //nolint:gosec // deliberate bit reinterpretation
	binary.LittleEndian.PutUint32(buf[24:], n.Parent)
	binary.LittleEndian.PutUint32(buf[28:], n.NameOff)
	binary.LittleEndian.PutUint32(buf[32:], n.FirstChild)
	binary.LittleEndian.PutUint32(buf[36:], n.ChildCount)
	binary.LittleEndian.PutUint32(buf[40:], n.Files)
	binary.LittleEndian.PutUint32(buf[44:], n.Dirs)
	binary.LittleEndian.PutUint32(buf[48:], n.UID)
	binary.LittleEndian.PutUint32(buf[52:], n.GID)
	binary.LittleEndian.PutUint16(buf[56:], n.NameLen)
	binary.LittleEndian.PutUint16(buf[58:], n.Mode)
	buf[60] = byte(n.Kind)
	buf[61] = byte(n.Flags)
	buf[62], buf[63] = 0, 0
}

// decodeNode reads one fixed-size record.
func decodeNode(buf []byte, n *model.Node) {
	n.Size = binary.LittleEndian.Uint64(buf[0:])
	n.Alloc = binary.LittleEndian.Uint64(buf[8:])
	n.Mtime = int64(binary.LittleEndian.Uint64(buf[16:])) //nolint:gosec // deliberate bit reinterpretation
	n.Parent = binary.LittleEndian.Uint32(buf[24:])
	n.NameOff = binary.LittleEndian.Uint32(buf[28:])
	n.FirstChild = binary.LittleEndian.Uint32(buf[32:])
	n.ChildCount = binary.LittleEndian.Uint32(buf[36:])
	n.Files = binary.LittleEndian.Uint32(buf[40:])
	n.Dirs = binary.LittleEndian.Uint32(buf[44:])
	n.UID = binary.LittleEndian.Uint32(buf[48:])
	n.GID = binary.LittleEndian.Uint32(buf[52:])
	n.NameLen = binary.LittleEndian.Uint16(buf[56:])
	n.Mode = binary.LittleEndian.Uint16(buf[58:])
	n.Kind = model.Kind(buf[60])
	n.Flags = model.Flags(buf[61])
}

// writeHeader emits the uncompressed frame header.
func writeHeader(w io.Writer, h Header, flags, version uint16) error {
	body, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if len(body) > maxHeaderBytes {
		return fmt.Errorf("snapshot header of %d bytes exceeds the %d byte limit", len(body), maxHeaderBytes)
	}
	var fixed [16]byte
	copy(fixed[:8], Magic[:])
	binary.LittleEndian.PutUint16(fixed[8:], version)
	binary.LittleEndian.PutUint16(fixed[10:], flags)
	binary.LittleEndian.PutUint32(fixed[12:], u32len(len(body)))
	if _, err := w.Write(fixed[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// readHeader parses the uncompressed frame header and returns the file's
// format version, which selects the payload layout.
func readHeader(r io.Reader) (Header, uint16, uint16, error) {
	var fixed [16]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return Header{}, 0, 0, fmt.Errorf("%w: %w", ErrBadMagic, err)
	}
	if [8]byte(fixed[:8]) != Magic {
		return Header{}, 0, 0, ErrBadMagic
	}
	version := binary.LittleEndian.Uint16(fixed[8:])
	if version < minFormatVersion || version > FormatVersion {
		return Header{}, 0, 0, fmt.Errorf("%w: file is version %d, this build reads versions %d-%d", ErrVersion, version, minFormatVersion, FormatVersion)
	}
	flags := binary.LittleEndian.Uint16(fixed[10:])
	n := binary.LittleEndian.Uint32(fixed[12:])
	if n > maxHeaderBytes {
		return Header{}, 0, 0, fmt.Errorf("%w: header of %d bytes is implausible", ErrCorrupt, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Header{}, 0, 0, fmt.Errorf("%w: short header: %w", ErrCorrupt, err)
	}
	var h Header
	if err := json.Unmarshal(body, &h); err != nil {
		return Header{}, 0, 0, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return h, flags, version, nil
}

// crcTable is the Castagnoli table; hardware-accelerated on arm64 and amd64.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// u32len narrows a slice length for the wire format. Every length written
// here is bounded by the arena, which model caps below MaxUint32; the clamp
// is belt and braces so a length can never wrap silently.
func u32len(n int) uint32 {
	if n < 0 {
		return 0
	}
	if uint64(n) > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n) //nolint:gosec // bounds-checked immediately above
}

// writePayload encodes the arrays, streaming through the CRC hasher. The
// version must match what writeHeader put in the frame: version 2 appends
// the media sections, version 1 predates them.
func writePayload(w io.Writer, raw model.Raw, version uint16) error {
	h := crc32.New(crcTable)
	mw := io.MultiWriter(w, h)
	bw := bufio.NewWriterSize(mw, 256*1024)

	var scratch [NodeRecordSize]byte
	putU64 := func(v uint64) error {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], v)
		_, err := bw.Write(b[:])
		return err
	}
	putU32 := func(v uint32) error {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		_, err := bw.Write(b[:])
		return err
	}
	putStr := func(s string) error {
		if err := putU32(u32len(len(s))); err != nil {
			return err
		}
		_, err := bw.WriteString(s)
		return err
	}

	if err := putU64(uint64(len(raw.Nodes))); err != nil {
		return err
	}
	for i := range raw.Nodes {
		encodeNode(scratch[:], &raw.Nodes[i])
		if _, err := bw.Write(scratch[:]); err != nil {
			return err
		}
	}
	if err := putU64(uint64(len(raw.Names))); err != nil {
		return err
	}
	if _, err := bw.Write(raw.Names); err != nil {
		return err
	}

	if err := putU32(u32len(len(raw.Exts))); err != nil {
		return err
	}
	for _, e := range raw.Exts {
		if err := putStr(e.Ext); err != nil {
			return err
		}
		if err := putU64(e.Files); err != nil {
			return err
		}
		if err := putU64(e.Size); err != nil {
			return err
		}
		if err := putU64(e.Alloc); err != nil {
			return err
		}
	}

	if err := putU32(u32len(len(raw.Top))); err != nil {
		return err
	}
	for _, idx := range raw.Top {
		if err := putU32(idx); err != nil {
			return err
		}
	}

	if err := putU32(u32len(len(raw.Errors))); err != nil {
		return err
	}
	for _, e := range raw.Errors {
		for _, s := range []string{e.Path, e.Op, e.Errno, e.Message} {
			if err := putStr(s); err != nil {
				return err
			}
		}
	}
	if err := putU64(raw.ErrorsDropped); err != nil {
		return err
	}

	if version >= 2 {
		present := byte(0)
		if raw.Durs != nil {
			present = 1
		}
		if err := bw.WriteByte(present); err != nil {
			return err
		}
		if present == 1 {
			for _, d := range raw.Durs {
				if err := putU32(d); err != nil {
					return err
				}
			}
			for _, m := range raw.MediaSizes {
				if err := putU64(m); err != nil {
					return err
				}
			}
		}
	}

	if err := bw.Flush(); err != nil {
		return err
	}

	// Trailer: checksum of everything above, so a truncated file is detected.
	var tail [4]byte
	binary.LittleEndian.PutUint32(tail[:], h.Sum32())
	_, err := w.Write(tail[:])
	return err
}

// payloadReader reads the decompressed payload while feeding exactly the
// bytes it consumes through the checksum. Hashing must not be done with an
// io.TeeReader here: the buffered reader reads ahead, so the trailer would
// be folded into the digest before it could be compared.
type payloadReader struct {
	br  *bufio.Reader
	h   hash.Hash32
	buf [8]byte
}

func (p *payloadReader) read(b []byte) error {
	if _, err := io.ReadFull(p.br, b); err != nil {
		return err
	}
	_, _ = p.h.Write(b)
	return nil
}

func (p *payloadReader) u32() (uint32, error) {
	if err := p.read(p.buf[:4]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(p.buf[:4]), nil
}

func (p *payloadReader) u64() (uint64, error) {
	if err := p.read(p.buf[:8]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(p.buf[:8]), nil
}

func (p *payloadReader) str(maxLen uint32) (string, error) {
	n, err := p.u32()
	if err != nil {
		return "", err
	}
	if n > maxLen {
		return "", fmt.Errorf("%w: implausible string length %d", ErrCorrupt, n)
	}
	b := make([]byte, n)
	if err := p.read(b); err != nil {
		return "", err
	}
	return string(b), nil
}

// maxStringLen bounds any single string decoded from a snapshot, so a
// corrupt length field cannot drive a huge allocation.
const maxStringLen = 1 << 20

// Initial capacities for the two big arrays. Decoding grows from here as
// bytes are actually read, so a corrupt length allocates no more than the
// file can back.
const (
	initialNodeAlloc = 1 << 16
	initialNameAlloc = 1 << 20
)

// readPayload decodes the arrays and verifies the checksum. version is the
// frame version readHeader returned.
func readPayload(r io.Reader, h Header, version uint16) (model.Raw, error) {
	p := &payloadReader{br: bufio.NewReaderSize(r, 256*1024), h: crc32.New(crcTable)}
	var raw model.Raw
	corrupt := func(what string, err error) (model.Raw, error) {
		return raw, fmt.Errorf("%w: %s: %w", ErrCorrupt, what, err)
	}

	nodeCount, err := p.u64()
	if err != nil {
		return corrupt("node count", err)
	}
	if nodeCount != h.Nodes {
		return raw, fmt.Errorf("%w: header says %d nodes, payload has %d", ErrCorrupt, h.Nodes, nodeCount)
	}
	if nodeCount > model.MaxNodes {
		return raw, fmt.Errorf("%w: %d nodes exceeds the arena addressing limit", ErrCorrupt, nodeCount)
	}
	// Grow as the bytes actually arrive rather than sizing the slice from the
	// declared count. The count is cross-checked only against a JSON header in
	// the same untrusted file, and the CRC that would reject a forgery is not
	// verified until the end -- so a one-line length field could otherwise
	// drive a multi-gigabyte allocation, or panic makeslice outright, before
	// anything had a chance to reject the file.
	raw.Nodes = make([]model.Node, 0, min(nodeCount, initialNodeAlloc))
	var scratch [NodeRecordSize]byte
	for i := uint64(0); i < nodeCount; i++ {
		if err := p.read(scratch[:]); err != nil {
			return corrupt(fmt.Sprintf("node %d", i), err)
		}
		var n model.Node
		decodeNode(scratch[:], &n)
		raw.Nodes = append(raw.Nodes, n)
	}

	nameLen, err := p.u64()
	if err != nil {
		return corrupt("name length", err)
	}
	if nameLen != h.NameBytes {
		return raw, fmt.Errorf("%w: header says %d name bytes, payload has %d", ErrCorrupt, h.NameBytes, nameLen)
	}
	if nameLen > model.MaxNameBytes {
		return raw, fmt.Errorf("%w: %d name bytes exceeds the arena addressing limit", ErrCorrupt, nameLen)
	}
	// Same reasoning as the node array: read it in bounded chunks so the
	// declared length cannot size an allocation on its own.
	raw.Names = make([]byte, 0, min(nameLen, initialNameAlloc))
	for remaining := nameLen; remaining > 0; {
		chunk := min(remaining, initialNameAlloc)
		buf := make([]byte, chunk)
		if err := p.read(buf); err != nil {
			return corrupt("names", err)
		}
		raw.Names = append(raw.Names, buf...)
		remaining -= chunk
	}

	extCount, err := p.u32()
	if err != nil {
		return corrupt("extension count", err)
	}
	// Bound it like the top and error lists: an extension table cannot have
	// more rows than the share has files, and a corrupt length here would
	// otherwise size an allocation straight from the file.
	if uint64(extCount) > nodeCount {
		return raw, fmt.Errorf("%w: extension table longer than the node array", ErrCorrupt)
	}
	raw.Exts = make([]model.ExtStat, extCount)
	for i := range raw.Exts {
		if raw.Exts[i].Ext, err = p.str(64); err != nil {
			return corrupt("extension", err)
		}
		if raw.Exts[i].Files, err = p.u64(); err != nil {
			return corrupt("extension", err)
		}
		if raw.Exts[i].Size, err = p.u64(); err != nil {
			return corrupt("extension", err)
		}
		if raw.Exts[i].Alloc, err = p.u64(); err != nil {
			return corrupt("extension", err)
		}
	}

	topCount, err := p.u32()
	if err != nil {
		return corrupt("top count", err)
	}
	if uint64(topCount) > nodeCount {
		return raw, fmt.Errorf("%w: top list longer than the node array", ErrCorrupt)
	}
	raw.Top = make([]uint32, topCount)
	for i := range raw.Top {
		if raw.Top[i], err = p.u32(); err != nil {
			return corrupt("top", err)
		}
	}

	errCount, err := p.u32()
	if err != nil {
		return corrupt("error count", err)
	}
	if errCount > model.MaxErrors {
		return raw, fmt.Errorf("%w: error list exceeds the retention cap", ErrCorrupt)
	}
	raw.Errors = make([]model.ScanError, errCount)
	for i := range raw.Errors {
		for _, f := range [...]*string{&raw.Errors[i].Path, &raw.Errors[i].Op, &raw.Errors[i].Errno, &raw.Errors[i].Message} {
			if *f, err = p.str(maxStringLen); err != nil {
				return corrupt("errors", err)
			}
		}
	}
	if raw.ErrorsDropped, err = p.u64(); err != nil {
		return corrupt("dropped errors", err)
	}

	if version >= 2 {
		if err := p.read(p.buf[:1]); err != nil {
			return corrupt("media flag", err)
		}
		if p.buf[0] == 1 {
			// Both arrays are exactly one entry per node; the incremental
			// growth mirrors the node array's, so the declared count cannot
			// size an allocation the file does not back.
			raw.Durs = make([]uint32, 0, min(nodeCount, initialNodeAlloc))
			for i := uint64(0); i < nodeCount; i++ {
				d, err := p.u32()
				if err != nil {
					return corrupt("media durations", err)
				}
				raw.Durs = append(raw.Durs, d)
			}
			raw.MediaSizes = make([]uint64, 0, min(nodeCount, initialNodeAlloc))
			for i := uint64(0); i < nodeCount; i++ {
				m, err := p.u64()
				if err != nil {
					return corrupt("media sizes", err)
				}
				raw.MediaSizes = append(raw.MediaSizes, m)
			}
		}
	}

	// The trailer is read straight from the buffer, outside the digest.
	want := p.h.Sum32()
	var tail [4]byte
	if _, err := io.ReadFull(p.br, tail[:]); err != nil {
		return corrupt("missing checksum", err)
	}
	if got := binary.LittleEndian.Uint32(tail[:]); got != want {
		return raw, fmt.Errorf("%w: checksum mismatch (want %08x, got %08x)", ErrCorrupt, want, got)
	}

	basis, _ := model.ParseBasis(h.Basis)
	raw.Basis = basis
	raw.Stats = h.Stats
	raw.TopN = h.TopN
	raw.Meta = model.GenerationMeta{
		ID:        h.Generation,
		ShareID:   h.ShareID,
		RootPath:  h.RootPath,
		Basis:     h.Basis,
		ScannedAt: h.ScannedAt,
		Duration:  time.Duration(h.DurationMS) * time.Millisecond,
		Trigger:   h.Trigger,
		ScanID:    h.ScanID,
	}
	return raw, nil
}
