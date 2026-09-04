// Package scan implements the parallel directory crawler (specification §7).
//
// A scan builds a brand new model.Generation off to the side; the previous
// one stays browsable until the new one is complete, so scanning never
// interrupts the UI (FR-SCAN-02).
package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/media"
	"github.com/StevenGann/ShareDirStat/internal/model"
)

// readDirBatch bounds how many directory entries are materialised at once.
const readDirBatch = 4096

// Options configures one crawl.
type Options struct {
	ShareID          string
	Root             string // absolute path of the directory to scan
	RelRoot          string // share-relative path of Root ("" for a full scan)
	Basis            model.Basis
	Concurrency      int
	Excludes         []string
	FollowSymlinks   bool
	CrossMountPoints bool
	// MediaDurations probes audio/video files for their playing time (one
	// bounded header read per media file) so the model can offer the
	// size-per-minute metric.
	MediaDurations   bool
	MaxNodes         int
	TopN             int
	SizeHint         int
	ProgressInterval time.Duration
	OnProgress       func(Progress)
}

// Progress is a point-in-time snapshot of a running scan (FR-SCAN-07).
type Progress struct {
	Dirs     uint64 `json:"dirs"`
	Files    uint64 `json:"files"`
	Bytes    uint64 `json:"bytes"`
	Errors   uint64 `json:"errors"`
	Excluded uint64 `json:"excluded"`
	// Vanished counts entries that existed at readdir but were gone by the
	// lstat. Normal on a live share, so they are not errors.
	Vanished    uint64        `json:"vanished"`
	Pending     int           `json:"pending"`
	CurrentPath string        `json:"current_path"`
	Elapsed     time.Duration `json:"-"`
	ElapsedMS   int64         `json:"elapsed_ms"`
	Rate        float64       `json:"rate_files_per_s"`
	Paused      bool          `json:"paused"`
}

// Controller allows a running scan to be paused and resumed.
type Controller struct {
	q      atomic.Pointer[workQueue]
	paused atomic.Bool
}

// NewController creates a controller for a scan that has not started yet.
func NewController() *Controller { return &Controller{} }

func (c *Controller) attach(q *workQueue) { c.q.Store(q) }

// Pause stops workers from taking new directories; in-flight ones finish.
func (c *Controller) Pause() {
	c.paused.Store(true)
	if q := c.q.Load(); q != nil {
		q.setPaused(true)
	}
}

// Resume restarts a paused scan.
func (c *Controller) Resume() {
	c.paused.Store(false)
	if q := c.q.Load(); q != nil {
		q.setPaused(false)
	}
}

// Paused reports whether the scan is paused.
func (c *Controller) Paused() bool { return c.paused.Load() }

// counters holds the atomics shared by a scan's workers.
type counters struct {
	dirs     atomic.Uint64
	files    atomic.Uint64
	bytes    atomic.Uint64
	errs     atomic.Uint64
	excluded atomic.Uint64
	vanished atomic.Uint64
	current  atomic.Pointer[string]
}

// inodeSet is a sharded set of (dev, ino) pairs used for hard-link
// de-duplication and symlink cycle detection.
type inodeSet struct {
	shards [16]struct {
		mu sync.Mutex
		m  map[[2]uint64]struct{}
		_  [40]byte // pad to keep shards off the same cache line
	}
}

func newInodeSet() *inodeSet {
	s := &inodeSet{}
	for i := range s.shards {
		s.shards[i].m = make(map[[2]uint64]struct{})
	}
	return s
}

// seen inserts a key and reports whether it was already present.
func (s *inodeSet) seen(dev, ino uint64) bool {
	k := [2]uint64{dev, ino}
	sh := &s.shards[ino&15]
	sh.mu.Lock()
	_, ok := sh.m[k]
	if !ok {
		sh.m[k] = struct{}{}
	}
	sh.mu.Unlock()
	return ok
}

// Run crawls opts.Root and returns the finished generation. It returns
// ctx.Err() if the scan was cancelled, in which case the partial generation
// is discarded (FR-SCAN-05).
func Run(ctx context.Context, opts Options, meta model.GenerationMeta, ctrl *Controller) (*model.Generation, error) {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = 500 * time.Millisecond
	}
	if opts.TopN <= 0 {
		opts.TopN = 1000
	}

	rootFI, err := os.Lstat(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("scan root %s: %w", opts.Root, err)
	}
	if !rootFI.IsDir() {
		return nil, fmt.Errorf("scan root %s is not a directory", opts.Root)
	}
	rootStat, haveRootStat := fromSys(rootFI.Sys())
	rootStat.Size = rootFI.Size()
	if !haveRootStat {
		rootStat.Mtime = rootFI.ModTime().Unix()
	}

	b := model.NewBuilder(opts.ShareID, opts.Root, opts.Basis, opts.MaxNodes, opts.SizeHint)
	if opts.MediaDurations {
		b.CollectMedia()
	}
	b.SetRootMeta(entryFromStat(filepath.Base(opts.Root), rootStat, rootFI.Mode(), haveRootStat, model.KindDir))

	s := &scanner{
		opts:    opts,
		builder: b,
		matcher: NewMatcher(opts.Excludes),
		rootDev: rootStat.Dev,
		links:   newInodeSet(),
		visited: newInodeSet(),
	}
	// Every other directory is entered into visited by classify before any of
	// its descendants are processed; the root is pushed straight onto the
	// queue and would otherwise be the single node a symlink can re-enter,
	// double-counting every file directly beneath it.
	if haveRootStat {
		s.visited.seen(rootStat.Dev, rootStat.Ino)
	}
	if ctrl == nil {
		ctrl = NewController()
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	q := newWorkQueue(opts.Concurrency)
	s.q = q
	s.cancel = cancel
	ctrl.attach(q)
	if ctrl.Paused() {
		q.setPaused(true)
	}
	// Seed the relative path from RelRoot so that a subtree rescan matches
	// exclusion patterns against the *share*-relative path, as FR-SCAN-15
	// requires. Starting at "" would make "Movies/tmp" unmatchable during a
	// rescan of Movies, and "tmp" wrongly matchable.
	q.push(dirTask{path: opts.Root, relPath: opts.RelRoot, node: b.Root()})

	start := time.Now()
	stopWatch := make(chan struct{})
	var watchWG sync.WaitGroup
	watchWG.Add(1)
	go func() {
		defer watchWG.Done()
		select {
		case <-ctx.Done():
			q.close()
		case <-stopWatch:
		}
	}()

	if opts.OnProgress != nil {
		watchWG.Add(1)
		go func() {
			defer watchWG.Done()
			t := time.NewTicker(opts.ProgressInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					opts.OnProgress(s.progress(start, ctrl.Paused()))
				case <-stopWatch:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	var wg sync.WaitGroup
	for i := 0; i < opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scratch := make([]uint32, 0, 256)
			entries := make([]model.Entry, 0, 256)
			for {
				t, ok := q.pop()
				if !ok {
					return
				}
				if ctx.Err() != nil {
					q.close()
					return
				}
				entries, scratch = s.processDir(ctx, t, entries, scratch)
			}
		}()
	}
	wg.Wait()
	close(stopWatch)
	watchWG.Wait()

	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if opts.OnProgress != nil {
		opts.OnProgress(s.progress(start, false))
	}

	meta.Basis = opts.Basis.String()
	gen := b.Finalize(meta, opts.TopN)
	return gen, nil
}

type scanner struct {
	opts    Options
	builder *model.Builder
	matcher *Matcher
	rootDev uint64
	links   *inodeSet
	visited *inodeSet
	q       *workQueue
	cancel  context.CancelCauseFunc
	counts  counters
}

func (s *scanner) progress(start time.Time, paused bool) Progress {
	elapsed := time.Since(start)
	files := s.counts.files.Load()
	cur := ""
	if p := s.counts.current.Load(); p != nil {
		cur = *p
	}
	rate := 0.0
	if elapsed > 0 {
		rate = float64(files) / elapsed.Seconds()
	}
	return Progress{
		Dirs:        s.counts.dirs.Load(),
		Files:       files,
		Bytes:       s.counts.bytes.Load(),
		Errors:      s.counts.errs.Load(),
		Excluded:    s.counts.excluded.Load(),
		Vanished:    s.counts.vanished.Load(),
		Pending:     s.q.pending(),
		CurrentPath: cur,
		Elapsed:     elapsed,
		ElapsedMS:   elapsed.Milliseconds(),
		Rate:        rate,
		Paused:      paused,
	}
}

func (s *scanner) recordError(path, op string, err error) {
	s.counts.errs.Add(1)
	var errno syscall.Errno
	name := ""
	if errors.As(err, &errno) {
		name = errnoName(errno)
	}
	s.builder.AddError(model.ScanError{Path: path, Op: op, Errno: name, Message: err.Error()})
}

// processDir reads one directory, appends its entries to the builder as one
// contiguous block, and queues the subdirectories it found.
func (s *scanner) processDir(ctx context.Context, t dirTask, entries []model.Entry, scratch []uint32) ([]model.Entry, []uint32) {
	cur := t.path
	s.counts.current.Store(&cur)
	s.counts.dirs.Add(1)

	f, err := os.Open(t.path)
	if err != nil {
		s.recordError(t.path, "open", err)
		s.builder.SetFlags(t.node, model.FlagPartial)
		return entries, scratch
	}
	defer func() { _ = f.Close() }()
	// One descriptor for the whole directory: every entry is stat-ed relative
	// to it. f stays reachable through the deferred Close for the whole loop,
	// so the descriptor cannot be finalized underneath us.
	fd := int(f.Fd())

	entries = entries[:0]
	// Positions in entries of the subdirectories to descend into. Recording
	// positions rather than one name per entry means a leaf directory of a
	// thousand files allocates nothing here at all.
	var dirPos []int
	var excluded, bytes, vanished uint64
	var flags model.Flags

	// The parent's path segments are identical for every entry, so they are
	// split once here; the last slot is overwritten with each entry's name.
	var segs []string
	if !s.matcher.Empty() {
		segs = append(splitRel(nil, t.relPath), "")
	}

	for {
		batch, err := f.ReadDir(readDirBatch)
		for i := range batch {
			de := batch[i]
			name := de.Name()
			// Exclusions are matched on the pre-split parent segments plus
			// this name, so nothing is joined or re-split per entry.
			if segs != nil {
				segs[len(segs)-1] = name
				if s.matcher.MatchSegments(segs, de.IsDir()) {
					excluded++
					flags |= model.FlagExcluded
					continue
				}
			}
			st, mode, haveStat, lerr := lstatEntry(fd, t.path, name)
			if lerr != nil {
				// Entries disappear between readdir and lstat all the time on
				// a live share (a torrent's incomplete/, a Maildir, a build
				// cache). Treating that as an error floods the error list and
				// marks every ancestor partial, hiding real I/O failures.
				if errors.Is(lerr, fs.ErrNotExist) || errors.Is(lerr, syscall.ESTALE) {
					vanished++
					continue
				}
				s.recordError(joinPath(t.path, name), "lstat", lerr)
				flags |= model.FlagPartial
				continue
			}
			e, descend := s.classify(fd, t.path, name, st, mode, haveStat)
			// Duplicate hard links skip the probe: the inode was measured at
			// its first link, and the duplicate contributes nothing to the
			// media aggregates anyway (FR-SCAN-17).
			if s.opts.MediaDurations && e.Kind == model.KindFile && e.Size > 0 && !e.Flags.Has(model.FlagHardlinkDup) {
				if ext := model.ExtensionOf(name); media.KnownExt(ext) {
					e.Dur = s.probeDuration(fd, t.path, name, ext, e.Size)
				}
			}
			// Hard-link duplicates occupy no additional space and are excluded
			// from the aggregates at finalize; counting them here makes the
			// live byte counter climb past the total the scan finally reports.
			if e.Kind != model.KindDir && !e.Flags.Has(model.FlagHardlinkDup) {
				bytes += e.Size
			}
			if descend {
				dirPos = append(dirPos, len(entries))
			}
			entries = append(entries, e)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.recordError(t.path, "readdir", err)
				flags |= model.FlagPartial
			}
			break
		}
		if len(batch) == 0 {
			break
		}
		if ctx.Err() != nil {
			return entries, scratch
		}
	}

	if flags != 0 {
		s.builder.SetFlags(t.node, flags)
	}
	if excluded > 0 {
		s.builder.AddExcluded(excluded)
		s.counts.excluded.Add(excluded)
	}
	if vanished > 0 {
		s.counts.vanished.Add(vanished)
	}
	if len(entries) == 0 {
		return entries, scratch
	}

	idx, err := s.builder.AddChildren(t.node, entries, scratch)
	if err != nil {
		s.cancel(err)
		s.q.close()
		return entries, scratch
	}
	scratch = idx

	var files uint64
	for i := range entries {
		if entries[i].Kind != model.KindDir {
			files++
		}
	}
	s.counts.files.Add(files)
	s.counts.bytes.Add(bytes)

	for _, p := range dirPos {
		name := entries[p].Name
		s.q.push(dirTask{
			path:    joinPath(t.path, name),
			relPath: joinRel(t.relPath, name),
			node:    idx[p],
			depth:   t.depth + 1,
		})
	}
	return entries, scratch
}

// probeDuration opens one media file relative to its directory and asks the
// media package for its playing time. Failures are deliberately silent: an
// unreadable or malformed media file still has a correct size, and recording
// them as scan errors would mark ancestors partial and drown real I/O
// failures in files that merely lack a parseable header.
func (s *scanner) probeDuration(fd int, dir, name, ext string, size uint64) uint32 {
	f, err := openEntry(fd, dir, name)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	// Re-check the type through the opened descriptor: the entry can have
	// been replaced since the lstat, and the probe must only ever read a
	// regular file.
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	d, ok := media.Probe(f, int64(min(size, math.MaxInt64)), ext) //nolint:gosec // clamped
	if !ok {
		return 0
	}
	return d
}

// joinPath appends a directory entry's name to its parent's absolute path.
// filepath.Join would re-Clean the whole path on every call; the parent is
// already clean and absolute, and a dirent name never contains a separator.
func joinPath(dir, name string) string {
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

func joinRel(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}

// classify turns a stat result into a model entry and reports whether the
// scanner should descend into it.
// classify turns one stat-ed directory entry into a model.Entry and reports
// whether the crawl should descend into it. fd and dir identify the containing
// directory, so a symlink target can be re-stat-ed without building a path at
// all; the full path is only assembled on the error branch that reports one.
func (s *scanner) classify(fd int, dir, name string, st rawStat, mode fs.FileMode, haveStat bool) (model.Entry, bool) {
	switch {
	case mode.IsDir():
		e := entryFromStat(name, st, mode, haveStat, model.KindDir)
		if haveStat && st.Dev != s.rootDev && !s.opts.CrossMountPoints {
			e.Flags |= model.FlagMountPoint | model.FlagUnscanned
			e.Alloc = 0
			return e, false
		}
		// Cycle detection is only needed when symlinks are followed; the
		// visited set would otherwise cost memory per directory for nothing.
		if s.opts.FollowSymlinks && haveStat && s.visited.seen(st.Dev, st.Ino) {
			e.Flags |= model.FlagUnscanned
			return e, false
		}
		return e, true

	case mode&fs.ModeSymlink != 0:
		e := entryFromStat(name, st, mode, haveStat, model.KindSymlink)
		if !s.opts.FollowSymlinks {
			return e, false
		}
		tst, tmode, ok, err := statEntry(fd, dir, name)
		if err != nil {
			s.recordError(joinPath(dir, name), "stat", err)
			return e, false
		}
		if !tmode.IsDir() {
			return e, false
		}
		if !ok || s.visited.seen(tst.Dev, tst.Ino) {
			e.Flags |= model.FlagUnscanned
			return e, false
		}
		if tst.Dev != s.rootDev && !s.opts.CrossMountPoints {
			e.Flags |= model.FlagMountPoint | model.FlagUnscanned
			return e, false
		}
		return entryFromStat(name, tst, tmode, ok, model.KindDir), true

	case mode.IsRegular():
		e := entryFromStat(name, st, mode, haveStat, model.KindFile)
		if haveStat && st.Nlink > 1 && s.links.seen(st.Dev, st.Ino) {
			// Second and later links: keep the real size for display but
			// contribute nothing to aggregates (FR-SCAN-17).
			e.Flags |= model.FlagHardlinkDup
		}
		return e, false

	default:
		e := entryFromStat(name, st, mode, haveStat, model.KindOther)
		e.Size, e.Alloc = 0, 0
		return e, false
	}
}

// entryFromStat builds a model entry from a stat result. A directory carries
// only its own inode's allocation; its aggregate sizes come from its children
// during finalization (FR-SCAN-18).
func entryFromStat(name string, st rawStat, mode fs.FileMode, haveStat bool, kind model.Kind) model.Entry {
	e := model.Entry{
		Name:  name,
		Kind:  kind,
		Mtime: st.Mtime,
		Mode:  uint16(mode.Perm() & 0o777),
	}
	if haveStat {
		e.UID, e.GID = st.UID, st.GID
		e.Alloc = uint64(max(st.Blocks, 0)) * 512
	}
	if kind != model.KindDir {
		e.Size = uint64(max(st.Size, 0))
	}
	if mode&fs.ModeSetuid != 0 {
		e.Mode |= 0o4000
	}
	if mode&fs.ModeSetgid != 0 {
		e.Mode |= 0o2000
	}
	if mode&fs.ModeSticky != 0 {
		e.Mode |= 0o1000
	}
	return e
}
