package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// statFixture builds a directory holding one of every entry kind the crawl's
// classify switch can encounter, including the mode bits and name encodings
// that a naive stat translation gets wrong.
func statFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, mode fs.FileMode) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("plain.txt", 0o644)
	write("setuid.bin", 0o4755)
	write("setgid.bin", 0o2755)
	write("exec.sh", 0o755)
	write("noperm.dat", 0o000)
	write("weird\xff\xfename", 0o644)
	write("spaces and.dots.tar.gz", 0o644)
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sticky"), 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "sticky"), 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("plain.txt", filepath.Join(dir, "link-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("subdir", filepath.Join(dir, "link-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "link-broken")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "plain.txt"), filepath.Join(dir, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Logf("mkfifo unavailable: %v", err)
	}
	return dir
}

func openDirFD(t *testing.T, dir string) (int, func()) {
	t.Helper()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return int(f.Fd()), func() { _ = f.Close() }
}

// The crawl stats entries relative to an open directory descriptor instead of
// calling os.Lstat on a joined path. That is a pure performance change, so the
// facts it produces must be identical, field for field, to what os.Lstat
// reports -- including the mode bits (setuid/setgid/sticky and the file type)
// that are reconstructed by hand from the raw st_mode.
func TestLstatEntryMatchesOsLstat(t *testing.T) {
	dir := statFixture(t)
	fd, closeDir := openDirFD(t, dir)
	defer closeDir()

	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 12 {
		t.Fatalf("fixture only has %d entries", len(names))
	}
	for _, de := range names {
		name := de.Name()
		t.Run(name, func(t *testing.T) {
			fi, err := os.Lstat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			want, haveWant := fromSys(fi.Sys())
			got, mode, haveGot, err := lstatEntry(fd, dir, name)
			if err != nil {
				t.Fatalf("lstatEntry: %v", err)
			}
			if haveGot != haveWant {
				t.Fatalf("haveStat = %v, os.Lstat path says %v", haveGot, haveWant)
			}
			if mode != fi.Mode() {
				t.Errorf("mode = %v (%#o), want %v (%#o)", mode, mode, fi.Mode(), fi.Mode())
			}
			if got != want {
				t.Errorf("rawStat mismatch\n got %+v\nwant %+v", got, want)
			}
			// The classifications the scanner actually branches on.
			if mode.IsDir() != fi.IsDir() ||
				mode.IsRegular() != fi.Mode().IsRegular() ||
				(mode&fs.ModeSymlink != 0) != (fi.Mode()&fs.ModeSymlink != 0) {
				t.Errorf("type bits disagree: got %v want %v", mode, fi.Mode())
			}
		})
	}
}

// The follow_symlinks path re-stats a link's target; it must agree with os.Stat.
func TestStatEntryMatchesOsStat(t *testing.T) {
	dir := statFixture(t)
	fd, closeDir := openDirFD(t, dir)
	defer closeDir()

	for _, name := range []string{"link-file", "link-dir", "plain.txt", "subdir"} {
		t.Run(name, func(t *testing.T) {
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			want, _ := fromSys(fi.Sys())
			got, mode, _, err := statEntry(fd, dir, name)
			if err != nil {
				t.Fatalf("statEntry: %v", err)
			}
			if mode != fi.Mode() {
				t.Errorf("mode = %v, want %v", mode, fi.Mode())
			}
			if got != want {
				t.Errorf("rawStat mismatch\n got %+v\nwant %+v", got, want)
			}
		})
	}
	// A broken link resolves for lstat but not for stat, which is how the
	// scanner decides not to descend.
	if _, _, _, err := statEntry(fd, dir, "link-broken"); err == nil {
		t.Error("statEntry on a dangling symlink: want an error, got nil")
	}
	if _, _, _, err := lstatEntry(fd, dir, "link-broken"); err != nil {
		t.Errorf("lstatEntry on a dangling symlink: %v", err)
	}
}

// A name that no longer exists must report a not-exist error, because the
// crawl distinguishes that case (an entry that vanished mid-scan, which is
// normal) from a real I/O failure (which marks the directory partial).
func TestLstatEntryVanishedIsNotExist(t *testing.T) {
	dir := statFixture(t)
	fd, closeDir := openDirFD(t, dir)
	defer closeDir()

	_, _, _, err := lstatEntry(fd, dir, "gone-between-readdir-and-stat")
	if !os.IsNotExist(err) {
		t.Fatalf("want a not-exist error, got %v", err)
	}
}

func TestJoinPath(t *testing.T) {
	cases := map[[2]string]string{
		{"/shares/media", "Movies"}: "/shares/media/Movies",
		{"/", "etc"}:                "/etc",
		{"", "x"}:                   "/x",
	}
	for in, want := range cases {
		if got := joinPath(in[0], in[1]); got != want {
			t.Errorf("joinPath(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
		// It must agree with the filepath.Join it replaced.
		if in[0] != "" {
			if got, std := joinPath(in[0], in[1]), filepath.Join(in[0], in[1]); got != std {
				t.Errorf("joinPath(%q,%q)=%q but filepath.Join=%q", in[0], in[1], got, std)
			}
		}
	}
}
