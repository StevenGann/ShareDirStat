package model

import (
	"math"
	"testing"
	"time"
)

// buildMediaGen assembles a tree with media durations through the Builder.
//
//	/                 10200
//	  Movies           9000   big.mkv 6000/60s, small.mkv 3000/120s
//	  Music            1000   song.mp3 1000/100s
//	  readme.txt        200   (no duration)
func buildMediaGen(t *testing.T) *Generation {
	t.Helper()
	b := NewBuilder("s", "/root", BasisApparent, 0, 16)
	b.CollectMedia()
	kids, err := b.AddChildren(b.Root(), []Entry{
		{Name: "Movies", Kind: KindDir},
		{Name: "Music", Kind: KindDir},
		{Name: "readme.txt", Kind: KindFile, Size: 200},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	movies, music := kids[0], kids[1]
	if _, err := b.AddChildren(movies, []Entry{
		{Name: "big.mkv", Kind: KindFile, Size: 6000, Dur: 60},
		{Name: "small.mkv", Kind: KindFile, Size: 3000, Dur: 120},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddChildren(music, []Entry{
		{Name: "song.mp3", Kind: KindFile, Size: 1000, Dur: 100},
	}, nil); err != nil {
		t.Fatal(err)
	}
	return b.Finalize(GenerationMeta{ID: "gm", ScannedAt: time.Unix(1000, 0)}, 10)
}

func TestMediaAggregation(t *testing.T) {
	g := buildMediaGen(t)

	st := g.Stats()
	if st.MediaDur != 280 || st.MediaSize != 10000 {
		t.Errorf("stats media = %ds/%dB, want 280s/10000B", st.MediaDur, st.MediaSize)
	}

	movies, _ := g.Info("Movies")
	if movies.Dur != 180 || movies.MediaSize != 9000 {
		t.Errorf("Movies media = %ds/%dB, want 180s/9000B", movies.Dur, movies.MediaSize)
	}
	root, _ := g.Info("")
	if root.Dur != 280 || root.MediaSize != 10000 {
		t.Errorf("root media = %ds/%dB, want 280s/10000B", root.Dur, root.MediaSize)
	}
	txt, _ := g.Info("readme.txt")
	if txt.Dur != 0 || txt.MediaSize != 0 {
		t.Errorf("readme.txt media = %ds/%dB, want none", txt.Dur, txt.MediaSize)
	}
}

// Children are stored sorted by size, which permutes the arena after the
// duration array was filled; every file must keep its own duration.
func TestMediaArraysSurviveChildSorting(t *testing.T) {
	g := buildMediaGen(t)
	want := map[string]uint32{
		"Movies/big.mkv":   60,
		"Movies/small.mkv": 120,
		"Music/song.mp3":   100,
		"readme.txt":       0,
	}
	for path, dur := range want {
		n, ok := g.Info(path)
		if !ok {
			t.Fatalf("resolve %q failed", path)
		}
		if n.Dur != dur {
			t.Errorf("%s duration = %d, want %d", path, n.Dur, dur)
		}
	}
}

func TestMediaSortFields(t *testing.T) {
	g := buildMediaGen(t)

	// duration desc: Movies (180) > Music (100) > readme.txt (0, always last).
	res, ok := g.Tree(ListOptions{Sort: SortDur, Desc: true})
	if !ok {
		t.Fatal("tree failed")
	}
	got := []string{res.Children[0].Name, res.Children[1].Name, res.Children[2].Name}
	if got[0] != "Movies" || got[1] != "Music" || got[2] != "readme.txt" {
		t.Errorf("duration order = %v", got)
	}

	// spm desc: Movies 9000B/3min = 3000, Music 1000B/(100/60)min = 600,
	// readme.txt has no media and sorts beneath both.
	res, ok = g.Tree(ListOptions{Sort: SortSpm, Desc: true})
	if !ok {
		t.Fatal("tree failed")
	}
	got = []string{res.Children[0].Name, res.Children[1].Name, res.Children[2].Name}
	if got[0] != "Movies" || got[1] != "Music" || got[2] != "readme.txt" {
		t.Errorf("spm order = %v", got)
	}

	// Inside Movies, small.mkv (3000B/2min = 1500) beats big.mkv only on
	// duration; big.mkv (6000B/1min) wins on spm.
	res, _ = g.Tree(ListOptions{Path: "Movies", Sort: SortSpm, Desc: true})
	if res.Children[0].Name != "big.mkv" {
		t.Errorf("spm in Movies = %s first", res.Children[0].Name)
	}
	res, _ = g.Tree(ListOptions{Path: "Movies", Sort: SortDur, Desc: true})
	if res.Children[0].Name != "small.mkv" {
		t.Errorf("duration in Movies = %s first", res.Children[0].Name)
	}
}

func TestMediaRemoveUpdatesAncestors(t *testing.T) {
	g := buildMediaGen(t)
	if _, _, ok := g.Remove("Movies/big.mkv"); !ok {
		t.Fatal("remove failed")
	}
	movies, _ := g.Info("Movies")
	if movies.Dur != 120 || movies.MediaSize != 3000 {
		t.Errorf("Movies after remove = %ds/%dB, want 120s/3000B", movies.Dur, movies.MediaSize)
	}
	st := g.Stats()
	if st.MediaDur != 220 || st.MediaSize != 4000 {
		t.Errorf("stats after remove = %ds/%dB, want 220s/4000B", st.MediaDur, st.MediaSize)
	}
}

func TestMediaSplice(t *testing.T) {
	g := buildMediaGen(t)

	// Rescan of Music: the song was re-encoded smaller and a new one arrived.
	sub := func() *Generation {
		b := NewBuilder("s", "/root/Music", BasisApparent, 0, 16)
		b.CollectMedia()
		if _, err := b.AddChildren(b.Root(), []Entry{
			{Name: "song.mp3", Kind: KindFile, Size: 500, Dur: 100},
			{Name: "new.mp3", Kind: KindFile, Size: 2000, Dur: 200},
		}, nil); err != nil {
			t.Fatal(err)
		}
		return b.Finalize(GenerationMeta{ID: "gs", ScannedAt: time.Unix(2000, 0)}, 10)
	}()
	if err := g.Splice("Music", sub); err != nil {
		t.Fatal(err)
	}
	music, _ := g.Info("Music")
	if music.Dur != 300 || music.MediaSize != 2500 {
		t.Errorf("Music after splice = %ds/%dB, want 300s/2500B", music.Dur, music.MediaSize)
	}
	root, _ := g.Info("")
	if root.Dur != 480 || root.MediaSize != 11500 {
		t.Errorf("root after splice = %ds/%dB, want 480s/11500B", root.Dur, root.MediaSize)
	}
	st := g.Stats()
	if st.MediaDur != 480 || st.MediaSize != 11500 {
		t.Errorf("stats after splice = %ds/%dB", st.MediaDur, st.MediaSize)
	}

	// A rescan whose builder found no media splices in as zeros.
	empty := func() *Generation {
		b := NewBuilder("s", "/root/Music", BasisApparent, 0, 16)
		b.CollectMedia()
		if _, err := b.AddChildren(b.Root(), []Entry{
			{Name: "notes.txt", Kind: KindFile, Size: 10},
		}, nil); err != nil {
			t.Fatal(err)
		}
		return b.Finalize(GenerationMeta{ID: "ge", ScannedAt: time.Unix(3000, 0)}, 10)
	}()
	if empty.Export().Durs != nil {
		t.Fatal("media-free generation should have dropped its arrays")
	}
	if err := g.Splice("Music", empty); err != nil {
		t.Fatal(err)
	}
	root, _ = g.Info("")
	if root.Dur != 180 || root.MediaSize != 9000 {
		t.Errorf("root after media-free splice = %ds/%dB, want 180s/9000B", root.Dur, root.MediaSize)
	}
}

func TestHardlinkDupContributesNoMedia(t *testing.T) {
	b := NewBuilder("s", "/root", BasisApparent, 0, 16)
	b.CollectMedia()
	if _, err := b.AddChildren(b.Root(), []Entry{
		{Name: "one.mp3", Kind: KindFile, Size: 1000, Dur: 100},
		{Name: "two.mp3", Kind: KindFile, Size: 1000, Dur: 100, Flags: FlagHardlinkDup},
	}, nil); err != nil {
		t.Fatal(err)
	}
	g := b.Finalize(GenerationMeta{ID: "gh"}, 10)
	root, _ := g.Info("")
	if root.Dur != 100 || root.MediaSize != 1000 {
		t.Errorf("root media = %ds/%dB, want the inode counted once", root.Dur, root.MediaSize)
	}
	// Removing the duplicate must not subtract what it never added.
	if _, _, ok := g.Remove("two.mp3"); !ok {
		t.Fatal("remove failed")
	}
	root, _ = g.Info("")
	if root.Dur != 100 || root.MediaSize != 1000 {
		t.Errorf("root media after dup removal = %ds/%dB, want unchanged", root.Dur, root.MediaSize)
	}
}

func TestMediaFreeShareCostsNothing(t *testing.T) {
	b := NewBuilder("s", "/root", BasisApparent, 0, 16)
	b.CollectMedia()
	if _, err := b.AddChildren(b.Root(), []Entry{
		{Name: "a.txt", Kind: KindFile, Size: 10},
	}, nil); err != nil {
		t.Fatal(err)
	}
	g := b.Finalize(GenerationMeta{ID: "gz"}, 10)
	raw := g.Export()
	if raw.Durs != nil || raw.MediaSizes != nil {
		t.Error("arrays should be dropped when the share holds no media")
	}
	if st := g.Stats(); st.MediaDur != 0 || st.MediaSize != 0 {
		t.Errorf("stats media = %+v, want zero", st)
	}
}

func TestDurationAggregateSaturates(t *testing.T) {
	b := NewBuilder("s", "/root", BasisApparent, 0, 16)
	b.CollectMedia()
	if _, err := b.AddChildren(b.Root(), []Entry{
		{Name: "a.mkv", Kind: KindFile, Size: 1, Dur: math.MaxUint32},
		{Name: "b.mkv", Kind: KindFile, Size: 1, Dur: 1000},
	}, nil); err != nil {
		t.Fatal(err)
	}
	g := b.Finalize(GenerationMeta{ID: "gsat"}, 10)
	root, _ := g.Info("")
	if root.Dur != math.MaxUint32 {
		t.Errorf("root duration = %d, want saturation at MaxUint32", root.Dur)
	}
}
