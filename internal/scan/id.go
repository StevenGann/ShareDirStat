package scan

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// crockford is the alphabet used by ULIDs: no I, L, O or U, so ids survive
// being read aloud and copied by hand.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var idMu sync.Mutex

// NewID returns a lexicographically sortable 26-character identifier: 48
// bits of millisecond timestamp followed by 80 bits of randomness. Sorting
// scan and generation ids therefore sorts them by time.
func NewID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(b[0:], ms<<16)
	idMu.Lock()
	_, _ = rand.Read(b[6:])
	idMu.Unlock()

	var out [26]byte
	// 128 bits encoded as 26 base-32 characters (the first holds 2 bits).
	var carry uint16
	bits := 0
	pos := len(out)
	for i := len(b) - 1; i >= 0; i-- {
		carry |= uint16(b[i]) << bits
		bits += 8
		for bits >= 5 {
			pos--
			out[pos] = crockford[carry&31]
			carry >>= 5
			bits -= 5
		}
	}
	for pos > 0 {
		pos--
		out[pos] = crockford[carry&31]
		carry >>= 5
	}
	return string(out[:])
}
