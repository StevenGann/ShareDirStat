package api

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
)

// OwnerResolver maps uid/gid to names using the passwd and group databases
// visible inside the container, with configuration overrides on top
// (FR-UI-05a). Unknown ids render as their number.
type OwnerResolver struct {
	mu     sync.RWMutex
	users  map[uint32]string
	groups map[uint32]string
}

// NewOwnerResolver reads /etc/passwd and /etc/group, then applies overrides.
// Missing files are not an error: a distroless image has almost no entries,
// which is exactly why the overrides exist.
func NewOwnerResolver(userOverrides, groupOverrides map[int]string) *OwnerResolver {
	r := &OwnerResolver{
		users:  parseIDFile("/etc/passwd"),
		groups: parseIDFile("/etc/group"),
	}
	for id, name := range userOverrides {
		if uid, ok := toUID(id); ok {
			r.users[uid] = name
		}
	}
	for id, name := range groupOverrides {
		if gid, ok := toUID(id); ok {
			r.groups[gid] = name
		}
	}
	return r
}

// toUID validates a configured id. Negative or out-of-range values are
// ignored rather than wrapped into a different user.
func toUID(id int) (uint32, bool) {
	// Compared as uint64: on a 32-bit build `id > math.MaxUint32` does not
	// compile, since the untyped constant overflows int.
	if id < 0 || uint64(id) > math.MaxUint32 {
		return 0, false
	}
	return uint32(id), true //nolint:gosec // range-checked above
}

// parseIDFile reads a colon-separated name:x:id:... database.
func parseIDFile(path string) map[uint32]string {
	out := map[uint32]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 3 {
			continue
		}
		id, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			continue
		}
		out[uint32(id)] = parts[0]
	}
	return out
}

// Lookup returns display names for a uid and gid, falling back to the
// numeric form.
func (r *OwnerResolver) Lookup(uid, gid uint32) (user, group string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n, ok := r.users[uid]; ok {
		user = n
	} else {
		user = strconv.FormatUint(uint64(uid), 10)
	}
	if n, ok := r.groups[gid]; ok {
		group = n
	} else {
		group = strconv.FormatUint(uint64(gid), 10)
	}
	return user, group
}
