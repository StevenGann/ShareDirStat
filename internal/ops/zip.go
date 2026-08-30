package ops

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// ErrZipTooLarge means the request exceeds the configured ZIP limits.
var ErrZipTooLarge = errors.New("the selection is larger than the configured ZIP limit")

// ZipPlan is what a ZIP request would contain, measured before a single byte
// is sent so the limits can be enforced with a clean HTTP error rather than a
// truncated download (FR-DL-03).
type ZipPlan struct {
	Paths []string
	// Prefix is the common parent stripped from every entry name, so an
	// archive of one folder unpacks as that folder rather than a deep chain.
	Prefix  string
	Files   uint64
	Bytes   uint64
	Name    string
	Skipped []string
}

// ZipSummary is the ZIP outcome, reported to the metrics hooks and the log.
type ZipSummary struct {
	Entries int64
	Bytes   int64
	Skipped int
}

// PlanZip resolves and measures a ZIP request from the scan results.
func (m *Manager) PlanZip(shareID string, paths []string) (*ZipPlan, error) {
	if err := m.CanZip(shareID); err != nil {
		return nil, err
	}
	sh, s, err := m.lookup(shareID)
	if err != nil {
		return nil, err
	}
	clean, err := normalizePaths(paths)
	if err != nil {
		return nil, err
	}
	clean = dropNested(clean)

	plan := &ZipPlan{Paths: clean, Prefix: commonParent(clean)}
	gen := sh.Generation()
	for _, rel := range clean {
		fi, err := s.Lstat(rel)
		if err != nil {
			return nil, err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			plan.Skipped = append(plan.Skipped, rel+" (symlink)")
		case fi.IsDir():
			if gen != nil {
				if info, ok := gen.Info(rel); ok {
					plan.Files += uint64(info.Files)
					plan.Bytes += info.Size
					continue
				}
			}
			// Not in the results: fall back to walking it, which is slower
			// but keeps a freshly created folder downloadable.
			files, bytes, err := measureDir(s, rel)
			if err != nil {
				return nil, err
			}
			plan.Files += files
			plan.Bytes += bytes
		case fi.Mode().IsRegular():
			plan.Files++
			plan.Bytes += uint64(max(fi.Size(), 0))
		default:
			plan.Skipped = append(plan.Skipped, rel+" (not a regular file)")
		}
	}

	limits := m.cfg.Operations.Download
	if limits.ZipMaxBytes > 0 && plan.Bytes > uint64(limits.ZipMaxBytes) {
		return nil, fmt.Errorf("%w: %d bytes, the limit is %d", ErrZipTooLarge, plan.Bytes, limits.ZipMaxBytes)
	}
	if limits.ZipMaxEntries > 0 && plan.Files > uint64(limits.ZipMaxEntries) {
		return nil, fmt.Errorf("%w: %d entries, the limit is %d", ErrZipTooLarge, plan.Files, limits.ZipMaxEntries)
	}

	plan.Name = zipName(sh.Config.Name, clean)
	return plan, nil
}

// measureDir totals a directory that is not in the scan results.
func measureDir(s *Store, rel string) (files, bytes uint64, err error) {
	entries, err := s.ReadDir(rel)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		child := path.Join(rel, e.Name())
		if e.IsDir() {
			f, b, err := measureDir(s, child)
			if err != nil {
				continue // an unreadable subdirectory is reported at write time
			}
			files += f
			bytes += b
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			files++
			bytes += uint64(max(fi.Size(), 0))
		}
	}
	return files, bytes, nil
}

// WriteZip streams an archive of the planned paths.
//
// Entries are stored, not deflated: a media share compresses to nothing and
// the CPU on a Raspberry Pi is better spent elsewhere (D7). Anything that
// could not be read is listed in a _SKIPPED.txt member at the end, so the
// archive is honest about what it does not contain.
func (m *Manager) WriteZip(ctx context.Context, w io.Writer, shareID string, plan *ZipPlan) (ZipSummary, error) {
	var sum ZipSummary
	_, s, err := m.lookup(shareID)
	if err != nil {
		return sum, err
	}

	counter := &countingWriter{w: w}
	zw := zip.NewWriter(counter)
	skipped := append([]string(nil), plan.Skipped...)

	addFile := func(rel string) error {
		fi, err := s.Lstat(rel)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", rel, err))
			return nil
		}
		if !fi.Mode().IsRegular() {
			skipped = append(skipped, rel+" (not a regular file)")
			return nil
		}
		f, err := s.OpenFile(rel)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", rel, err))
			return nil
		}
		defer func() { _ = f.Close() }()

		hdr := &zip.FileHeader{
			Name:   entryName(plan.Prefix, rel),
			Method: zip.Store,
		}
		hdr.Modified = fi.ModTime()
		hdr.SetMode(fi.Mode().Perm())
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, f); err != nil {
			return err
		}
		sum.Entries++
		return nil
	}

	var walk func(rel string) error
	walk = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := s.Lstat(rel)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", rel, err))
			return nil
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			skipped = append(skipped, rel+" (symlink)")
			return nil
		case fi.IsDir():
			if !s.SameDevice(fi) {
				skipped = append(skipped, rel+" (mount point)")
				return nil
			}
			entries, err := s.ReadDir(rel)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s (%v)", rel, err))
				return nil
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			if len(names) == 0 {
				// Preserve an empty directory as a directory entry.
				if _, err := zw.Create(entryName(plan.Prefix, rel) + "/"); err != nil {
					return err
				}
			}
			for _, name := range names {
				if err := walk(path.Join(rel, name)); err != nil {
					return err
				}
			}
			return nil
		case fi.Mode().IsRegular():
			return addFile(rel)
		default:
			skipped = append(skipped, rel+" (special file)")
			return nil
		}
	}

	for _, rel := range plan.Paths {
		if err := walk(rel); err != nil {
			_ = zw.Close()
			sum.Bytes = counter.n
			return sum, err
		}
	}

	if len(skipped) > 0 {
		sum.Skipped = len(skipped)
		if out, err := zw.Create("_SKIPPED.txt"); err == nil {
			_, _ = io.WriteString(out, "These items were not included:\n\n")
			for _, sk := range skipped {
				_, _ = io.WriteString(out, sk+"\n")
			}
		}
	}
	if err := zw.Close(); err != nil {
		sum.Bytes = counter.n
		return sum, err
	}
	sum.Bytes = counter.n
	m.noteDownload(shareID, "zip", sum.Bytes)
	return sum, nil
}

// entryName maps a share-relative path to its name inside the archive.
func entryName(prefix, rel string) string {
	if prefix == "" {
		return rel
	}
	return strings.TrimPrefix(strings.TrimPrefix(rel, prefix), "/")
}

// commonParent returns the deepest directory containing every path, so an
// archive of one folder unpacks as that folder rather than its whole chain.
func commonParent(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		parent := path.Dir(paths[0])
		if parent == "." {
			return ""
		}
		return parent
	}
	parts := strings.Split(path.Dir(paths[0]), "/")
	for _, p := range paths[1:] {
		other := strings.Split(path.Dir(p), "/")
		n := min(len(parts), len(other))
		i := 0
		for i < n && parts[i] == other[i] {
			i++
		}
		parts = parts[:i]
	}
	joined := strings.Join(parts, "/")
	if joined == "." {
		return ""
	}
	return joined
}

// zipName builds the download's filename.
func zipName(shareName string, paths []string) string {
	base := shareName
	if len(paths) == 1 {
		base = path.Base(paths[0])
	}
	base = strings.Map(func(r rune) rune {
		if strings.ContainsRune("/\\:*?\"<>|\x00", r) || r < 0x20 {
			return '_'
		}
		return r
	}, base)
	if base == "" {
		base = "download"
	}
	if len(base) > 80 {
		base = base[:80]
	}
	return base + ".zip"
}

// countingWriter tracks how many bytes reached the client.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
