package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/StevenGann/ShareDirStat/internal/scan"
)

// scanRequest is the body of POST .../scan.
type scanRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleScanStart(w http.ResponseWriter, r *http.Request) {
	if s.scans == nil {
		writeError(w, http.StatusServiceUnavailable, "scans_unavailable", "the scan manager is not running", nil)
		return
	}
	var req scanRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON: "+err.Error(), nil)
			return
		}
	}
	trigger := scan.TriggerManual
	if strings.Trim(req.Path, "/") != "" {
		trigger = scan.TriggerRescan
	}
	st, err := s.scans.Start(r.Context(), r.PathValue("id"), strings.Trim(req.Path, "/"), trigger)
	if err != nil {
		writeScanError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) handleScanCancel(w http.ResponseWriter, r *http.Request) {
	s.scanAction(w, r, s.scansCancel)
}

func (s *Server) handleScanPause(w http.ResponseWriter, r *http.Request) {
	s.scanAction(w, r, s.scansPause)
}

func (s *Server) handleScanResume(w http.ResponseWriter, r *http.Request) {
	s.scanAction(w, r, s.scansResume)
}

func (s *Server) scansCancel(shareID, scanID string) error { return s.scans.Cancel(shareID, scanID) }
func (s *Server) scansPause(shareID, scanID string) error  { return s.scans.Pause(shareID, scanID) }
func (s *Server) scansResume(shareID, scanID string) error { return s.scans.Resume(shareID, scanID) }

func (s *Server) scanAction(w http.ResponseWriter, r *http.Request, fn func(string, string) error) {
	if s.scans == nil {
		writeError(w, http.StatusServiceUnavailable, "scans_unavailable", "the scan manager is not running", nil)
		return
	}
	shareID, scanID := r.PathValue("id"), r.PathValue("scanId")
	if _, ok := s.reg.Get(shareID); !ok {
		writeError(w, http.StatusNotFound, "share_not_found", "no share with id "+shareID, nil)
		return
	}
	if err := fn(shareID, scanID); err != nil {
		writeScanError(w, err)
		return
	}
	st, running := s.scans.RunningFor(shareID)
	if !running {
		writeJSON(w, http.StatusAccepted, map[string]any{"share_id": shareID, "scan_id": scanID, "running": false})
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

func (s *Server) handleScanHistory(w http.ResponseWriter, r *http.Request) {
	shareID := r.PathValue("id")
	if _, ok := s.reg.Get(shareID); !ok {
		writeError(w, http.StatusNotFound, "share_not_found", "no share with id "+shareID, nil)
		return
	}
	var history []scan.Record
	var running *scan.Status
	if s.scans != nil {
		history = s.scans.History(shareID)
		if st, ok := s.scans.RunningFor(shareID); ok {
			running = &st
		}
	}
	if history == nil {
		history = []scan.Record{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": shareID, "scans": history, "running": running})
}

func (s *Server) handleScansRunning(w http.ResponseWriter, _ *http.Request) {
	var running []scan.Status
	if s.scans != nil {
		running = s.scans.Running()
	}
	if running == nil {
		running = []scan.Status{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scans": running})
}

// writeScanError maps manager errors onto HTTP status codes (§9.1).
func writeScanError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, scan.ErrShareNotFound):
		writeError(w, http.StatusNotFound, "share_not_found", err.Error(), nil)
	case errors.Is(err, scan.ErrScanNotFound):
		writeError(w, http.StatusNotFound, "scan_not_found", err.Error(), nil)
	case errors.Is(err, scan.ErrPathNotFound):
		writeError(w, http.StatusNotFound, "path_not_found", err.Error(), nil)
	case errors.Is(err, scan.ErrScanInProgress):
		writeError(w, http.StatusConflict, "scan_in_progress", err.Error(), nil)
	case errors.Is(err, scan.ErrShareUnavailable):
		writeError(w, http.StatusConflict, "share_unavailable", err.Error(), nil)
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error(), nil)
	}
}
