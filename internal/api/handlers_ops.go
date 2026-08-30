package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/StevenGann/ShareDirStat/internal/ops"
)

// deleteRequest is the body of the delete endpoints.
type deleteRequest struct {
	Paths   []string `json:"paths"`
	Confirm string   `json:"confirm"`
}

// zipRequest is the body of POST .../download/zip.
type zipRequest struct {
	Paths []string `json:"paths"`
}

func (s *Server) opsAvailable(w http.ResponseWriter) bool {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "operations_unavailable",
			"file operations are not enabled on this server", nil)
		return false
	}
	return true
}

func decodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error(), nil)
		return v, false
	}
	return v, true
}

func (s *Server) handleDeletePreview(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	req, ok := decodeJSON[deleteRequest](w, r)
	if !ok {
		return
	}
	preview, err := s.ops.Preview(r.PathValue("id"), req.Paths)
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	req, ok := decodeJSON[deleteRequest](w, r)
	if !ok {
		return
	}
	res, err := s.ops.Delete(r.PathValue("id"), req.Paths, req.Confirm, clientInfo(r, s.cfg.Server.TrustProxyHeaders))
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	entries, err := s.ops.Audit().Recent(clampInt(r, "limit", 200, 1, 1000))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not read the audit log: "+err.Error(), nil)
		return
	}
	if entries == nil {
		entries = []ops.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deletions": entries})
}

// clientInfo captures who asked, for the audit log (§8.5).
func clientInfo(r *http.Request, trustProxy bool) ops.ClientInfo {
	info := ops.ClientInfo{
		IP:        clientIP(r),
		UserAgent: truncate(r.UserAgent(), 256),
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" && trustProxy {
		info.Forwarded = truncate(fwd, 256)
	}
	return info
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	shareID := r.PathValue("id")
	rel := queryPath(r)

	f, fi, err := s.ops.OpenForDownload(shareID, rel)
	if err != nil {
		writeOpsError(w, err)
		return
	}
	defer func() { _ = f.Close() }()

	name := path.Base(rel)
	h := w.Header()
	h.Set("Content-Type", contentType(name))
	h.Set("Content-Disposition", contentDisposition(name))
	// A file on a share is untrusted content: never let a browser sniff it
	// into something executable in this origin (FR-SEC-04).
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", downloadETag(fi))

	s.metrics.DownloadsInFlight.Inc()
	defer s.metrics.DownloadsInFlight.Dec()

	// ServeContent handles conditional and range requests, which is what
	// makes a large download resumable (FR-DL-01).
	counted := &countingResponseWriter{ResponseWriter: w}
	http.ServeContent(counted, r, name, fi.ModTime(), f)
	s.metrics.DownloadBytes.WithLabelValues(shareID, "file").Add(float64(counted.n))
}

func (s *Server) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	shareID := r.PathValue("id")

	// GET takes repeated ?path= parameters so the browser can stream the
	// archive straight to its downloader; a form cannot set the CSRF header
	// that POST requires, and buffering a multi-gigabyte archive in a fetch
	// just to hand it back to the browser would defeat the streaming.
	var paths []string
	if r.Method == http.MethodPost {
		req, ok := decodeJSON[zipRequest](w, r)
		if !ok {
			return
		}
		paths = req.Paths
	} else {
		for _, p := range r.URL.Query()["path"] {
			if trimmed := strings.Trim(p, "/"); trimmed != "" {
				paths = append(paths, trimmed)
			}
		}
	}
	if len(paths) == 0 {
		writeError(w, http.StatusBadRequest, "no_paths",
			"give one or more path parameters, or a paths array in a POST body", nil)
		return
	}

	plan, err := s.ops.PlanZip(shareID, paths)
	if err != nil {
		writeOpsError(w, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", contentDisposition(plan.Name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	// The archive is produced as it is sent, so its length is not known in
	// advance; the response is chunked and cannot be resumed.
	h.Set("Accept-Ranges", "none")

	s.metrics.DownloadsInFlight.Inc()
	defer s.metrics.DownloadsInFlight.Dec()

	sum, err := s.ops.WriteZip(r.Context(), w, shareID, plan)
	// Not counted here: ops.WriteZip already reports the streamed bytes through
	// the download hook, which the metrics wiring feeds into this same counter.
	// Adding them again made download_bytes_total{type="zip"} double what
	// type="file" reports for the same traffic.
	if err != nil {
		// Headers are long gone by now, so the only honest signal left is to
		// break off the response; the client sees a truncated archive.
		s.log.Warn("zip download failed part way through",
			"share", shareID, "entries", sum.Entries, "bytes", sum.Bytes, "error", err)
		return
	}
	s.log.Info("zip download", "share", shareID, "entries", sum.Entries,
		"bytes", sum.Bytes, "skipped", sum.Skipped)
}

// countingResponseWriter records how many bytes reached the client.
type countingResponseWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingResponseWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	return n, err
}

// contentType maps a filename to a media type, defaulting to a generic
// binary so nothing is ever served as active content by accident.
func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" && !isRisky(ct) {
			return ct
		}
	}
	return "application/octet-stream"
}

// isRisky reports media types a browser might execute or render in this
// origin. They are downgraded to a plain download.
func isRisky(ct string) bool {
	base, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return true
	}
	switch base {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "application/xml", "text/xml",
		"application/javascript", "text/javascript", "application/x-shockwave-flash":
		return true
	}
	return false
}

// contentDisposition builds a header that survives non-ASCII filenames:
// a sanitised ASCII fallback for old clients plus RFC 5987 for the rest.
func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if ascii == "" {
		ascii = "download"
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", ascii, url.PathEscape(name))
}

// downloadETag identifies the exact bytes being served, so a resumed or
// repeated download can be revalidated cheaply. Size and modification time
// are enough here and stay portable; the inode would add nothing a client
// can act on.
func downloadETag(fi fs.FileInfo) string {
	return fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano())
}

// writeOpsError maps file-operation errors onto HTTP status codes (§9.1).
func writeOpsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ops.ErrShareNotFound):
		writeError(w, http.StatusNotFound, "share_not_found", err.Error(), nil)
	case errors.Is(err, ops.ErrNotFound):
		writeError(w, http.StatusNotFound, "path_not_found", err.Error(), nil)
	case errors.Is(err, ops.ErrUnsafePath), errors.Is(err, ops.ErrIsShareRoot):
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error(), nil)
	case errors.Is(err, ops.ErrNotAFile):
		writeError(w, http.StatusBadRequest, "not_a_file", err.Error(), nil)
	case errors.Is(err, ops.ErrNoPaths), errors.Is(err, ops.ErrTooManyPaths):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
	case errors.Is(err, ops.ErrDeleteDisabled):
		writeError(w, http.StatusForbidden, "delete_disabled", err.Error(), nil)
	case errors.Is(err, ops.ErrDownloadDisabled):
		writeError(w, http.StatusForbidden, "download_disabled", err.Error(), nil)
	case errors.Is(err, ops.ErrZipDisabled):
		writeError(w, http.StatusForbidden, "zip_disabled", err.Error(), nil)
	case errors.Is(err, ops.ErrTrashDisabled):
		writeError(w, http.StatusForbidden, "trash_disabled", err.Error(), nil)
	case errors.Is(err, ops.ErrRestoreExists):
		writeError(w, http.StatusConflict, "restore_blocked", err.Error(), nil)
	case errors.Is(err, ops.ErrBadToken):
		writeError(w, http.StatusForbidden, "confirmation_required", err.Error(), nil)
	case errors.Is(err, ops.ErrZipTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "zip_too_large", err.Error(), nil)
	case errors.Is(err, ops.ErrDeleteBusy):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "busy", err.Error(), nil)
	case errors.Is(err, ops.ErrScanInProgress):
		writeError(w, http.StatusConflict, "scan_in_progress", err.Error(), nil)
	case errors.Is(err, ops.ErrShareUnavailable):
		writeError(w, http.StatusConflict, "share_unavailable", err.Error(), nil)
	case errors.Is(err, ops.ErrReadOnly), errors.Is(err, ops.ErrMountPoint):
		writeError(w, http.StatusConflict, "not_writable", err.Error(), nil)
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error(), nil)
	}
}

// --- trash -----------------------------------------------------------------

// restoreRequest is the body of POST .../trash/restore.
type restoreRequest struct {
	Path string `json:"path"`
}

// emptyRequest is the body of POST .../trash/empty. An empty batch means
// "everything".
type emptyRequest struct {
	Batch string `json:"batch"`
}

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	items, err := s.ops.ListTrash(r.PathValue("id"))
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": r.PathValue("id"), "items": items})
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	req, ok := decodeJSON[restoreRequest](w, r)
	if !ok {
		return
	}
	item, err := s.ops.RestoreTrash(r.PathValue("id"), req.Path,
		clientInfo(r, s.cfg.Server.TrustProxyHeaders))
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleTrashEmpty(w http.ResponseWriter, r *http.Request) {
	if !s.opsAvailable(w) {
		return
	}
	req, ok := decodeJSON[emptyRequest](w, r)
	if !ok {
		return
	}
	removed, err := s.ops.EmptyTrash(r.PathValue("id"), req.Batch,
		clientInfo(r, s.cfg.Server.TrustProxyHeaders))
	if err != nil {
		writeOpsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}
