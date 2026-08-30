// Package api serves the embedded web UI, the JSON API, the SSE event
// stream, health and metrics. Specification §9 and §12.
package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/events"
	"github.com/StevenGann/ShareDirStat/internal/metrics"
	"github.com/StevenGann/ShareDirStat/internal/ops"
	"github.com/StevenGann/ShareDirStat/internal/scan"
	"github.com/StevenGann/ShareDirStat/internal/share"
	"github.com/StevenGann/ShareDirStat/internal/version"
)

// CSRFHeaderValue is the required X-Requested-With value on mutating requests (FR-SEC-02).
const CSRFHeaderValue = "ShareDirStat"

// Deps are the collaborators a Server needs.
type Deps struct {
	Config  *config.Config
	Shares  *share.Registry
	Scans   *scan.Manager
	Ops     *ops.Manager
	Events  *events.Broker
	Metrics *metrics.Metrics
	Log     *slog.Logger
	UI      fs.FS
	Owners  *OwnerResolver
}

// Server wires configuration, registry, scans and metrics into an http.Handler.
type Server struct {
	cfg     *config.Config
	reg     *share.Registry
	scans   *scan.Manager
	ops     *ops.Manager
	bus     *events.Broker
	metrics *metrics.Metrics
	log     *slog.Logger
	owners  *OwnerResolver
	ui      fs.FS
	ready   atomic.Bool
	handler http.Handler
}

// New constructs the server. Deps.UI is the built web UI (a directory
// containing index.html); an empty fs.FS serves a placeholder page.
func New(d Deps) *Server {
	if d.Owners == nil {
		d.Owners = NewOwnerResolver(nil, nil)
	}
	s := &Server{
		cfg: d.Config, reg: d.Shares, scans: d.Scans, ops: d.Ops, bus: d.Events,
		metrics: d.Metrics, log: d.Log, owners: d.Owners, ui: d.UI,
	}
	s.handler = s.build()
	return s
}

// SetReady flips the readiness probe.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Handler returns the root handler including base-path handling.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) build() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/shares", s.handleShares)
	api.HandleFunc("GET /api/v1/shares/{id}", s.handleShare)
	api.HandleFunc("GET /api/v1/shares/{id}/tree", s.handleTree)
	api.HandleFunc("GET /api/v1/shares/{id}/node", s.handleNode)
	api.HandleFunc("GET /api/v1/shares/{id}/treemap", s.handleTreemap)
	api.HandleFunc("GET /api/v1/shares/{id}/top", s.handleTop)
	api.HandleFunc("GET /api/v1/shares/{id}/extensions", s.handleExtensions)
	api.HandleFunc("GET /api/v1/shares/{id}/search", s.handleSearch)
	api.HandleFunc("GET /api/v1/shares/{id}/errors", s.handleErrors)
	api.HandleFunc("GET /api/v1/shares/{id}/scans", s.handleScanHistory)
	api.HandleFunc("POST /api/v1/shares/{id}/scan", s.handleScanStart)
	api.HandleFunc("POST /api/v1/shares/{id}/scan/{scanId}/cancel", s.handleScanCancel)
	api.HandleFunc("POST /api/v1/shares/{id}/scan/{scanId}/pause", s.handleScanPause)
	api.HandleFunc("POST /api/v1/shares/{id}/scan/{scanId}/resume", s.handleScanResume)
	api.HandleFunc("GET /api/v1/shares/{id}/download", s.handleDownload)
	api.HandleFunc("GET /api/v1/shares/{id}/download/zip", s.handleDownloadZip)
	api.HandleFunc("POST /api/v1/shares/{id}/download/zip", s.handleDownloadZip)
	api.HandleFunc("POST /api/v1/shares/{id}/delete/preview", s.handleDeletePreview)
	api.HandleFunc("POST /api/v1/shares/{id}/delete", s.handleDelete)
	api.HandleFunc("GET /api/v1/shares/{id}/trash", s.handleTrashList)
	api.HandleFunc("POST /api/v1/shares/{id}/trash/restore", s.handleTrashRestore)
	api.HandleFunc("POST /api/v1/shares/{id}/trash/empty", s.handleTrashEmpty)
	api.HandleFunc("GET /api/v1/audit/deletes", s.handleAudit)
	api.HandleFunc("GET /api/v1/scans", s.handleScansRunning)
	api.HandleFunc("GET /api/v1/events", s.handleEvents)
	api.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPI)
	api.HandleFunc("GET /api/v1/openapi.yaml", s.handleOpenAPIYAML)
	api.HandleFunc("GET /api/v1/version", s.handleVersion)
	api.HandleFunc("GET /api/v1/config", s.handleConfig)
	api.HandleFunc("/api/", s.handleAPINotFound)

	inner := http.NewServeMux()
	inner.HandleFunc("GET /healthz", s.handleHealthz)
	inner.HandleFunc("GET /readyz", s.handleReadyz)
	inner.Handle("GET /metrics", s.metrics.Handler())
	inner.Handle("/api/", s.csrf(api))
	inner.Handle("/", spaHandler(s.ui))

	var h http.Handler = inner
	h = s.instrument(h)
	h = s.securityHeaders(h)
	h = s.hostCheck(h)
	h = s.basePath(h)
	h = s.requestLog(h)
	h = s.recoverPanic(h)
	return h
}

// --- handlers -------------------------------------------------------------

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// shareView augments the registry's view with the share's live scan status
// and whether file operations are actually possible right now.
type shareView struct {
	share.Info
	Scan *scan.Status `json:"scan"`
	// DeleteBlocked explains why deleting is unavailable, empty when it is
	// available. The UI uses it to disable the action with a real reason
	// rather than silently doing nothing.
	DeleteBlocked string `json:"delete_blocked,omitempty"`
	TrashEnabled  bool   `json:"trash_enabled"`
	ZipEnabled    bool   `json:"zip_enabled"`
}

func (s *Server) shareView(sh *share.Share) shareView {
	v := shareView{Info: sh.Snapshot()}
	if s.scans != nil {
		if st, ok := s.scans.RunningFor(sh.Config.ID); ok {
			v.Scan = &st
		}
	}
	if s.ops != nil {
		if err := s.ops.CanDelete(sh.Config.ID); err != nil {
			v.DeleteBlocked = err.Error()
		}
		v.TrashEnabled = s.ops.TrashEnabled()
		v.ZipEnabled = s.cfg.Operations.Download.ZipEnabled && sh.Config.CanDownload()
	}
	return v
}

func (s *Server) handleShares(w http.ResponseWriter, _ *http.Request) {
	all := s.reg.All()
	out := make([]shareView, 0, len(all))
	for _, sh := range all {
		out = append(out, s.shareView(sh))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out})
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.reg.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "share_not_found", "no share with id "+r.PathValue("id"), nil)
		return
	}
	writeJSON(w, http.StatusOK, s.shareView(sh))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, version.Get())
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	m, err := s.cfg.Redacted()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "cannot render configuration", nil)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "no such API route: "+r.Method+" "+r.URL.Path, nil)
}

// --- helpers --------------------------------------------------------------

// apiError is the error envelope (§9.1).
type apiError struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	var e apiError
	e.Error.Code, e.Error.Message, e.Error.Details = code, msg, details
	writeJSON(w, status, e)
}

// basePathPrefix returns the normalised prefix ("" for root).
func (s *Server) basePathPrefix() string {
	return strings.TrimSuffix(s.cfg.Server.BasePath, "/")
}
