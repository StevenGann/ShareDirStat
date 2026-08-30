package ops

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Audit log defaults (§8.5).
const (
	DefaultAuditMaxBytes int64 = 50 << 20
	DefaultAuditKeep           = 5
	// tailWindow bounds how much of the log Recent reads back.
	tailWindow int64 = 1 << 20
)

// AuditEntry is one line of the delete audit log. Every delete attempt is
// recorded, successful or not, because "what happened to my files?" is the
// question an audit log exists to answer.
type AuditEntry struct {
	Time      time.Time `json:"time"`
	Share     string    `json:"share"`
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	Size      uint64    `json:"size"`
	Alloc     uint64    `json:"alloc"`
	Files     uint32    `json:"files"`
	Dirs      uint32    `json:"dirs"`
	Outcome   string    `json:"outcome"`
	Removed   int       `json:"removed"`
	Failed    int       `json:"failed"`
	Errno     string    `json:"errno,omitempty"`
	Error     string    `json:"error,omitempty"`
	Trash     string    `json:"trash,omitempty"`
	ClientIP  string    `json:"client_ip,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	Forwarded string    `json:"forwarded_for,omitempty"`
}

// AuditLog appends JSON lines to <data_dir>/audit/deletes.log and rotates it.
type AuditLog struct {
	path     string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	file *os.File
	size int64
}

// NewAuditLog opens (or creates) the audit log under the data directory.
func NewAuditLog(dataDir string, maxBytes int64, keep int) (*AuditLog, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultAuditMaxBytes
	}
	if keep <= 0 {
		keep = DefaultAuditKeep
	}
	dir := filepath.Join(dataDir, "audit")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("audit directory: %w", err)
	}
	a := &AuditLog{path: filepath.Join(dir, "deletes.log"), maxBytes: maxBytes, keep: keep}
	if err := a.open(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *AuditLog) open() error {
	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	a.file, a.size = f, fi.Size()
	return nil
}

// Path returns the audit log's location, for diagnostics.
func (a *AuditLog) Path() string { return a.path }

// Append writes one entry and flushes it to the OS. The audit record is
// written before the caller reports success, so a crash cannot lose the
// record of a delete that did happen.
func (a *AuditLog) Append(e AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return fmt.Errorf("audit log is closed")
	}
	if a.size+int64(len(line)) > a.maxBytes {
		if err := a.rotate(); err != nil {
			return err
		}
	}
	n, err := a.file.Write(line)
	a.size += int64(n)
	if err != nil {
		return err
	}
	return a.file.Sync()
}

// rotate shifts deletes.log.N to N+1 and starts a fresh log. Caller holds the
// lock.
func (a *AuditLog) rotate() error {
	if err := a.file.Close(); err != nil {
		return err
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", a.path, a.keep))
	for i := a.keep - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", a.path, i)
		to := fmt.Sprintf("%s.%d", a.path, i+1)
		if _, err := os.Stat(from); err == nil {
			_ = os.Rename(from, to)
		}
	}
	if err := os.Rename(a.path, a.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return a.open()
}

// Recent returns up to n of the most recent entries, newest first. Only the
// tail of the current log is read, so a 50 MB file costs one small read.
func (a *AuditLog) Recent(n int) ([]AuditEntry, error) {
	if n <= 0 {
		n = 200
	}
	a.mu.Lock()
	path, size := a.path, a.size
	a.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	start := size - tailWindow
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if start > 0 {
		// The window almost certainly begins mid-line; drop the fragment.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}

	var entries []AuditEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // a torn line from a crash must not break the view
		}
		entries = append(entries, e)
	}
	// Newest first, capped.
	for l, r := 0, len(entries)-1; l < r; l, r = l+1, r-1 {
		entries[l], entries[r] = entries[r], entries[l]
	}
	if len(entries) > n {
		entries = entries[:n]
	}
	return entries, nil
}

// Close closes the log file.
func (a *AuditLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	return err
}
