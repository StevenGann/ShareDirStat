// Package share holds the registry of configured storage locations and their
// runtime state (§6 of the specification).
package share

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/model"
)

// State is the lifecycle state of a share (FR-SHR-05).
type State string

const (
	StateUnavailable  State = "unavailable"
	StateNeverScanned State = "never-scanned"
	StateScanning     State = "scanning"
	StateReady        State = "ready"
	StateReadyStale   State = "ready-stale"
	StateError        State = "error"
)

// Filesystem describes the mount a share lives on (FR-SHR-06).
type Filesystem struct {
	Type       string `json:"type"`
	MountPoint string `json:"mount_point"`
	Readonly   bool   `json:"readonly"`
}

// Info is the API representation of a share.
type Info struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Path          string       `json:"path"`
	State         State        `json:"state"`
	Error         string       `json:"error,omitempty"`
	Discovered    bool         `json:"discovered"`
	AllowDelete   bool         `json:"allow_delete"`
	AllowDownload bool         `json:"allow_download"`
	Concurrency   int          `json:"concurrency"`
	Schedule      string       `json:"schedule"`
	NextScan      *time.Time   `json:"next_scan"`
	SizeBasis     string       `json:"size_basis"`
	Excludes      []string     `json:"excludes"`
	Filesystem    *Filesystem  `json:"filesystem"`
	Generation    *string      `json:"generation"`
	LastScan      *time.Time   `json:"last_scan"`
	Stats         *model.Stats `json:"stats"`
	CheckedAt     time.Time    `json:"checked_at"`
}

// Share is one registered storage location with mutable runtime state.
type Share struct {
	Config config.Share

	// gen is swapped atomically when a scan completes, so readers never
	// block on a scan and always see a complete generation.
	gen atomic.Pointer[model.Generation]

	mu        sync.RWMutex
	state     State
	err       string
	fs        *Filesystem
	checkedAt time.Time
	nextScan  time.Time
}

// SetGeneration publishes a newly completed generation.
func (s *Share) SetGeneration(g *model.Generation) {
	s.gen.Store(g)
	if g != nil {
		s.SetState(StateReady, "")
	}
}

// Generation returns the currently published generation, or nil when the
// share has never been scanned.
func (s *Share) Generation() *model.Generation { return s.gen.Load() }

// SetNextScan records when the scheduler will next run this share.
func (s *Share) SetNextScan(t time.Time) {
	s.mu.Lock()
	s.nextScan = t
	s.mu.Unlock()
}

// Registry is the set of shares.
type Registry struct {
	log    *slog.Logger
	shares []*Share
	byID   map[string]*Share
}

// New builds a registry from configuration. Call CheckAll before serving.
func New(cfg *config.Config, log *slog.Logger) *Registry {
	r := &Registry{log: log, byID: make(map[string]*Share, len(cfg.Shares))}
	for _, sc := range cfg.Shares {
		s := &Share{Config: sc, state: StateNeverScanned}
		r.shares = append(r.shares, s)
		r.byID[sc.ID] = s
	}
	return r
}

// Len returns the number of registered shares.
func (r *Registry) Len() int { return len(r.shares) }

// Get returns a share by id.
func (r *Registry) Get(id string) (*Share, bool) {
	s, ok := r.byID[id]
	return s, ok
}

// All returns the shares in configuration order.
func (r *Registry) All() []*Share {
	out := make([]*Share, len(r.shares))
	copy(out, r.shares)
	return out
}

// CheckAll verifies every share path (FR-SHR-02) and refreshes filesystem
// information. It is called at startup and before each scan attempt.
func (r *Registry) CheckAll() {
	for _, s := range r.shares {
		s.Check()
		st := s.Snapshot()
		if st.State == StateUnavailable {
			r.log.Warn("share unavailable", "share", st.ID, "path", st.Path, "error", st.Error)
		} else {
			r.log.Info("share available", "share", st.ID, "path", st.Path, "fs", fsType(st.Filesystem))
		}
	}
}

func fsType(f *Filesystem) string {
	if f == nil {
		return "unknown"
	}
	return f.Type
}

// Check verifies the share path is an existing, readable directory and
// updates the state accordingly. A share that becomes available again moves
// to never-scanned (a later milestone restores ready when a snapshot exists).
func (s *Share) Check() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkedAt = time.Now()

	fi, err := os.Stat(s.Config.Path)
	switch {
	case err != nil:
		s.setUnavailable(fmt.Sprintf("cannot stat path: %v", err))
		return
	case !fi.IsDir():
		s.setUnavailable("path is not a directory")
		return
	}
	f, err := os.Open(s.Config.Path)
	if err != nil {
		s.setUnavailable(fmt.Sprintf("cannot open directory: %v", err))
		return
	}
	_, err = f.Readdirnames(1)
	_ = f.Close()
	if err != nil && err.Error() != "EOF" {
		s.setUnavailable(fmt.Sprintf("cannot read directory: %v", err))
		return
	}

	s.fs = lookupFilesystem(s.Config.Path)
	s.err = ""
	if s.state == StateUnavailable {
		if s.gen.Load() != nil {
			s.state = StateReady
		} else {
			s.state = StateNeverScanned
		}
	}
}

func (s *Share) setUnavailable(msg string) {
	s.state = StateUnavailable
	s.err = msg
	s.fs = nil
}

// SetState updates the lifecycle state and error message.
func (s *Share) SetState(st State, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
	s.err = errMsg
}

// State returns the current state.
func (s *Share) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Snapshot returns the API representation of the share.
func (s *Share) Snapshot() Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.Config
	excl := append([]string{}, c.Excludes...)
	var genID *string
	var lastScan *time.Time
	var stats *model.Stats
	if g := s.gen.Load(); g != nil {
		id := g.ID()
		at := g.ScannedAt()
		st := g.Stats()
		genID, lastScan, stats = &id, &at, &st
	}
	var next *time.Time
	if !s.nextScan.IsZero() {
		n := s.nextScan
		next = &n
	}
	return Info{
		ID:            c.ID,
		Name:          c.Name,
		Path:          c.Path,
		State:         s.state,
		Error:         s.err,
		Discovered:    c.Discovered,
		AllowDelete:   c.CanDelete(),
		AllowDownload: c.CanDownload(),
		Concurrency:   c.Concurrency,
		Schedule:      c.CronSchedule(),
		SizeBasis:     c.SizeBasis,
		Excludes:      excl,
		Filesystem:    s.fs,
		Generation:    genID,
		LastScan:      lastScan,
		Stats:         stats,
		NextScan:      next,
		CheckedAt:     s.checkedAt,
	}
}

// Infos returns API representations for all shares sorted by id.
func (r *Registry) Infos() []Info {
	out := make([]Info, 0, len(r.shares))
	for _, s := range r.shares {
		out = append(out, s.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
