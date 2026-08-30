package scan

import "syscall"

// errnoName maps the errno values a crawl realistically hits to their
// symbolic names, so the API can report a stable machine-readable code
// (FR-SCAN-08).
func errnoName(e syscall.Errno) string {
	switch e {
	case syscall.EACCES:
		return "EACCES"
	case syscall.EPERM:
		return "EPERM"
	case syscall.ENOENT:
		return "ENOENT"
	case syscall.ENOTDIR:
		return "ENOTDIR"
	case syscall.EIO:
		return "EIO"
	case syscall.ELOOP:
		return "ELOOP"
	case syscall.ENAMETOOLONG:
		return "ENAMETOOLONG"
	case syscall.EMFILE:
		return "EMFILE"
	case syscall.ENFILE:
		return "ENFILE"
	case syscall.ESTALE:
		return "ESTALE"
	case syscall.ENOTCONN:
		return "ENOTCONN"
	case syscall.EOVERFLOW:
		return "EOVERFLOW"
	case 0:
		return ""
	}
	return e.Error()
}
