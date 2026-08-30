//go:build linux

package ops

import "golang.org/x/sys/unix"

// unixAccessW checks write permission on an already-open directory using the
// process's effective ids. Checking against the file descriptor rather than a
// path means the answer refers to the very object the delete will act on.
func unixAccessW(fd int) error {
	return unix.Faccessat(fd, ".", unix.W_OK, unix.AT_EACCESS)
}
