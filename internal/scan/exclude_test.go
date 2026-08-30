package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/StevenGann/ShareDirStat/internal/model"
)

func TestMatcherSemantics(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		isDir    bool
		want     bool
	}{
		// gitignore's defining rule: no separator means "this basename, at any
		// depth". Anchoring these to the share root is what made the natural
		// spellings silently match nothing below the top level.
		{"bare name at root", []string{"@eaDir"}, "@eaDir", true, true},
		{"bare name nested", []string{"@eaDir"}, "a/b/@eaDir", true, true},
		{"bare glob nested", []string{"*.tmp"}, "a/b/c.tmp", false, true},
		{"bare glob no match", []string{"*.tmp"}, "a/b/c.txt", false, false},
		{"leading slash anchors", []string{"/build"}, "a/build", true, false},
		{"leading slash at root", []string{"/build"}, "build", true, true},
		// A pattern that contains a separator stays anchored at the root.
		{"anchored by separator", []string{"a/b"}, "a/b", true, true},
		{"anchored not nested", []string{"a/b"}, "x/a/b", true, false},
		{"doublestar prefix", []string{"**/node_modules"}, "x/y/node_modules", true, true},
		{"doublestar at root", []string{"**/node_modules"}, "node_modules", true, true},
		{"doublestar suffix", []string{"cache/**"}, "cache/a/b", false, true},
		// Trailing slash restricts to directories.
		{"dir only matches dir", []string{"logs/"}, "a/logs", true, true},
		{"dir only skips file", []string{"logs/"}, "a/logs", false, false},
		// Last matching pattern wins, so a negation can re-include.
		{"negation re-includes", []string{"*.log", "!keep.log"}, "a/keep.log", false, false},
		{"negation order matters", []string{"!keep.log", "*.log"}, "a/keep.log", false, true},
		{"comments and blanks ignored", []string{"# a comment", "", "*.bak"}, "x.bak", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMatcher(tc.patterns)
			if got := m.Match(tc.path, tc.isDir); got != tc.want {
				t.Errorf("Match(%q, dir=%v) with %v = %v, want %v", tc.path, tc.isDir, tc.patterns, got, tc.want)
			}
			// The crawl uses the pre-split form; it must agree exactly.
			if got := m.MatchSegments(splitRel(nil, tc.path), tc.isDir); got != tc.want {
				t.Errorf("MatchSegments disagrees with Match for %q", tc.path)
			}
		})
	}
}

func TestSplitRel(t *testing.T) {
	cases := map[string][]string{
		"":      {},
		"a":     {"a"},
		"a/b":   {"a", "b"},
		"a/b/c": {"a", "b", "c"},
	}
	for in, want := range cases {
		got := splitRel(nil, in)
		if len(got) != len(want) {
			t.Fatalf("splitRel(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("splitRel(%q) = %v, want %v", in, got, want)
			}
		}
	}
	// The buffer is reused across entries, so a longer path after a shorter
	// one must not leave stale trailing segments behind.
	buf := splitRel(nil, "a/b/c")
	if got := splitRel(buf, "x"); len(got) != 1 || got[0] != "x" {
		t.Fatalf("reused buffer leaked: %v", got)
	}
}

// Exclusions are matched against the share-relative path, and the crawl builds
// that path from the parent's segments plus the entry name. This exercises it
// through a real scan at several depths, which is the wiring that a unit test
// of the matcher alone cannot reach.
func TestScanAppliesExclusionsAtEveryDepth(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("keep.dat")
	mk("skip.tmp")
	mk("a/keep.dat")
	mk("a/skip.tmp")
	mk("a/b/keep.dat")
	mk("a/b/skip.tmp")
	mk("a/@eaDir/thumb.jpg")
	mk("@eaDir/thumb.jpg")
	mk("a/b/c/@eaDir/thumb.jpg")

	gen, err := Run(context.Background(), Options{
		ShareID: "x", Root: root, Basis: model.BasisApparent, Concurrency: 2,
		Excludes: []string{"*.tmp", "@eaDir"},
	}, model.GenerationMeta{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := gen.Stats()
	if st.Files != 3 {
		t.Errorf("files = %d, want 3 (only the keep.dat files)", st.Files)
	}
	if st.Size != 30 {
		t.Errorf("size = %d, want 30", st.Size)
	}
	// Three @eaDir directories and three .tmp files were skipped.
	if st.Excluded != 6 {
		t.Errorf("excluded = %d, want 6", st.Excluded)
	}
	for _, gone := range []string{"skip.tmp", "a/skip.tmp", "a/b/skip.tmp", "@eaDir", "a/@eaDir", "a/b/c/@eaDir"} {
		if _, ok := gen.Info(gone); ok {
			t.Errorf("%s should have been excluded but is in the model", gone)
		}
	}
	for _, present := range []string{"keep.dat", "a/keep.dat", "a/b/keep.dat"} {
		if _, ok := gen.Info(present); !ok {
			t.Errorf("%s is missing from the model", present)
		}
	}
}
