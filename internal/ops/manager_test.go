package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/events"
	"github.com/StevenGann/ShareDirStat/internal/model"
	"github.com/StevenGann/ShareDirStat/internal/scan"
	"github.com/StevenGann/ShareDirStat/internal/share"
)

type env struct {
	m       *Manager
	reg     *share.Registry
	sh      *share.Share
	share   string
	outside string
	data    string
	events  *events.Broker
}

// newEnv builds a share with a small tree, scans it so the model is
// populated, and returns a Manager wired to it.
func newEnv(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()
	base := t.TempDir()
	sharePath := filepath.Join(base, "media")
	outside := filepath.Join(base, "outside")
	data := filepath.Join(base, "data")
	for _, d := range []string{filepath.Join(sharePath, "Movies", "2019"), filepath.Join(sharePath, "Music"), outside, data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string, n int) {
		if err := os.WriteFile(filepath.Join(sharePath, p), bytes.Repeat([]byte{'x'}, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Movies/2019/big.mkv", 8000)
	write("Movies/2019/small.mkv", 100)
	write("Movies/trailer.mp4", 2000)
	write("Music/song.flac", 500)
	write("readme.txt", 42)
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(sharePath, "escape")); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DataDir = data
	tr := true
	sched := ""
	cfg.Shares = []config.Share{{
		ID: "media", Name: "Media", Path: sharePath,
		AllowDelete: &tr, AllowDownload: &tr, Concurrency: 2,
		Schedule: &sched, SizeBasis: "apparent",
	}}
	if mutate != nil {
		mutate(cfg)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := share.New(cfg, log)
	reg.CheckAll()
	sh, _ := reg.Get("media")

	// Populate the model the way a real scan would.
	gen, err := scan.Run(context.Background(), scan.Options{
		ShareID: "media", Root: sharePath, Concurrency: 2, TopN: 10,
		Excludes: []string{"**/" + TrashDir},
	}, model.GenerationMeta{ID: "G1", ScannedAt: time.Now()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sh.SetGeneration(gen)

	bus := events.NewBroker()
	m, err := NewManager(cfg, reg, bus, log, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return &env{m: m, reg: reg, sh: sh, share: sharePath, outside: outside, data: data, events: bus}
}

// deleteNow runs the full preview-then-delete protocol.
func (e *env) deleteNow(t *testing.T, paths ...string) *DeleteResult {
	t.Helper()
	p, err := e.m.Preview("media", paths)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	res, err := e.m.Delete("media", paths, p.Confirm, ClientInfo{IP: "127.0.0.1", UserAgent: "test"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	return res
}

func TestPreviewDescribesWhatWouldGo(t *testing.T) {
	e := newEnv(t, nil)
	p, err := e.m.Preview("media", []string{"Movies/2019"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 {
		t.Fatalf("targets = %+v", p.Targets)
	}
	tgt := p.Targets[0]
	if tgt.Kind != "dir" || tgt.Size != 8100 || tgt.Files != 2 || tgt.Dirs != 1 {
		t.Errorf("target = %+v, want dir 8100 bytes, 2 files, 1 dir", tgt)
	}
	if !tgt.Exists {
		t.Error("target should exist")
	}
	if p.TotalSize != 8100 {
		t.Errorf("total = %d", p.TotalSize)
	}
	if p.Confirm == "" || p.ExpiresAt.Before(time.Now()) {
		t.Errorf("token = %q expiring %v", p.Confirm, p.ExpiresAt)
	}
	// A single directory is confirmed by typing its own name.
	if p.NameToType != "2019" {
		t.Errorf("name_to_type = %q, want 2019", p.NameToType)
	}
}

func TestPreviewWarnsAboutStaleAndOddTargets(t *testing.T) {
	e := newEnv(t, nil)
	// A file created after the scan is not in the model.
	if err := os.WriteFile(filepath.Join(e.share, "new.bin"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := e.m.Preview("media", []string{"new.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Targets[0].Warnings, " "), "not present in the last scan") {
		t.Errorf("warnings = %v", p.Targets[0].Warnings)
	}
	if p.Targets[0].Kind != "file" || !p.Targets[0].Exists {
		t.Errorf("target = %+v", p.Targets[0])
	}

	// A file deleted behind our back.
	if err := os.Remove(filepath.Join(e.share, "readme.txt")); err != nil {
		t.Fatal(err)
	}
	p2, err := e.m.Preview("media", []string{"readme.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Targets[0].Exists {
		t.Error("target should be reported as gone")
	}
	if !strings.Contains(strings.Join(p2.Targets[0].Warnings, " "), "no longer on disk") {
		t.Errorf("warnings = %v", p2.Targets[0].Warnings)
	}
}

func TestDeleteFileUpdatesDiskModelAndAudit(t *testing.T) {
	e := newEnv(t, nil)
	before := e.sh.Generation().Stats().Size

	res := e.deleteNow(t, "Movies/2019/big.mkv")
	if len(res.Results) != 1 || res.Results[0].Outcome != OutcomeDeleted {
		t.Fatalf("results = %+v", res.Results)
	}
	if res.FreedTotal != 8000 {
		t.Errorf("freed = %d, want 8000", res.FreedTotal)
	}
	if _, err := os.Stat(filepath.Join(e.share, "Movies/2019/big.mkv")); !os.IsNotExist(err) {
		t.Error("the file is still on disk")
	}
	// The live model must agree without waiting for a rescan (FR-DEL-05).
	gen := e.sh.Generation()
	if _, ok := gen.Info("Movies/2019/big.mkv"); ok {
		t.Error("the deleted file still resolves in the model")
	}
	if got := gen.Stats().Size; got != before-8000 {
		t.Errorf("share total = %d, want %d", got, before-8000)
	}
	if n, _ := gen.Info("Movies/2019"); n.Size != 100 {
		t.Errorf("parent size = %d, want 100", n.Size)
	}

	entries, err := e.m.Audit().Recent(10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
	a := entries[0]
	if a.Share != "media" || a.Path != "Movies/2019/big.mkv" || a.Outcome != OutcomeDeleted ||
		a.Size != 8000 || a.ClientIP != "127.0.0.1" || a.UserAgent != "test" {
		t.Errorf("audit entry = %+v", a)
	}
}

func TestDeleteDirectoryIsRecursive(t *testing.T) {
	e := newEnv(t, nil)
	res := e.deleteNow(t, "Movies")
	if res.Results[0].Outcome != OutcomeDeleted {
		t.Fatalf("result = %+v", res.Results[0])
	}
	// 3 files + 2 directories.
	if res.Results[0].Removed != 5 {
		t.Errorf("removed = %d, want 5", res.Results[0].Removed)
	}
	if _, err := os.Stat(filepath.Join(e.share, "Movies")); !os.IsNotExist(err) {
		t.Error("the directory is still on disk")
	}
	gen := e.sh.Generation()
	for _, p := range []string{"Movies", "Movies/2019", "Movies/2019/big.mkv"} {
		if _, ok := gen.Info(p); ok {
			t.Errorf("%s still resolves in the model", p)
		}
	}
	if _, ok := gen.Info("Music/song.flac"); !ok {
		t.Error("an unrelated subtree was damaged")
	}
}

func TestDeleteUnlinksSymlinkNotTarget(t *testing.T) {
	e := newEnv(t, nil)
	res := e.deleteNow(t, "escape")
	if res.Results[0].Outcome != OutcomeDeleted {
		t.Fatalf("result = %+v", res.Results[0])
	}
	if _, err := os.Stat(filepath.Join(e.outside, "secret.txt")); err != nil {
		t.Fatalf("SECURITY: the symlink's target was deleted: %v", err)
	}
}

func TestDeleteRejectsEscapesAndTheRoot(t *testing.T) {
	e := newEnv(t, nil)
	for _, p := range []string{"../outside/secret.txt", "", "/", "Movies/../../outside", "a\x00b"} {
		if _, err := e.m.Preview("media", []string{p}); err == nil {
			t.Errorf("SECURITY: preview accepted %q", p)
		}
	}
	if _, err := os.Stat(filepath.Join(e.outside, "secret.txt")); err != nil {
		t.Fatalf("SECURITY: a file outside the share was touched: %v", err)
	}
}

func TestConfirmationTokenIsSingleUseAndBound(t *testing.T) {
	e := newEnv(t, nil)

	p, err := e.m.Preview("media", []string{"readme.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// A token issued for one path must not delete another.
	if _, err := e.m.Delete("media", []string{"Music/song.flac"}, p.Confirm, ClientInfo{}); !errors.Is(err, ErrBadToken) {
		t.Errorf("token reuse on another path = %v, want ErrBadToken", err)
	}
	if _, err := os.Stat(filepath.Join(e.share, "Music/song.flac")); err != nil {
		t.Fatalf("SECURITY: the wrong file was deleted: %v", err)
	}

	// A fresh token works exactly once.
	p2, _ := e.m.Preview("media", []string{"readme.txt"})
	if _, err := e.m.Delete("media", []string{"readme.txt"}, p2.Confirm, ClientInfo{}); err != nil {
		t.Fatalf("first use should succeed: %v", err)
	}
	if _, err := e.m.Delete("media", []string{"readme.txt"}, p2.Confirm, ClientInfo{}); !errors.Is(err, ErrBadToken) {
		t.Errorf("second use = %v, want ErrBadToken", err)
	}

	// Garbage and empty tokens are refused.
	for _, tok := range []string{"", "not-a-token", strings.Repeat("A", 43)} {
		if _, err := e.m.Delete("media", []string{"Music/song.flac"}, tok, ClientInfo{}); !errors.Is(err, ErrBadToken) {
			t.Errorf("token %q = %v, want ErrBadToken", tok, err)
		}
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	e := newEnv(t, nil)
	// Reach into the store to age the token rather than sleeping for minutes.
	p, _ := e.m.Preview("media", []string{"readme.txt"})
	e.m.toks.mu.Lock()
	for _, it := range e.m.toks.items {
		it.expires = time.Now().Add(-time.Second)
	}
	e.m.toks.mu.Unlock()

	if _, err := e.m.Delete("media", []string{"readme.txt"}, p.Confirm, ClientInfo{}); !errors.Is(err, ErrBadToken) {
		t.Errorf("expired token = %v, want ErrBadToken", err)
	}
	if _, err := os.Stat(filepath.Join(e.share, "readme.txt")); err != nil {
		t.Error("the file should not have been deleted")
	}
}

func TestDeleteDisabledByConfig(t *testing.T) {
	t.Run("read-only server", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { c.Operations.Readonly = true })
		if err := e.m.CanDelete("media"); !errors.Is(err, ErrDeleteDisabled) {
			t.Errorf("CanDelete = %v, want ErrDeleteDisabled", err)
		}
		if _, err := e.m.Preview("media", []string{"readme.txt"}); !errors.Is(err, ErrDeleteDisabled) {
			t.Errorf("preview = %v", err)
		}
	})
	t.Run("share opt-out", func(t *testing.T) {
		e := newEnv(t, func(c *config.Config) { f := false; c.Shares[0].AllowDelete = &f })
		if err := e.m.CanDelete("media"); !errors.Is(err, ErrDeleteDisabled) {
			t.Errorf("CanDelete = %v, want ErrDeleteDisabled", err)
		}
	})
}

func TestPartialFailureIsReportedAndReconciled(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	var rescans []string
	e := newEnv(t, nil)
	e.m.hooks.StartRescan = func(_, path string) error {
		rescans = append(rescans, path)
		return nil
	}

	// Make one subdirectory unwritable so its child cannot be unlinked.
	locked := filepath.Join(e.share, "Movies", "2019")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	res := e.deleteNow(t, "Movies")
	pr := res.Results[0]
	if pr.Outcome != OutcomePartial {
		t.Fatalf("outcome = %s, want partial (%+v)", pr.Outcome, pr)
	}
	if pr.Removed == 0 || pr.Failed == 0 {
		t.Errorf("removed=%d failed=%d, want both non-zero", pr.Removed, pr.Failed)
	}
	if len(pr.Errors) == 0 || pr.Errors[0].Errno == "" {
		t.Errorf("expected per-entry errors with an errno: %+v", pr.Errors)
	}
	// The model must not be told the subtree is gone when it is not.
	if _, ok := e.sh.Generation().Info("Movies/2019/big.mkv"); !ok {
		t.Error("a file that survived was removed from the model")
	}
	if len(rescans) == 0 {
		t.Error("a reconcile rescan should have been requested")
	}
	entries, _ := e.m.Audit().Recent(5)
	if len(entries) == 0 || entries[0].Outcome != OutcomePartial {
		t.Errorf("audit should record the partial outcome: %+v", entries)
	}
}

func TestNestedPathsAreCollapsed(t *testing.T) {
	e := newEnv(t, nil)
	// Asking for a directory and something inside it must not double count
	// or try to delete the child twice.
	p, err := e.m.Preview("media", []string{"Movies/2019/big.mkv", "Movies", "Movies/2019"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 || p.Targets[0].Path != "Movies" {
		t.Fatalf("targets = %+v, want just Movies", p.Targets)
	}
	res, err := e.m.Delete("media", []string{"Movies/2019/big.mkv", "Movies", "Movies/2019"}, p.Confirm, ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Results[0].Outcome != OutcomeDeleted {
		t.Errorf("results = %+v", res.Results)
	}
}

func TestDeleteEmitsEvent(t *testing.T) {
	e := newEnv(t, nil)
	sub := e.events.Subscribe(0)
	defer sub.Close()

	e.deleteNow(t, "readme.txt")

	select {
	case ev := <-sub.C:
		if ev.Type != events.NodeDeleted {
			t.Errorf("event = %s, want node.deleted", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no node.deleted event")
	}
}

func TestTrashMode(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Operations.Delete.Trash.Enabled = true
		c.Operations.Delete.Trash.Retention = config.Duration(time.Hour)
	})
	if !e.m.TrashEnabled() {
		t.Fatal("trash should be enabled")
	}
	res := e.deleteNow(t, "Movies/2019/big.mkv")
	pr := res.Results[0]
	if pr.Outcome != OutcomeDeleted || pr.Trash == "" {
		t.Fatalf("result = %+v", pr)
	}
	if _, err := os.Stat(filepath.Join(e.share, "Movies/2019/big.mkv")); !os.IsNotExist(err) {
		t.Error("the original should be gone")
	}
	moved := filepath.Join(e.share, pr.Trash)
	fi, err := os.Stat(moved)
	if err != nil {
		t.Fatalf("the file should be in the trash at %s: %v", pr.Trash, err)
	}
	if fi.Size() != 8000 {
		t.Errorf("trashed size = %d, want 8000", fi.Size())
	}
	if !strings.Contains(pr.Trash, TrashDir) || !strings.HasSuffix(pr.Trash, "Movies/2019/big.mkv") {
		t.Errorf("trash path %q should preserve the original layout", pr.Trash)
	}

	// Purging leaves a recent batch alone and removes an old one.
	e.m.PurgeTrash()
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("a fresh batch should survive a purge: %v", err)
	}
	batch := filepath.Dir(filepath.Join(e.share, TrashDir, strings.TrimPrefix(pr.Trash, TrashDir+"/")))
	_ = batch
	old := filepath.Join(e.share, TrashDir, "20200101T000000Z")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "ancient.bin"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.m.PurgeTrash()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an expired batch should have been purged")
	}
}

func TestDownload(t *testing.T) {
	e := newEnv(t, nil)

	f, fi, err := e.m.OpenForDownload("media", "Movies/2019/big.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if fi.Size() != 8000 {
		t.Errorf("size = %d", fi.Size())
	}
	b, _ := io.ReadAll(f)
	if len(b) != 8000 {
		t.Errorf("read %d bytes", len(b))
	}

	// A file created after the scan is still downloadable (FR-DL-05).
	if err := os.WriteFile(filepath.Join(e.share, "fresh.bin"), []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}
	nf, _, err := e.m.OpenForDownload("media", "fresh.bin")
	if err != nil {
		t.Fatalf("a new file should be downloadable: %v", err)
	}
	_ = nf.Close()
}

func TestDownloadRefusals(t *testing.T) {
	e := newEnv(t, nil)
	cases := map[string]error{
		"Movies":                ErrNotAFile,
		"escape":                ErrNotAFile, // a symlink is not a regular file
		"":                      ErrIsShareRoot,
		"../outside/secret.txt": ErrUnsafePath,
		"missing.bin":           ErrNotFound,
	}
	for rel, want := range cases {
		_, _, err := e.m.OpenForDownload("media", rel)
		if !errors.Is(err, want) {
			t.Errorf("OpenForDownload(%q) = %v, want %v", rel, err, want)
		}
	}

	off := newEnv(t, func(c *config.Config) { f := false; c.Shares[0].AllowDownload = &f })
	if _, _, err := off.m.OpenForDownload("media", "readme.txt"); !errors.Is(err, ErrDownloadDisabled) {
		t.Errorf("disabled share = %v, want ErrDownloadDisabled", err)
	}
}

// unzip reads an archive into a name -> content map.
func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the archive is not readable: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestZipDirectory(t *testing.T) {
	e := newEnv(t, nil)
	plan, err := e.m.PlanZip("media", []string{"Movies"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Files != 3 || plan.Bytes != 10100 {
		t.Errorf("plan = %d files, %d bytes; want 3, 10100", plan.Files, plan.Bytes)
	}
	if plan.Name != "Movies.zip" {
		t.Errorf("name = %q", plan.Name)
	}

	var buf bytes.Buffer
	sum, err := e.m.WriteZip(context.Background(), &buf, "media", plan)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Entries != 3 {
		t.Errorf("entries = %d, want 3", sum.Entries)
	}
	files := unzip(t, buf.Bytes())
	want := []string{"Movies/2019/big.mkv", "Movies/2019/small.mkv", "Movies/trailer.mp4"}
	got := make([]string, 0, len(files))
	for name := range files {
		got = append(got, name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("entries = %v, want %v", got, want)
	}
	if len(files["Movies/2019/big.mkv"]) != 8000 {
		t.Errorf("content length = %d", len(files["Movies/2019/big.mkv"]))
	}
}

func TestZipSkipsSymlinksAndSaysSo(t *testing.T) {
	e := newEnv(t, nil)
	plan, err := e.m.PlanZip("media", []string{"escape", "readme.txt"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	sum, err := e.m.WriteZip(context.Background(), &buf, "media", plan)
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, buf.Bytes())
	if _, ok := files["escape"]; ok {
		t.Error("SECURITY: a symlink was written into the archive")
	}
	note, ok := files["_SKIPPED.txt"]
	if !ok || !strings.Contains(note, "escape") {
		t.Errorf("skipped note = %q", note)
	}
	if sum.Skipped == 0 {
		t.Error("summary should report the skip")
	}
	if files["readme.txt"] == "" {
		t.Error("the real file should still be included")
	}
}

func TestZipLimits(t *testing.T) {
	byBytes := newEnv(t, func(c *config.Config) { c.Operations.Download.ZipMaxBytes = 1000 })
	if _, err := byBytes.m.PlanZip("media", []string{"Movies"}); !errors.Is(err, ErrZipTooLarge) {
		t.Errorf("byte limit = %v, want ErrZipTooLarge", err)
	}
	byEntries := newEnv(t, func(c *config.Config) { c.Operations.Download.ZipMaxEntries = 2 })
	if _, err := byEntries.m.PlanZip("media", []string{"Movies"}); !errors.Is(err, ErrZipTooLarge) {
		t.Errorf("entry limit = %v, want ErrZipTooLarge", err)
	}
	off := newEnv(t, func(c *config.Config) { c.Operations.Download.ZipEnabled = false })
	if _, err := off.m.PlanZip("media", []string{"Movies"}); !errors.Is(err, ErrZipDisabled) {
		t.Errorf("disabled = %v, want ErrZipDisabled", err)
	}
}

func TestZipCommonPrefix(t *testing.T) {
	e := newEnv(t, nil)
	plan, err := e.m.PlanZip("media", []string{"Movies/2019/big.mkv", "Movies/trailer.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := e.m.WriteZip(context.Background(), &buf, "media", plan); err != nil {
		t.Fatal(err)
	}
	files := unzip(t, buf.Bytes())
	// The shared "Movies" prefix is stripped, so the archive unpacks tidily.
	if _, ok := files["2019/big.mkv"]; !ok {
		t.Errorf("entries = %v", keys(files))
	}
	if _, ok := files["trailer.mp4"]; !ok {
		t.Errorf("entries = %v", keys(files))
	}
}

func TestZipCancellation(t *testing.T) {
	e := newEnv(t, nil)
	plan, _ := e.m.PlanZip("media", []string{"Movies"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.m.WriteZip(ctx, io.Discard, "media", plan); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled write = %v, want context.Canceled", err)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestNeedsTypedName(t *testing.T) {
	dir := Target{Path: "Movies/2019", Kind: "dir"}
	file := Target{Path: "a.txt", Kind: "file"}
	big := Target{Path: "b.txt", Kind: "file", Files: 500}

	if got := needsTypedName([]Target{dir}, "name"); got != "2019" {
		t.Errorf("single directory = %q, want 2019", got)
	}
	if got := needsTypedName([]Target{file}, "name"); got != "" {
		t.Errorf("single file = %q, want no typing", got)
	}
	if got := needsTypedName([]Target{file, dir}, "name"); got != ConfirmWord {
		t.Errorf("batch with a directory = %q, want %q", got, ConfirmWord)
	}
	if got := needsTypedName([]Target{big}, "name"); got != ConfirmWord {
		t.Errorf("many files = %q, want %q", got, ConfirmWord)
	}
	if got := needsTypedName([]Target{dir}, "simple"); got != "" {
		t.Errorf("simple mode = %q, want no typing", got)
	}
}

func TestAuditRotation(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuditLog(dir, 512, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()

	for i := 0; i < 40; i++ {
		if err := a.Append(AuditEntry{Share: "s", Path: strings.Repeat("p", 40), Outcome: OutcomeDeleted}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(a.Path() + ".1"); err != nil {
		t.Errorf("the log should have rotated: %v", err)
	}
	if _, err := os.Stat(a.Path() + ".3"); !os.IsNotExist(err) {
		t.Error("rotation should keep only the configured number of files")
	}
	recent, err := a.Recent(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) == 0 {
		t.Error("Recent should still return entries after a rotation")
	}
}

func TestAuditRecentIsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAuditLog(dir, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	for _, p := range []string{"first", "second", "third"} {
		if err := a.Append(AuditEntry{Path: p}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Recent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "third" || got[1].Path != "second" {
		t.Errorf("recent = %+v, want third then second", got)
	}
}

func TestCommonParent(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"a/b/c.txt"}, "a/b"},
		{[]string{"top.txt"}, ""},
		{[]string{"a/b/c.txt", "a/b/d.txt"}, "a/b"},
		{[]string{"a/b/c.txt", "a/e/f.txt"}, "a"},
		{[]string{"a/x.txt", "b/y.txt"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := commonParent(c.in); got != c.want {
			t.Errorf("commonParent(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDropNested(t *testing.T) {
	in := []string{"a", "a/b", "a/b/c", "ab", "b/c"}
	got := dropNested(in)
	want := []string{"a", "ab", "b/c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dropNested = %v, want %v", got, want)
	}
}

func TestTrashListRestoreAndEmpty(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Operations.Delete.Trash.Enabled = true
		c.Operations.Delete.Trash.Retention = config.Duration(time.Hour)
	})
	var rescans []string
	e.m.hooks.StartRescan = func(_, path string) error {
		rescans = append(rescans, path)
		return nil
	}

	e.deleteNow(t, "Movies/2019/big.mkv")
	e.deleteNow(t, "readme.txt")

	items, err := e.m.ListTrash("media")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("trash = %+v", items)
	}
	byOriginal := map[string]TrashItem{}
	for _, it := range items {
		byOriginal[it.OriginalPath] = it
		if it.Batch == "" || it.DeletedAt.IsZero() {
			t.Errorf("item missing batch metadata: %+v", it)
		}
		if it.Blocked {
			t.Errorf("nothing occupies %s, so it should be restorable", it.OriginalPath)
		}
	}
	deep, ok := byOriginal["Movies/2019/big.mkv"]
	if !ok {
		t.Fatalf("the nested item was not found: %+v", items)
	}
	if deep.Size != 8000 || deep.Kind != "file" {
		t.Errorf("item = %+v", deep)
	}

	// Restoring puts it back exactly where it came from.
	restored, err := e.m.RestoreTrash("media", deep.TrashPath, ClientInfo{IP: "1.2.3.4"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.OriginalPath != "Movies/2019/big.mkv" {
		t.Errorf("restored to %q", restored.OriginalPath)
	}
	fi, err := os.Stat(filepath.Join(e.share, "Movies/2019/big.mkv"))
	if err != nil || fi.Size() != 8000 {
		t.Fatalf("the file should be back: %v", err)
	}
	if len(rescans) == 0 {
		t.Error("a rescan should follow a restore so the sizes come back")
	}
	entries, _ := e.m.Audit().Recent(5)
	if len(entries) == 0 || entries[0].Outcome != "restored" {
		t.Errorf("the restore should be audited: %+v", entries)
	}

	// Restoring on top of something that exists again is refused.
	e.deleteNow(t, "Movies/2019/big.mkv")
	again, _ := e.m.ListTrash("media")
	var blocked TrashItem
	for _, it := range again {
		if it.OriginalPath == "Movies/2019/big.mkv" {
			blocked = it
		}
	}
	if err := os.WriteFile(filepath.Join(e.share, "Movies/2019/big.mkv"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RestoreTrash("media", blocked.TrashPath, ClientInfo{}); !errors.Is(err, ErrRestoreExists) {
		t.Errorf("restore over an existing file = %v, want ErrRestoreExists", err)
	}

	// Emptying clears everything.
	if _, err := e.m.EmptyTrash("media", "", ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	left, err := e.m.ListTrash("media")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("trash should be empty, got %+v", left)
	}
}

func TestTrashRefusesPathsOutsideIt(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Operations.Delete.Trash.Enabled = true })
	for _, p := range []string{"Music/song.flac", "../outside/secret.txt", TrashDir, TrashDir + "/20200101T000000Z"} {
		if _, err := e.m.RestoreTrash("media", p, ClientInfo{}); err == nil {
			t.Errorf("SECURITY: restore accepted %q", p)
		}
	}
	for _, b := range []string{"../outside", "not-a-batch", "20200101T000000Z/nested"} {
		if _, err := e.m.EmptyTrash("media", b, ClientInfo{}); err == nil {
			t.Errorf("SECURITY: empty accepted batch %q", b)
		}
	}
	if _, err := os.Stat(filepath.Join(e.share, "Music/song.flac")); err != nil {
		t.Fatal("a real file was affected")
	}
}

func TestTrashDisabled(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.m.ListTrash("media"); !errors.Is(err, ErrTrashDisabled) {
		t.Errorf("list = %v, want ErrTrashDisabled", err)
	}
	if _, err := e.m.RestoreTrash("media", TrashDir+"/x/y", ClientInfo{}); !errors.Is(err, ErrTrashDisabled) {
		t.Errorf("restore = %v, want ErrTrashDisabled", err)
	}
	if _, err := e.m.EmptyTrash("media", "", ClientInfo{}); !errors.Is(err, ErrTrashDisabled) {
		t.Errorf("empty = %v, want ErrTrashDisabled", err)
	}
}
