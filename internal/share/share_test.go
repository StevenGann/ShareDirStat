package share

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StevenGann/ShareDirStat/internal/config"
)

func TestRegistryCheck(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present")
	_ = os.Mkdir(present, 0o755)
	file := filepath.Join(dir, "file")
	_ = os.WriteFile(file, []byte("x"), 0o644)

	tr, f := true, false
	cfg := &config.Config{Shares: []config.Share{
		{ID: "p", Name: "P", Path: present, AllowDelete: &tr, AllowDownload: &tr},
		{ID: "m", Name: "M", Path: filepath.Join(dir, "missing"), AllowDelete: &f, AllowDownload: &tr},
		{ID: "f", Name: "F", Path: file, AllowDelete: &f, AllowDownload: &tr},
	}}
	r := New(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	r.CheckAll()

	got := map[string]State{}
	for _, i := range r.Infos() {
		got[i.ID] = i.State
	}
	if got["p"] != StateNeverScanned || got["m"] != StateUnavailable || got["f"] != StateUnavailable {
		t.Errorf("states = %v", got)
	}
	s, _ := r.Get("m")
	if info := s.Snapshot(); info.Error == "" || info.AllowDelete {
		t.Errorf("unavailable share should carry an error and honour allow_delete: %+v", info)
	}
	if ids := r.Infos(); ids[0].ID != "f" || ids[2].ID != "p" {
		t.Errorf("Infos must be sorted by id: %v", ids)
	}
}

const sampleMountInfo = `22 28 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:5 - proc proc rw
28 0 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro
101 28 0:45 / /shares/media rw,relatime shared:60 - nfs4 nas:/volume1/media rw,vers=4.1,rsize=131072
102 28 0:46 / /shares/backups ro,relatime shared:61 - cifs //nas/backups ro,vers=3.0
103 28 0:47 / /shares/with\040space rw,relatime - ext4 /dev/sdb1 rw
104 101 0:48 / /shares/media/nested rw,relatime - tmpfs tmpfs rw
`

func TestFindMount(t *testing.T) {
	cases := []struct {
		path string
		typ  string
		mp   string
		ro   bool
	}{
		{"/shares/media", "nfs4", "/shares/media", false},
		{"/shares/media/Movies", "nfs4", "/shares/media", false},
		{"/shares/media/nested/x", "tmpfs", "/shares/media/nested", false},
		{"/shares/mediafoo", "ext4", "/", false},
		{"/shares/backups/2024", "cifs", "/shares/backups", true},
		{"/shares/with space/x", "ext4", "/shares/with space", false},
		{"/", "ext4", "/", false},
	}
	for _, c := range cases {
		got := findMount(strings.NewReader(sampleMountInfo), c.path)
		if got == nil || got.Type != c.typ || got.MountPoint != c.mp || got.Readonly != c.ro {
			t.Errorf("findMount(%q) = %+v; want %s %s ro=%v", c.path, got, c.typ, c.mp, c.ro)
		}
	}
}
