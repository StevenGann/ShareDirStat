//go:build !linux && !darwin

package scan

import (
	"io/fs"
	"os"
	"path/filepath"
)

// lstatEntry falls back to a path-based lstat on platforms without the *at
// syscalls. The directory descriptor is ignored; dir supplies the path.
func lstatEntry(_ int, dir, name string) (rawStat, fs.FileMode, bool, error) {
	return fromFileInfo(os.Lstat(filepath.Join(dir, name)))
}

// statEntry is lstatEntry but follows a final symlink.
func statEntry(_ int, dir, name string) (rawStat, fs.FileMode, bool, error) {
	return fromFileInfo(os.Stat(filepath.Join(dir, name)))
}

func fromFileInfo(fi fs.FileInfo, err error) (rawStat, fs.FileMode, bool, error) {
	if err != nil {
		return rawStat{}, 0, false, err
	}
	st, have := fromSys(fi.Sys())
	// Size and mtime are available portably even when the platform stat is
	// not, and the scanner reads them unconditionally.
	st.Size = fi.Size()
	if !have {
		st.Mtime = fi.ModTime().Unix()
	}
	return st, fi.Mode(), have, nil
}
