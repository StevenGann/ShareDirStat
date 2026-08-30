package share

import (
	"bufio"
	"io"
	"os"
	"strings"
)

const mountInfoPath = "/proc/self/mountinfo"

// lookupFilesystem finds the mount that contains path. Returns nil when
// /proc/self/mountinfo is not available (non-Linux, restricted container).
func lookupFilesystem(path string) *Filesystem {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return findMount(f, path)
}

// findMount parses mountinfo(5) content and returns the longest mount point
// that is a prefix of path.
func findMount(r io.Reader, path string) *Filesystem {
	var best *Filesystem
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
		//  0  1  2    3     4      5           6      7   8     9        10
		sep := -1
		for i, fv := range fields {
			if fv == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+2 >= len(fields) {
			continue
		}
		mountPoint := unescapeMountField(fields[4])
		if !pathWithin(mountPoint, path) {
			continue
		}
		if best != nil && len(mountPoint) <= len(best.MountPoint) {
			continue
		}
		opts := fields[5]
		superOpts := ""
		if sep+3 < len(fields) {
			superOpts = fields[sep+3]
		}
		best = &Filesystem{
			Type:       fields[sep+1],
			MountPoint: mountPoint,
			Readonly:   hasOpt(opts, "ro") || hasOpt(superOpts, "ro"),
		}
	}
	return best
}

func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

func pathWithin(mount, path string) bool {
	if mount == "/" {
		return true
	}
	return path == mount || strings.HasPrefix(path, mount+"/")
}

// unescapeMountField decodes the octal escapes used by mountinfo for
// spaces, tabs, newlines and backslashes.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
