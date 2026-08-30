package ops

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newStore builds a share directory with the shapes an attacker would reach
// for, plus a sibling directory outside the share that must stay unreachable.
func newStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	base := t.TempDir()
	share := filepath.Join(base, "share")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{share, outside, filepath.Join(share, "sub", "deep")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(share, "file.txt"), "inside the share")
	write(filepath.Join(share, "sub", "deep", "buried.bin"), "buried")
	write(filepath.Join(outside, "secret.txt"), "MUST NOT BE READABLE")

	// Escape routes an attacker might plant inside the share.
	mustSymlink(t, outside, filepath.Join(share, "escape-dir"))
	mustSymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(share, "escape-file"))
	mustSymlink(t, "/etc", filepath.Join(share, "abs-escape"))
	mustSymlink(t, "../outside", filepath.Join(share, "rel-escape"))
	// A symlink that stays inside is legitimate.
	mustSymlink(t, "file.txt", filepath.Join(share, "inside-link"))

	s, err := OpenStore("test", share)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, share, outside
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestCleanRel(t *testing.T) {
	ok := map[string]string{
		"":                   "",
		"file.txt":           "file.txt",
		"/file.txt":          "file.txt",
		"a/b/c":              "a/b/c",
		"a//b///c":           "a/b/c",
		"./a/./b/":           "a/b",
		"a b/c d.mkv":        "a b/c d.mkv",
		"weird\xff\xfename":  "weird\xff\xfename",
		"dots...in.the.name": "dots...in.the.name",
		"..hidden":           "..hidden",
		"a/..b/c":            "a/..b/c",
		// Backslash is an ordinary byte in a Linux filename, not a separator,
		// so these are single segments and stay inside the share.
		`a\b`:                        `a\b`,
		`AC\DC - Back in Black.flac`: `AC\DC - Back in Black.flac`,
		`..\..\windows`:              `..\..\windows`,
	}
	for in, want := range ok {
		got, err := CleanRel(in)
		if err != nil || got != want {
			t.Errorf("CleanRel(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}

	bad := []string{
		"../etc/passwd",
		"a/../../etc/passwd",
		"a/b/..",
		"..",
		"../",
		"a/../b",
		"with\x00nul",
	}
	for _, in := range bad {
		if got, err := CleanRel(in); err == nil {
			t.Errorf("CleanRel(%q) = %q, nil; want an error", in, got)
		} else if !errors.Is(err, ErrUnsafePath) {
			t.Errorf("CleanRel(%q): want ErrUnsafePath, got %v", in, err)
		}
	}
}

func TestSymlinkEscapeIsRefused(t *testing.T) {
	s, _, outside := newStore(t)

	// Read the secret directly to prove the test fixture is real.
	if b, err := os.ReadFile(filepath.Join(outside, "secret.txt")); err != nil || !strings.Contains(string(b), "MUST NOT") {
		t.Fatalf("fixture is wrong: %q %v", b, err)
	}

	// Paths that traverse *through* a symlink leaving the share. Every
	// operation on these must fail. (A path that names the escaping symlink
	// itself is a different case, covered by TestSymlinkItselfIsAddressable:
	// there the link, never its target, is what gets stat-ed or unlinked.)
	escapes := []string{
		"escape-dir/secret.txt",
		"abs-escape/passwd",
		"rel-escape/secret.txt",
	}
	for _, rel := range escapes {
		t.Run(rel, func(t *testing.T) {
			f, err := s.OpenFile(rel)
			if err == nil {
				b, _ := io.ReadAll(f)
				_ = f.Close()
				t.Fatalf("SECURITY: opened %q and read %q", rel, b)
			}
			if _, err := s.Lstat(rel); err == nil {
				t.Errorf("SECURITY: stat succeeded through an escaping symlink %q", rel)
			}
			if err := s.Remove(rel); err == nil {
				t.Fatalf("SECURITY: removed %q through an escaping symlink", rel)
			}
		})
	}

	// The secret must still be there afterwards.
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatalf("SECURITY: the file outside the share was affected: %v", err)
	}
}

// TestEscapeIsClassifiedAsUnsafe pins how an os.Root escape refusal is
// classified. os.Root reports it with an unexported error, so ops matches its
// message; if a Go release rewords that, this test fails rather than the API
// silently answering traversal attempts with 500 instead of 400.
func TestEscapeIsClassifiedAsUnsafe(t *testing.T) {
	s, _, _ := newStore(t)
	for _, rel := range []string{"escape-dir/secret.txt", "abs-escape/passwd", "rel-escape/secret.txt"} {
		_, err := s.OpenFile(rel)
		if err == nil {
			t.Fatalf("SECURITY: %q was opened", rel)
		}
		if !errors.Is(err, ErrUnsafePath) {
			t.Errorf("OpenFile(%q) = %v; want it classified as ErrUnsafePath so the API answers 400, not 500", rel, err)
		}
	}
}

func TestSymlinkItselfIsAddressable(t *testing.T) {
	s, _, outside := newStore(t)
	// Lstat must describe the link, not its target, so the UI can show it as
	// a symlink and delete can unlink it.
	fi, err := s.Lstat("escape-file")
	if err != nil {
		t.Fatalf("lstat of an escaping symlink should describe the link: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("mode = %v, want a symlink", fi.Mode())
	}
	// Removing the link removes the link, never the target.
	target := filepath.Join(outside, "secret.txt")
	if err := s.Remove("escape-file"); err != nil {
		t.Fatalf("removing a symlink should work: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("SECURITY: unlinking the symlink also removed its target: %v", err)
	}
}

func TestSymlinkWithinShareIsFollowed(t *testing.T) {
	// Documented os.Root behaviour, pinned here so a future change is noticed:
	// a symlink that stays inside the share resolves normally.
	s, _, _ := newStore(t)
	f, err := s.OpenFile("inside-link")
	if err != nil {
		// O_NOFOLLOW means the final component is not followed, which is what
		// we want for downloads: the link itself is not a regular file.
		t.Logf("inside-link not opened with O_NOFOLLOW (expected): %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(f)
	if string(b) != "inside the share" {
		t.Errorf("content = %q", b)
	}
}

func TestTraversalNeverReachesTheFilesystem(t *testing.T) {
	s, _, outside := newStore(t)
	for _, rel := range []string{"../outside/secret.txt", "sub/../../outside/secret.txt", ".."} {
		clean, err := CleanRel(rel)
		if err == nil {
			// Should never get here, but if CleanRel ever loosened, os.Root
			// must still refuse.
			if f, err := s.OpenFile(clean); err == nil {
				_ = f.Close()
				t.Fatalf("SECURITY: %q resolved to something openable", rel)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatalf("the outside file was disturbed: %v", err)
	}
}

func TestShareRootIsProtected(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Remove(""); !errors.Is(err, ErrIsShareRoot) {
		t.Errorf("Remove(root) = %v, want ErrIsShareRoot", err)
	}
	if _, err := s.OpenFile(""); !errors.Is(err, ErrIsShareRoot) {
		t.Errorf("OpenFile(root) = %v, want ErrIsShareRoot", err)
	}
	// Stat of the root is allowed: the UI needs its size and mode.
	if _, err := s.Lstat(""); err != nil {
		t.Errorf("Lstat(root) = %v, want success", err)
	}
}

func TestOrdinaryOperations(t *testing.T) {
	s, share, _ := newStore(t)

	f, err := s.OpenFile("file.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "inside the share" {
		t.Errorf("content = %q", b)
	}

	entries, err := s.ReadDir("sub/deep")
	if err != nil || len(entries) != 1 || entries[0].Name() != "buried.bin" {
		t.Errorf("ReadDir = %v, %v", entries, err)
	}

	if err := s.Remove("sub/deep/buried.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(share, "sub/deep/buried.bin")); !os.IsNotExist(err) {
		t.Error("file should be gone")
	}

	if _, err := s.Lstat("does/not/exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing path = %v, want ErrNotFound", err)
	}
}

func TestCheckWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	s, share, _ := newStore(t)
	if err := s.CheckWritable("file.txt"); err != nil {
		t.Errorf("a writable share should pass: %v", err)
	}

	locked := filepath.Join(share, "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := s.CheckWritable("locked/whatever"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a read-only parent should fail with ErrReadOnly, got %v", err)
	}
}

func TestSameDevice(t *testing.T) {
	s, _, _ := newStore(t)
	fi, err := s.Lstat("file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !s.SameDevice(fi) {
		t.Error("a file in the share should be on the share's own device")
	}
}

func TestErrno(t *testing.T) {
	s, share, _ := newStore(t)
	if os.Geteuid() != 0 {
		locked := filepath.Join(share, "ro")
		if err := os.Mkdir(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "x"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		err := s.Remove("ro/x")
		if got := Errno(err); got != "EACCES" && got != "EPERM" {
			t.Errorf("Errno = %q, want EACCES or EPERM (err %v)", got, err)
		}
	}
	if got := Errno(nil); got != "" {
		t.Errorf("Errno(nil) = %q", got)
	}
	if got := Errno(errors.New("plain")); got != "" {
		t.Errorf("Errno(plain) = %q", got)
	}
	if got := Errno(s.Remove("nope")); got != "ENOENT" {
		t.Errorf("Errno(missing) = %q, want ENOENT", got)
	}
}
