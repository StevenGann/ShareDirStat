package ops

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path"
	"syscall"
	"time"
)

// ManifestName is the record of what a trash batch actually holds.
//
// The directory layout inside a batch mirrors the original paths, which makes
// it readable but ambiguous: a batch containing Movies/2019/big.mkv could be
// that one file, or the Movies folder that contained it. The manifest records
// what was deleted so restore never has to guess.
const ManifestName = ".manifest.jsonl"

type manifestEntry struct {
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	Size      uint64    `json:"size"`
	DeletedAt time.Time `json:"deleted_at"`
}

// appendManifest adds one line to a batch's manifest.
func appendManifest(s *Store, stamp string, e manifestEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	rel := path.Join(TrashDir, stamp, ManifestName)
	f, err := s.root.OpenFile(rel, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(line, '\n'))
	return err
}

// readManifest returns what a batch recorded. A missing manifest is not an
// error: the batch simply cannot be listed for restore.
func readManifest(s *Store, stamp string) ([]manifestEntry, error) {
	rel := path.Join(TrashDir, stamp, ManifestName)
	f, err := s.OpenFile(rel)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []manifestEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e manifestEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a torn line must not hide the rest of the batch
		}
		// The manifest lives inside the share and could have been edited by
		// anyone with write access, so its paths are validated like any other
		// input before being acted on.
		clean, err := CleanRel(e.Path)
		if err != nil || clean == "" {
			continue
		}
		e.Path = clean
		out = append(out, e)
	}
	return out, sc.Err()
}
