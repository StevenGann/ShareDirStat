//go:build !linux && !darwin

package scan

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

// fromSys reports that no platform stat data is available; the scanner then
// falls back to the portable os.FileInfo fields.
func fromSys(any) (rawStat, bool) { return rawStat{}, false }
