package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Documented parameter ceilings (FR-SEC-05). Requests above these are
// clamped rather than rejected, so a client can always ask for "everything"
// and get the maximum the server is willing to compute.
const (
	MaxListLimit     = 5000
	DefaultListLimit = 500
	MaxTreeDepth     = 3
	MaxTopN          = 1000
	MaxTreemapNodes  = 50000
	MaxSearchLimit   = 1000
	SearchTimeout    = 2 * time.Second
)

// clampInt parses a query parameter and clamps it into [lo, hi].
func clampInt(r *http.Request, key string, def, lo, hi int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return min(max(v, lo), hi)
}

// clampFloat parses a float query parameter and clamps it.
func clampFloat(r *http.Request, key string, def, lo, hi float64) float64 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return min(max(v, lo), hi)
}

// queryUint parses an unsigned integer parameter, 0 when absent or invalid.
func queryUint(r *http.Request, key string) uint64 {
	v, err := strconv.ParseUint(r.URL.Query().Get(key), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// queryTime parses an RFC 3339 timestamp, zero when absent or invalid.
func queryTime(r *http.Request, key string) time.Time {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// Accept a plain date too; it is what a date input sends.
		t, err = time.Parse("2006-01-02", raw)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}

// queryPath normalises a share-relative path parameter. Leading and
// trailing separators are stripped so "" always means the share root.
func queryPath(r *http.Request) string {
	return strings.Trim(r.URL.Query().Get("path"), "/")
}
