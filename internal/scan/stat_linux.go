//go:build linux

package scan

import "syscall"

// rawStat is the platform-independent subset of stat(2) the scanner needs.
type rawStat struct {
	Dev    uint64
	Ino    uint64
	Nlink  uint64
	Size   int64
	Blocks int64
	Mode   uint32
	UID    uint32
	GID    uint32
	Mtime  int64
}

// fromSys extracts rawStat from the value returned by FileInfo.Sys().
func fromSys(sys any) (rawStat, bool) {
	st, ok := sys.(*syscall.Stat_t)
	if !ok {
		return rawStat{}, false
	}
	return rawStat{
		Dev:    uint64(st.Dev),
		Ino:    uint64(st.Ino),
		Nlink:  uint64(st.Nlink),
		Size:   st.Size,
		Blocks: st.Blocks,
		Mode:   uint32(st.Mode),
		UID:    st.Uid,
		GID:    st.Gid,
		Mtime:  st.Mtim.Sec,
	}, true
}
