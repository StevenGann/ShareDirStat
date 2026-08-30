package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/events"
	"github.com/StevenGann/ShareDirStat/internal/model"
	"github.com/StevenGann/ShareDirStat/internal/share"
	"github.com/StevenGann/ShareDirStat/internal/snapshot"
)

// Trigger records why a scan started (FR-SCAN-09).
type Trigger string

// Scan triggers.
const (
	TriggerManual    Trigger = "manual"
	TriggerSchedule  Trigger = "schedule"
	TriggerStartup   Trigger = "startup"
	TriggerRescan    Trigger = "rescan"
	TriggerReconcile Trigger = "reconcile"
)

// Outcome is how a scan ended.
type Outcome string

// Scan outcomes.
const (
	OutcomeCompleted Outcome = "completed"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeFailed    Outcome = "failed"
)

// HistoryDepth is the number of past scans retained per share (FR-SCAN-09).
const HistoryDepth = 50

// Record is one entry of a share's scan history.
type Record struct {
	ID         string     `json:"id"`
	ShareID    string     `json:"share_id"`
	Trigger    Trigger    `json:"trigger"`
	Path       string     `json:"path"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	DurationMS int64      `json:"duration_ms"`
	Outcome    Outcome    `json:"outcome"`
	Error      string     `json:"error,omitempty"`
	Generation string     `json:"generation,omitempty"`
	Files      uint64     `json:"files"`
	Dirs       uint64     `json:"dirs"`
	Bytes      uint64     `json:"bytes"`
	Errors     uint64     `json:"errors"`
}

// Status describes a scan that is running or queued.
type Status struct {
	ID        string    `json:"id"`
	ShareID   string    `json:"share_id"`
	Trigger   Trigger   `json:"trigger"`
	Path      string    `json:"path"`
	StartedAt time.Time `json:"started_at"`
	Queued    bool      `json:"queued"`
	Paused    bool      `json:"paused"`
	Progress  Progress  `json:"progress"`
}

// Errors returned by the manager.
var (
	ErrShareNotFound    = errors.New("share not found")
	ErrShareUnavailable = errors.New("share path is not available")
	ErrScanInProgress   = errors.New("a scan of this share is already running")
	ErrScanNotFound     = errors.New("scan not found")
	ErrPathNotFound     = errors.New("path not found in the current scan results")
)

type runningScan struct {
	status Status
	ctrl   *Controller
	cancel context.CancelFunc
	mu     sync.Mutex
	done   chan struct{}
}

func (r *runningScan) snapshotStatus() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	s.Paused = r.ctrl.Paused()
	return s
}

func (r *runningScan) setProgress(p Progress) {
	r.mu.Lock()
	r.status.Progress = p
	r.mu.Unlock()
}

// Manager owns scan lifecycle, history, scheduling and persistence.
type Manager struct {
	cfg      *config.Config
	reg      *share.Registry
	store    *snapshot.Store
	bus      events.Publisher
	log      *slog.Logger
	hooks    Hooks
	sem      chan struct{}
	stateDir string

	mu      sync.Mutex
	running map[string]*runningScan
	history map[string][]Record
	// dirty tracks shares whose in-memory results have been mutated (by a
	// delete) since their snapshot was written, with the pending rewrite.
	dirty map[string]*time.Timer

	cron   *cron.Cron
	wg     sync.WaitGroup
	closed bool
}

// Hooks lets the caller observe scans without the manager importing the
// metrics package.
type Hooks struct {
	OnScanStart    func(shareID string)
	OnScanFinish   func(shareID string, outcome Outcome, d time.Duration, errs uint64)
	OnShareState   func(shareID string, state share.State)
	OnSnapshotSave func(shareID string, bytes int64, d time.Duration)
	// OnResults fires whenever a share's published generation changes,
	// whether it came from a scan or was restored from a snapshot.
	OnResults func(shareID string, gen *model.Generation)
}

// publishResults installs a generation on a share and notifies the hooks.
func (m *Manager) publishResults(sh *share.Share, gen *model.Generation) {
	sh.SetGeneration(gen)
	if m.hooks.OnResults != nil {
		m.hooks.OnResults(sh.Config.ID, gen)
	}
}

// NewManager wires a manager. The caller must call Close on shutdown.
func NewManager(cfg *config.Config, reg *share.Registry, store *snapshot.Store, bus events.Publisher, log *slog.Logger, hooks Hooks) *Manager {
	m := &Manager{
		cfg:      cfg,
		reg:      reg,
		store:    store,
		bus:      bus,
		log:      log,
		hooks:    hooks,
		sem:      make(chan struct{}, max(cfg.Scan.MaxConcurrentShares, 1)),
		stateDir: filepath.Join(cfg.DataDir, "state"),
		running:  map[string]*runningScan{},
		history:  map[string][]Record{},
		dirty:    map[string]*time.Timer{},
	}
	_ = os.MkdirAll(m.stateDir, 0o750)
	m.loadHistory()
	return m
}

// LoadSnapshots restores the most recent generation of every share. A
// snapshot that cannot be used is quarantined and the share is treated as
// never scanned (FR-DATA-02).
func (m *Manager) LoadSnapshots() {
	for _, sh := range m.reg.All() {
		id := sh.Config.ID
		start := time.Now()
		gen, hdr, err := m.store.Load(id)
		switch {
		case err != nil:
			reason := err.Error()
			m.log.Warn("ignoring unusable snapshot", "share", id, "error", reason)
			if moved, qerr := m.store.Quarantine(id, reason); qerr == nil {
				m.log.Warn("snapshot quarantined", "share", id, "path", moved)
			}
		case gen == nil:
			m.log.Info("no snapshot yet", "share", id)
		case hdr.RootPath != sh.Config.Path:
			reason := fmt.Sprintf("snapshot was taken of %s but the share now points at %s", hdr.RootPath, sh.Config.Path)
			m.log.Warn("ignoring snapshot for a different path", "share", id, "reason", reason)
			if moved, qerr := m.store.Quarantine(id, reason); qerr == nil {
				m.log.Warn("snapshot quarantined", "share", id, "path", moved)
			}
		default:
			m.publishResults(sh, gen)
			m.setState(sh, share.StateReady, "")
			st := gen.Stats()
			m.log.Info("snapshot loaded", "share", id, "generation", gen.ID(),
				"nodes", gen.NodeCount(), "files", st.Files, "dirs", st.Dirs,
				"bytes", st.Size, "scanned_at", gen.ScannedAt(), "load_ms", time.Since(start).Milliseconds())
		}
	}
}

// StartupScans launches the scans required by scan.on_startup (FR-SCAN-22).
func (m *Manager) StartupScans(ctx context.Context) {
	mode := m.cfg.Scan.OnStartup
	if mode == "never" {
		return
	}
	for _, sh := range m.reg.All() {
		if sh.State() == share.StateUnavailable {
			continue
		}
		if mode == "if-missing" && sh.Generation() != nil {
			continue
		}
		if _, err := m.Start(ctx, sh.Config.ID, "", TriggerStartup); err != nil {
			m.log.Warn("startup scan not started", "share", sh.Config.ID, "error", err)
		}
	}
}

// StartScheduler installs the cron entries for every share (FR-SCAN-21).
func (m *Manager) StartScheduler(ctx context.Context) {
	c := cron.New(cron.WithParser(cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)))
	installed := 0
	for _, sh := range m.reg.All() {
		spec := sh.Config.CronSchedule()
		if spec == "" {
			continue
		}
		id := sh.Config.ID
		if _, err := c.AddFunc(spec, func() {
			if _, err := m.Start(ctx, id, "", TriggerSchedule); err != nil {
				// A scheduled scan that collides with a running one is
				// skipped, not queued (FR-SCAN-21).
				m.log.Info("scheduled scan skipped", "share", id, "reason", err)
			}
		}); err != nil {
			m.log.Error("invalid schedule", "share", id, "schedule", spec, "error", err)
			continue
		}
		installed++
	}
	if installed == 0 {
		m.log.Info("no scan schedules configured")
		return
	}
	c.Start()
	m.mu.Lock()
	m.cron = c
	m.mu.Unlock()
	m.refreshNextRuns()
	m.log.Info("scan scheduler started", "entries", installed)
}

// refreshNextRuns copies the scheduler's next fire times onto the shares.
func (m *Manager) refreshNextRuns() {
	m.mu.Lock()
	c := m.cron
	m.mu.Unlock()
	if c == nil {
		return
	}
	_ = c
	// cron does not expose which entry belongs to which share, so each
	// share's next fire time is recomputed from its own schedule.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	now := time.Now()
	for _, sh := range m.reg.All() {
		spec := sh.Config.CronSchedule()
		if spec == "" {
			continue
		}
		sched, err := parser.Parse(spec)
		if err != nil {
			continue
		}
		sh.SetNextScan(sched.Next(now))
	}
}

// Start begins a scan. path selects a subtree rescan; "" scans the whole
// share. It returns immediately with the scan's status.
func (m *Manager) Start(ctx context.Context, shareID, path string, trigger Trigger) (Status, error) {
	sh, ok := m.reg.Get(shareID)
	if !ok {
		return Status{}, fmt.Errorf("%w: %s", ErrShareNotFound, shareID)
	}
	sh.Check()
	if sh.State() == share.StateUnavailable {
		return Status{}, fmt.Errorf("%w: %s", ErrShareUnavailable, sh.Snapshot().Error)
	}

	absRoot := sh.Config.Path
	relRoot := ""
	if path != "" {
		gen := sh.Generation()
		if gen == nil {
			return Status{}, fmt.Errorf("%w: scan the share before rescanning a subtree", ErrPathNotFound)
		}
		info, found := gen.Info(path)
		if !found {
			return Status{}, fmt.Errorf("%w: %s", ErrPathNotFound, path)
		}
		if info.Kind != model.KindDir {
			return Status{}, fmt.Errorf("%w: %s is not a directory", ErrPathNotFound, path)
		}
		relRoot = info.Path
		absRoot = filepath.Join(sh.Config.Path, filepath.FromSlash(relRoot))
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, errors.New("server is shutting down")
	}
	if _, busy := m.running[shareID]; busy {
		m.mu.Unlock()
		return Status{}, ErrScanInProgress
	}
	scanCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	rs := &runningScan{
		status: Status{
			ID: NewID(), ShareID: shareID, Trigger: trigger, Path: relRoot,
			StartedAt: time.Now(), Queued: true,
		},
		ctrl:   NewController(),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.running[shareID] = rs
	m.mu.Unlock()

	m.wg.Add(1)
	go m.run(scanCtx, sh, rs, absRoot, relRoot)
	return rs.snapshotStatus(), nil
}

// run executes one scan and publishes its lifecycle events.
func (m *Manager) run(ctx context.Context, sh *share.Share, rs *runningScan, absRoot, relRoot string) {
	defer m.wg.Done()
	defer close(rs.done)
	shareID := sh.Config.ID

	// Bound how many shares scan at once (scan.max_concurrent_shares).
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		m.finish(sh, rs, OutcomeCancelled, "cancelled before starting", nil)
		return
	}
	defer func() { <-m.sem }()

	rs.mu.Lock()
	rs.status.Queued = false
	rs.status.StartedAt = time.Now()
	started := rs.status
	rs.mu.Unlock()

	if sh.Generation() != nil {
		m.setState(sh, share.StateReadyStale, "")
	} else {
		m.setState(sh, share.StateScanning, "")
	}
	if m.hooks.OnScanStart != nil {
		m.hooks.OnScanStart(shareID)
	}
	m.bus.Publish(events.ScanStarted, started)
	m.log.Info("scan started", "share", shareID, "scan", started.ID, "trigger", started.Trigger, "path", relRoot, "root", absRoot)

	sizeHint := 1024
	if g := sh.Generation(); g != nil {
		sizeHint = g.NodeCount() + g.NodeCount()/8 + 1024
	}
	basis, _ := model.ParseBasis(sh.Config.SizeBasis)

	opts := Options{
		ShareID:          shareID,
		Root:             absRoot,
		RelRoot:          relRoot,
		Basis:            basis,
		Concurrency:      sh.Config.Concurrency,
		Excludes:         sh.Config.Excludes,
		FollowSymlinks:   m.cfg.Scan.FollowSymlinks,
		CrossMountPoints: m.cfg.Scan.CrossMountPoints,
		MaxNodes:         m.cfg.Scan.MaxNodesPerShare,
		SizeHint:         sizeHint,
		OnProgress: func(p Progress) {
			rs.setProgress(p)
			m.bus.Publish(events.ScanProgress, progressEvent{
				ScanID: started.ID, ShareID: shareID, Path: relRoot, Progress: p,
			})
		},
	}
	meta := model.GenerationMeta{
		ID: NewID(), ScannedAt: time.Now(), Trigger: string(started.Trigger), ScanID: started.ID,
	}

	begin := time.Now()
	gen, err := Run(ctx, opts, meta, rs.ctrl)
	elapsed := time.Since(begin)

	switch {
	case err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil):
		m.log.Info("scan cancelled", "share", shareID, "scan", started.ID, "after", elapsed)
		m.finish(sh, rs, OutcomeCancelled, "", nil)
		return
	case err != nil:
		m.log.Error("scan failed", "share", shareID, "scan", started.ID, "error", err)
		m.finish(sh, rs, OutcomeFailed, err.Error(), nil)
		return
	}

	gen.SetDuration(elapsed)
	if relRoot == "" {
		m.publishResults(sh, gen)
	} else {
		// Subtree rescan: splice the fresh subtree into the live model.
		cur := sh.Generation()
		if cur == nil {
			m.finish(sh, rs, OutcomeFailed, "the share was rescanned in full while this subtree rescan was running", nil)
			return
		}
		if err := cur.Splice(relRoot, gen); err != nil {
			m.log.Error("subtree rescan could not be applied", "share", shareID, "path", relRoot, "error", err)
			m.finish(sh, rs, OutcomeFailed, "could not apply the rescanned subtree: "+err.Error(), nil)
			return
		}
		gen = cur
		m.publishResults(sh, gen)
	}
	m.setState(sh, share.StateReady, "")

	saveStart := time.Now()
	if size, serr := m.store.Save(gen); serr != nil {
		m.log.Error("snapshot not saved", "share", shareID, "error", serr)
	} else {
		m.log.Info("snapshot saved", "share", shareID, "bytes", size, "ms", time.Since(saveStart).Milliseconds())
		if m.hooks.OnSnapshotSave != nil {
			m.hooks.OnSnapshotSave(shareID, size, time.Since(saveStart))
		}
	}

	st := gen.Stats()
	m.log.Info("scan completed", "share", shareID, "scan", started.ID,
		"files", st.Files, "dirs", st.Dirs, "bytes", st.Size, "errors", st.Errors,
		"nodes", gen.NodeCount(), "duration_ms", elapsed.Milliseconds(),
		"files_per_s", int64(float64(st.Files)/max(elapsed.Seconds(), 0.001)))
	m.finish(sh, rs, OutcomeCompleted, "", gen)
}

type progressEvent struct {
	ScanID  string `json:"scan_id"`
	ShareID string `json:"share_id"`
	Path    string `json:"path"`
	Progress
}

// finish records the outcome, publishes the terminal event and releases the
// share's running slot.
func (m *Manager) finish(sh *share.Share, rs *runningScan, outcome Outcome, errMsg string, gen *model.Generation) {
	shareID := sh.Config.ID
	now := time.Now()
	st := rs.snapshotStatus()

	rec := Record{
		ID: st.ID, ShareID: shareID, Trigger: st.Trigger, Path: st.Path,
		StartedAt: st.StartedAt, FinishedAt: &now,
		DurationMS: now.Sub(st.StartedAt).Milliseconds(),
		Outcome:    outcome, Error: errMsg,
		Files: st.Progress.Files, Dirs: st.Progress.Dirs,
		Bytes: st.Progress.Bytes, Errors: st.Progress.Errors,
	}
	if gen != nil {
		gs := gen.Stats()
		rec.Generation = gen.ID()
		rec.Files, rec.Dirs, rec.Bytes, rec.Errors = gs.Files, gs.Dirs, gs.Size, gs.Errors
	}

	m.mu.Lock()
	delete(m.running, shareID)
	h := append(append(make([]Record, 0, len(m.history[shareID])+1), m.history[shareID]...), rec)
	if len(h) > HistoryDepth {
		h = h[len(h)-HistoryDepth:]
	}
	m.history[shareID] = h
	m.mu.Unlock()
	m.saveHistory(shareID)

	if outcome != OutcomeCompleted && sh.Generation() == nil {
		state := share.StateNeverScanned
		if outcome == OutcomeFailed {
			state = share.StateError
		}
		m.setState(sh, state, errMsg)
	} else if sh.Generation() != nil {
		m.setState(sh, share.StateReady, "")
	}

	if m.hooks.OnScanFinish != nil {
		m.hooks.OnScanFinish(shareID, outcome, now.Sub(st.StartedAt), rec.Errors)
	}
	switch outcome {
	case OutcomeCompleted:
		m.bus.Publish(events.ScanCompleted, rec)
	case OutcomeCancelled:
		m.bus.Publish(events.ScanCancelled, rec)
	case OutcomeFailed:
		m.bus.Publish(events.ScanFailed, rec)
	}
	m.refreshNextRuns()
}

func (m *Manager) setState(sh *share.Share, st share.State, errMsg string) {
	sh.SetState(st, errMsg)
	if m.hooks.OnShareState != nil {
		m.hooks.OnShareState(sh.Config.ID, st)
	}
	m.bus.Publish(events.ShareState, map[string]any{"share_id": sh.Config.ID, "state": st, "error": errMsg})
}

// MarkDirty records that a share's in-memory results no longer match its
// snapshot, and schedules a rewrite (FR-DATA-04).
//
// The write is debounced: deleting five hundred files in one request, or a
// user working through a folder a file at a time, costs one snapshot rather
// than one per delete. That matters on the SD card of a Raspberry Pi.
func (m *Manager) MarkDirty(shareID string) {
	delay := m.cfg.SnapshotDebounce.D()
	if delay <= 0 {
		m.persist(shareID)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if _, pending := m.dirty[shareID]; pending {
		return // a rewrite is already scheduled; it will cover this change too
	}
	m.dirty[shareID] = time.AfterFunc(delay, func() {
		m.mu.Lock()
		delete(m.dirty, shareID)
		m.mu.Unlock()
		// persist takes the lock itself, so it must be called without it.
		m.persist(shareID)
	})
}

// persist writes a share's current results to its snapshot.
func (m *Manager) persist(shareID string) {
	sh, ok := m.reg.Get(shareID)
	if !ok {
		return
	}
	gen := sh.Generation()
	if gen == nil {
		return
	}
	// A scan in flight will write a fresh snapshot when it finishes, so
	// there is nothing to gain from racing it.
	if _, running := m.RunningFor(shareID); running {
		return
	}
	start := time.Now()
	size, err := m.store.Save(gen)
	if err != nil {
		m.log.Error("could not rewrite the snapshot after a change", "share", shareID, "error", err)
		return
	}
	m.log.Info("snapshot rewritten", "share", shareID, "bytes", size, "ms", time.Since(start).Milliseconds())
	if m.hooks.OnSnapshotSave != nil {
		m.hooks.OnSnapshotSave(shareID, size, time.Since(start))
	}
}

// FlushSnapshots writes any pending debounced snapshot immediately. It is
// called on shutdown so a delete made seconds before a restart is not lost.
func (m *Manager) FlushSnapshots() {
	m.mu.Lock()
	pending := make([]string, 0, len(m.dirty))
	for id, t := range m.dirty {
		t.Stop()
		pending = append(pending, id)
	}
	m.dirty = map[string]*time.Timer{}
	m.mu.Unlock()
	for _, id := range pending {
		m.persist(id)
	}
}

// Cancel stops a running scan (FR-SCAN-05).
func (m *Manager) Cancel(shareID, scanID string) error {
	rs, err := m.lookup(shareID, scanID)
	if err != nil {
		return err
	}
	rs.cancel()
	return nil
}

// Pause suspends a running scan (FR-SCAN-10).
func (m *Manager) Pause(shareID, scanID string) error {
	rs, err := m.lookup(shareID, scanID)
	if err != nil {
		return err
	}
	rs.ctrl.Pause()
	m.bus.Publish(events.ScanPaused, rs.snapshotStatus())
	return nil
}

// Resume restarts a paused scan.
func (m *Manager) Resume(shareID, scanID string) error {
	rs, err := m.lookup(shareID, scanID)
	if err != nil {
		return err
	}
	rs.ctrl.Resume()
	m.bus.Publish(events.ScanResumed, rs.snapshotStatus())
	return nil
}

func (m *Manager) lookup(shareID, scanID string) (*runningScan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs, ok := m.running[shareID]
	if !ok {
		return nil, fmt.Errorf("%w: no scan running for share %s", ErrScanNotFound, shareID)
	}
	if scanID != "" && rs.status.ID != scanID {
		return nil, fmt.Errorf("%w: scan %s is not running", ErrScanNotFound, scanID)
	}
	return rs, nil
}

// Running returns the status of every active scan, newest first.
func (m *Manager) Running() []Status {
	m.mu.Lock()
	rs := make([]*runningScan, 0, len(m.running))
	for _, r := range m.running {
		rs = append(rs, r)
	}
	m.mu.Unlock()

	out := make([]Status, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.snapshotStatus())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// RunningFor returns the active scan of one share.
func (m *Manager) RunningFor(shareID string) (Status, bool) {
	m.mu.Lock()
	rs, ok := m.running[shareID]
	m.mu.Unlock()
	if !ok {
		return Status{}, false
	}
	return rs.snapshotStatus(), true
}

// History returns a share's past scans, newest first.
func (m *Manager) History(shareID string) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.history[shareID]
	out := make([]Record, len(h))
	for i, r := range h {
		out[len(h)-1-i] = r
	}
	return out
}

// historyFile is where a share's scan history is persisted.
func (m *Manager) historyFile(shareID string) string {
	return filepath.Join(m.stateDir, shareID+".json")
}

type persistedState struct {
	History []Record `json:"history"`
}

func (m *Manager) loadHistory() {
	for _, sh := range m.reg.All() {
		id := sh.Config.ID
		data, err := os.ReadFile(m.historyFile(id))
		if err != nil {
			continue
		}
		var ps persistedState
		if err := json.Unmarshal(data, &ps); err != nil {
			m.log.Warn("ignoring unreadable scan history", "share", id, "error", err)
			continue
		}
		m.history[id] = ps.History
	}
}

func (m *Manager) saveHistory(shareID string) {
	m.mu.Lock()
	ps := persistedState{History: append([]Record(nil), m.history[shareID]...)}
	m.mu.Unlock()
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return
	}
	path := m.historyFile(shareID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		m.log.Warn("could not persist scan history", "share", shareID, "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		m.log.Warn("could not install scan history", "share", shareID, "error", err)
	}
}

// Close cancels every running scan and waits for the workers to unwind. A
// cancelled scan discards its partial generation, so the previously served
// results stay intact (NFR-9).
func (m *Manager) Close(ctx context.Context) {
	// Any delete made in the last few seconds still owes a snapshot write.
	m.FlushSnapshots()

	m.mu.Lock()
	m.closed = true
	c := m.cron
	for _, rs := range m.running {
		rs.cancel()
	}
	m.mu.Unlock()

	if c != nil {
		<-c.Stop().Done()
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		m.log.Warn("scans did not stop before the shutdown deadline")
	}
}
